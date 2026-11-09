package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"regexp"
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

var identityFillPattern = regexp.MustCompile(`^[A-Za-z]{8,16}$`)
var filledJSONPrompt = regexp.MustCompile(`("(?:text|content|instructions|system)"\s*:\s*")[A-Za-z]{8,16}(You are an AI agent running on the Codar runtime\.|Original\.)`)

func hasRandomIdentityFill(got, want string) bool {
	return strings.HasSuffix(got, want) && identityFillPattern.MatchString(strings.TrimSuffix(got, want))
}

func assertJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	if strings.Contains(want, "__FILL__") {
		got = filledJSONPrompt.ReplaceAllString(got, "${1}__FILL__${2}")
	}
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
			`{"model":"auto","messages":[{"role":"developer","content":[{"type":"text","text":"__FILL__You are an AI agent running on the Codar runtime.\nOriginal ","cache_control":{"type":"ephemeral"}},{"type":"text","text":"rules."}]},{"role":"user","content":"Leave alone"}],"integer":9007199254740993}`,
		},
		{
			"responses instructions and input", "/v1/responses",
			`{"model":"auto","instructions":"Keep this.","input":[{"role":"system","content":[{"type":"input_text","text":"Input rule."}]},{"role":"user","content":"hello"}]}`,
			`{"model":"auto","instructions":"__FILL__You are an AI agent running on the Codar runtime.\nKeep this.","input":[{"role":"system","content":[{"type":"input_text","text":"__FILL__You are an AI agent running on the Codar runtime.\nInput rule."}]},{"role":"user","content":"hello"}]}`,
		},
		{
			"anthropic system blocks", "/v1/messages",
			`{"model":"auto","system":[{"type":"text","text":"Rules.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Other rules."}],"messages":[{"role":"user","content":"hello"}]}`,
			`{"model":"auto","system":[{"type":"text","text":"__FILL__You are an AI agent running on the Codar runtime.\nRules.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Other rules."}],"messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			"anthropic count tokens", "/v1/messages/count_tokens",
			`{"model":"auto","system":"Rules.","messages":[{"role":"user","content":"hello"}]}`,
			`{"model":"auto","system":"__FILL__You are an AI agent running on the Codar runtime.\nRules.","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			"text separated by image and non-text prompt", "/v1/chat/completions",
			`{"model":"auto","messages":[{"role":"system","content":[{"type":"text","text":"First"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}},{"type":"text","text":"Last"}]},{"role":"developer","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`,
			`{"model":"auto","messages":[{"role":"system","content":[{"type":"text","text":"__FILL__You are an AI agent running on the Codar runtime.\nFirst"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}},{"type":"text","text":"Last"}]},{"role":"developer","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]}`,
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
		{false, true, `__FILL__Original.\nRules.`},
		{true, true, `__FILL__You are an AI agent running on the Codar runtime.\nOriginal.\nRules.`},
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
		{"legacy fill", ".\u200bOriginal", codarIdentity + "\nOriginal"},
		{"legacy fill with X", legacyFillX + "Original", codarIdentity + "\nOriginal"},
		{"existing DSH identity replaced", "You are an AI agent powered by DeepSeek Harness.\nRules", codarIdentity + "\nYou are an AI coding agent.\nRules"},
		{"previous Codar prefix with DSH", codarIdentity + "\n" + harnessIdentity + "\nRules", codarIdentity + "\n" + neutralIdentity + "\nRules"},
		{"previous fill and Codar prefix with DSH", legacyFillX + codarIdentity + "\n" + harnessIdentity + "\nRules", codarIdentity + "\n" + neutralIdentity + "\nRules"},
		{"existing Codar identity", codarIdentity + "\nOriginal", codarIdentity + "\nOriginal"},
		{"empty string", "", codarIdentity + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := M{"content": tc.original}
			transformIdentity(prompt, "content", true, true)
			if !hasRandomIdentityFill(text(prompt["content"]), tc.want) {
				t.Fatalf("got %q, want a random fill followed by %q", prompt["content"], tc.want)
			}
		})
	}
	blocks := []any{M{"type": "text", "text": "."}, M{"type": "text", "text": "\u200bOriginal", "cache_control": M{"type": "ephemeral"}}}
	prompt := M{"content": blocks}
	transformIdentity(prompt, "content", true, true)
	if got := text(obj(blocks[0])["text"]) + text(obj(blocks[1])["text"]); !hasRandomIdentityFill(got, codarIdentity+"\nOriginal") {
		t.Fatalf("split prefix changed incorrectly: %#v", blocks)
	}
	if obj(obj(blocks[1])["cache_control"])["type"] != "ephemeral" {
		t.Fatal("split prefix changed block metadata")
	}
}

