package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelUserAgentForwarding(t *testing.T) {
	seen := make(chan string, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		seen <- r.UserAgent()
		var p M
		_ = json.NewDecoder(r.Body).Decode(&p)
		if yes(p["stream"]) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		jsonResponse(w, 200, M{"choices": []any{M{"message": M{"content": "ok"}}}})
	})
	h.add(t, "a", nil)
	for _, mode := range []struct {
		name  string
		patch M
		want  string
	}{
		{"default", M{}, defaultCodarUA},
		{"edited", M{"codar_ua": "Codar-Custom/1.0"}, "Codar-Custom/1.0"},
		{"disabled", M{"use_codar_ua": false}, "Go-http-client/1.1"},
		{"reenabled", M{"use_codar_ua": true}, "Codar-Custom/1.0"},
	} {
		h.post(t, "/settings", mode.patch)
		for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/count_tokens"} {
			for _, stream := range []bool{false, true} {
				req, _ := http.NewRequest("POST", h.server.URL+path, strings.NewReader(string(mustJSON(M{"model": "auto", "stream": stream}))))
				req.Header.Set("Authorization", "Bearer "+h.key)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("User-Agent", "Incoming-SDK/9.9")
				resp, err := h.client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Fatalf("%s %s stream=%v: %d %s", mode.name, path, stream, resp.StatusCode, body)
				}
				if got := <-seen; got != mode.want {
					t.Fatalf("%s %s stream=%v: UA %q, want %q", mode.name, path, stream, got, mode.want)
				}
			}
		}
		want := mode.want
		if mode.name == "disabled" {
			want = "node"
		}
		h.post(t, "/accounts/test", M{"uid": "a", "model": "auto"})
		if got := <-seen; got != want {
			t.Fatalf("%s panel call: UA %q, want %q", mode.name, got, want)
		}
	}
}

func TestModelUserAgentCatalogAndAccountScope(t *testing.T) {
	seen := make(chan string, 1)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.UserAgent()
		jsonResponse(w, 200, M{"data": []any{}})
	}))
	defer sink.Close()
	h := newHarness(t, nil)
	for _, enabled := range []bool{true, false} {
		h.post(t, "/settings", M{"use_codar_ua": enabled, "codar_ua": "Codar-Custom/2.0"})
		for _, path := range []string{"/v1/codar/models", "/api/user/self", "/v1/daily-sign/status"} {
			if _, err := h.app.requestJSON(context.Background(), "GET", sink.URL+path, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			want := "node"
			if enabled && path == "/v1/codar/models" {
				want = "Codar-Custom/2.0"
			}
			if got := <-seen; got != want {
				t.Fatalf("enabled=%v %s: UA %q, want %q", enabled, path, got, want)
			}
		}
	}
}

func TestCodarUserAgentSettingsValidationAndPersistence(t *testing.T) {
	h := newHarness(t, nil)
	for _, bad := range []any{nil, true, 123, "", "   ", "Codar\r\nX-Injected: yes", "Codar\x00", "Codar\tUA", "中文", strings.Repeat("a", 1025)} {
		before := h.app.store.Settings()
		resp, _ := h.request(t, "POST", "/settings", M{"use_codar_ua": false, "codar_ua": bad}, false)
		if resp.StatusCode != 400 {
			t.Fatalf("accepted invalid UA %q: HTTP %d", bad, resp.StatusCode)
		}
		after := h.app.store.Settings()
		if before["codar_ua"] != after["codar_ua"] || before["use_codar_ua"] != after["use_codar_ua"] {
			t.Fatal("rejected settings changed active values")
		}
	}
	h.post(t, "/settings", M{"use_codar_ua": false, "codar_ua": "  Custom-Codar/3.0  "})
	s, err := NewStore(h.app.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Settings(); got["codar_ua"] != "Custom-Codar/3.0" || yes(got["use_codar_ua"]) {
		t.Fatalf("settings did not survive reload: %v", got)
	}
}
