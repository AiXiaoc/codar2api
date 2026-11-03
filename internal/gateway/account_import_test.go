package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func directImportToken(claims M) string {
	body, _ := json.Marshal(claims)
	return "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(body) + ".c3ludGhldGlj"
}

func TestDirectImportsSucceedWithUpstreamOffline(t *testing.T) {
	h := newHarness(t, nil)
	h.upstream.Close()
	single := h.post(t, "/accounts/add", M{"token": "Bearer " + fakeToken("single"), "note": "直接保存"})
	if !yes(single["ok"]) || obj(single["account"])["uid"] != "single" {
		t.Fatal(single)
	}
	expired := directImportToken(M{"username": "expired", "exp": 1})
	batch := h.post(t, "/accounts/import-batch", M{"text": expired + "\n" + fakeToken("batch")})
	if num(batch["added"]) != 2 || num(batch["failed"]) != 0 {
		t.Fatal(batch)
	}
	file := h.post(t, "/accounts/import", M{"format": exportFormat, "accounts": []any{
		M{"uid": "exported-id", "username": "exported", "display_name": "文件账号", "token": fakeToken("exported"),
			"note": "保留", "enabled": false, "weight": 7, "max_concurrency": 2},
	}})
	if num(file["added"]) != 1 || num(file["failed"]) != 0 {
		t.Fatal(file)
	}
	source := filepath.Join(t.TempDir(), "config.json")
	if err := writeJSON(source, M{"provider": M{"speedai": M{"token": fakeToken("local"), "username": "local"}}}); err != nil {
		t.Fatal(err)
	}
	h.app.cfg.LocalConfig = source
	if local := h.post(t, "/local-login/import", M{}); !yes(local["ok"]) {
		t.Fatal(local)
	}
	reloaded, err := NewStore(h.app.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.List()) != 5 || text(reloaded.Get("expired")["token"]) != expired {
		t.Fatal("direct imports or expired token were not persisted")
	}
	acc := reloaded.Get("exported-id")
	if acc["note"] != "保留" || acc["display_name"] != "文件账号" || yes(acc["enabled"]) || num(acc["weight"]) != 7 || num(acc["max_concurrency"]) != 2 {
		t.Fatal("file metadata was not preserved", acc)
	}
	h.callsMu.Lock()
	defer h.callsMu.Unlock()
	if len(h.calls) != 0 {
		t.Fatal("import contacted upstream", h.calls)
	}
}

func TestDirectImportsBindSelectedProxyWithoutUpstream(t *testing.T) {
	h := newHarness(t, nil)
	h.upstream.Close()
	h.post(t, "/proxy-pool/manage", M{"action": "create", "name": "指定出口", "url": "http://127.0.0.1:18080"})
	boundTo := func(uid string, id int) {
		t.Helper()
		policy := h.app.proxies.Policy(uid)
		if id == 0 {
			if policy["mode"] != "direct" {
				t.Fatalf("%s was not set to direct: %v", uid, policy)
			}
			return
		}
		if policy["mode"] != "pick" || int(num(arr(policy["ids"])[0])) != id {
			t.Fatalf("%s did not bind proxy %d: %v", uid, id, policy)
		}
	}
	h.post(t, "/accounts/add", M{"token": fakeToken("single-proxy"), "proxy_id": 1})
	boundTo("single-proxy", 1)
	h.post(t, "/accounts/add", M{"token": fakeToken("single-proxy"), "proxy_id": 0})
	boundTo("single-proxy", 0)
	batch := h.post(t, "/accounts/import-batch", M{"text": fakeToken("batch-one") + "\n" + fakeToken("batch-two"), "proxy_id": 1})
	if num(batch["added"]) != 2 || num(batch["failed"]) != 0 {
		t.Fatal(batch)
	}
	boundTo("batch-one", 1)
	boundTo("batch-two", 1)
	config := filepath.Join(t.TempDir(), "config.json")
	if err := writeJSON(config, M{"provider": M{"speedai": M{"token": fakeToken("local-proxy"), "username": "local-proxy"}}}); err != nil {
		t.Fatal(err)
	}
	h.app.cfg.LocalConfig = config
	h.post(t, "/local-login/import", M{"proxy_id": 1})
	boundTo("local-proxy", 1)
	resp, _ := h.request(t, "POST", "/accounts/add", M{"token": fakeToken("invalid-proxy"), "proxy_id": 999}, false)
	if resp.StatusCode != 400 || h.app.store.Get("invalid-proxy") != nil {
		t.Fatal("missing proxy was accepted or account was imported")
	}
	h.post(t, "/proxy-pool/manage", M{"action": "toggle", "id": 1, "enabled": false})
	resp, _ = h.request(t, "POST", "/accounts/add", M{"token": fakeToken("disabled-proxy"), "proxy_id": 1}, false)
	if resp.StatusCode != 400 || h.app.store.Get("disabled-proxy") != nil {
		t.Fatal("disabled proxy was accepted or account was imported")
	}
	h.callsMu.Lock()
	defer h.callsMu.Unlock()
	if len(h.calls) != 0 {
		t.Fatal("direct import contacted upstream", h.calls)
	}
}

