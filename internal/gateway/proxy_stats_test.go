package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/iotest"
)

func assertProxyRequestStats(t *testing.T, pool *ProxyPool, id int, succeeded, failed float64) {
	t.Helper()
	view := pool.View(true)
	exit := obj(view["direct"])
	for _, v := range arr(view["proxies"]) {
		if m := obj(v); num(m["id"]) == float64(id) {
			exit = m
		}
	}
	s := obj(exit["stats"])
	if num(s["requests"]) != succeeded+failed || num(s["succeeded"]) != succeeded || num(s["failed"]) != failed {
		t.Fatalf("exit %d: incorrect statistics: %v", id, s)
	}
	if succeeded+failed == 0 {
		if s["success_rate"] != nil {
			t.Fatal("unused exit must not display a success percentage", s)
		}
	} else if num(s["success_rate"]) != succeeded/(succeeded+failed) {
		t.Fatal("incorrect success rate", s)
	}
}

func TestProxyStatsAllUpstreamRequestsAndPersistence(t *testing.T) {
	h := newHarness(t, nil)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := http.StatusOK
		if r.URL.Path == "/v1/login" {
			status = http.StatusUnauthorized
		}
		jsonResponse(w, status, M{"data": M{}})
	}))
	t.Cleanup(proxy.Close)
	h.post(t, "/proxy-pool/manage", M{"action": "create", "name": "all upstream", "url": proxy.URL})
	route, err := h.app.proxies.Pin(1)
	if err != nil {
		t.Fatal(err)
	}
	assertProxyRequestStats(t, h.app.proxies, 1, 0, 0)
	for _, path := range []string{"/v1/messages", "/api/user/self", "/v1/gettoken", "/v1/login", "/v1/codar/models"} {
		_, err := h.app.requestJSON(context.Background(), "GET", h.upstream.URL+path, nil, &route, nil)
		if (err != nil) != (path == "/v1/login") {
			t.Fatalf("unexpected request result for %s: %v", path, err)
		}
	}
	if result, err := h.app.proxies.Check(context.Background(), 1); err != nil || !yes(result["ok"]) {
		t.Fatal("proxy check failed", result, err)
	}
	if _, err := h.app.catalog(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertProxyRequestStats(t, h.app.proxies, 1, 5, 1)
	assertProxyRequestStats(t, h.app.proxies, 0, 1, 0)
	resp, raw := h.request(t, "GET", "/proxy-pool", nil, false)
	if resp.StatusCode != 200 || num(obj(obj(arr(decode(t, raw)["proxies"])[0])["stats"])["requests"]) != 6 {
		t.Fatalf("management endpoint did not expose the statistics: %d %s", resp.StatusCode, raw)
	}
	// Proxy edits and a normal restart retain counters without changing bindings.
	h.post(t, "/proxy-pool/manage", M{"action": "update", "id": 1, "name": "renamed"})
	if err := h.app.proxies.Flush(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewProxyPool(h.app.cfg.DataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reloaded.Close)
	assertProxyRequestStats(t, reloaded, 1, 5, 1)
	assertProxyRequestStats(t, reloaded, 0, 1, 0)
}

func TestProxyStatsCountsRetryAttemptsByExit(t *testing.T) {
	pool := rotationPool(t, 2)
	installProxyRetryTransport(t, pool, 1, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})
	installProxyRetryTransport(t, pool, 2, func(*http.Request) (*http.Response, error) {
		return proxyRetryResponse(`{"ok":true}`), nil
	})
	route, _ := pool.Pin(1)
	req, _ := http.NewRequest("GET", "https://upstream.invalid/v1/codar/models", nil)
	resp, actual, err := pool.doWithPolicy(req, route, proxyConnectPolicy{true, true}, nil)
	if err != nil || actual.ID != 2 {
		t.Fatal("retry did not switch to the second exit", actual, err)
	}
	assertProxyRequestStats(t, pool, 1, 0, 2)
	assertProxyRequestStats(t, pool, 2, 0, 0) // Response not consumed yet.
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp.Body.Close()
	assertProxyRequestStats(t, pool, 1, 0, 2)
	assertProxyRequestStats(t, pool, 2, 1, 0)
	assertProxyRequestStats(t, pool, 0, 0, 0)
}

func TestProxyStatsResponseStatusAndReadFailure(t *testing.T) {
	pool := rotationPool(t, 1)
	for _, tc := range []struct {
		name string
		code int
		err  error
		ok   bool
	}{
		{"complete response", 200, nil, true},
		{"empty response", 204, nil, true},
		{"HTTP error", 403, nil, false},
		{"redirect", 302, nil, false},
		{"interrupted response", 200, io.ErrUnexpectedEOF, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installProxyRetryTransport(t, pool, 1, func(*http.Request) (*http.Response, error) {
				r := proxyRetryResponse(`{"ok":true}`)
				r.StatusCode = tc.code
				if tc.err != nil {
					r.Body = io.NopCloser(iotest.ErrReader(tc.err))
				}
				return r, nil
			})
			route, _ := pool.Pin(1)
			req, _ := http.NewRequest("GET", "https://upstream.invalid", nil)
			before := pool.stats.view(1)
			resp, err := pool.Do(req, route)
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.Copy(io.Discard, resp.Body)
			if !errors.Is(err, tc.err) {
				t.Fatal("reading the response changed its error", err)
			}
			resp.Body.Close()
			resp.Body.Close()
			succeeded, failed := num(before["succeeded"]), num(before["failed"])
			if tc.ok {
				succeeded++
			} else {
				failed++
			}
			assertProxyRequestStats(t, pool, 1, succeeded, failed)
		})
	}
}

func TestProxyStatsConcurrentCompletionAndCloseOnly(t *testing.T) {
	pool := rotationPool(t, 1)
	installProxyRetryTransport(t, pool, 1, func(*http.Request) (*http.Response, error) {
		return proxyRetryResponse(`{"ok":true}`), nil
	})
	route, _ := pool.Pin(1)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 20 {
			if err := pool.Flush(); err != nil {
				t.Error(err)
			}
		}
	})
	for range 100 {
		wg.Go(func() {
			req, _ := http.NewRequest("GET", "https://upstream.invalid", nil)
			resp, err := pool.Do(req, route)
			if err != nil {
				t.Error(err)
				return
			}
			// Some callers intentionally read a bounded response or only check HTTP
			// status. Close must still count them, and repeated Close cannot add rows.
			resp.Body.Close()
			resp.Body.Close()
		})
	}
	wg.Wait()
	assertProxyRequestStats(t, pool, 1, 100, 0)
	pool.Close()
	reloaded, err := NewProxyPool(pool.dir, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reloaded.Close)
	assertProxyRequestStats(t, reloaded, 1, 100, 0)
}
