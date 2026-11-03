package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRestrictedRequestStopsRetryAndPersistsBlock(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			var bad, good atomic.Int32
			h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				user := text(jwt(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["username"])
				if user == "bad" {
					bad.Add(1)
					jsonResponse(w, 403, M{"detail": M{"code": "account_restricted", "message": "restricted"}})
					return
				}
				good.Add(1)
				jsonResponse(w, 200, M{"choices": []any{}})
			})
			h.add(t, "bad", nil)
			h.add(t, "good", nil)
			if yes(h.app.store.Settings()["restricted_request_block"]) {
				t.Fatal("must default to disabled")
			}
			h.post(t, "/settings", M{"restricted_request_block": true})
			payload := M{"model": "auto", "messages": []any{M{"role": "user", "content": "blocked content"}}}
			ask := func(body M) int {
				resp, _ := h.request(t, "POST", path, body, true)
				return resp.StatusCode
			}
			if ask(payload) != 403 || bad.Load() != 1 || good.Load() != 0 || !yes(h.app.store.Get("bad")["restricted"]) {
				t.Fatal("restricted request retried or account not restricted")
			}
			if ask(payload) != 403 || bad.Load() != 1 || good.Load() != 0 {
				t.Fatal("blocked request reached another account")
			}
			reloaded, err := newRequestBlocklist(h.app.cfg.DataDir)
			if err != nil || !reloaded.Has(requestFingerprint(path, payload)) || reloaded.Count() != 1 {
				t.Fatal("block did not survive reload", err)
			}
			if ask(M{"model": "auto", "messages": []any{M{"role": "user", "content": "different"}}}) != 200 {
				t.Fatal("different content was blocked")
			}
			h.post(t, "/settings", M{"restricted_request_block": false})
			if ask(payload) != 200 || h.app.requestBlocks.Count() != 1 {
				t.Fatal("disabled switch must bypass but retain blocks")
			}
			h.post(t, "/settings", M{"restricted_request_block": true})
			before := good.Load()
			if ask(payload) != 403 || good.Load() != before {
				t.Fatal("reenabling did not restore block")
			}
			h.post(t, "/request-blocklist/clear", M{})
			if ask(payload) != 200 || !yes(h.app.store.Get("bad")["restricted"]) {
				t.Fatal("clear should unblock request and retain account restriction")
			}
			reloaded, err = newRequestBlocklist(h.app.cfg.DataDir)
			if err != nil || reloaded.Count() != 0 {
				t.Fatal("clear was not persisted", err)
			}
			resp, _ := h.request(t, "POST", "/settings", M{"restricted_request_block": "true"}, false)
			if resp.StatusCode != 400 {
				t.Fatal("invalid switch accepted")
			}
		})
	}
}

func TestRequestBlockFingerprintUsesOriginalJSON(t *testing.T) {
	decode := func(raw string) M {
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		var value M
		if err := dec.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	a := decode(`{"model":"auto", "messages":[{"role":"user","content":"same"}], "extension":9007199254740993}`)
	b := decode(`{ "extension":9007199254740993,"messages":[{"content":"same","role":"user"}],"model":"auto" }`)
	id := requestFingerprint("/v1/chat/completions", a)
	if id != requestFingerprint("/v1/chat/completions", b) {
		t.Fatal("JSON formatting bypassed block")
	}
	if id == requestFingerprint("/v1/messages", b) {
		t.Fatal("protocol paths must be distinct")
	}
	b["extension"] = json.Number("9007199254740992")
	if id == requestFingerprint("/v1/chat/completions", b) {
		t.Fatal("numeric precision lost")
	}
}

func TestRequestBlacklistIgnoresUnrelated403(t *testing.T) {
	for _, html := range []bool{false, true} {
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
			if html {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(403)
				_, _ = w.Write([]byte("<html>403 Forbidden nginx</html>"))
				return
			}
			jsonResponse(w, 403, M{"error": M{"code": "model_forbidden", "message": "not allowed"}})
		})
		h.add(t, "a", nil)
		h.post(t, "/settings", M{"restricted_request_block": true, "ip_block": false})
		resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
		if resp.StatusCode != 403 || h.app.requestBlocks.Count() != 0 || yes(h.app.store.Get("a")["restricted"]) {
			t.Fatal("unrelated 403 triggered request blacklist")
		}
	}
}
