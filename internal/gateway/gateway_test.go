package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fakeToken(user string) string {
	b, _ := json.Marshal(M{"username": user, "exp": time.Now().Add(7 * 24 * time.Hour).Unix()})
	return "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(b) + ".c3ludGhldGlj"
}

type harness struct {
	app              *App
	server, upstream *httptest.Server
	client           *http.Client
	key              string
	callsMu          sync.Mutex
	calls            []string
}

func newHarness(t *testing.T, handler http.HandlerFunc) *harness {
	t.Helper()
	h := &harness{}
	h.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.callsMu.Lock()
		h.calls = append(h.calls, r.URL.Path)
		h.callsMu.Unlock()
		if handler != nil && isChat(r.URL.Path) {
			handler(w, r)
			return
		}
		switch r.URL.Path {
		case "/v1/codar/models":
			jsonResponse(w, 200, M{"data": []any{M{"id": "auto", "display_name": "Auto", "pricing": M{"input": 2, "output": 8}, "protocol": M{"type": "openai"}}}})
		case "/api/user/self":
			user := text(jwt(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["username"])
			jsonResponse(w, 200, M{"data": M{"id": user, "username": user, "display_name": user}})
		case "/v1/gettoken":
			jsonResponse(w, 200, M{"code": 200, "token": 100, "agent_token": 1000})
		case "/api/codar/activities/free-model":
			jsonResponse(w, 200, M{"data": M{"status": "claimed", "free_until": now() + 86400, "model_ids": []any{"auto"}}})
		case "/api/codar/activities/mine":
			jsonResponse(w, 200, M{"data": []any{}})
		case "/v1/daily-sign/status":
			jsonResponse(w, 200, M{"code": 200, "data": M{"isSigned": true, "retry_after": 86400}})
		case "/v1/agent/ledger":
			jsonResponse(w, 200, M{"code": 200, "data": M{"total": 0, "items": []any{}}})
		default:
			jsonResponse(w, 200, M{"code": 200, "data": M{}})
		}
	}))
	app, _, e := New(Config{DataDir: t.TempDir(), Password: "test-admin-password", APIBase: h.upstream.URL, PayBase: h.upstream.URL, WebBase: h.upstream.URL, NoBackground: true})
	if e != nil {
		t.Fatal(e)
	}
	h.app = app
	h.server = httptest.NewServer(app)
	jar, _ := cookiejar.New(nil)
	h.client = &http.Client{Jar: jar, Timeout: 10 * time.Second}
	resp, _ := h.request(t, "POST", "/auth/login", M{"password": "test-admin-password"}, false)
	if resp.StatusCode != 200 {
		t.Fatal("admin login failed")
	}
	key, e := h.app.auth.KeysAction(M{"action": "create", "name": "tests"})
	if e != nil {
		t.Fatal(e)
	}
	h.key = text(key["token"])
	t.Cleanup(func() {
		h.server.Close()
		if e := h.app.Close(); e != nil {
			t.Error(e)
		}
		h.upstream.Close()
	})
	return h
}
func (h *harness) request(t *testing.T, method, path string, p any, withKey bool) (*http.Response, []byte) {
	t.Helper()
	var body io.Reader
	if p != nil {
		b, _ := json.Marshal(p)
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequest(method, h.server.URL+path, body)
	if e != nil {
		t.Fatal(e)
	}
	if p != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if withKey {
		req.Header.Set("Authorization", "Bearer "+h.key)
	}
	resp, e := h.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	return resp, b
}
func (h *harness) post(t *testing.T, path string, p M) M {
	t.Helper()
	resp, b := h.request(t, "POST", path, p, false)
	if resp.StatusCode != 200 {
		t.Fatalf("%s: HTTP %d: %s", path, resp.StatusCode, b)
	}
	var out M
	if e := json.Unmarshal(b, &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func (h *harness) add(t *testing.T, id string, fields M) {
	t.Helper()
	a := M{"uid": id, "username": id, "token": fakeToken(id), "enabled": true, "balance": M{"agent_token": 100}, "free": M{}, "stats": M{}}
	for k, v := range fields {
		a[k] = v
	}
	if _, e := h.app.store.Upsert(a); e != nil {
		t.Fatal(e)
	}
}
func decode(t *testing.T, b []byte) M {
	t.Helper()
	var d M
	if e := json.Unmarshal(b, &d); e != nil {
		t.Fatalf("decode %s: %v", b, e)
	}
	return d
}
func TestIndependentAuthenticationAndKeyScope(t *testing.T) {
	h := newHarness(t, nil)
	req, _ := http.NewRequest("GET", h.server.URL+"/accounts", nil)
	req.Header.Set("X-Hub-Internal", "anything")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("Hub header bypassed independent auth")
	}
	resp, _ = h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, false)
	if resp.StatusCode != 401 {
		t.Fatal("admin cookie authorized public API")
	}
	req, _ = http.NewRequest("GET", h.server.URL+"/keys", nil)
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("API key authorized administration")
	}
	resp, b := h.request(t, "GET", "/v1/models", nil, true)
	if resp.StatusCode != 200 || len(arr(decode(t, b)["data"])) != 1 {
		t.Fatal("model listing failed")
	}
	key := h.post(t, "/keys", M{"action": "create", "name": "limited", "models": []any{"allowed"}})
	req, _ = http.NewRequest("POST", h.server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"auto"}`))
	req.Header.Set("x-api-key", text(key["token"]))
	resp, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("model restriction not enforced")
	}
	for _, file := range []string{"keys.json", "admin.json"} {
		b, e := os.ReadFile(filepath.Join(h.app.cfg.DataDir, file))
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(b, []byte("test-admin-password")) {
			t.Fatal("plaintext admin password stored")
		}
	}
}
func TestKeyCanBeCopiedAgain(t *testing.T) {
	h := newHarness(t, nil)
	id := h.app.auth.Keys()[0]["id"]
	if _, b := h.request(t, "GET", "/keys", nil, false); bytes.Contains(b, []byte(h.key)) || !bytes.Contains(b, []byte(`"copyable":true`)) {
		t.Fatalf("key list leaks the key or hides that it can be copied: %s", b)
	}
	for i := 0; i < 2; i++ {
		if r := h.post(t, "/keys", M{"action": "reveal", "id": id}); r["token"] != h.key {
			t.Fatalf("copy %d returned %v", i+1, r)
		}
	}
	h.app.auth.mu.Lock()
	delete(h.app.auth.keys[0], "token") // a key created before keys were stored
	h.app.auth.mu.Unlock()
	if resp, b := h.request(t, "POST", "/keys", M{"action": "reveal", "id": id}, false); resp.StatusCode != 400 || !strings.Contains(string(b), "旧版本") {
		t.Fatalf("old key should explain it cannot be copied: %d %s", resp.StatusCode, b)
	}
	if resp, _ := h.request(t, "GET", "/v1/models", nil, true); resp.StatusCode != 200 {
		t.Fatal("old key without stored value stopped working")
	}
}
func TestKeyModelScopeCanBeEdited(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, M{"choices": []any{}}) })
	h.add(t, "a", nil)
	key := h.app.auth.Keys()[0]
	call := func() int {
		resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
		return resp.StatusCode
	}
	h.post(t, "/keys", M{"action": "update", "id": key["id"], "models": []any{"other", "other"}})
	if call() != 403 || len(arr(h.app.auth.Keys()[0]["models"])) != 1 {
		t.Fatal("model scope not applied or not deduplicated")
	}
	h.post(t, "/keys", M{"action": "update", "id": key["id"], "models": []any{}})
	if call() != 200 {
		t.Fatal("empty scope should allow every model")
	}
	if resp, _ := h.request(t, "POST", "/keys", M{"action": "update", "id": key["id"], "models": "auto"}, false); resp.StatusCode != 400 {
		t.Fatal("non-list scope accepted")
	}
}
func TestDefaultPasswordOnlyOnFirstStart(t *testing.T) {
	dir := t.TempDir()
	a, initial, e := NewAuth(dir, "", "admin", false)
	if e != nil || initial != "admin" || !equal(passwordHash("admin", text(a.credentials["salt"])), text(a.credentials["hash"])) {
		t.Fatalf("default password not applied: %q %v", initial, e)
	}
	if a, initial, e = NewAuth(dir, "", "other-default", false); e != nil || initial != "" || !equal(passwordHash("admin", text(a.credentials["salt"])), text(a.credentials["hash"])) {
		t.Fatal("default password replaced an existing admin password")
	}
	if _, _, e = NewAuth(t.TempDir(), "short", "admin", false); e == nil {
		t.Fatal("short explicit password accepted")
	}
	if _, initial, e = NewAuth(t.TempDir(), "", "", false); e != nil || len(initial) != 24 {
		t.Fatal("random password not generated without a default")
	}
}
func TestSessionOriginLogoutAndPassword(t *testing.T) {
	h := newHarness(t, nil)
	req, _ := http.NewRequest("POST", h.server.URL+"/keys", strings.NewReader(`{"action":"create","name":"csrf"}`))
	req.Header.Set("Origin", "https://evil.example")
	resp, e := h.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("foreign origin allowed")
	}
	h.post(t, "/auth/password", M{"current_password": "test-admin-password", "password": "new-admin-password"})
	resp, _ = h.request(t, "GET", "/accounts", nil, false)
	if resp.StatusCode != 401 {
		t.Fatal("password change did not revoke session")
	}
	resp, _ = h.request(t, "POST", "/auth/login", M{"password": "new-admin-password"}, false)
	if resp.StatusCode != 200 {
		t.Fatal("new password failed")
	}
	h.post(t, "/auth/logout", M{})
	resp, _ = h.request(t, "GET", "/accounts", nil, false)
	if resp.StatusCode != 401 {
		t.Fatal("logout failed")
	}
}
func TestRestrictedAccountRetryAndPersistence(t *testing.T) {
	var bad, good atomic.Int32
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		user := text(jwt(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["username"])
		if user == "bad" {
			bad.Add(1)
			jsonResponse(w, 403, M{"detail": M{"code": "account_restricted", "message": "API access is restricted. Please contact support."}})
			return
		}
		good.Add(1)
		jsonResponse(w, 200, M{"choices": []any{M{"message": M{"content": "ok"}}}, "usage": M{"prompt_tokens": 12, "completion_tokens": 3}})
	})
	h.add(t, "bad", nil)
	h.add(t, "good", nil)
	for i := 0; i < 2; i++ {
		resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto", "messages": []any{M{"role": "user", "content": "hello"}}}, true)
		if resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode)
		}
	}
	if bad.Load() != 1 || good.Load() != 2 {
		t.Fatalf("unexpected upstream calls: %d / %d", bad.Load(), good.Load())
	}
	if !yes(h.app.store.Get("bad")["restricted"]) {
		t.Fatal("restriction missing")
	}
	reloaded, e := NewStore(h.app.cfg.DataDir)
	if e != nil {
		t.Fatal(e)
	}
	if !yes(reloaded.Get("bad")["restricted"]) {
		t.Fatal("restriction not persisted")
	}
	h.post(t, "/accounts/update", M{"uid": "bad", "enabled": true, "reset": true})
	if !yes(h.app.store.Get("bad")["restricted"]) {
		t.Fatal("enable/reset cleared restriction")
	}
	before := bad.Load()
	result := h.post(t, "/accounts/test", M{"uid": "bad"})
	if yes(result["ok"]) || bad.Load() != before {
		t.Fatal("panel test used restricted account")
	}
	h.post(t, "/accounts/batch", M{"action": "clear_restriction", "uids": []any{"bad"}})
	if yes(h.app.store.Get("bad")["restricted"]) {
		t.Fatal("manual clear failed")
	}
}
func TestGeneric403DoesNotRestrictOrRetry(t *testing.T) {
	var calls atomic.Int32
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		jsonResponse(w, 403, M{"error": M{"code": "model_forbidden", "message": "not permitted"}})
	})
	h.add(t, "a", nil)
	h.add(t, "b", nil)
	resp, _ := h.request(t, "POST", "/v1/messages", M{"model": "auto"}, true)
	if resp.StatusCode != 403 || calls.Load() != 1 {
		t.Fatal("generic 403 retried")
	}
	for _, a := range h.app.store.List() {
		if yes(a["restricted"]) {
			t.Fatal("generic 403 flagged account")
		}
	}
}
func TestSSEPassThroughAndRestriction(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":20,\"prompt_tokens_details\":{\"cached_tokens\":5},\"completion_tokens\":3}}\n\ndata: {\"error\":{\"code\":\"account_restricted\",\"message\":\"restricted\"}}\n\ndata: [DONE]\n\n"
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < len(stream); i += 7 {
			_, _ = io.WriteString(w, stream[i:min(i+7, len(stream))])
			w.(http.Flusher).Flush()
		}
	})
	h.add(t, "stream", nil)
	resp, b := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto", "stream": true}, true)
	if resp.StatusCode != 200 || string(b) != stream {
		t.Fatal("SSE bytes modified")
	}
	if !yes(h.app.store.Get("stream")["restricted"]) {
		t.Fatal("SSE did not flag account")
	}
	stats := h.app.usage.Stats(0, h.app.cost)
	total := obj(stats["total"])
	if num(total["prompt"]) != 20 || num(total["cached"]) != 5 || num(total["completion"]) != 3 || num(total["failed"]) != 1 {
		t.Fatalf("bad stream stats: %v", total)
	}
	resp, _ = h.request(t, "POST", "/v1/responses", M{"model": "auto"}, true)
	if resp.StatusCode != 503 {
		t.Fatal("restricted account reused")
	}
}
func TestHTTP200RestrictionAndOrdinaryText(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       M
		restricted bool
	}{{"business", M{"detail": M{"code": "account_restricted", "message": "restricted"}}, true}, {"ordinary", M{"choices": []any{M{"message": M{"content": "account_restricted is a code"}}}}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, tc.body) })
			h.add(t, "a", nil)
			h.request(t, "POST", "/v1/responses", M{"model": "auto"}, true)
			if yes(h.app.store.Get("a")["restricted"]) != tc.restricted {
				t.Fatal("wrong restriction state")
			}
		})
	}
}
func TestRetryBoundariesAndCountTokens(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status, want int
		body         M
	}{{"rate", 429, 2, M{"error": M{"message": "too many requests"}}}, {"channel", 503, 1, M{"error": M{"message": "No available channel"}}}, {"request", 400, 1, M{"error": M{"message": "invalid model"}}}} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1); jsonResponse(w, tc.status, tc.body) })
			h.add(t, "a", nil)
			h.add(t, "b", nil)
			h.request(t, "POST", "/v1/messages/count_tokens", M{"model": "auto"}, true)
			if n.Load() != int32(tc.want) {
				t.Fatalf("%d attempts", n.Load())
			}
			if num(obj(h.app.usage.Stats(0, h.app.cost)["total"])["requests"]) != 0 {
				t.Fatal("count_tokens counted in stats")
			}
		})
	}
}
func TestAccountRequestRecordsKeepSwitchedAttempts(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if text(jwt(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["username"]) == "a" {
			jsonResponse(w, 429, M{"error": M{"message": "too many requests"}})
			return
		}
		jsonResponse(w, 200, M{"choices": []any{}, "usage": M{"prompt_tokens": 30, "prompt_tokens_details": M{"cached_tokens": 10}, "completion_tokens": 2}})
	})
	h.add(t, "a", nil)
	h.add(t, "b", nil)
	for i := 0; i < 2; i++ {
		if resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto", "messages": []any{M{"role": "user", "content": "hi"}}}, true); resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode)
		}
	}
	records := func(query string) M {
		t.Helper()
		resp, b := h.request(t, "GET", "/accounts/requests?"+query, nil, false)
		if resp.StatusCode != 200 {
			t.Fatalf("HTTP %d: %s", resp.StatusCode, b)
		}
		return decode(t, b)
	}
	a := records("uid=a")
	first := obj(arr(a["items"])[0])
	if num(a["requests"]) != 1 || num(a["failed"]) != 1 || num(first["status"]) != 429 || first["error"] != "too many requests" || !yes(first["switched"]) || first["prompt"] != nil {
		t.Fatalf("switched failure not recorded for a: %v", a)
	}
	b := records("uid=b")
	latest, switched := obj(arr(b["items"])[0]), obj(arr(b["items"])[1])
	if num(b["requests"]) != 2 || num(b["succeeded"]) != 2 || !yes(latest["ok"]) || num(latest["prompt"]) != 30 || num(latest["cached"]) != 10 || yes(switched["switched"]) || num(switched["prompt"]) != 30 {
		t.Fatalf("final attempts wrong for b: %v", b)
	}
	if d := records("uid=b&failed=1"); num(d["total"]) != 0 || len(arr(d["items"])) != 0 {
		t.Fatalf("failed filter returned successes: %v", d)
	}
	if resp, _ := h.request(t, "GET", "/accounts/requests?uid=missing", nil, false); resp.StatusCode != 404 {
		t.Fatal("unknown account accepted")
	}
	if e := h.app.usage.Flush(); e != nil {
		t.Fatal(e)
	}
	reloaded, e := NewUsageLog(h.app.cfg.DataDir)
	if e != nil {
		t.Fatal(e)
	}
	if d := reloaded.Requests("a", "a", false, 1, 20); num(d["failed"]) != 1 {
		t.Fatalf("switched attempt not persisted: %v", d)
	}
}
func TestAccountExportImportRoundTrip(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "a", M{"note": "主力", "enabled": false, "login_via": "wechat"})
	h.add(t, "b", nil)
	file := h.post(t, "/accounts/export", M{})
	if file["format"] != exportFormat || len(arr(file["accounts"])) != 2 || !bytes.Contains(mustJSON(file), []byte(fakeToken("a"))) {
		t.Fatalf("export incomplete: %v", file)
	}
	if one := h.post(t, "/accounts/export", M{"uids": []any{"b"}}); len(arr(one["accounts"])) != 1 {
		t.Fatal("export ignored the selection")
	}
	h.post(t, "/accounts/batch", M{"action": "delete", "all": true})
	file["accounts"] = append(arr(file["accounts"]), M{"token": "not-a-token"}, fakeToken("c"))
	r := h.post(t, "/accounts/import", file)
	if num(r["added"]) != 3 || num(r["failed"]) != 1 {
		t.Fatalf("import result wrong: %v", r)
	}
	a := h.app.store.Get("a")
	if a == nil || yes(a["enabled"]) || a["note"] != "主力" || a["login_via"] != "wechat" || !yes(h.app.store.Get("b")["enabled"]) {
		t.Fatalf("exported settings not restored: %v", a)
	}
	h.post(t, "/accounts/update", M{"uid": "a", "enabled": true})
	if r = h.post(t, "/accounts/import", file); num(r["updated"]) != 3 || !yes(h.app.store.Get("a")["enabled"]) {
		t.Fatalf("re-import should update tokens and keep current settings: %v", r)
	}
	if resp, _ := h.request(t, "POST", "/accounts/import", M{"format": "other", "accounts": []any{fakeToken("a")}}, false); resp.StatusCode != 400 {
		t.Fatal("foreign file accepted")
	}
}
func TestPanelCallUsesPromptAndModel(t *testing.T) {
	var got M
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		jsonResponse(w, 200, M{"choices": []any{M{"message": M{"content": "回答"}}}, "usage": M{"prompt_tokens": 9, "prompt_tokens_details": M{"cached_tokens": 4}, "completion_tokens": 2}})
	})
	h.add(t, "a", nil)
	r := h.post(t, "/accounts/test", M{"uid": "a", "model": "auto", "prompt": "  讲个笑话  ", "max_tokens": 512})
	msg := obj(arr(got["messages"])[0])
	if !yes(r["ok"]) || r["reply"] != "回答" || msg["content"] != "讲个笑话" || num(got["max_tokens"]) != 512 || num(obj(r["usage"])["cached"]) != 4 {
		t.Fatalf("call wrong: result %v, upstream body %v", r, got)
	}
	h.post(t, "/accounts/test", M{"uid": "a"})
	if obj(arr(got["messages"])[0])["content"] != "Hi" || num(got["max_tokens"]) != 16 {
		t.Fatalf("quick test defaults changed: %v", got)
	}
	if r = h.post(t, "/accounts/test", M{"uid": "a", "max_tokens": 99999}); yes(r["ok"]) {
		t.Fatal("max_tokens limit not enforced")
	}
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func TestRequestsWithoutSessionRotateAccounts(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[text(jwt(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["username"])]++
		mu.Unlock()
		jsonResponse(w, 200, M{"output": []any{}})
	})
	h.add(t, "a", nil)
	h.add(t, "b", nil)
	for i := 0; i < 4; i++ {
		h.request(t, "POST", "/v1/responses", M{"model": "auto", "input": []any{M{"role": "user", "content": fmt.Sprint("question ", i)}}}, true)
		h.request(t, "POST", "/v1/messages/count_tokens", M{"model": "auto"}, true)
	}
	if seen["a"] == 0 || seen["b"] == 0 {
		t.Fatalf("requests without a session id stuck to one account: %v", seen)
	}
	seen = map[string]int{}
	for i := 0; i < 4; i++ {
		h.request(t, "POST", "/v1/responses", M{"model": "auto", "input": "same conversation", "prompt_cache_key": "conv-1"}, true)
	}
	if len(seen) != 1 {
		t.Fatalf("same session moved between accounts: %v", seen)
	}
}
func TestBatchAtomicValidationAndPersistence(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "a", M{"restricted": true, "restricted_reason": "restricted"})
	h.add(t, "b", M{"enabled": false})
	for _, p := range []M{{"action": "delete", "uids": []any{}}, {"action": "disable", "uids": []any{"a", "missing"}}, {"action": "enable", "all": "true"}, {"action": "delete", "all": true, "uids": []any{"a"}}} {
		resp, _ := h.request(t, "POST", "/accounts/batch", p, false)
		if resp.StatusCode != 400 {
			t.Fatal("invalid batch accepted")
		}
	}
	if len(h.app.store.List()) != 2 || !yes(h.app.store.Get("a")["enabled"]) {
		t.Fatal("invalid batch mutated data")
	}
	h.post(t, "/accounts/batch", M{"action": "disable", "uids": []any{"a", "a"}})
	if yes(h.app.store.Get("a")["enabled"]) || !yes(h.app.store.Get("a")["restricted"]) {
		t.Fatal("disable changed restriction")
	}
	h.post(t, "/accounts/batch", M{"action": "clear_restriction", "all": true})
	if yes(h.app.store.Get("a")["enabled"]) {
		t.Fatal("clear enabled account")
	}
	h.post(t, "/accounts/batch", M{"action": "delete", "uids": []any{"b"}})
	s, e := NewStore(h.app.cfg.DataDir)
	if e != nil {
		t.Fatal(e)
	}
	if len(s.List()) != 1 || s.Get("b") != nil {
		t.Fatal("delete not persisted")
	}
}
func TestTokenReimportPreservesRestrictionAndPreferences(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "a", M{"restricted": true, "restricted_reason": "restriction", "enabled": false, "note": "keep", "stats": M{"requests": 50}})
	result := h.post(t, "/accounts/add", M{"token": fakeToken("a")})
	if !yes(result["ok"]) || !yes(result["updated"]) {
		t.Fatal(result)
	}
	acc := h.app.store.Get("a")
	if !yes(acc["restricted"]) || yes(acc["enabled"]) || acc["note"] != "keep" || num(obj(acc["stats"])["requests"]) != 50 {
		t.Fatal("import cleared protected fields")
	}
}
func TestClientCancellationStopsUpstream(t *testing.T) {
	cancelled := make(chan struct{})
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	})
	h.add(t, "a", nil)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", h.server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"auto","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, e := h.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	buf := make([]byte, 16)
	_, _ = resp.Body.Read(buf)
	cancel()
	resp.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream was not cancelled")
	}
}
func TestNormalizedUsageProtocols(t *testing.T) {
	cases := []struct {
		u       M
		p, c, o float64
	}{{M{"prompt_tokens": 100, "prompt_tokens_details": M{"cached_tokens": 20}, "completion_tokens": 10}, 100, 20, 10}, {M{"input_tokens": 100, "input_tokens_details": M{"cached_tokens": 30}, "output_tokens": 10}, 100, 30, 10}, {M{"input_tokens": 50, "cache_read_input_tokens": 30, "cache_creation_input_tokens": 20, "output_tokens": 10}, 100, 30, 10}}
	for _, tc := range cases {
		v := normalizeUsage(tc.u)
		if num(v["prompt"]) != tc.p || num(v["cached"]) != tc.c || num(v["completion"]) != tc.o {
			t.Fatal(v)
		}
	}
}
func TestManagementPagesAndNoSecretLeaks(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "secret-user", nil)
	for _, path := range []string{"/assets/../defaults.json", "/assets/login.html", "/assets/missing.js"} {
		if resp, _ := h.request(t, "GET", path, nil, false); resp.StatusCode != 404 {
			t.Fatalf("%s served: HTTP %d", path, resp.StatusCode)
		}
	}
	for _, path := range []string{"/", "/assets/app.css", "/assets/app2.css", "/assets/ui.js", "/assets/accounts.js", "/assets/drawer.js", "/assets/add.js", "/assets/manage.js", "/assets/tools.js", "/assets/pages.js", "/accounts", "/settings", "/keys", "/proxy-pool", "/stats", "/credit-rate", "/announcements", "/catalog", "/prices"} {
		resp, b := h.request(t, "GET", path, nil, false)
		if resp.StatusCode != 200 {
			t.Fatalf("%s HTTP %d: %s", path, resp.StatusCode, b)
		}
		if bytes.Contains(b, []byte(fakeToken("secret-user"))) || bytes.Contains(b, []byte(h.key)) {
			t.Fatalf("secret leaked at %s", path)
		}
	}
}
