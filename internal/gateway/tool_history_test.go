package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestHistoricalToolRepliesForwarding(t *testing.T) {
	seen := make(chan string, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen <- string(raw)
		jsonResponse(w, 200, M{"choices": []any{}})
	})
	h.add(t, "a", nil)
	h.post(t, "/settings", M{"dsh_identity_rewrite": false, "identity_first_line_fill": false})
	body := `{ "model":"auto", "integer":9007199254740993, "messages":[{"role":"developer","content":"Rules."},{"role":"assistant","content":null,"tool_calls":[{"id":"old-a","type":"function","function":{"name":"read_file","arguments":"{}"}},{"id":"old-b","type":"function","function":{"name":"search","arguments":"{}"}}]},{"role":"tool","tool_call_id":"old-a","content":"first result"},{"role":"tool","tool_call_id":"old-b","content":[{"type":"text","text":"second result"}]},{"role":"assistant","tool_calls":[{"id":"new-a","function":{"name":"read_file"}},{"id":"new-b","function":{"name":"search"}}]},{"role":"tool","tool_call_id":"new-a","content":"latest a"},{"role":"tool","tool_call_id":"new-b","content":"latest b"}] }`
	if yes(h.app.store.Settings()["historical_tool_as_user"]) {
		t.Fatal("option must default to disabled")
	}
	identityRequest(t, h, "/v1/chat/completions", body)
	if got := <-seen; got != body {
		t.Fatal("disabled option changed raw passthrough")
	}
	h.post(t, "/settings", M{"historical_tool_as_user": true})
	for _, passthrough := range []bool{true, false} {
		h.post(t, "/settings", M{"auto_passthrough": passthrough})
		identityRequest(t, h, "/v1/chat/completions", body)
		raw := <-seen
		if !strings.Contains(raw, "9007199254740993") {
			t.Fatal("conversion lost numeric precision")
		}
		var got, original M
		_ = json.Unmarshal([]byte(raw), &got)
		_ = json.Unmarshal([]byte(body), &original)
		messages, before := arr(got["messages"]), arr(original["messages"])
		if obj(messages[0])["content"] != "Rules.\n\n"+historicalToolPrompt {
			t.Fatal("missing appended prompt", messages[0])
		}
		if obj(messages[2])["role"] != "user" || obj(messages[2])["content"] != "[tool_result id=old-a name=read_file]\nfirst result" || obj(messages[2])["tool_call_id"] != nil {
			t.Fatal("historical result not converted", messages[2])
		}
		blocks := arr(obj(messages[3])["content"])
		if obj(messages[3])["role"] != "user" || obj(blocks[0])["text"] != "[tool_result id=old-b name=search]" || !reflect.DeepEqual(blocks[1], obj(before[3])["content"].([]any)[0]) {
			t.Fatal("content blocks were lost", messages[3])
		}
		for _, i := range []int{1, 4, 5, 6} {
			if !reflect.DeepEqual(messages[i], before[i]) {
				t.Fatalf("assistant calls or current tool group changed at %d", i)
			}
		}
	}
	reloaded, err := NewStore(h.app.cfg.DataDir)
	if err != nil || !yes(reloaded.Settings()["historical_tool_as_user"]) {
		t.Fatal("setting was not persisted", err)
	}
	resp, _ := h.request(t, "POST", "/settings", M{"historical_tool_as_user": "true"}, false)
	if resp.StatusCode != 400 {
		t.Fatal("non-boolean setting accepted")
	}
}

func TestHistoricalToolRepliesPromptAndBoundaryCases(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prompt any
	}{
		{"string", "Rules.\n"},
		{"blocks", []any{M{"type": "text", "text": "Rules.", "cache_control": M{"type": "ephemeral"}}}},
		{"no text blocks", []any{M{"type": "image_url", "image_url": M{"url": "test"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := M{"messages": []any{M{"role": "system", "content": tc.prompt}, M{"role": "tool", "tool_call_id": "orphan", "name": "fallback", "content": "result"}, M{"role": "user", "content": "continue"}}}
			if !transformHistoricalToolReplies("/v1/chat/completions", p, true) {
				t.Fatal("all tools before a final user message are historical")
			}
			result := obj(arr(p["messages"])[1])
			if result["content"] != "[tool_result id=orphan name=fallback]\nresult" || result["name"] != nil {
				t.Fatal(result)
			}
			before, _ := json.Marshal(p)
			if transformHistoricalToolReplies("/v1/chat/completions", p, true) {
				t.Fatal("repeated conversion changed the request")
			}
			after, _ := json.Marshal(p)
			if string(before) != string(after) || strings.Count(string(after), "Historical tool calls appear") != 1 {
				t.Fatal("prompt duplicated or payload changed")
			}
		})
	}
	p := M{"messages": []any{M{"role": "tool", "content": "new result"}}}
	if transformHistoricalToolReplies("/v1/chat/completions", p, true) {
		t.Fatal("latest-only tool group must remain untouched")
	}
	p = M{"messages": []any{M{"role": "tool", "content": nil}, M{"role": "assistant", "content": "done"}}}
	if !transformHistoricalToolReplies("/v1/chat/completions", p, true) || obj(arr(p["messages"])[0])["content"] != historicalToolPrompt {
		t.Fatal("missing system prompt was not created")
	}
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/responses"} {
		if transformHistoricalToolReplies(path, p, true) {
			t.Fatal("unrelated protocol was transformed")
		}
	}
}
