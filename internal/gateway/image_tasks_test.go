package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const pixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAACklEQVR4nGNgAAAAAgABSK+kcQAAAABJRU5ErkJggg=="

func mockImageCDN(t *testing.T, h *harness) (string, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	image, err := base64.StdEncoding.DecodeString(pixelPNG)
	if err != nil {
		t.Fatal(err)
	}
	calls, fail := new(atomic.Int32), new(atomic.Bool)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/image.png" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected CDN request or credentials")
		}
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(image)
	}))
	t.Cleanup(cdn.Close)
	h.app.imageClient = cdn.Client() // Local test only; production dials public IPs.
	return cdn.URL + "/image.png", calls, fail
}

func TestAsyncCodarImageReturnsTaskAndAllowsOnlyItsKey(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "image-user", M{"max_concurrency": 1})
	imageURL, downloads, downloadFailure := mockImageCDN(t, h)
	var submits, polls atomic.Int32
	started := make(chan struct{}, 1)
	complete := make(chan struct{})
	token := text(h.app.store.Get("image-user")["token"])
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != codarResearchPath || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("async task used the wrong upstream or account")
		}
		var p M
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		switch p["tool_name"] {
		case "image_gen":
			submits.Add(1)
			if _, exists := obj(p["arguments"])["async"]; exists {
				t.Error("gateway async parameter leaked to Codar")
			}
			jsonResponse(w, 200, M{"code": 200, "data": M{"task_id": "private-upstream-id", "status": "queued"}})
		case "get_tool_task_result":
			polls.Add(1)
			if obj(p["arguments"])["task_id"] != "private-upstream-id" {
				t.Error("upstream received the gateway task ID")
			}
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-complete:
				jsonResponse(w, 200, M{"code": 200, "data": M{"result": M{"status": "succeeded", "url": imageURL}}})
			case <-r.Context().Done():
			}
		default:
			t.Error("unknown research tool", p["tool_name"])
		}
	}))
	t.Cleanup(func() { h.app.cancel(); upstream.Close() })
	h.app.cfg.PayBase = upstream.URL

	r, raw := h.request(t, "POST", codarImageGenerationPath, M{"prompt": "async image", "async": true}, true)
	out := decode(t, raw)
	id := text(out["task_id"])
	path := codarImageTaskPath + id
	if r.StatusCode != 202 || id == "" || id == "private-upstream-id" || out["status"] != "queued" || r.Header.Get("Location") != path || out["data"] != nil {
		t.Fatalf("wrong async acceptance: HTTP %d %s", r.StatusCode, raw)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("background worker didn't start after the POST returned")
	}
	r, raw = h.request(t, "GET", path, nil, true)
	if r.StatusCode != 200 || decode(t, raw)["status"] != "running" || r.Header.Get("Retry-After") == "" {
		t.Fatalf("wrong running state: %d %s", r.StatusCode, raw)
	}
	r, _ = h.request(t, "POST", codarImageGenerationPath, M{"prompt": "busy", "async": true}, true)
	if r.StatusCode != 429 || submits.Load() != 1 {
		t.Fatal("async task released the account reservation too early", r.StatusCode)
	}
	owner := h.key
	other := h.post(t, "/keys", M{"action": "create", "name": "other task user"})
	h.key = text(other["token"])
	r, _ = h.request(t, "GET", path, nil, true)
	if r.StatusCode != 404 {
		t.Fatal("another API key can see this image task", r.StatusCode)
	}
	r, _ = h.request(t, "GET", path, nil, false)
	if r.StatusCode != 401 {
		t.Fatal("admin cookie can access image tasks", r.StatusCode)
	}
	h.key = owner
	close(complete)
	deadline := time.Now().Add(4 * time.Second)
	for {
		r, raw = h.request(t, "GET", path, nil, true)
		out = decode(t, raw)
		if out["status"] == "succeeded" || out["status"] == "failed" || time.Now().After(deadline) {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	data := arr(out["data"])
	if r.StatusCode != 200 || out["status"] != "succeeded" || len(data) != 1 || obj(data[0])["url"] != imageURL || downloads.Load() != 0 {
		t.Fatalf("default result must be a link without downloading: %d %s", r.StatusCode, raw)
	}
	h.post(t, "/settings", M{"image_result_base64": true})
	downloadFailure.Store(true)
	r, raw = h.request(t, "GET", path, nil, true)
	if r.StatusCode != 502 || obj(decode(t, raw)["error"])["task_id"] != id {
		t.Fatalf("image download failure lost the retryable task ID: %d %s", r.StatusCode, raw)
	}
	downloadFailure.Store(false)
	r, raw = h.request(t, "GET", path, nil, true)
	assertBase64Image(t, r, raw)
	if submits.Load() != 1 || polls.Load() != 1 || downloads.Load() != 2 {
		t.Fatal("query re-ran the generation or checked upstream after completion", submits.Load(), polls.Load(), downloads.Load())
	}
	waitConcurrency(t, h.app.store, "image-user", 0)
}

func TestSyncCodarImageBase64(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "image-user", nil)
	imageURL, downloads, _ := mockImageCDN(t, h)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p M
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if p["tool_name"] == "image_gen" {
			jsonResponse(w, 200, M{"code": 200, "data": M{"task_id": "task-1", "status": "queued"}})
			return
		}
		jsonResponse(w, 200, M{"code": 200, "data": M{"result": M{"status": "succeeded", "url": imageURL}}})
	}))
	t.Cleanup(upstream.Close)
	h.app.cfg.PayBase = upstream.URL
	h.post(t, "/settings", M{"image_result_base64": true})
	r, raw := h.request(t, "POST", codarImageGenerationPath, M{"prompt": "sync image", "async": false, "response_format": "url"}, true)
	assertBase64Image(t, r, raw)
	if downloads.Load() != 1 || r.Header.Get("Location") != "" {
		t.Fatal("sync generation didn't wait for the downloaded image")
	}
	r, raw = h.request(t, "POST", codarImageGenerationPath, M{"prompt": "test", "async": "true"}, true)
	if r.StatusCode != 400 || !strings.Contains(string(raw), "async") {
		t.Fatalf("invalid async mode accepted: %d %s", r.StatusCode, raw)
	}
}

func assertBase64Image(t *testing.T, r *http.Response, raw []byte) {
	t.Helper()
	data := arr(decode(t, raw)["data"])
	if r.StatusCode != 200 || len(data) != 1 || obj(data[0])["url"] != nil {
		t.Fatalf("expected Base64 image without an URL: %d %s", r.StatusCode, raw)
	}
	image, err := base64.StdEncoding.DecodeString(text(obj(data[0])["b64_json"]))
	want, _ := base64.StdEncoding.DecodeString(pixelPNG)
	if err != nil || !bytes.Equal(image, want) {
		t.Fatal("response did not Base64 encode the downloaded bytes")
	}
}

func TestCodarImageDownloadRejectsInternalDestinations(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.10.1.5", "169.254.169.254", "100.64.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "64:ff9b::a00:1", "2002:7f00:1::"} {
		if publicImageIP(netip.MustParseAddr(ip)) {
			t.Error("private or special destination accepted", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		if !publicImageIP(netip.MustParseAddr(ip)) {
			t.Error("public destination rejected", ip)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if conn, err := dialPublicImage(ctx, "tcp", "127.0.0.1:80"); conn != nil || err == nil || !strings.Contains(err.Error(), "公网") {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatal("image client tried to dial loopback", err)
	}
}
