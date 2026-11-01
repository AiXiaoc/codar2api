package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func lastDetail(t *testing.T, h *harness) (M, M) {
	t.Helper()
	h.app.usage.mu.Lock()
	rows := h.app.usage.rows
	if len(rows) == 0 {
		h.app.usage.mu.Unlock()
		t.Fatal("missing request record")
	}
	row := clone(rows[len(rows)-1])
	h.app.usage.mu.Unlock()
	id := text(row["diagnostic_id"])
	if id == "" {
		t.Fatalf("missing diagnostic ID: %v", row)
	}
	resp, body := h.request(t, "GET", "/stats/request-detail?id="+id, nil, false)
	if resp.StatusCode != 200 {
		t.Fatalf("detail HTTP %d: %s", resp.StatusCode, body)
	}
	return row, obj(decode(t, body)["detail"])
}

func TestDetailCaptureFailuresAndSuccessfulRetry(t *testing.T) {
	large := strings.Repeat("L", (1<<20)+200)
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		var p M
		_ = json.NewDecoder(r.Body).Decode(&p)
		model := text(p["model"])
		user := text(jwt(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["username"])
		w.Header().Set("X-Upstream-Detail", "yes")
		switch {
		case model == "fail" || model == "manual":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"message":"拒绝"}}`)
		case model == "large":
			w.WriteHeader(403)
			io.WriteString(w, large)
		case model == "retry" && user == "a":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			io.WriteString(w, `{"error":{"message":"retry"}}`)
		case model == "stream":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"error\":{\"message\":\"stream failure\"}}\n\ndata: [DONE]\n\n")
		default:
			jsonResponse(w, 200, M{"choices": []any{}})
		}
	})
	h.add(t, "a", nil)
	h.post(t, "/settings", M{"detailed_debug": true, "max_attempts": 2})
	body := `{"model":"fail","messages":[{"role":"user","content":"private text"}]}`
	req, _ := http.NewRequest("POST", h.server.URL+"/v1/chat/completions?x=1", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Add("X-Custom", "first")
	req.Header.Add("X-Custom", "second")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("unexpected response %d", resp.StatusCode)
	}
	row, d := lastDetail(t, h)
	incoming := obj(d["request"])
	outgoing := obj(d["response"])
	attempts := arr(d["attempts"])
	if text(incoming["body"]) != body || text(incoming["url"]) != "/v1/chat/completions?x=1" || text(outgoing["body"]) != string(responseBody) || len(attempts) != 1 {
		t.Fatal("missing complete request or response")
	}
	if text(arr(obj(incoming["headers"])["Authorization"])[0]) != "Bearer "+h.key || len(arr(obj(incoming["headers"])["X-Custom"])) != 2 {
		t.Fatal("incoming headers missing")
	}
	attempt := obj(attempts[0])
	upReq, upResp := obj(attempt["request"]), obj(attempt["response"])
	if text(arr(obj(upReq["headers"])["Authorization"])[0]) != "Bearer "+fakeToken("a") || text(upReq["body"]) != body || text(upResp["body"]) != string(responseBody) || num(upResp["status"]) != 403 {
		t.Fatal("upstream details missing")
	}
	if mode, err := os.Stat(filepath.Join(h.app.cfg.DataDir, "request-details", text(row["diagnostic_id"])+".json")); err != nil || runtime.GOOS != "windows" && mode.Mode().Perm()&0077 != 0 {
		t.Fatal("detail permissions too broad")
	}
	h.post(t, "/accounts/test", M{"uid": "a", "model": "manual"})
	manual, md := lastDetail(t, h)
	if manual["source"] != "panel" || text(obj(md["request"])["url"]) != "/accounts/test" || len(arr(md["attempts"])) != 1 {
		t.Fatal("manual failure detail missing")
	}
	resp, _ = h.request(t, "POST", "/v1/chat/completions", M{"model": "large"}, true)
	_, d = lastDetail(t, h)
	if len(text(obj(obj(arr(d["attempts"])[0])["response"])["body"])) != len(large) {
		t.Fatal("large upstream error was truncated")
	}
	h.add(t, "b", nil)
	resp, _ = h.request(t, "POST", "/v1/chat/completions", M{"model": "retry"}, true)
	if resp.StatusCode != 200 {
		t.Fatalf("retry failed %d", resp.StatusCode)
	}
	row, d = lastDetail(t, h)
	if !yes(row["ok"]) || len(arr(d["attempts"])) != 2 {
		t.Fatal("retry details missing")
	}
	resp, _ = h.request(t, "POST", "/v1/chat/completions", M{"model": "stream", "stream": true}, true)
	row, d = lastDetail(t, h)
	if yes(row["ok"]) || !strings.Contains(text(obj(d["response"])["body"]), "stream failure") {
		t.Fatal("stream error not captured")
	}
	resp, _ = h.request(t, "POST", "/v1/chat/completions", M{"model": "success"}, true)
	h.app.usage.mu.Lock()
	last := h.app.usage.rows[len(h.app.usage.rows)-1]
	h.app.usage.mu.Unlock()
	if resp.StatusCode != 200 || text(last["diagnostic_id"]) != "" {
		t.Fatal("successful request stored detail")
	}
	for _, f := range mustReadDir(t, filepath.Join(h.app.cfg.DataDir, "request-details")) {
		if strings.HasPrefix(f.Name(), ".active-") || strings.HasPrefix(f.Name(), ".detail-") {
			t.Fatal("temporary diagnostic not removed")
		}
	}
}

func mustReadDir(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	x, e := os.ReadDir(path)
	if e != nil {
		t.Fatal(e)
	}
	return x
}

func TestDetailCapturePreflightAndAuth(t *testing.T) {
	h := newHarness(t, nil)
	h.post(t, "/settings", M{"detailed_debug": true})
	bad := []byte(`{"model":`)
	req, _ := http.NewRequest("POST", h.server.URL+"/v1/chat/completions", bytes.NewReader(bad))
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("invalid JSON accepted")
	}
	_, d := lastDetail(t, h)
	if text(obj(d["request"])["body"]) != string(bad) || num(obj(d["response"])["status"]) != 400 {
		t.Fatal("preflight error not captured")
	}
	before := len(mustReadDir(t, filepath.Join(h.app.cfg.DataDir, "request-details")))
	resp, _ = h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, false)
	if resp.StatusCode != 401 || len(mustReadDir(t, filepath.Join(h.app.cfg.DataDir, "request-details"))) != before {
		t.Fatal("unauthenticated request persisted")
	}
}

func TestDetailCaptureOversizedRequestRecordsRemainingBody(t *testing.T) {
	h := newHarness(t, nil)
	h.post(t, "/settings", M{"detailed_debug": true})
	body := `{"model":"auto","text":"` + strings.Repeat("x", 8<<20) + `"}`
	req, _ := http.NewRequest("POST", h.server.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.key)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("oversized request was accepted")
	}
	_, detail := lastDetail(t, h)
	if got := text(obj(detail["request"])["body"]); got != body {
		t.Fatalf("oversized request detail truncated: %d / %d bytes", len(got), len(body))
	}
}

func TestDetailCaptureRemovesOnlyStaleTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	u, err := NewUsageLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "request-details")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".active-old", ".active-new", "unrelated"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "body"), []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".detail-old", ".detail-new"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-25 * time.Hour)
	for _, name := range []string{".active-old", ".detail-old", "unrelated"} {
		if err := os.Chtimes(filepath.Join(root, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.pruneRequestDetails(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".active-old", ".detail-old"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("stale capture %s remains", name)
		}
	}
	for _, name := range []string{".active-new", ".detail-new", "unrelated"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("removed live or unrelated entry %s: %v", name, err)
		}
	}
}

func TestDetailCaptureRetainsBinaryBodiesLosslessly(t *testing.T) {
	binaryBody := []byte{0xff, 0, 0xfe, 'a', '\n', 0xc3, 0xa9}
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(403)
		_, _ = w.Write(binaryBody)
	})
	h.add(t, "a", nil)
	h.post(t, "/settings", M{"detailed_debug": true, "max_attempts": 1})
	resp, raw := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
	if resp.StatusCode != 403 || !bytes.Equal(raw, binaryBody) {
		t.Fatal("binary upstream response changed")
	}
	_, detail := lastDetail(t, h)
	for _, part := range []M{obj(detail["response"]), obj(obj(arr(detail["attempts"])[0])["response"])} {
		if part["body_encoding"] != "base64" {
			t.Fatal("binary encoding not identified")
		}
		decoded, err := base64.StdEncoding.DecodeString(text(part["body"]))
		if err != nil || !bytes.Equal(decoded, binaryBody) {
			t.Fatal("binary response was not preserved losslessly")
		}
	}
	invalidRequest := []byte{'{', 0xff, 0xfe, '}'}
	req, _ := http.NewRequest("POST", h.server.URL+"/v1/chat/completions", bytes.NewReader(invalidRequest))
	req.Header.Set("Authorization", "Bearer "+h.key)
	r, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	_, detail = lastDetail(t, h)
	part := obj(detail["request"])
	decoded, err := base64.StdEncoding.DecodeString(text(part["body"]))
	if part["body_encoding"] != "base64" || err != nil || !bytes.Equal(decoded, invalidRequest) {
		t.Fatal("binary request was not preserved losslessly")
	}
}
