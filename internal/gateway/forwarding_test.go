package gateway

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type forwardedRequest struct {
	body, query, host string
	header            http.Header
	length            int64
}

func TestAutomaticPassthroughPreservesBodyQueryAndOrdinaryHeaders(t *testing.T) {
	seen := make(chan forwardedRequest, 1)
	responseBody := "{\n  \"choices\": [], \"extension\": 9007199254740993\n}\n"
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen <- forwardedRequest{string(body), r.URL.RawQuery, r.Host, r.Header.Clone(), r.ContentLength}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, responseBody)
	})
	h.add(t, "a", nil)
	h.post(t, "/settings", M{"use_codar_ua": false})
	if !yes(h.app.store.Settings()["auto_passthrough"]) {
		t.Fatal("automatic passthrough is not enabled by default")
	}
	query := "beta=raw%2Fvalue&tag=a+b&tag=a%20b&api_key=local-key&api%5Fkey=other&access_token=secret&tail=%2f"
	wantQuery := "beta=raw%2Fvalue&tag=a+b&tag=a%20b&tail=%2f"
	body := "{\n \"model\":\"auto\", \"messages\":[], \"extension\":{\"integer\":9007199254740993,\"escaped\":\"\\u0061\"}, \"stream_options\":{\"include_usage\":false}\n}\n"
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/count_tokens"} {
		req, _ := http.NewRequest("POST", h.server.URL+path+"?"+query, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.key)
		req.Header.Set("X-Api-Key", h.key)
		req.Header.Set("Api-Key", h.key)
		req.Header.Set("Proxy-Authorization", "Basic local-secret")
		req.Header.Set("Cookie", "local-session=private")
		req.Header.Set("Connection", "X-Remove, Anthropic-Beta")
		req.Header.Set("X-Remove", "private-connection-value")
		req.Header.Set("Anthropic-Beta", "removed-with-connection")
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("User-Agent", "Caller-SDK/1.0")
		req.Header.Set("OpenAI-Beta", "responses=v1")
		req.Header.Set("X-Stainless-Lang", "python")
		req.Header.Set("X-Codar-Internal", "private")
		req.Header.Set("X-Forwarded-For", "192.0.2.1")
		req.Header.Add("X-Extension", "first")
		req.Header.Add("X-Extension", "second")
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		reply, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(reply) != responseBody {
			t.Fatalf("%s response changed: %d %s", path, resp.StatusCode, reply)
		}
		got := <-seen
		upstream, _ := url.Parse(h.upstream.URL)
		if got.body != body || got.query != wantQuery || got.host != upstream.Host || got.length != int64(len(body)) {
			t.Fatalf("%s body, query, target or length changed: %+v", path, got)
		}
		for _, name := range []string{"X-Api-Key", "Api-Key", "Cookie", "Proxy-Authorization", "Connection", "X-Remove", "Anthropic-Beta", "X-Codar-Internal", "X-Forwarded-For"} {
			if got.header.Get(name) != "" {
				t.Fatalf("%s leaked header %s", path, name)
			}
		}
		if got.header.Get("Authorization") != "Bearer "+fakeToken("a") || got.header.Get("Authorization") == "Bearer "+h.key {
			t.Fatal("upstream authentication was not replaced")
		}
		if got.header.Get("User-Agent") != "Caller-SDK/1.0" || got.header.Get("Content-Type") != "application/json; charset=utf-8" || got.header.Get("OpenAI-Beta") != "responses=v1" || got.header.Get("X-Stainless-Lang") != "python" || len(got.header.Values("X-Extension")) != 2 {
			t.Fatalf("%s ordinary headers were not preserved: %v", path, got.header)
		}
		if strings.HasPrefix(path, "/v1/messages") && got.header.Get("Anthropic-Version") != "2023-06-01" {
			t.Fatal("required default Anthropic version missing")
		}
	}
}

func TestPassthroughSwitchControlsStreamBodyAndLegacyForwarding(t *testing.T) {
	seen := make(chan forwardedRequest, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen <- forwardedRequest{body: string(body), query: r.URL.RawQuery, header: r.Header.Clone()}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	})
	h.add(t, "a", nil)
	body := "{ \"model\":\"auto\", \"stream\":true, \"stream_options\":{\"include_usage\":false}, \"extension\":9007199254740993 }"
	for _, enabled := range []bool{true, false, true} {
		h.post(t, "/settings", M{"auto_passthrough": enabled})
		req, _ := http.NewRequest("POST", h.server.URL+"/v1/chat/completions?feature=value", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.key)
		req.Header.Set("X-Extension", "caller-value")
		req.Header.Set("Anthropic-Beta", "retained-value")
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		stream, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(stream) != "data: [DONE]\n\n" {
			t.Fatal("SSE was not forwarded")
		}
		got := <-seen
		if enabled {
			if got.body != body || got.query != "feature=value" || got.header.Get("X-Extension") != "caller-value" {
				t.Fatal("passthrough did not preserve the request")
			}
		} else if got.query != "" || got.header.Get("X-Extension") != "" || !strings.Contains(got.body, `"include_usage":true`) || !strings.Contains(got.body, `9007199254740993`) {
			t.Fatalf("legacy behavior or numeric precision changed: %+v", got)
		}
		if got.header.Get("Anthropic-Beta") != "retained-value" {
			t.Fatal("legacy whitelisted header lost")
		}
	}
}

func TestForwardingSettingsValidateAndPersist(t *testing.T) {
	h := newHarness(t, nil)
	for _, key := range []string{"use_codar_ua", "auto_passthrough"} {
		for _, bad := range []any{"true", 1, nil} {
			resp, _ := h.request(t, "POST", "/settings", M{key: bad}, false)
			if resp.StatusCode != 400 || !yes(h.app.store.Settings()[key]) {
				t.Fatal("invalid switch changed active settings")
			}
		}
	}
	h.post(t, "/settings", M{"use_codar_ua": false, "auto_passthrough": false})
	reloaded, err := NewStore(h.app.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Settings()["use_codar_ua"] != false || reloaded.Settings()["auto_passthrough"] != false {
		t.Fatal("forwarding switches were not persisted")
	}
}

func TestDisabledCustomUserAgentPreservesAbsence(t *testing.T) {
	seen := make(chan string, 1)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		seen <- r.UserAgent()
		jsonResponse(w, 200, M{"choices": []any{}})
	})
	h.add(t, "a", nil)
	h.post(t, "/settings", M{"use_codar_ua": false})
	req, _ := http.NewRequest("POST", h.server.URL+"/v1/responses", strings.NewReader(`{"model":"auto"}`))
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("User-Agent", "")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || <-seen != "" {
		t.Fatal("missing caller UA was replaced")
	}
}
