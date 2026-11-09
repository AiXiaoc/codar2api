package gateway

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCodarImageSubmitsAndPollsThroughTheSameAccountExit(t *testing.T) {
	h := newHarness(t, nil)
	const uid = "image-user"
	const task = "image-task_123"
	const image = "https://images.example.test/result.png"
	h.add(t, uid, M{"max_concurrency": 1, "balance": M{"agent_token": 0}})
	token := text(h.app.store.Get(uid)["token"])
	var mu sync.Mutex
	seen := map[string]bool{}
	var submits, polls int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != codarResearchPath || r.Header.Get("Authorization") != "Bearer "+token || r.UserAgent() != "Go-http-client/2.0" {
			t.Error("incorrect upstream path, method, authorization or user agent")
		}
		id := r.Header.Get("X-Request-ID")
		if bytes, err := hex.DecodeString(id); err != nil || len(bytes) != 16 || seen[id] {
			t.Error("missing or repeated research request ID")
		}
		seen[id] = true
		var payload M
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["username"] != uid {
			t.Error("incorrect upstream tool envelope", err)
		}
		if num(h.app.store.Concurrency()[0]["current_concurrency"]) != 1 {
			t.Error("image request did not reserve the account until completion")
		}
		args := obj(payload["arguments"])
		switch payload["tool_name"] {
		case "image_gen":
			submits++
			if len(args) != 4 || args["model"] != "gemini-nano-banana-2.1" || args["prompt"] != "一座山" || args["resolution"] != "2K" || args["size"] != "16:9" {
				t.Errorf("wrong generation arguments: %v", args)
			}
			// A concurrent admin edit must not move a running task to a new exit.
			if err := h.app.proxies.Bind([]string{uid}, Route{}); err != nil {
				t.Error(err)
			}
			jsonResponse(w, 200, M{"code": 200, "data": M{"tool_name": "image_gen", "task_id": task, "result": task, "status": "queued", "cost": 0}})
		case "get_tool_task_result":
			polls++
			if len(args) != 1 || args["task_id"] != task {
				t.Error("polled the wrong task", args)
			}
			if polls == 1 {
				jsonResponse(w, 200, M{"code": 200, "data": M{"tool_name": "get_tool_task_result", "result": M{"status": "running"}}})
				return
			}
			jsonResponse(w, 200, M{"code": 200, "data": M{"tool_name": "get_tool_task_result", "result": M{"status": "succeeded", "url": image}}})
		default:
			t.Error("wrong tool")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(proxy.Close)
	h.post(t, "/proxy-pool/manage", M{"action": "create", "name": "research exit", "url": proxy.URL})
	h.post(t, "/accounts/proxy", M{"uid": uid, "proxy_id": 1})
	r, raw := h.request(t, "POST", codarImageGenerationPath, M{"prompt": "一座山", "resolution": "2K", "n": 1, "response_format": "url"}, true)
	d := decode(t, raw)
	images := arr(d["data"])
	if r.StatusCode != 200 || r.Header.Get("X-Codar-Account") != uid || num(d["created"]) == 0 || len(images) != 1 || obj(images[0])["url"] != image {
		t.Fatalf("wrong image response: HTTP %d %s", r.StatusCode, raw)
	}
	mu.Lock()
	if submits != 1 || polls != 2 {
		t.Errorf("wrong submission/poll counts: %d/%d", submits, polls)
	}
	mu.Unlock()
	waitConcurrency(t, h.app.store, uid, 0)
}

func TestCodarImageDoesNotResubmitAnAmbiguousFailure(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "first", nil)
	h.add(t, "second", nil)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		jsonResponse(w, 502, M{"message": "research service disconnected"})
	}))
	t.Cleanup(upstream.Close)
	h.app.cfg.PayBase = upstream.URL
	r, raw := h.request(t, "POST", codarImageGenerationPath, M{"prompt": "one image"}, true)
	if r.StatusCode != 502 || calls.Load() != 1 || !strings.Contains(string(raw), "请先核对再重试") {
		t.Fatalf("ambiguous submission should return a diagnostic without retrying: %d %s, calls %d", r.StatusCode, raw, calls.Load())
	}
	_, raw = h.request(t, "GET", "/stats/recent?errors_only=1", nil, false)
	items := arr(decode(t, raw)["items"])
	if len(items) != 1 || obj(items[0])["path"] != codarImageGenerationPath || num(obj(items[0])["status"]) != 502 {
		t.Fatalf("image error was not recorded: %s", raw)
	}
}

