package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func identityRequest(t *testing.T, h *harness, path, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	response, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: HTTP %d: %s", path, resp.StatusCode, response)
	}
}

func assertJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	decode := func(raw string) any {
		var v any
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return v
	}
	if actual, expected := decode(got), decode(want); !reflect.DeepEqual(actual, expected) {
		t.Fatalf("forwarded request mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func TestIdentityRulesAcrossModelFormats(t *testing.T) {
	seen := make(chan string, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen <- string(raw)
		jsonResponse(w, 200, M{"choices": []any{}})
	})
	h.add(t, "a", nil)
	h.post(t, "/settings", M{"dsh_identity_rewrite": true, "identity_first_line_fill": true})

	tests := []struct {
		name, path, body, want string
	}{
		{
			"chat text blocks split across prefix", "/v1/chat/completions",
			`{"model":"auto","messages":[{"role":"developer","content":[{"type":"text","text":"You are an AI agent powered by DeepSeek ","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Harness.\nBe precise."}]},{"role":"user","content":"You are an AI agent powered by DeepSeek Harness."}],"tools":[{"description":"You are an AI agent powered by DeepSeek Harness."}],"integer":9007199254740993}`,
			`{"model":"auto","messages":[{"role":"developer","content":[{"type":"text","text":".\u200bYou are an AI coding agent.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"\nBe precise."}]},{"role":"user","content":"You are an AI agent powered by DeepSeek Harness."}],"tools":[{"description":"You are an AI agent powered by DeepSeek Harness."}],"integer":9007199254740993}`,
		},
		{
			"responses instructions and input", "/v1/responses",
			`{"model":"auto","instructions":"You are an AI agent powered by DeepSeek Harness.\nKeep this.","input":[{"role":"system","content":[{"type":"input_text","text":"You are an AI agent powered by DeepSeek Harness.\nInput rule."}]},{"role":"user","content":"You are an AI agent powered by DeepSeek Harness."}]}`,
			`{"model":"auto","instructions":".\u200bYou are an AI coding agent.\nKeep this.","input":[{"role":"system","content":[{"type":"input_text","text":".\u200bYou are an AI coding agent.\nInput rule."}]},{"role":"user","content":"You are an AI agent powered by DeepSeek Harness."}]}`,
		},
		{
			"anthropic system blocks", "/v1/messages",
			`{"model":"auto","system":[{"type":"text","text":"You are an AI agent powered by DeepSeek Harness.\nRules.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Other rules."}],"messages":[{"role":"user","content":"You are an AI agent powered by DeepSeek Harness."}]}`,
			`{"model":"auto","system":[{"type":"text","text":".\u200bYou are an AI coding agent.\nRules.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Other rules."}],"messages":[{"role":"user","content":"You are an AI agent powered by DeepSeek Harness."}]}`,
		},
		{
			"split identity does not create an empty block", "/v1/messages",
			`{"model":"auto","system":[{"type":"text","text":"You are an AI agent powered by DeepSeek "},{"type":"text","text":"Harness.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hello"}]}`,
			`{"model":"auto","system":[{"type":"text","text":".\u200bYou are an AI coding agent"},{"type":"text","text":".","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			"anthropic count tokens", "/v1/messages/count_tokens",
			`{"model":"auto","system":"You are an AI agent powered by DeepSeek Harness.\nRules.","messages":[{"role":"user","content":"hello"}]}`,
			`{"model":"auto","system":".\u200bYou are an AI coding agent.\nRules.","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			"generic system prompt", "/v1/chat/completions",
			`{"model":"auto","messages":[{"role":"system","content":"Other identity\nSecond line."},{"role":"assistant","content":"You are an AI agent powered by DeepSeek Harness."}]}`,
			`{"model":"auto","messages":[{"role":"system","content":".\u200bOther identity\nSecond line."},{"role":"assistant","content":"You are an AI agent powered by DeepSeek Harness."}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			identityRequest(t, h, tc.path, tc.body)
			assertJSONEqual(t, <-seen, tc.want)
		})
	}
}

func TestIdentityOptionsAreIndependentAndPreserveRawPassthrough(t *testing.T) {
	seen := make(chan string, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen <- string(raw)
		jsonResponse(w, 200, M{"choices": []any{}})
	})
	h.add(t, "a", nil)
	body := `{ "model":"auto", "messages":[{"role":"system","content":"You are an AI agent powered by DeepSeek Harness.\nRules."}], "n":9007199254740993 }`
	for _, tc := range []struct {
		rewrite, fill bool
		want          string
	}{
		{false, false, `You are an AI agent powered by DeepSeek Harness.\nRules.`},
		{true, false, `You are an AI coding agent.\nRules.`},
		{false, true, `.\u200bYou are an AI agent powered by DeepSeek Harness.\nRules.`},
		{true, true, `.\u200bYou are an AI coding agent.\nRules.`},
	} {
		h.post(t, "/settings", M{"dsh_identity_rewrite": tc.rewrite, "identity_first_line_fill": tc.fill})
		identityRequest(t, h, "/v1/chat/completions", body)
		got := <-seen
		if !tc.rewrite && !tc.fill && got != body {
			t.Fatal("disabled rules should preserve raw bytes")
		}
		want := `{"model":"auto","messages":[{"role":"system","content":"` + tc.want + `"}],"n":9007199254740993}`
		assertJSONEqual(t, got, want)
	}

	h.post(t, "/settings", M{"dsh_identity_rewrite": true, "identity_first_line_fill": false})
	unmatched := `{ "model":"auto", "messages":[{"role":"user","content":"You are an AI agent powered by DeepSeek Harness."}] }`
	identityRequest(t, h, "/v1/chat/completions", unmatched)
	if got := <-seen; got != unmatched {
		t.Fatalf("unmatched request lost original bytes: %s", got)
	}
}

func TestIdentityFillSettingsDefaultValidationAndPersistence(t *testing.T) {
	h := newHarness(t, nil)
	if got := h.app.store.Settings()["identity_first_line_fill"]; got != false {
		t.Fatalf("new option should default to false, got %#v", got)
	}
	resp, _ := h.request(t, "POST", "/settings", M{"identity_first_line_fill": "true"}, false)
	if resp.StatusCode != http.StatusBadRequest || h.app.store.Settings()["identity_first_line_fill"] != false {
		t.Fatal("invalid option value should be rejected")
	}
	h.post(t, "/settings", M{"identity_first_line_fill": true})
	reloaded, err := NewStore(h.app.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Settings()["identity_first_line_fill"] != true {
		t.Fatal("new option was not persisted")
	}
}

func TestIdentityAlreadyPrefixedAndEmptyPrompts(t *testing.T) {
	seen := make(chan string, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen <- string(raw)
		jsonResponse(w, 200, M{"choices": []any{}})
	})
	h.add(t, "a", nil)
	h.post(t, "/settings", M{"dsh_identity_rewrite": true, "identity_first_line_fill": true})
	body := `{"model":"auto","messages":[{"role":"system","content":".\u200bYou are an AI agent powered by DeepSeek Harness.\nRules."},{"role":"developer","content":""}]}`
	want := `{"model":"auto","messages":[{"role":"system","content":".\u200bYou are an AI coding agent.\nRules."},{"role":"developer","content":""}]}`
	identityRequest(t, h, "/v1/chat/completions", body)
	assertJSONEqual(t, <-seen, want)
}

func TestIdentityDoesNotJoinAcrossNonTextBlocks(t *testing.T) {
	seen := make(chan string, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen <- string(raw)
		jsonResponse(w, 200, M{"choices": []any{}})
	})
	h.add(t, "a", nil)
	h.post(t, "/settings", M{"dsh_identity_rewrite": true, "identity_first_line_fill": true})
	body := `{"model":"auto","messages":[{"role":"system","content":[{"type":"text","text":"You are an AI agent "},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}},{"type":"text","text":"powered by DeepSeek Harness."}]},{"role":"developer","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`
	want := `{"model":"auto","messages":[{"role":"system","content":[{"type":"text","text":".\u200bYou are an AI agent "},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}},{"type":"text","text":"powered by DeepSeek Harness."}]},{"role":"developer","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`
	identityRequest(t, h, "/v1/chat/completions", body)
	assertJSONEqual(t, <-seen, want)
}
