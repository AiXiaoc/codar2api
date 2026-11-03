package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type proxyRetryTransport func(*http.Request) (*http.Response, error)

func (f proxyRetryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func installProxyRetryTransport(t *testing.T, pool *ProxyPool, id int, f proxyRetryTransport) {
	t.Helper()
	route, err := pool.Pin(id)
	if err != nil {
		t.Fatal(err)
	}
	pool.mu.Lock()
	pool.clients[fmt.Sprintf("%d:%s", route.ID, route.Fingerprint)] = &http.Client{Transport: f}
	pool.mu.Unlock()
}

func proxyRetryResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestProxyConnectPolicyIndependentSwitchesAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name          string
		policy        proxyConnectPolicy
		firstFailures int
		allFail       bool
		want          []int
		wantError     bool
	}{
		{"disabled", proxyConnectPolicy{}, 2, false, []int{1}, true},
		{"retry recovers", proxyConnectPolicy{retry: true}, 1, false, []int{1, 1}, false},
		{"retry exhausted", proxyConnectPolicy{retry: true}, 2, false, []int{1, 1}, true},
		{"switch only", proxyConnectPolicy{switchProxy: true}, 2, false, []int{1, 2}, false},
		{"retry then switch", proxyConnectPolicy{true, true}, 2, false, []int{1, 1, 2}, false},
		{"all fail bounded", proxyConnectPolicy{true, true}, 2, true, []int{1, 1, 2, 2, 3, 3}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := rotationPool(t, 3)
			var seen []int
			firstHits := 0
			for id := 1; id <= 3; id++ {
				installProxyRetryTransport(t, pool, id, func(req *http.Request) (*http.Response, error) {
					seen = append(seen, id)
					raw, _ := io.ReadAll(req.Body)
					req.Body.Close()
					if string(raw) != `{"n":9007199254740993}` || req.Header.Get("X-Test") != "preserved" {
						t.Error("retry lost request bytes or headers")
					}
					if id == 1 {
						firstHits++
					}
					if tc.allFail || id == 1 && firstHits <= tc.firstFailures {
						return nil, errors.New("connection refused with private proxy credentials")
					}
					return proxyRetryResponse(`{"ok":true}`), nil
				})
			}
			route, _ := pool.Pin(1)
			req, _ := http.NewRequest("POST", "https://upstream.invalid/v1/messages", strings.NewReader(`{"n":9007199254740993}`))
			req.Header.Set("X-Test", "preserved")
			resp, actual, err := pool.doWithPolicy(req, route, tc.policy, nil)
			if (err != nil) != tc.wantError || !reflect.DeepEqual(seen, tc.want) || actual.ID != tc.want[len(tc.want)-1] {
				t.Fatalf("seen=%v route=%v err=%v", seen, actual, err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("transport error exposed proxy credentials")
			}
			if resp != nil {
				resp.Body.Close()
			}
		})
	}
}

