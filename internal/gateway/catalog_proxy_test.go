package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func globalCatalogResult(t *testing.T, h *harness, force bool) (int, M) {
	t.Helper()
	path := "/catalog"
	if force {
		path += "?force=1"
	}
	r, raw := h.request(t, "GET", path, nil, false)
	return r.StatusCode, decode(t, raw)
}

func TestGlobalCatalogProxySelectionCacheAndPersistence(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "account", nil)
	h.post(t, "/settings", M{"codar_ua": "Global-Catalog/1.0"})
	var calls [2]atomic.Int32
	for index, name := range []string{"first", "second"} {
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls[index].Add(1)
			if r.URL.Path != "/v1/codar/models" || r.Method != "GET" || r.UserAgent() != "Global-Catalog/1.0" || r.Header.Get("Authorization") != "" {
				t.Error("incorrect global catalog request", r.Method, r.URL.Path)
			}
			if r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
				t.Error("proxy credentials were not sent to the selected proxy")
			}
			jsonResponse(w, 200, M{"data": []any{M{"id": name}}})
		}))
		t.Cleanup(proxy.Close)
		h.post(t, "/proxy-pool/manage", M{"action": "create", "name": name, "url": strings.Replace(proxy.URL, "http://", "http://user:pass@", 1)})
	}
	h.post(t, "/accounts/proxy", M{"uid": "account", "proxy_id": 2})
	assertModel := func(want string, force bool) {
		t.Helper()
		status, result := globalCatalogResult(t, h, force)
		if status != 200 || len(arr(result["models"])) != 1 || obj(arr(result["models"])[0])["id"] != want {
			t.Fatal("wrong catalog", status, result)
		}
	}
	assertModel("auto", false)
	h.post(t, "/catalog/proxy", M{"proxy_id": 1})
	assertModel("first", false)
	assertModel("first", false)
	if calls[0].Load() != 1 {
		t.Fatal("matching proxy cache was not reused")
	}
	assertModel("first", true)
	if calls[0].Load() != 2 {
		t.Fatal("manual refresh did not bypass cache")
	}
	h.post(t, "/catalog/proxy", M{"proxy_id": 2})
	assertModel("second", false)
	response, raw := h.request(t, "GET", "/v1/models", nil, true)
	if response.StatusCode != 200 || obj(arr(decode(t, raw)["data"])[0])["id"] != "second" || calls[1].Load() != 1 {
		t.Fatal("API model list did not share the configured global catalog", string(raw))
	}
	pool, e := NewProxyPool(h.app.cfg.DataDir, h.app.cfg.APIBase+"/v1/codar/models")
	if e != nil {
		t.Fatal(e)
	}
	store, e := NewStore(h.app.cfg.DataDir)
	if e != nil {
		t.Fatal(e)
	}
	route, e := pool.CatalogRoute()
	if e != nil || route.ID != 2 || store.catalogSource != "2:"+route.Fingerprint || store.catalogAt == 0 {
		t.Fatal("catalog proxy or cache source did not survive restart", route, e)
	}
	h.app.store, h.app.proxies = store, pool
	assertModel("second", false)
	if calls[1].Load() != 1 {
		t.Fatal("persisted cache was not reused")
	}
	accountRoute, e := pool.Account("account")
	if e != nil || accountRoute.ID != 2 || store.Settings()["codar_ua"] != "Global-Catalog/1.0" {
		t.Fatal("global proxy selection changed account bindings or settings")
	}
	h.post(t, "/catalog/proxy", M{"proxy_id": 0})
	assertModel("auto", false)
	h.callsMu.Lock()
	defer h.callsMu.Unlock()
	if len(h.calls) != 2 {
		t.Fatal("proxied requests also reached the direct upstream", h.calls)
	}
}

func TestGlobalCatalogProxyAddressChangeAndUnavailableExits(t *testing.T) {
	h := newHarness(t, nil)
	var fail atomic.Bool
	for _, name := range []string{"old", "new"} {
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if fail.Load() {
				jsonResponse(w, 502, M{"error": M{"message": "proxy upstream failed"}})
				return
			}
			jsonResponse(w, 200, M{"data": []any{M{"id": name}}})
		}))
		t.Cleanup(proxy.Close)
		if name == "old" {
			h.post(t, "/proxy-pool/manage", M{"action": "create", "name": name, "url": proxy.URL})
			h.post(t, "/catalog/proxy", M{"proxy_id": 1})
		} else {
			h.post(t, "/proxy-pool/manage", M{"action": "update", "id": 1, "name": name, "url": proxy.URL})
		}
		status, result := globalCatalogResult(t, h, false)
		if status != 200 || obj(arr(result["models"])[0])["id"] != name {
			t.Fatal("changed proxy address reused the old address's cache", status, result)
		}
	}
	fail.Store(true)
	status, result := globalCatalogResult(t, h, true)
	if status != 200 || text(result["warning"]) == "" || obj(arr(result["models"])[0])["id"] != "new" {
		t.Fatal("refresh failure should explicitly warn when returning this exit's cache", status, result)
	}
	h.post(t, "/proxy-pool/manage", M{"action": "toggle", "id": 1, "enabled": false})
	status, _ = globalCatalogResult(t, h, false)
	if status != 502 {
		t.Fatal("disabled proxy reused cached data")
	}
	h.post(t, "/proxy-pool/manage", M{"action": "delete", "id": 1})
	status, _ = globalCatalogResult(t, h, false)
	if status != 502 || num(h.app.proxies.View(true)["catalog_proxy_id"]) != 1 {
		t.Fatal("deleted proxy silently changed to direct")
	}
	h.post(t, "/proxy-pool/manage", M{"action": "create", "name": "dead", "url": "http://127.0.0.1:1"})
	h.post(t, "/catalog/proxy", M{"proxy_id": 2})
	status, result = globalCatalogResult(t, h, false)
	if status != 502 || len(arr(result["models"])) != 0 {
		t.Fatal("a different proxy's cache hid the connection failure", status, result)
	}
	h.callsMu.Lock()
	defer h.callsMu.Unlock()
	if len(h.calls) != 0 {
		t.Fatal("unavailable proxy fell back to direct", h.calls)
	}
}

func TestGlobalCatalogProxyRejectsInvalidSelection(t *testing.T) {
	h := newHarness(t, nil)
	h.post(t, "/proxy-pool/manage", M{"action": "create", "name": "disabled", "url": "http://127.0.0.1:1"})
	h.post(t, "/proxy-pool/manage", M{"action": "toggle", "id": 1, "enabled": false})
	for _, body := range []M{{}, {"proxy_id": -1}, {"proxy_id": 1.5}, {"proxy_id": true}, {"proxy_id": "round_robin"}, {"proxy_id": 1}, {"proxy_id": 99}} {
		r, _ := h.request(t, "POST", "/catalog/proxy", body, false)
		route, e := h.app.proxies.CatalogRoute()
		if r.StatusCode != 400 || e != nil || route.ID != 0 {
			t.Fatal("invalid selection changed the catalog route", body, r.StatusCode, route, e)
		}
	}
	for _, key := range []string{"", h.key} {
		r, e := http.NewRequest("POST", h.server.URL+"/catalog/proxy", bytes.NewBufferString(`{"proxy_id":0}`))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		response, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatal("catalog proxy selection allowed access without an admin session", response.StatusCode)
		}
	}
}
