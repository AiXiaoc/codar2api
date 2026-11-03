package gateway

import (
	"errors"
	"net/http"
	"testing"
)

func TestZeroBalanceSchedulingEveryStrategy(t *testing.T) {
	for _, option := range schedulingOptions {
		t.Run(text(option["value"]), func(t *testing.T) {
			s := schedulerStore(t, 100, 1)
			if _, err := s.SaveSettings(M{"scheduling_strategy": option["value"]}); err != nil {
				t.Fatal(err)
			}
			if err := s.Update("0", func(a M) error {
				a["balance"] = M{"agent_token": 0}
				a["free"] = M{"free_until": now() + 3600, "model_ids": []any{"free-model"}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// Seed a sticky session on the zero-balance account using its free model.
			if _, err := s.Pick("free-model", "session", map[string]bool{"1": true}, nil); err != nil {
				t.Fatal(err)
			}
			s.Release("0")
			for i := 0; i < 10; i++ {
				if uid := pickAndRelease(t, s, "session", nil); uid != "1" {
					t.Fatalf("paid request selected depleted account: %s", uid)
				}
			}
			for _, reserve := range []func(string, string, func(string) bool) (M, error){s.Reserve, s.ReserveManual} {
				if _, err := reserve("0", "paid-model", nil); err == nil {
					t.Fatal("explicit paid request bypassed balance check")
				}
				if _, err := reserve("0", "free-model", nil); err != nil {
					t.Fatalf("free request rejected: %v", err)
				}
				s.Release("0")
			}
		})
	}
}

func TestBalanceAccountReadiness(t *testing.T) {
	for _, tc := range []struct {
		name    string
		balance M
		free    M
		allow   bool
		ready   bool
	}{
		{"zero", M{"agent_token": 0}, M{}, true, false},
		{"negative", M{"agent_token": -1}, M{}, true, false},
		{"unknown", M{}, M{}, true, true},
		{"null", M{"agent_token": nil, "token": nil}, M{}, true, true},
		{"general allowed", M{"agent_token": 0, "token": 1}, M{}, true, true},
		{"general disabled", M{"agent_token": 0, "token": 1}, M{}, false, false},
		{"free active", M{"agent_token": 0}, M{"free_until": now() + 3600, "model_ids": []any{"free-model"}}, true, true},
		{"free expired", M{"agent_token": 0}, M{"free_until": now() - 1, "model_ids": []any{"free-model"}}, true, false},
		{"free empty", M{"agent_token": 0}, M{"free_until": now() + 3600}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := schedulerStore(t, 1)
			if _, err := s.SaveSettings(M{"allow_general_balance": tc.allow}); err != nil {
				t.Fatal(err)
			}
			if err := s.Update("0", func(a M) error { a["balance"], a["free"] = tc.balance, tc.free; return nil }); err != nil {
				t.Fatal(err)
			}
			public := s.Public(s.Get("0"), nil)
			if yes(public["ready"]) != tc.ready || !yes(public["enabled"]) {
				t.Fatalf("wrong readiness or enable state: %v", public)
			}
			if !tc.ready && public["reason"] != "余额不足，已暂停调度" {
				t.Fatalf("missing suspension reason: %v", public)
			}
		})
	}
}

func TestBalanceSchedulingStopsAndRecoversAfterRefresh(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, M{"choices": []any{}})
	})
	h.add(t, "empty", M{"balance": M{"agent_token": 0}, "free": M{"free_until": now() + 3600, "model_ids": []any{"free-model"}}})
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses", "/v1/messages/count_tokens"} {
		resp, body := h.request(t, "POST", path, M{"model": "paid-model"}, true)
		if resp.StatusCode != 503 {
			t.Fatalf("%s admitted a paid request: %d %s", path, resp.StatusCode, body)
		}
		h.callsMu.Lock()
		calls := len(h.calls)
		h.callsMu.Unlock()
		if calls != 0 {
			t.Fatal("rejected request reached upstream")
		}
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses", "/v1/messages/count_tokens"} {
		resp, body := h.request(t, "POST", path, M{"model": "free-model"}, true)
		if resp.StatusCode != 200 {
			t.Fatalf("%s rejected a free request: %d %s", path, resp.StatusCode, body)
		}
	}
	if err := h.app.store.Update("empty", func(a M) error { a["free"] = M{}; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.store.Pick("paid-model", "", nil, nil); !errors.Is(err, errNoAccount) {
		t.Fatalf("depleted account is available: %v", err)
	}
	if result := h.post(t, "/accounts/refresh", M{"uid": "empty"}); !yes(result["ok"]) {
		t.Fatalf("balance refresh blocked: %v", result)
	}
	account, err := h.app.store.Pick("paid-model", "", nil, nil)
	if err != nil || text(account["uid"]) != "empty" || !yes(account["enabled"]) {
		t.Fatalf("refreshed balance did not recover scheduling: %v %v", account, err)
	}
	h.app.store.Release("empty")
}
