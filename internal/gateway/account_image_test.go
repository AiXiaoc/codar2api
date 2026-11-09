package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAccountImageCallUsesSelectedAccountAndDisplaysEitherFormat(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "other", nil)
	h.add(t, "selected", M{"enabled": false, "max_concurrency": 1, "balance": M{"agent_token": 0}, "cooldown_kind": "balance", "cooldown_until": now() + 120})
	imageURL, downloads, _ := mockImageCDN(t, h)
	const model = "gpt-image-2.5"
	token := text(h.app.store.Get("selected")["token"])
	var submits, polls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != codarResearchPath || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("the account image call used the wrong upstream or token")
		}
		var payload M
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["username"] != "selected" {
			t.Error("invalid research tool envelope", err)
		}
		args := obj(payload["arguments"])
		switch payload["tool_name"] {
		case "image_gen":
			submits.Add(1)
			if len(args) != 4 || args["model"] != model || args["prompt"] != "生成海报" || args["size"] != "3:2" || args["resolution"] != "4K" {
				t.Error("invalid image arguments", args)
			}
			jsonResponse(w, 200, M{"code": 200, "data": M{"task_id": "account-image-1", "status": "queued"}})
		case "get_tool_task_result":
			polls.Add(1)
			if args["task_id"] != "account-image-1" {
				t.Error("queried the wrong image task")
			}
			jsonResponse(w, 200, M{"code": 200, "data": M{"result": M{"status": "succeeded", "url": imageURL}}})
		default:
			t.Error("unexpected research tool", payload["tool_name"])
		}
	}))
	t.Cleanup(proxy.Close)
	h.post(t, "/proxy-pool/manage", M{"action": "create", "name": "account exit", "url": proxy.URL})
	h.post(t, "/accounts/proxy", M{"uid": "selected", "proxy_id": 1})
	body := M{"uid": "selected", "model": model, "prompt": "生成海报", "size": "3:2", "resolution": "4K"}
	resp := h.post(t, "/accounts/test", body)
	images := arr(resp["data"])
	if !yes(resp["ok"]) || resp["model"] != model || len(images) != 1 || obj(images[0])["url"] != imageURL || downloads.Load() != 0 {
		t.Fatal("manual image call did not return the link", resp)
	}
	h.post(t, "/settings", M{"image_result_base64": true})
	r, raw := h.request(t, "POST", "/accounts/test", body, false)
	assertBase64Image(t, r, raw)
	resp = decode(t, raw)
	if !yes(resp["ok"]) || obj(arr(resp["data"])[0])["mime_type"] != "image/png" || downloads.Load() != 1 {
		t.Fatal("manual Base64 result lacks preview data", resp)
	}
	waitConcurrency(t, h.app.store, "selected", 0)
	if _, err := h.app.store.ReserveManual("selected", model, nil); err != nil {
		t.Fatal(err)
	}
	defer h.app.store.Release("selected")
	resp = h.post(t, "/accounts/test", body)
	if yes(resp["ok"]) || !strings.Contains(text(resp["error"]), "并发") || submits.Load() != 2 || polls.Load() != 2 {
		t.Fatal("manual image call bypassed account concurrency or retried", resp)
	}
	if yes(h.app.store.Get("selected")["enabled"]) {
		t.Fatal("manual image call enabled the selected account")
	}
	h.callsMu.Lock()
	defer h.callsMu.Unlock()
	if len(h.calls) != 0 {
		t.Fatal("manual image call fell back to another exit or account", h.calls)
	}
}

func TestAccountImageCallRequiresAdminSession(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "selected", nil)
	r, err := http.NewRequest("POST", h.server.URL+"/accounts/test", bytes.NewBufferString(`{"uid":"selected","model":"gpt-image-2","prompt":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+h.key)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("API key was allowed to bypass account scheduling", resp.StatusCode)
	}
}