func TestProxyConnectPolicySkipsBlockedDisabledDuplicateAndChangedRoutes(t *testing.T) {
	pool := rotationPool(t, 6)
	blocked, _ := pool.Pin(2)
	if err := pool.Block(blocked, "blocked"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Action(M{"action": "toggle", "id": 3, "enabled": false}); err != nil {
		t.Fatal(err)
	}
	pool.mu.Lock()
	rows := arr(pool.data["proxies"])
	obj(rows[3])["url"] = obj(rows[0])["url"] // A duplicate address must not replay the first exit.
	pool.mu.Unlock()
	seen := []int{}
	for _, id := range []int{1, 5, 6} {
		installProxyRetryTransport(t, pool, id, func(*http.Request) (*http.Response, error) {
			seen = append(seen, id)
			return nil, errors.New("connect failed")
		})
	}
	route, _ := pool.Pin(1)
	req, _ := http.NewRequest("GET", "https://upstream.invalid", nil)
	_, _, err := pool.doWithPolicy(req, route, proxyConnectPolicy{switchProxy: true}, func(_ *http.Request, r Route, _ int) func(*http.Response, error) {
		return func(*http.Response, error) {
			if r.ID == 5 {
				// The fallback snapshot already exists; a changed address is not used.
				if _, e := pool.Action(M{"action": "update", "id": 6, "name": "changed", "url": "http://127.0.0.1:11006"}); e != nil {
					t.Error(e)
				}
			}
		}
	})
	if err == nil || !reflect.DeepEqual(seen, []int{1, 5}) {
		t.Fatalf("used an ineligible fallback: %v %v", seen, err)
	}
}

func TestProxyConnectPolicyDoesNotReplayEstablishedRequestsCancelOrDirect(t *testing.T) {
	for _, mode := range []string{"http error", "after connection", "cancelled", "direct", "unreplayable body"} {
		t.Run(mode, func(t *testing.T) {
			pool := rotationPool(t, 2)
			routeID := 1
			if mode == "direct" {
				routeID = 0
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hits := 0
			installProxyRetryTransport(t, pool, routeID, func(req *http.Request) (*http.Response, error) {
				hits++
				if mode == "after connection" {
					httptrace.ContextClientTrace(req.Context()).GotConn(httptrace.GotConnInfo{})
				}
				if mode == "cancelled" {
					cancel()
					return nil, context.Canceled
				}
				if mode == "http error" {
					resp := proxyRetryResponse(`{"error":"upstream failed"}`)
					resp.StatusCode = 503
					return resp, nil
				}
				return nil, io.EOF
			})
			route, _ := pool.Pin(routeID)
			var body io.Reader = strings.NewReader("body")
			if mode == "unreplayable body" {
				body = io.LimitReader(body, 4)
			}
			req, _ := http.NewRequestWithContext(ctx, "POST", "https://upstream.invalid", body)
			attempts := 0
			resp, actual, _ := pool.doWithPolicy(req, route, proxyConnectPolicy{true, true}, func(*http.Request, Route, int) func(*http.Response, error) {
				attempts++
				return nil
			})
			if hits != 1 || attempts != 1 || actual.ID != routeID {
				t.Fatal("unsafe proxy retry", hits, attempts, actual)
			}
			if resp != nil {
				resp.Body.Close()
			}
		})
	}
}

func TestProxyConnectErrorClassificationWithRealTransport(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadURL := "http://" + listener.Addr().String()
	listener.Close()
	pool, err := NewProxyPool(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Action(M{"action": "create", "name": "dead", "url": deadURL}); err != nil {
		t.Fatal(err)
	}
	route, _ := pool.Pin(1)
	req, _ := http.NewRequest("POST", "https://upstream.invalid", strings.NewReader("body"))
	_, err = pool.Do(req, route)
	var connectErr *proxyConnectError
	if !errors.As(err, &connectErr) {
		t.Fatal("TCP refusal was not classified as connection failure", err)
	}
	var hits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		conn, _, e := w.(http.Hijacker).Hijack()
		if e != nil {
			t.Error(e)
			return
		}
		conn.Close() // HTTP request arrived, but no response was returned.
	}))
	defer proxy.Close()
	if _, err = pool.Action(M{"action": "create", "name": "drop response", "url": proxy.URL}); err != nil {
		t.Fatal(err)
	}
	route, _ = pool.Pin(2)
	req, _ = http.NewRequest("POST", "http://upstream.invalid", strings.NewReader("body"))
	_, actual, err := pool.doWithPolicy(req, route, proxyConnectPolicy{true, true}, nil)
	if err == nil || errors.As(err, &connectErr) || hits.Load() != 1 || actual.ID != 2 {
		t.Fatal("request that reached the proxy was replayed", hits.Load(), actual, err)
	}
}