func TestCodarImageSurfacesARejectedTask(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "image-user", nil)
	var submits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p M
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if p["tool_name"] == "image_gen" {
			submits.Add(1)
			args := obj(p["arguments"])
			if args["model"] != "gpt-image-2" || len(arr(args["image_urls"])) != 1 || args["mask_url"] != "https://images.example.test/mask.png" {
				t.Error("image reference arguments were not forwarded", args)
			}
			jsonResponse(w, 200, M{"code": 200, "data": M{"task_id": "task-1", "status": "queued"}})
			return
		}
		jsonResponse(w, 200, M{"code": 200, "data": M{"result": M{"status": "failed", "error": "provider rejected this prompt"}}})
	}))
	t.Cleanup(upstream.Close)
	h.app.cfg.PayBase = upstream.URL
	r, raw := h.request(t, "POST", codarImageGenerationPath, M{
		"prompt": "one image", "model": "gpt-image-2", "image_urls": []any{"https://images.example.test/input.png"}, "mask_url": "https://images.example.test/mask.png",
	}, true)
	e := obj(decode(t, raw)["error"])
	if r.StatusCode != 502 || e["task_id"] != "task-1" || !strings.Contains(text(e["message"]), "provider rejected this prompt") || submits.Load() != 1 {
		t.Fatalf("failed task should show its upstream reason and task ID without resubmitting: %d %s", r.StatusCode, raw)
	}
}

func TestCodarImageModelCatalogAndScope(t *testing.T) {
	h := newHarness(t, nil)
	const scoped = "gpt-image-2"
	key := h.post(t, "/keys", M{"action": "create", "name": "image only", "models": []any{scoped}})
	fullKey := h.key
	h.key = text(key["token"])
	r, raw := h.request(t, "GET", "/v1/models", nil, true)
	models := arr(decode(t, raw)["data"])
	if r.StatusCode != 200 || len(models) != 1 || obj(models[0])["id"] != scoped || obj(models[0])["type"] != "image" {
		t.Fatalf("scoped image catalog is incorrect: %d %s", r.StatusCode, raw)
	}
	for _, tc := range []struct {
		path   string
		body   M
		key    bool
		status int
	}{
		{codarImageGenerationPath, M{"prompt": "test", "model": scoped}, false, 401},
		{codarImageGenerationPath, M{"prompt": "test"}, true, 403},
		{codarImageGenerationPath, M{"prompt": "test", "model": scoped, "n": 2}, true, 400},
		{codarImageGenerationPath, M{"prompt": "test", "model": scoped, "image_urls": []any{"file:///test.png"}}, true, 400},
		{"/v1/chat/completions", M{"model": scoped}, true, 400},
	} {
		r, raw := h.request(t, "POST", tc.path, tc.body, tc.key)
		if r.StatusCode != tc.status {
			t.Errorf("%s: wanted %d, got %d %s", tc.path, tc.status, r.StatusCode, raw)
		}
	}
	h.callsMu.Lock()
	if len(h.calls) != 0 {
		t.Error("image-only catalog or invalid calls contacted upstream", h.calls)
	}
	h.callsMu.Unlock()
	h.key = fullKey
	r, raw = h.request(t, "GET", "/v1/models", nil, true)
	ids := map[string]bool{}
	for _, v := range arr(decode(t, raw)["data"]) {
		if m := obj(v); m["type"] == "image" && len(arr(m["output_modalities"])) == 1 && arr(m["output_modalities"])[0] == "image" {
			ids[text(m["id"])] = true
		}
	}
	if r.StatusCode != 200 || len(ids) != 5 || !ids["gemini-nano-banana-2.1"] || !ids["gpt-image-2.5"] || !ids[scoped] || !ids["gemini-3-pro-image"] || !ids["gemini-3-pro-image-preview"] {
		t.Fatalf("full model list is missing declared image models: %s", raw)
	}
}
