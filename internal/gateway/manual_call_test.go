package gateway

import (
	"net/http"
	"strings"
	"testing"
)

func TestDisabledAccountAllowsManualTestsWithoutEnteringAutomaticPool(t *testing.T) {
	seen := make(chan string, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		seen <- text(jwt(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["username"])
		jsonResponse(w, 200, M{"choices": []any{M{"message": M{"content": "ok"}}}})
	})
	h.add(t, "disabled", M{"enabled": false})
	for _, body := range []M{{"uid": "disabled"}, {"uid": "disabled", "model": "auto", "prompt": "manual prompt", "max_tokens": 32}} {
		result := h.post(t, "/accounts/test", body)
		if !yes(result["ok"]) || result["reply"] != "ok" || <-seen != "disabled" {
			t.Fatalf("disabled account manual test rejected: %v", result)
		}
		if yes(h.app.store.Get("disabled")["enabled"]) {
			t.Fatal("manual test enabled the account")
		}
		waitConcurrency(t, h.app.store, "disabled", 0)
	}
	resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
	if resp.StatusCode != 503 || len(seen) != 0 {
		t.Fatal("automatic request used a disabled account")
	}
	h.add(t, "active", nil)
	for i := 0; i < 3; i++ {
		resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
		if resp.StatusCode != 200 || <-seen != "active" {
			t.Fatal("disabled account entered automatic scheduling")
		}
	}
}

func TestManualDisabledAccountStillChecksLimitsAndRestrictions(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, M{"choices": []any{}})
	})
	h.add(t, "disabled", M{"enabled": false, "max_concurrency": 1})
	if _, err := h.app.store.ReserveManual("disabled", "auto", nil); err != nil {
		t.Fatal(err)
	}
	result := h.post(t, "/accounts/test", M{"uid": "disabled"})
	if yes(result["ok"]) || !strings.Contains(text(result["error"]), "并发") {
		t.Fatal("manual disabled request bypassed concurrency")
	}
	h.app.store.Release("disabled")
	for _, test := range []struct {
		id, want string
		fields   M
	}{
		{"restricted", "账号受限", M{"restricted": true}},
		{"invalid", "令牌无效", M{"token_invalid": true}},
		{"missing", "没有令牌", M{"token": ""}},
		{"cooling", "冷却中", M{"cooldown_until": now() + 60}},
		{"balance", "余额不足", M{"balance": M{"agent_token": 0}}},
	} {
		test.fields["enabled"] = false
		h.add(t, test.id, test.fields)
		r := h.post(t, "/accounts/test", M{"uid": test.id, "model": "auto"})
		if yes(r["ok"]) || !strings.Contains(text(r["error"]), test.want) {
			t.Fatalf("%s bypassed manual checks: %v", test.id, r)
		}
		waitConcurrency(t, h.app.store, test.id, 0)
	}
	if err := h.app.proxies.Block(Route{Name: "直连"}, "blocked test exit"); err != nil {
		t.Fatal(err)
	}
	result = h.post(t, "/accounts/test", M{"uid": "disabled"})
	if yes(result["ok"]) || !strings.Contains(text(result["error"]), "封控") {
		t.Fatal("manual disabled request bypassed exit blocking")
	}
	h.callsMu.Lock()
	defer h.callsMu.Unlock()
	if len(h.calls) != 0 {
		t.Fatal("rejected manual requests reached upstream")
	}
}