func TestProxyConnectSettingsRelayAndDetails(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses", "/v1/messages/count_tokens"} {
		t.Run(path, func(t *testing.T) {
			h := newHarness(t, nil)
			h.add(t, "a", nil)
			for id := 1; id <= 2; id++ {
				h.post(t, "/proxy-pool/manage", M{"action": "create", "name": fmt.Sprint("proxy-", id), "url": fmt.Sprintf("http://127.0.0.1:%d", 19000+id)})
			}
			h.post(t, "/accounts/proxy", M{"uid": "a", "proxy_id": 1})
			h.post(t, "/settings", M{"proxy_connect_retry": true, "proxy_connect_switch": true, "max_attempts": 1, "detailed_debug": true})
			seen := []int{}
			var mu sync.Mutex
			body := "{\n\"model\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}],\"input\":\"hello\",\"extension\":9007199254740993}"
			for id := 1; id <= 2; id++ {
				installProxyRetryTransport(t, h.app.proxies, id, func(req *http.Request) (*http.Response, error) {
					mu.Lock()
					seen = append(seen, id)
					mu.Unlock()
					raw, _ := io.ReadAll(req.Body)
					req.Body.Close()
					if string(raw) != body || req.Header.Get("Authorization") != "Bearer "+text(h.app.store.Get("a")["token"]) || req.Header.Get("X-Test") != "preserved" || req.URL.RawQuery != "x=1" {
						t.Error("proxy retry changed account, request body, headers or query")
					}
					if id == 1 {
						return nil, errors.New("connect failed")
					}
					return proxyRetryResponse(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`), nil
				})
			}
			req, _ := http.NewRequest("POST", h.server.URL+path+"?x=1", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+h.key)
			req.Header.Set("X-Test", "preserved")
			resp, err := h.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			mu.Lock()
			defer mu.Unlock()
			if resp.StatusCode != 200 || !reflect.DeepEqual(seen, []int{1, 1, 2}) {
				t.Fatalf("retry did not recover within one account: status=%d seen=%v", resp.StatusCode, seen)
			}
			row, detail := lastDetail(t, h)
			attempts := arr(detail["attempts"])
			if num(row["attempts"]) != 1 || len(attempts) != 3 || !yes(row["ok"]) {
				t.Fatal("proxy attempts were hidden or counted as account switches", row)
			}
			for i, id := range seen {
				capture := obj(attempts[i])
				if num(obj(capture["proxy"])["id"]) != float64(id) || text(obj(capture["request"])["body"]) != body || (text(capture["error"]) != "") != (id == 1) {
					t.Fatal("proxy attempt detail was incomplete", i)
				}
			}
			policy := h.app.proxies.Policy("a")
			if policy["mode"] != "pick" || num(arr(policy["ids"])[0]) != 1 || num(h.app.store.Get("a")["cooldown_until"]) > 0 {
				t.Fatal("successful failover changed binding or penalized the account")
			}
		})
	}
}

func TestProxyConnectPolicyPanelAndCatalog(t *testing.T) {
	for _, operation := range []string{"panel", "account models", "global models"} {
		t.Run(operation, func(t *testing.T) {
			h := newHarness(t, nil)
			h.add(t, "a", nil)
			for id := 1; id <= 2; id++ {
				h.post(t, "/proxy-pool/manage", M{"action": "create", "name": fmt.Sprint("proxy-", id), "url": fmt.Sprintf("http://127.0.0.1:%d", 19100+id)})
			}
			h.post(t, "/accounts/proxy", M{"uid": "a", "proxy_id": 1})
			if err := h.app.proxies.SetCatalogProxy(1); err != nil {
				t.Fatal(err)
			}
			h.post(t, "/settings", M{"proxy_connect_retry": true, "proxy_connect_switch": true})
			seen := []int{}
			var mu sync.Mutex
			for id := 1; id <= 2; id++ {
				installProxyRetryTransport(t, h.app.proxies, id, func(*http.Request) (*http.Response, error) {
					mu.Lock()
					seen = append(seen, id)
					mu.Unlock()
					if id == 1 {
						return nil, errors.New("connect failed")
					}
					return proxyRetryResponse(`{"data":[{"id":"proxy-model"}],"choices":[{"message":{"content":"ok"}}]}`), nil
				})
			}
			switch operation {
			case "panel":
				result := h.post(t, "/accounts/test", M{"uid": "a", "model": "auto"})
				if !yes(result["ok"]) {
					t.Fatal(result)
				}
			case "account models":
				resp, _ := h.request(t, "GET", "/accounts/catalog?uid=a", nil, false)
				if resp.StatusCode != 200 {
					t.Fatal("account catalog failed", resp.StatusCode)
				}
			case "global models":
				models, err := h.app.catalog(context.Background(), true)
				if err != nil || len(models) != 1 || obj(models[0])["id"] != "proxy-model" {
					t.Fatal("global catalog failed", models, err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(seen, []int{1, 1, 2}) {
				t.Fatal("proxy policy did not apply", seen)
			}
		})
	}
}

func TestProxyConnectSettingsDefaultsAndPersistence(t *testing.T) {
	s := schedulerStore(t, 1)
	if yes(s.Settings()["proxy_connect_retry"]) || yes(s.Settings()["proxy_connect_switch"]) {
		t.Fatal("proxy retry settings must default off")
	}
	if _, err := s.SaveSettings(M{"proxy_connect_retry": true}); err != nil {
		t.Fatal(err)
	}
	if yes(s.Settings()["proxy_connect_switch"]) {
		t.Fatal("retry switch implicitly enabled failover")
	}
	if _, err := s.SaveSettings(M{"proxy_connect_retry": false, "proxy_connect_switch": true}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(s.dir)
	if err != nil || yes(reopened.Settings()["proxy_connect_retry"]) || !yes(reopened.Settings()["proxy_connect_switch"]) {
		t.Fatal("independent settings were not persisted", err)
	}
}

func TestProxyConnectExhaustionThenAccountSwitch(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "a", nil)
	h.add(t, "b", nil)
	for id := 1; id <= 2; id++ {
		h.post(t, "/proxy-pool/manage", M{"action": "create", "name": fmt.Sprint("proxy-", id), "url": fmt.Sprintf("http://127.0.0.1:%d", 19200+id)})
	}
	h.post(t, "/accounts/proxy", M{"uid": "a", "proxy_id": 1})
	h.post(t, "/accounts/proxy", M{"uid": "b", "proxy_id": 2})
	h.post(t, "/settings", M{"proxy_connect_retry": true, "proxy_connect_switch": true, "max_attempts": 2, "detailed_debug": true})
	var mu sync.Mutex
	seen := []string{}
	for id := 1; id <= 2; id++ {
		installProxyRetryTransport(t, h.app.proxies, id, func(req *http.Request) (*http.Response, error) {
			user := text(jwt(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))["username"])
			mu.Lock()
			seen = append(seen, fmt.Sprintf("%s:%d", user, id))
			mu.Unlock()
			if user == "a" {
				return nil, errors.New("connect failed")
			}
			return proxyRetryResponse(`{"choices":[]}`), nil
		})
	}
	resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
	mu.Lock()
	defer mu.Unlock()
	if resp.StatusCode != 200 || !reflect.DeepEqual(seen, []string{"a:1", "a:1", "a:2", "a:2", "b:2"}) {
		t.Fatal("account switching bypassed or repeated the proxy policy", resp.StatusCode, seen)
	}
	row, detail := lastDetail(t, h)
	if num(row["attempts"]) != 2 || len(arr(detail["attempts"])) != 5 || num(h.app.store.Get("a")["cooldown_until"]) <= now() {
		t.Fatal("exhausted proxy attempts did not preserve account retry behavior", row)
	}
	for _, account := range h.app.store.Concurrency() {
		if num(account["current_concurrency"]) != 0 {
			t.Fatal("proxy retries leaked an account reservation")
		}
	}
}

func TestProxyConnectFallbackActualExitBlockAndRotation(t *testing.T) {
	for _, mode := range []string{"fixed", "rotation", "blocked response"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, nil)
			h.add(t, "a", nil)
			for id := 1; id <= 2; id++ {
				h.post(t, "/proxy-pool/manage", M{"action": "create", "name": fmt.Sprint("proxy-", id), "url": fmt.Sprintf("http://127.0.0.1:%d", 19300+id)})
			}
			if mode == "rotation" {
				h.post(t, "/accounts/proxy", M{"uid": "a", "proxy_mode": "round_robin"})
			} else {
				h.post(t, "/accounts/proxy", M{"uid": "a", "proxy_id": 1})
			}
			h.post(t, "/settings", M{"proxy_connect_switch": true, "max_attempts": 1})
			installProxyRetryTransport(t, h.app.proxies, 1, func(*http.Request) (*http.Response, error) {
				return nil, errors.New("connect failed")
			})
			installProxyRetryTransport(t, h.app.proxies, 2, func(*http.Request) (*http.Response, error) {
				if mode == "blocked response" {
					resp := proxyRetryResponse("<html><title>403 Forbidden</title></html>")
					resp.StatusCode = 403
					return resp, nil
				}
				return proxyRetryResponse(`{"choices":[]}`), nil
			})
			resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
			if mode == "blocked response" {
				h.app.proxies.mu.Lock()
				initialBlocked := h.app.proxies.blockLocked(1) != nil
				actualBlocked := h.app.proxies.blockLocked(2) != nil
				h.app.proxies.mu.Unlock()
				if initialBlocked || !actualBlocked {
					t.Fatal("IP block attributed to the wrong exit")
				}
			} else if resp.StatusCode != 200 {
				t.Fatal("proxy failover failed", resp.StatusCode)
			}
			if mode == "rotation" {
				next, err := h.app.proxies.Account("a")
				if err != nil || next.ID != 1 || !h.app.proxies.Rotating("a") {
					t.Fatal("rotation ignored the last exit actually used", next, err)
				}
			}
		})
	}
}
