package gateway

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type modelReply struct {
	status      int
	contentType string
	body        string
}

func TestReturnedModelsInRequestRecords(t *testing.T) {
	replies := make(chan modelReply, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		reply := <-replies
		w.Header().Set("Content-Type", reply.contentType)
		w.WriteHeader(reply.status)
		_, _ = fmt.Fprint(w, reply.body)
	})
	h.add(t, "a", nil)

	tests := []struct {
		name, path, requestModel, contentType, body, returned string
		stream, hasModel                                      bool
		status                                                int
	}{
		{"chat response", "/v1/chat/completions", "auto", "application/json", `{"model":"other-model","choices":[]}`, "other-model", false, true, 200},
		{"identical", "/v1/chat/completions", "MODEL", "application/json", `{"model":"MODEL","choices":[]}`, "MODEL", false, true, 200},
		{"case difference", "/v1/chat/completions", "MODEL", "application/json", `{"model":"model","choices":[]}`, "model", false, true, 200},
		{"request whitespace", "/v1/chat/completions", " MODEL ", "application/json", `{"model":"MODEL","choices":[]}`, "MODEL", false, true, 200},
		{"response whitespace", "/v1/chat/completions", "MODEL", "application/json", `{"model":"MODEL ","choices":[]}`, "MODEL ", false, true, 200},
		{"empty returned model", "/v1/chat/completions", "MODEL", "application/json", `{"model":"","choices":[]}`, "", false, true, 200},
		{"no returned model", "/v1/chat/completions", "MODEL", "application/json", `{"choices":[]}`, "", false, false, 200},
		{"non-string returned model", "/v1/chat/completions", "MODEL", "application/json", `{"model":7,"choices":[]}`, "", false, false, 200},
		{"chat stream", "/v1/chat/completions", "auto", "text/event-stream", "data: {\"model\":\"chat-model\",\"choices\":[]}\n\ndata: [DONE]\n\n", "chat-model", true, true, 200},
		{"responses response", "/v1/responses", "auto", "application/json", `{"model":"responses-model","output":[]}`, "responses-model", false, true, 200},
		{"responses stream latest", "/v1/responses", "auto", "text/event-stream", "data: {\"type\":\"response.created\",\"response\":{\"model\":\"early\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"final\"}}\n\n", "final", true, true, 200},
		{"messages response", "/v1/messages", "auto", "application/json", `{"model":"claude-model","content":[]}`, "claude-model", false, true, 200},
		{"messages stream", "/v1/messages", "auto", "text/event-stream", "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-stream\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", "claude-stream", true, true, 200},
		{"error response", "/v1/chat/completions", "auto", "application/json", `{"model":"rejected","error":{"message":"bad request"}}`, "rejected", false, true, 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			replies <- modelReply{tc.status, tc.contentType, tc.body}
			resp, body := h.request(t, "POST", tc.path, M{"model": tc.requestModel, "stream": tc.stream}, true)
			if resp.StatusCode != tc.status || string(body) != tc.body {
				t.Fatalf("response changed: HTTP %d %q", resp.StatusCode, body)
			}
			stats := h.app.usage.Stats(0, h.app.cost)
			recent := arr(stats["recent"])
			if len(recent) == 0 {
				t.Fatal("request was not recorded")
			}
			row := obj(recent[0])
			if row["requested_model"] != tc.requestModel {
				t.Fatalf("requested model was not preserved exactly: %#v", row)
			}
			actual, ok := row["response_model"]
			if ok != tc.hasModel || ok && actual != tc.returned {
				t.Fatalf("recent returned model = %#v, present=%v; want %q, present=%v", actual, ok, tc.returned, tc.hasModel)
			}
			response, data := h.request(t, "GET", "/accounts/requests?uid=a", nil, false)
			if response.StatusCode != 200 {
				t.Fatalf("account requests: HTTP %d: %s", response.StatusCode, data)
			}
			accountRow := obj(arr(decode(t, data)["items"])[0])
			actual, ok = accountRow["response_model"]
			if accountRow["requested_model"] != tc.requestModel || ok != tc.hasModel || ok && actual != tc.returned {
				t.Fatalf("account record has incorrect models: %#v", accountRow)
			}
		})
	}
	if err := h.app.usage.Flush(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewUsageLog(h.app.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	row := obj(arr(reloaded.Requests("a", "a", false, 1, 20)["items"])[0])
	if row["requested_model"] != "auto" || row["response_model"] != "rejected" {
		t.Fatalf("response model did not survive reload: %#v", row)
	}
}

func TestReturnedModelBelongsToItsOwnRetry(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if text(jwt(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["username"]) == "a" {
			jsonResponse(w, 429, M{"model": "first-model", "error": M{"message": "rate limited"}})
			return
		}
		jsonResponse(w, 200, M{"model": "final-model", "choices": []any{}})
	})
	h.add(t, "a", nil)
	h.add(t, "b", nil)
	resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	first := obj(arr(h.app.usage.Requests("a", "a", false, 1, 20)["items"])[0])
	last := obj(arr(h.app.usage.Requests("b", "b", false, 1, 20)["items"])[0])
	recent := obj(arr(h.app.usage.Stats(0, h.app.cost)["recent"])[0])
	if first["response_model"] != "first-model" || !yes(first["switched"]) || last["response_model"] != "final-model" || recent["response_model"] != "final-model" {
		t.Fatalf("models attributed to the wrong account: first=%v, last=%v, recent=%v", first, last, recent)
	}
}

