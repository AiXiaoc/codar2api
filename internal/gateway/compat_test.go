package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestHarnessIdentitySentToUpstream(t *testing.T) {
	seen := make(chan string, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		var body M
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		seen <- text(obj(arr(body["messages"])[0])["content"])
		jsonResponse(w, 200, M{"choices": []any{}})
	})
	h.add(t, "a", nil)
	original := "You are an AI agent powered by DeepSeek Harness.\n\nYou are a coding agent powered by the deepseek-v4.1-flash model."
	want := "You are an AI coding agent." + strings.TrimPrefix(original, "You are an AI agent powered by DeepSeek Harness.")
	for _, enabled := range []bool{true, false} {
		h.post(t, "/settings", M{"dsh_identity_rewrite": enabled, "auto_passthrough": true})
		resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto", "messages": []any{M{"role": "developer", "content": original}}}, true)
		got := <-seen
		expected := original
		if enabled {
			expected = want
		}
		if resp.StatusCode != 200 || got != expected {
			t.Fatalf("enabled=%v status=%d got=%q", enabled, resp.StatusCode, got)
		}
	}
}
