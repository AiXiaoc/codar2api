package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func accountCatalogResult(t *testing.T, h *harness, uid string) (int, M) {
	t.Helper()
	response, raw := h.request(t, "GET", "/accounts/catalog?uid="+url.QueryEscape(uid), nil, false)
	return response.StatusCode, decode(t, raw)
}

func TestAccountCatalogUsesSelectedProxyAndCredentials(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "a", M{"enabled": false})
	h.add(t, "b", nil)
	h.post(t, "/settings", M{"codar_ua": "Catalog-Test/1.0"})
	cached := []any{M{"id": "global-cached"}}
	h.app.store.mu.Lock()
	h.app.store.catalog, h.app.store.catalogAt = clone(cached), now()
	h.app.store.mu.Unlock()
	type observed struct{ proxy, user string }
	seen := make(chan observed, 8)
	for _, name := range []string{"first", "second"} {
		proxyName := name
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" || r.URL.Path != "/v1/codar/models" || r.UserAgent() != "Catalog-Test/1.0" {
				t.Error("catalog proxy received the wrong request", r.Method, r.URL.Path, r.UserAgent())
			}
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			user := text(jwt(token)["username"])
			seen <- observed{proxyName, user}
			jsonResponse(w, 200, M{"data": []any{
				M{"id": proxyName + "-" + user, "display_name": "模型", "protocol": M{"type": "openai"}, "sort_order": 1},
				M{"id": "auto", "display_name": "Auto", "sort_order": 0},
			}})
		}))
		t.Cleanup(proxy.Close)
		h.post(t, "/proxy-pool/manage", M{"action": "create", "name": name, "url": proxy.URL})
	}
	h.post(t, "/accounts/proxy", M{"uid": "a", "proxy_id": 1})
	h.post(t, "/accounts/proxy", M{"uid": "b", "proxy_id": 2})
	for _, tc := range []struct{ uid, proxy string }{{"a", "first"}, {"b", "second"}, {"a", "first"}} {
		status, result := accountCatalogResult(t, h, tc.uid)
		models := arr(result["models"])
		if status != 200 || result["uid"] != tc.uid || len(models) != 2+len(codarImageModels) || obj(models[0])["id"] != "auto" ||
			obj(models[1])["id"] != tc.proxy+"-"+tc.uid || obj(models[1])["protocol"] != "openai" || obj(models[2])["type"] != "image" {
			t.Fatal("wrong account catalog", status, result)
		}
		if len(seen) != 1 || <-seen != (observed{tc.proxy, tc.uid}) {
			t.Fatal("catalog did not use this account's proxy and token")
		}
	}
	h.post(t, "/accounts/proxy", M{"uid": "a", "proxy_id": 2})
	status, result := accountCatalogResult(t, h, "a")
	if status != 200 || obj(arr(result["models"])[1])["id"] != "second-a" || len(seen) != 1 || <-seen != (observed{"second", "a"}) {
		t.Fatal("a changed proxy reused stale catalog data", status, result)
	}
	h.app.store.mu.RLock()
	defer h.app.store.mu.RUnlock()
	if !reflect.DeepEqual(h.app.store.catalog, cached) || yes(h.app.store.Get("a")["enabled"]) {
		t.Fatal("account catalogs changed the global catalog or account enabled state")
	}
	h.callsMu.Lock()
	defer h.callsMu.Unlock()
	if len(h.calls) != 0 {
		t.Fatal("account catalog bypassed the configured proxy", h.calls)
	}
}

func TestAccountCatalogRotatesConfiguredProxies(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "a", nil)
	seen := make(chan string, 4)
	for _, name := range []string{"one", "two"} {
		proxyName := name
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- proxyName
			jsonResponse(w, 200, M{"data": []any{M{"id": proxyName}}})
		}))
		t.Cleanup(proxy.Close)
		h.post(t, "/proxy-pool/manage", M{"action": "create", "name": name, "url": proxy.URL})
	}
	h.post(t, "/accounts/proxy", M{"uid": "a", "proxy_mode": "round_robin"})
	for _, want := range []string{"one", "two", "one"} {
		status, result := accountCatalogResult(t, h, "a")
		if status != 200 || obj(arr(result["models"])[0])["id"] != want || len(seen) != 1 || <-seen != want {
			t.Fatal("catalog request did not rotate proxy exits", status, result)
		}
	}
	h.post(t, "/proxy-pool/manage", M{"action": "toggle", "id": 2, "enabled": false})
	status, result := accountCatalogResult(t, h, "a")
	if status != http.StatusOK || text(result["warning"]) == "" || len(arr(result["models"])) != len(codarImageModels) || len(seen) != 0 {
		t.Fatal("an unavailable rotating pool used a different exit")
	}
	h.callsMu.Lock()
	defer h.callsMu.Unlock()
	if len(h.calls) != 0 {
		t.Fatal("rotating catalog fell back to direct")
	}
}

func TestAccountCatalogProxyFailureNeverUsesGlobalCacheOrDirect(t *testing.T) {
	for _, failure := range []string{"disabled", "deleted", "unreachable", "upstream"} {
		t.Run(failure, func(t *testing.T) {
			h := newHarness(t, nil)
			h.add(t, "a", nil)
			h.app.store.catalog, h.app.store.catalogAt = []any{M{"id": "cached-direct"}}, now()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				jsonResponse(w, 502, M{"error": M{"message": "proxy request failed"}})
			}))
			t.Cleanup(proxy.Close)
			h.post(t, "/proxy-pool/manage", M{"action": "create", "name": "proxy", "url": proxy.URL})
			h.post(t, "/accounts/proxy", M{"uid": "a", "proxy_id": 1})
			switch failure {
			case "disabled":
				h.post(t, "/proxy-pool/manage", M{"action": "toggle", "id": 1, "enabled": false})
			case "deleted":
				h.post(t, "/proxy-pool/manage", M{"action": "delete", "id": 1})
			case "unreachable":
				proxy.Close()
			}
			status, result := accountCatalogResult(t, h, "a")
			if status != http.StatusOK || text(result["warning"]) == "" || len(arr(result["models"])) != len(codarImageModels) {
				t.Fatal("proxy failure returned cached models", status, result)
			}
			for _, v := range arr(result["models"]) {
				if obj(v)["type"] != "image" {
					t.Fatal("proxy failure should only leave the built-in image catalog", result)
				}
			}
			h.callsMu.Lock()
			defer h.callsMu.Unlock()
			if len(h.calls) != 0 {
				t.Fatal("proxy failure fell back to direct", h.calls)
			}
		})
	}
}

func TestAccountCatalogRequiresLoginAndKnownAccount(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "a", nil)
	response, err := http.Get(h.server.URL + "/accounts/catalog?uid=a")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("account catalog did not require administrator login")
	}
	for _, uid := range []string{"", "missing"} {
		if status, _ := accountCatalogResult(t, h, uid); status != http.StatusNotFound {
			t.Fatal("unknown account accepted", uid, status)
		}
	}
	h.callsMu.Lock()
	defer h.callsMu.Unlock()
	if len(h.calls) != 0 {
		t.Fatal("rejected catalog request contacted upstream")
	}
}