func TestDirectReimportKeepsExistingIdentityAndState(t *testing.T) {
	h := newHarness(t, nil)
	oldToken := fakeToken("person")
	h.add(t, "42", M{"username": "person", "token": oldToken, "note": "当前备注", "enabled": false,
		"profile": M{"id": 42, "username": "person"}, "balance": M{"agent_token": 123},
		"weight": 8, "max_concurrency": 2, "restricted": true, "stats": M{"requests": 10}})
	newToken := directImportToken(M{"username": "person", "exp": now() + 86400, "nonce": 1})
	result := h.post(t, "/accounts/import", M{"accounts": []any{
		M{"uid": "different-export-id", "token": newToken, "note": "文件备注", "enabled": true, "weight": 1},
	}})
	if num(result["updated"]) != 1 || num(result["added"]) != 0 || len(h.app.store.List()) != 1 {
		t.Fatal(result)
	}
	acc := h.app.store.Get("42")
	if acc["token"] != newToken || acc["note"] != "当前备注" || yes(acc["enabled"]) || !yes(acc["restricted"]) ||
		num(obj(acc["balance"])["agent_token"]) != 123 || num(acc["weight"]) != 8 || num(acc["max_concurrency"]) != 2 ||
		num(obj(acc["stats"])["requests"]) != 10 || num(obj(acc["profile"])["id"]) != 42 {
		t.Fatal("reimport replaced existing local state", acc)
	}
}

func TestParallelDirectImportDeduplicatesIdentity(t *testing.T) {
	h := newHarness(t, nil)
	items := []any{}
	for i := 0; i < 12; i++ {
		items = append(items, M{"uid": "export-" + text(i), "token": directImportToken(M{"username": "same", "nonce": i})})
	}
	result := h.post(t, "/accounts/import", M{"accounts": items})
	if num(result["added"]) != 1 || num(result["updated"]) != 11 || num(result["failed"]) != 0 || len(h.app.store.List()) != 1 {
		t.Fatal("concurrent imports created duplicate accounts", result)
	}
}

func TestDirectImportProfileRefreshLearnsIdentityOnce(t *testing.T) {
	h := newHarness(t, nil)
	var mode atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, username := 42, "person"
		if mode.Load() == 1 {
			id = 43
		}
		if mode.Load() == 2 {
			username = "other"
		}
		jsonResponse(w, 200, M{"data": M{"id": id, "username": username, "display_name": "账号名称"}})
	}))
	defer upstream.Close()
	h.app.cfg.APIBase = upstream.URL
	result := h.post(t, "/accounts/add", M{"token": fakeToken("person")})
	uid := text(obj(result["account"])["uid"])
	acc := h.app.store.Get(uid)
	if errs := h.app.refresh(context.Background(), acc, []string{"profile"}); len(errs) != 0 {
		t.Fatal("first refresh rejected locally derived ID", errs)
	}
	if err := h.app.saveBusiness(acc); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(h.app.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if saved := reloaded.Get(uid); num(obj(saved["profile"])["id"]) != 42 || yes(saved["profile_pending"]) {
		t.Fatal("learned profile identity was not persisted", saved)
	}
	for _, value := range []int32{0, 1, 2} {
		mode.Store(value)
		acc = h.app.store.Get(uid)
		errs := h.app.refresh(context.Background(), acc, []string{"profile"})
		if (len(errs) == 0) != (value == 0) || num(obj(acc["profile"])["id"]) != 42 {
			t.Fatal("refresh did not preserve the learned identity", value, errs)
		}
	}
}

func TestDirectImportWithoutClaimsUsesStableLocalIdentity(t *testing.T) {
	h := newHarness(t, nil)
	token := directImportToken(M{"exp": now() + 86400})
	first := h.post(t, "/accounts/add", M{"token": token})
	second := h.post(t, "/accounts/add", M{"token": token})
	if !yes(second["updated"]) || obj(first["account"])["uid"] != obj(second["account"])["uid"] || len(h.app.store.List()) != 1 {
		t.Fatal("identity-free tokens are not saved or deduplicated", first, second)
	}
}
