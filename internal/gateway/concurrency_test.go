package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitConcurrency(t *testing.T, s *Store, uid string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, row := range s.Concurrency() {
			if row["uid"] == uid && int(num(row["current_concurrency"])) == want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("account %s concurrency did not reach %d: %v", uid, want, s.Concurrency())
}

func streamRequest(t *testing.T, h *harness) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", h.server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"auto","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	return resp, cancel
}

func TestStreamConcurrencyCapacityAndRelease(t *testing.T) {
	finish := make(chan struct{})
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
			io.WriteString(w, "data: [DONE]\n\n")
		case <-r.Context().Done():
		}
	})
	defer close(finish)
	h.add(t, "a", M{"max_concurrency": 1})
	resp, cancel := streamRequest(t, h)
	defer cancel()
	defer resp.Body.Close()
	waitConcurrency(t, h.app.store, "a", 1)
	full, body := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
	if full.StatusCode != 429 || full.Header.Get("Retry-After") == "" || !strings.Contains(string(body), "并发") {
		t.Fatalf("full account not rejected cleanly: %d %s", full.StatusCode, body)
	}
	if r := h.post(t, "/accounts/test", M{"uid": "a"}); yes(r["ok"]) || !strings.Contains(text(r["error"]), "并发") {
		t.Fatal("panel test bypassed concurrency limit")
	}
	_, body = h.request(t, "GET", "/accounts", nil, false)
	account := obj(arr(decode(t, body)["accounts"])[0])
	if num(account["current_concurrency"]) != 1 || yes(account["ready"]) {
		t.Fatal("account view omitted full state")
	}
	if a := h.app.store.Get("a"); yes(a["restricted"]) || num(a["cooldown_until"]) > 0 {
		t.Fatal("local concurrency rejection penalized account")
	}
	cancel()
	resp.Body.Close()
	waitConcurrency(t, h.app.store, "a", 0)
	// A new stream is admitted after cancellation; a protocol terminator frees it too.
	resp2, cancel2 := streamRequest(t, h)
	defer cancel2()
	defer resp2.Body.Close()
	finish <- struct{}{}
	if _, err := io.ReadAll(resp2.Body); err != nil {
		t.Fatal(err)
	}
	waitConcurrency(t, h.app.store, "a", 0)
}

func TestLeastConnectionsUsesLiveStreamCounts(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	h.add(t, "a", M{"max_concurrency": 1})
	h.add(t, "b", M{"max_concurrency": 1})
	h.post(t, "/settings", M{"scheduling_strategy": "least_connections"})
	first, cancel1 := streamRequest(t, h)
	defer cancel1()
	defer first.Body.Close()
	second, cancel2 := streamRequest(t, h)
	defer cancel2()
	defer second.Body.Close()
	if first.Header.Get("X-Codar-Account") != "a" || second.Header.Get("X-Codar-Account") != "b" {
		t.Fatal("least connections did not spread active streams")
	}
	waitConcurrency(t, h.app.store, "a", 1)
	waitConcurrency(t, h.app.store, "b", 1)
	full, _ := h.request(t, "POST", "/v1/messages/count_tokens", M{"model": "auto"}, true)
	if full.StatusCode != 429 {
		t.Fatal("count_tokens bypassed full accounts")
	}
	cancel1()
	first.Body.Close()
	waitConcurrency(t, h.app.store, "a", 0)
	third, cancel3 := streamRequest(t, h)
	defer cancel3()
	defer third.Body.Close()
	if third.Header.Get("X-Codar-Account") != "a" {
		t.Fatal("released account did not re-enter scheduling")
	}
}

func TestRetryAndPanelCallsReleaseConcurrency(t *testing.T) {
	var calls atomic.Int32
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if text(jwt(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["username"]) == "a" {
			jsonResponse(w, 500, M{"error": M{"message": "temporary failure"}})
			return
		}
		jsonResponse(w, 200, M{"choices": []any{M{"message": M{"content": "ok"}}}})
	})
	h.add(t, "a", M{"max_concurrency": 1})
	h.add(t, "b", M{"max_concurrency": 1})
	resp, _ := h.request(t, "POST", "/v1/responses", M{"model": "auto"}, true)
	if resp.StatusCode != 200 || calls.Load() != 2 {
		t.Fatal("retry did not reach second account")
	}
	waitConcurrency(t, h.app.store, "a", 0)
	waitConcurrency(t, h.app.store, "b", 0)
	if r := h.post(t, "/accounts/test", M{"uid": "b"}); !yes(r["ok"]) {
		t.Fatal(r)
	}
	waitConcurrency(t, h.app.store, "b", 0)
}

func TestFailedUpstreamAttemptsReleaseConcurrency(t *testing.T) {
	for _, mode := range []string{"http-error", "incomplete-stream", "network-error"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				if mode == "http-error" {
					jsonResponse(w, 403, M{"error": M{"message": "client rejected"}})
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
			})
			h.add(t, "a", M{"max_concurrency": 1})
			h.post(t, "/settings", M{"max_attempts": 1})
			if mode == "network-error" {
				h.upstream.Close()
			}
			h.request(t, "POST", "/v1/chat/completions", M{"model": "auto", "stream": true}, true)
			waitConcurrency(t, h.app.store, "a", 0)
		})
	}
}