func TestReturnedModelDoesNotLeakAcrossRetries(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if text(jwt(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["username"]) == "a" {
			jsonResponse(w, 429, M{"model": "first-model", "error": M{"message": "rate limited"}})
			return
		}
		jsonResponse(w, 200, M{"choices": []any{}})
	})
	h.add(t, "a", nil)
	h.add(t, "b", nil)
	resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	first := obj(arr(h.app.usage.Requests("a", "a", false, 1, 20)["items"])[0])
	last := obj(arr(h.app.usage.Requests("b", "b", false, 1, 20)["items"])[0])
	recent := obj(arr(h.app.usage.Stats(0, h.app.cost)["recent"])[0])
	if first["response_model"] != "first-model" {
		t.Fatalf("failed attempt lost its model: %v", first)
	}
	if _, ok := last["response_model"]; ok {
		t.Fatalf("model leaked to the next account: %v", last)
	}
	if _, ok := recent["response_model"]; ok {
		t.Fatalf("model leaked to the final result: %v", recent)
	}
}

func TestReturnedModelForPanelCalls(t *testing.T) {
	replies := make(chan modelReply, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		reply := <-replies
		w.Header().Set("Content-Type", reply.contentType)
		w.WriteHeader(reply.status)
		_, _ = fmt.Fprint(w, reply.body)
	})
	h.add(t, "a", nil)
	for _, tc := range []struct {
		status   int
		body     string
		returned string
	}{
		{200, `{"model":"panel-model","choices":[{"message":{"content":"ok"}}]}`, "panel-model"},
		{400, `{"model":"panel-rejected","error":{"message":"bad request"}}`, "panel-rejected"},
	} {
		replies <- modelReply{tc.status, "application/json", tc.body}
		h.post(t, "/accounts/test", M{"uid": "a", "model": "auto", "prompt": "hi"})
		account := obj(arr(h.app.usage.Requests("a", "a", false, 1, 20)["items"])[0])
		recent := obj(arr(h.app.usage.Stats(0, h.app.cost)["recent"])[0])
		if account["requested_model"] != "auto" || account["response_model"] != tc.returned || recent["response_model"] != tc.returned || recent["source"] != "panel" {
			t.Fatalf("panel model missing: account=%v recent=%v", account, recent)
		}
	}
}

func TestReturnedModelSurvivesSplitStreamEvents(t *testing.T) {
	tap := newTap(nil)
	stream := "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-returned\"}}\n\n"
	for _, b := range []byte(stream) {
		tap.Feed([]byte{b})
	}
	tap.Finish()
	if !tap.hasModel || tap.model != "claude-returned" {
		t.Fatalf("model missing from split stream: %#v", tap)
	}
}