func TestHarnessIdentityReplacementAcrossFormats(t *testing.T) {
	seen := make(chan string, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen <- string(raw)
		jsonResponse(w, 200, M{"choices": []any{}})
	})
	h.add(t, "a", nil)
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/count_tokens"} {
		t.Run(path, func(t *testing.T) {
			prompt := harnessIdentity + "\nKeep rules."
			payload := M{"model": "auto"}
			if path == "/v1/responses" {
				payload["instructions"] = prompt
				payload["input"] = []any{M{"role": "user", "content": prompt}}
			} else {
				payload["messages"] = []any{M{"role": "user", "content": prompt}}
				if path == "/v1/chat/completions" {
					payload["messages"] = append([]any{M{"role": "developer", "content": prompt}, M{"role": "system", "content": prompt}}, arr(payload["messages"])...)
				} else {
					payload["system"] = []any{M{"type": "text", "text": prompt}}
				}
			}
			raw, _ := json.Marshal(payload)
			identityRequest(t, h, path, string(raw))
			var forwarded M
			if err := json.Unmarshal([]byte(<-seen), &forwarded); err != nil {
				t.Fatal(err)
			}
			want := codarIdentity + "\n" + neutralIdentity + "\nKeep rules."
			switch path {
			case "/v1/responses":
				if !hasRandomIdentityFill(text(forwarded["instructions"]), want) || obj(arr(forwarded["input"])[0])["content"] != prompt {
					t.Fatalf("incorrect instructions or changed user: %#v", forwarded)
				}
			case "/v1/chat/completions":
				messages := arr(forwarded["messages"])
				if !hasRandomIdentityFill(text(obj(messages[0])["content"]), want) || !hasRandomIdentityFill(text(obj(messages[1])["content"]), want) || obj(messages[2])["content"] != prompt {
					t.Fatalf("incorrect roles rewritten: %#v", messages)
				}
			default:
				if !hasRandomIdentityFill(text(obj(arr(forwarded["system"])[0])["text"]), want) || obj(arr(forwarded["messages"])[0])["content"] != prompt {
					t.Fatalf("incorrect system or changed user: %#v", forwarded)
				}
			}
		})
	}
}

func TestHarnessIdentitySplitBlocksAndIndependentSwitches(t *testing.T) {
	for _, fill := range []bool{false, true} {
		for _, rewrite := range []bool{false, true} {
			for _, split := range []bool{false, true} {
				blocks := []any{}
				if split {
					for _, char := range harnessIdentity {
						blocks = append(blocks, M{"type": "text", "text": string(char), "cache_control": M{"type": "ephemeral"}})
					}
				} else {
					blocks = append(blocks, M{"type": "text", "text": harnessIdentity, "cache_control": M{"type": "ephemeral"}})
				}
				blocks = append(blocks, M{"type": "text", "text": "\nKeep " + harnessIdentity})
				prompt := M{"content": blocks}
				transformIdentity(prompt, "content", rewrite, fill)
				var got strings.Builder
				for _, block := range blocks {
					text := obj(block)["text"].(string)
					if text == "" {
						t.Fatal("replacement emptied an existing text block")
					}
					got.WriteString(text)
				}
				want := harnessIdentity + "\nKeep " + harnessIdentity
				if rewrite {
					want = codarIdentity + "\n" + neutralIdentity + "\nKeep " + harnessIdentity
				}
				if (!fill && got.String() != want) || (fill && !hasRandomIdentityFill(got.String(), want)) {
					t.Fatalf("rewrite=%v fill=%v split=%v: got %q, want %q", rewrite, fill, split, got.String(), want)
				}
				for _, block := range blocks[:len(blocks)-1] {
					if obj(obj(block)["cache_control"])["type"] != "ephemeral" {
						t.Fatal("block metadata changed")
					}
				}
				if !fill && transformIdentity(prompt, "content", rewrite, fill) {
					t.Fatal("repeated transform changed prompt")
				}
			}
		}
	}
}
