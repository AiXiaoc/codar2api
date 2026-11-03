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
	tests := []struct{ name, path, body, want string }{
		{
			"chat developer blocks", "/v1/chat/completions",
			`{"model":"auto","messages":[{"role":"developer","content":[{"type":"text","text":"Original ","cache_control":{"type":"ephemeral"}},{"type":"text","text":"rules."}]},{"role":"user","content":"Leave alone"}],"integer":9007199254740993}`,
			`{"model":"auto","messages":[{"role":"developer","content":[{"type":"text","text":".\u200bXYou are an AI agent running on the Codar runtime.\nOriginal ","cache_control":{"type":"ephemeral"}},{"type":"text","text":"rules."}]},{"role":"user","content":"Leave alone"}],"integer":9007199254740993}`,
		},
		{
			"responses instructions and input", "/v1/responses",
			`{"model":"auto","instructions":"Keep this.","input":[{"role":"system","content":[{"type":"input_text","text":"Input rule."}]},{"role":"user","content":"hello"}]}`,
			`{"model":"auto","instructions":".\u200bXYou are an AI agent running on the Codar runtime.\nKeep this.","input":[{"role":"system","content":[{"type":"input_text","text":".\u200bXYou are an AI agent running on the Codar runtime.\nInput rule."}]},{"role":"user","content":"hello"}]}`,
		},
		{
			"anthropic system blocks", "/v1/messages",
			`{"model":"auto","system":[{"type":"text","text":"Rules.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Other rules."}],"messages":[{"role":"user","content":"hello"}]}`,
			`{"model":"auto","system":[{"type":"text","text":".\u200bXYou are an AI agent running on the Codar runtime.\nRules.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Other rules."}],"messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			"anthropic count tokens", "/v1/messages/count_tokens",
			`{"model":"auto","system":"Rules.","messages":[{"role":"user","content":"hello"}]}`,
			`{"model":"auto","system":".\u200bXYou are an AI agent running on the Codar runtime.\nRules.","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			"text separated by image and non-text prompt", "/v1/chat/completions",
			`{"model":"auto","messages":[{"role":"system","content":[{"type":"text","text":"First"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}},{"type":"text","text":"Last"}]},{"role":"developer","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`,
			`{"model":"auto","messages":[{"role":"system","content":[{"type":"text","text":".\u200bXYou are an AI agent running on the Codar runtime.\nFirst"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}},{"type":"text","text":"Last"}]},{"role":"developer","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { identityRequest(t, h, tc.path, tc.body); assertJSONEqual(t, <-seen, tc.want) })
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
	body := `{ "model":"auto", "messages":[{"role":"system","content":"Original.\nRules."}], "n":9007199254740993 }`
	for _, tc := range []struct {
		rewrite, fill bool
		want          string
	}{
		{false, false, `Original.\nRules.`},
		{true, false, `You are an AI agent running on the Codar runtime.\nOriginal.\nRules.`},
		{false, true, `.\u200bXOriginal.\nRules.`},
		{true, true, `.\u200bXYou are an AI agent running on the Codar runtime.\nOriginal.\nRules.`},
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
}

func TestIdentitySettingsDefaultValidationAndPersistence(t *testing.T) {
	h := newHarness(t, nil)
	for _, key := range []string{"dsh_identity_rewrite", "identity_first_line_fill"} {
		if got := h.app.store.Settings()[key]; got != true {
			t.Fatalf("%s should default to true, got %#v", key, got)
		}
	}
	resp, _ := h.request(t, "POST", "/settings", M{"identity_first_line_fill": "false"}, false)
	if resp.StatusCode != http.StatusBadRequest || h.app.store.Settings()["identity_first_line_fill"] != true {
		t.Fatal("invalid option value should be rejected")
	}
	h.post(t, "/settings", M{"dsh_identity_rewrite": false, "identity_first_line_fill": false})
	reloaded, err := NewStore(h.app.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"dsh_identity_rewrite", "identity_first_line_fill"} {
		if reloaded.Settings()[key] != false {
			t.Fatalf("saved disabled option %s was not preserved", key)
		}
	}
}

func TestIdentityExistingPrefixesAndEmptyPrompt(t *testing.T) {
	for _, tc := range []struct{ name, original, want string }{
		{"legacy fill", ".\u200bOriginal", ".\u200bX" + codarIdentity + "\nOriginal"},
		{"current fill", identityFill + "Original", identityFill + codarIdentity + "\nOriginal"},
		{"existing DSH identity retained", "You are an AI agent powered by DeepSeek Harness.\nRules", identityFill + codarIdentity + "\nYou are an AI agent powered by DeepSeek Harness.\nRules"},
		{"already transformed", identityFill + codarIdentity + "\nOriginal", identityFill + codarIdentity + "\nOriginal"},
		{"empty string", "", identityFill + codarIdentity + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := M{"content": tc.original}
			transformIdentity(prompt, "content", true, true)
			if prompt["content"] != tc.want {
				t.Fatalf("got %q, want %q", prompt["content"], tc.want)
			}
			if transformIdentity(prompt, "content", true, true) {
				t.Fatal("second transform changed prompt")
			}
		})
	}
	blocks := []any{M{"type": "text", "text": "."}, M{"type": "text", "text": "\u200bOriginal", "cache_control": M{"type": "ephemeral"}}}
	prompt := M{"content": blocks}
	transformIdentity(prompt, "content", true, true)
	if obj(blocks[0])["text"] != "." || obj(blocks[1])["text"] != "\u200bX"+codarIdentity+"\nOriginal" {
		t.Fatalf("split prefix changed incorrectly: %#v", blocks)
	}
	if transformIdentity(prompt, "content", true, true) {
		t.Fatal("split prefix should not be added twice")
	}
}
