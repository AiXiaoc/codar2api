package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type streamReadStep struct {
	data string
	err  error
}

type scriptedStream struct {
	steps []streamReadStep
	reads int
}

func (s *scriptedStream) Read(p []byte) (int, error) {
	s.reads++
	if len(s.steps) == 0 {
		return 0, fmt.Errorf("unexpected read after stream completion")
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	return copy(p, step.data), step.err
}

func TestStreamCompletionAndDisconnection(t *testing.T) {
	chunk := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
	usage := "data: {\"choices\":[{\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":3}}\n\n"
	for _, tc := range []struct {
		name        string
		steps       []streamReadStep
		cancel      bool
		completed   bool
		wantError   string
		wantAborted bool
	}{
		{"done-with-open-connection", []streamReadStep{{chunk + usage + "data: [DONE]\n\n", nil}}, false, true, "", false},
		{"done-and-http-truncation", []streamReadStep{{chunk + usage + "data: [DONE]\n\n", io.ErrUnexpectedEOF}}, false, true, "", false},
		{"done-and-client-cancellation", []streamReadStep{{chunk + usage + "data: [DONE]\n\n", context.Canceled}}, true, true, "", false},
		{"split-marker", []streamReadStep{{chunk + usage + "data: [DO", nil}, {"NE]\r\n\r\n", nil}}, false, true, "", false},
		{"upstream-interrupted", []streamReadStep{{chunk, io.ErrUnexpectedEOF}}, false, false, "上游流提前断开（未收到结束标记）", false},
		{"eof-without-marker", []streamReadStep{{chunk, io.EOF}}, false, false, "上游流提前断开（未收到结束标记）", false},
		{"finish-reason-without-marker", []streamReadStep{{chunk + usage, io.ErrUnexpectedEOF}}, false, false, "上游流提前断开（未收到结束标记）", false},
		{"client-stopped-early", []streamReadStep{{chunk, context.Canceled}}, true, false, "客户端已断开", true},
		{"stream-error-before-done", []streamReadStep{{"data: {\"error\":{\"message\":\"generation failed\"}}\n\ndata: [DONE]\n\n", nil}}, false, true, "generation failed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.add(t, "stream", nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			r := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
			w := httptest.NewRecorder()
			body := &scriptedStream{steps: tc.steps}
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(body)}
			row := M{"model": "auto"}
			h.app.pipe(w, r, h.app.store.Get("stream"), resp, row, time.Now())
			if yes(row["stream_completed"]) != tc.completed || text(row["error"]) != tc.wantError || yes(row["client_aborted"]) != tc.wantAborted || yes(row["ok"]) != (tc.wantError == "") {
				t.Fatalf("incorrect result: %v", row)
			}
			want := ""
			for _, step := range tc.steps {
				want += step.data
			}
			if w.Body.String() != want || body.reads != len(tc.steps) {
				t.Fatalf("stream bytes changed or unnecessary read: reads=%d", body.reads)
			}
			if strings.Contains(tc.name, "done-") || tc.name == "split-marker" {
				if num(row["prompt"]) != 20 || num(row["completion"]) != 3 {
					t.Fatalf("final usage lost: %v", row)
				}
			}
		})
	}
}

func TestStreamTerminalEvents(t *testing.T) {
	for _, tc := range []struct {
		data, terminal string
	}{
		{"data: [DONE]\n\n", "[DONE]"},
		{"event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n", "message_stop"},
		{"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":9,\"output_tokens\":2}}}\n\n", "response.completed"},
		{"data: {\"choices\":[{\"delta\":{\"content\":\"[DONE]\"}}]}\n\n", ""},
		{"data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n", ""},
	} {
		tap := newTap(nil)
		for _, b := range []byte(tc.data) {
			tap.Feed([]byte{b})
		}
		tap.Finish()
		if tap.terminal != tc.terminal {
			t.Fatalf("data %q: terminal %q, want %q", tc.data, tap.terminal, tc.terminal)
		}
	}
}
