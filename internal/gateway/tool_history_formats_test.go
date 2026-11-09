package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func decodeToolHistory(t *testing.T, raw string) M {
	t.Helper()
	var p M
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHistoricalToolNativeFormatsForwarding(t *testing.T) {
	const messagesBody = `{
 "model":"auto","extension":9007199254740993,"system":"Rules.",
 "tools":[{"name":"read","input_schema":{"type":"object"}}],
 "messages":[
  {"role":"assistant","content":[{"type":"tool_use","id":"old-a","name":"read","input":{}},{"type":"tool_use","id":"old-b","name":"read","input":{}}]},
  {"role":"user","content":[{"type":"text","text":"before"},{"type":"tool_result","tool_use_id":"old-a","content":"old result"},{"type":"tool_result","tool_use_id":"old-b","content":[{"type":"text","text":"image result"},{"type":"image","extension":9007199254740993,"source":{"type":"base64","media_type":"image/png","data":"AA=="}}],"is_error":true,"cache_control":{"type":"ephemeral"}},{"type":"text","text":"after"}]},
  {"role":"assistant","content":[{"type":"tool_use","id":"new-a","name":"read","input":{}},{"type":"tool_use","id":"new-b","name":"read","input":{}}]},
  {"role":"user","content":[{"type":"text","text":"mixed current text"},{"type":"tool_result","tool_use_id":"new-a","content":"latest a"}]},
  {"role":"user","content":[{"type":"tool_result","tool_use_id":"new-b","content":"latest b"}]}
 ]}`
	const responsesBody = `{
 "model":"auto","extension":9007199254740993,"instructions":"Rules.",
 "tools":[{"type":"function","name":"read","parameters":{"type":"object"}}],
 "input":[
  {"type":"function_call","id":"fc-old-a","call_id":"old-a","name":"read","arguments":"{}"},
  {"type":"function_call_output","id":"output-old-a","call_id":"old-a","output":"old result"},
  {"type":"function_call","call_id":"old-b","name":"read","arguments":"{}"},
  {"type":"function_call_output","call_id":"old-b","output":[{"type":"input_text","text":"image result"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]},
  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},
  {"type":"function_call","call_id":"new-a","name":"read","arguments":"{}"},
  {"type":"function_call","call_id":"new-b","name":"read","arguments":"{}"},
  {"type":"function_call_output","call_id":"new-a","output":"latest a"},
  {"type":"function_call_output","call_id":"new-b","output":"latest b"}
 ]}`
	for _, tc := range []struct{ path, body string }{
		{"/v1/messages", messagesBody}, {"/v1/messages/count_tokens", messagesBody}, {"/v1/responses", responsesBody},
	} {
		t.Run(tc.path, func(t *testing.T) {
			seen := make(chan string, 1)
			h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				seen <- string(raw)
				jsonResponse(w, 200, M{"choices": []any{}})
			})
			h.add(t, "a", nil)
			h.post(t, "/settings", M{"dsh_identity_rewrite": false, "identity_first_line_fill": false})
			identityRequest(t, h, tc.path, tc.body)
			if <-seen != tc.body {
				t.Fatal("disabled feature changed raw request")
			}
			h.post(t, "/settings", M{"historical_tool_as_user": true})
			for _, passthrough := range []bool{true, false} {
				h.post(t, "/settings", M{"auto_passthrough": passthrough})
				identityRequest(t, h, tc.path, tc.body)
				raw := <-seen
				got, original := decodeToolHistory(t, raw), decodeToolHistory(t, tc.body)
				if got["extension"] != json.Number("9007199254740993") || !reflect.DeepEqual(got["tools"], original["tools"]) {
					t.Fatal("tools or numeric precision changed")
				}
				if tc.path == "/v1/responses" {
					items, before := arr(got["input"]), arr(original["input"])
					for _, i := range []int{0, 2, 4, 5, 6, 7, 8} {
						if !reflect.DeepEqual(items[i], before[i]) {
							t.Fatalf("call or latest group changed at %d", i)
						}
					}
					m := obj(items[1])
					if m["type"] != "message" || m["role"] != "user" || m["call_id"] != nil || obj(arr(m["content"])[0])["text"] != "[tool_result id=old-a]\nold result" {
						t.Fatal(m)
					}
					blocks := arr(obj(items[3])["content"])
					if obj(blocks[0])["text"] != "[tool_result id=old-b]" || !reflect.DeepEqual(blocks[1:], arr(obj(before[3])["output"])) {
						t.Fatal("multimodal output was lost")
					}
					if got["instructions"] != "Rules.\n\n"+responsesToolPrompt {
						t.Fatal("wrong Responses prompt")
					}
				} else {
					messages, before := arr(got["messages"]), arr(original["messages"])
					for _, i := range []int{0, 2, 3, 4} {
						if !reflect.DeepEqual(messages[i], before[i]) {
							t.Fatalf("tool_use or latest group changed at %d", i)
						}
					}
					blocks := arr(obj(messages[1])["content"])
					if len(blocks) != 6 || obj(blocks[1])["text"] != "[tool_result id=old-a]\nold result" || obj(blocks[2])["text"] != "[tool_result id=old-b]\n[is_error=true]" {
						t.Fatal(blocks)
					}
					if obj(blocks[4])["type"] != "image" || obj(blocks[4])["extension"] != json.Number("9007199254740993") || obj(obj(blocks[4])["cache_control"])["type"] != "ephemeral" || obj(blocks[5])["text"] != "after" {
						t.Fatal("image, cache boundary or mixed text lost")
					}
					if got["system"] != "Rules.\n\n"+messagesToolPrompt {
						t.Fatal("wrong Messages prompt")
					}
				}
				if transformHistoricalToolReplies(tc.path, got, true) {
					t.Fatal("second pass duplicated conversion or prompt")
				}
			}
		})
	}
}

func TestHistoricalToolNativeFormatsCurrentOnlyAndNoPrompt(t *testing.T) {
	for _, tc := range []struct{ path, body, key, prompt string }{
		{"/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"result"}]}]}`, "system", messagesToolPrompt},
		{"/v1/responses", `{"input":[{"type":"function_call_output","call_id":"a","output":"result"}]}`, "instructions", responsesToolPrompt},
	} {
		p := decodeToolHistory(t, tc.body)
		before, _ := json.Marshal(p)
		if transformHistoricalToolReplies(tc.path, p, true) {
			t.Fatal("current-only results should remain native")
		}
		after, _ := json.Marshal(p)
		if string(before) != string(after) {
			t.Fatal("current-only request was modified")
		}
		if tc.key == "system" {
			p["messages"] = append(arr(p["messages"]), M{"role": "user", "content": "continue"})
		} else {
			p["input"] = append(arr(p["input"]), M{"type": "message", "role": "user", "content": "continue"})
		}
		if !transformHistoricalToolReplies(tc.path, p, true) || p[tc.key] != tc.prompt {
			t.Fatal("history or missing prompt was not converted")
		}
	}
	if transformHistoricalToolReplies("/v1/responses", M{"input": "hello"}, true) {
		t.Fatal("string input changed")
	}
}
