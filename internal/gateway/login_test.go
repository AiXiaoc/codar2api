package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

var fakeCaptcha = M{"lot_number": "secret-lot", "captcha_output": "secret-output", "pass_token": "secret-pass", "gen_time": "123456789"}

func TestManualPhoneLoginAndIPVerification(t *testing.T) {
	h := newHarness(t, nil)
	var sent, verified atomic.Int32
	pay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p M
		_ = json.NewDecoder(r.Body).Decode(&p)
		switch r.URL.Path {
		case "/v1/verification-code":
			if p["phone"] != "13800138000" || (p["type"] != "login" && p["type"] != "ip_verify") || p["pass_token"] != fakeCaptcha["pass_token"] {
				t.Error("wrong SMS request", p)
			}
			sent.Add(1)
			jsonResponse(w, 200, M{"code": 200})
		case "/v1/login":
			if p["username"] != "13800138000" || p["password"] != "123456" {
				t.Error("wrong phone login", p)
			}
			jsonResponse(w, 201, M{"code": 201, "data": M{"status": "ip_verify_required", "token": "private-temp", "username": "phone-user"}})
		case "/v1/user/verify_ip":
			if r.Header.Get("Authorization") != "Bearer private-temp" || p["phone"] != "13800138000" || p["username"] != "phone-user" || p["code"] != "654321" {
				t.Error("wrong IP verification", p)
			}
			verified.Add(1)
			jsonResponse(w, 200, M{"data": M{"token": fakeToken("phone-user")}})
		default:
			jsonResponse(w, 200, M{"code": 200, "data": M{}})
		}
	}))
	defer pay.Close()
	h.app.cfg.PayBase = pay.URL
	bad := h.post(t, "/phone/code", M{"phone": "13800138000", "captcha": M{"lot_number": true}})
	if yes(bad["ok"]) || sent.Load() != 0 {
		t.Fatal("invalid captcha reached upstream")
	}
	h.post(t, "/phone/code", M{"phone": "13800138000", "captcha": fakeCaptcha})
	out := h.post(t, "/phone/login", M{"phone": "13800138000", "code": "123456", "note": "manual"})
	flow := text(out["flow"])
	if out["state"] != "ip_verify" || flow == "" || strings.Contains(text(out), "private-temp") {
		t.Fatal(out)
	}
	h.post(t, "/phone/code", M{"flow": flow, "phone": "13900139000", "captcha": fakeCaptcha})
	result := h.post(t, "/phone/verify-ip", M{"flow": flow, "code": "654321"})
	if result["state"] != "success" || h.app.store.Get("phone-user") == nil || sent.Load() != 2 || verified.Load() != 1 {
		t.Fatal(result)
	}
	duplicate := h.post(t, "/phone/verify-ip", M{"flow": flow, "code": "654321"})
	if yes(duplicate["ok"]) || verified.Load() != 1 {
		t.Fatal("completed flow was replayed")
	}
}

func TestWechatLoginStatesAndIdempotentPolling(t *testing.T) {
	for _, state := range []string{"success", "bind_phone_required", "ip_verify_required"} {
		t.Run(state, func(t *testing.T) {
			h := newHarness(t, nil)
			var polls, sms atomic.Int32
			pay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p M
				_ = json.NewDecoder(r.Body).Decode(&p)
				switch r.URL.Path {
				case "/v1/wechat/login/qrcode":
					jsonResponse(w, 200, M{"data": M{"scene": "scene-test", "qr_url": "https://example.test/qr"}})
				case "/v1/wechat/login/status":
					if polls.Add(1) == 1 {
						jsonResponse(w, 200, M{"data": M{"status": "pending"}})
						return
					}
					d := M{"status": state, "token": fakeToken("wechat-user"), "username": "wechat-user"}
					if state == "ip_verify_required" {
						d["phone"] = "8000"
					}
					jsonResponse(w, 200, M{"data": d})
				case "/v1/verification-code":
					kind := "bind_phone"
					if state == "ip_verify_required" {
						kind = "ip_verify"
					}
					if p["type"] != kind {
						t.Error("wrong SMS kind", p)
					}
					sms.Add(1)
					jsonResponse(w, 200, M{"code": 200})
				case "/v1/bind-phone", "/v1/user/verify_ip":
					if p["username"] != "wechat-user" || p["phone"] != "13800138000" || p["code"] != "123456" {
						t.Error("wrong bind/verify request", p)
					}
					jsonResponse(w, 200, M{"data": M{"token": fakeToken("wechat-user")}})
				default:
					jsonResponse(w, 200, M{"code": 200, "data": M{}})
				}
			}))
			defer pay.Close()
			h.app.cfg.PayBase = pay.URL
			qr := h.post(t, "/wechat/qrcode", M{"note": "qr-note"})
			if qr["scene"] != "scene-test" {
				t.Fatal(qr)
			}
			if h.post(t, "/wechat/status", M{"scene": qr["scene"]})["state"] != "pending" {
				t.Fatal("initial QR state must be pending")
			}
			out := h.post(t, "/wechat/status", M{"scene": qr["scene"]})
			repeat := h.post(t, "/wechat/status", M{"scene": qr["scene"]})
			if polls.Load() != 2 || out["state"] != repeat["state"] || out["flow"] != repeat["flow"] {
				t.Fatal("QR polling repeated the completed upstream login")
			}
			if state != "success" {
				flow := text(out["flow"])
				if flow == "" || strings.Contains(text(out), fakeToken("wechat-user")) {
					t.Fatal("missing private verification flow")
				}
				if state == "ip_verify_required" {
					bad := h.post(t, "/phone/code", M{"flow": flow, "phone": "13900139001", "captcha": fakeCaptcha})
					if yes(bad["ok"]) || sms.Load() != 0 {
						t.Fatal("incorrect bound phone was accepted")
					}
				}
				h.post(t, "/phone/code", M{"flow": flow, "phone": "13800138000", "captcha": fakeCaptcha})
				endpoint := "/phone/bind"
				if state == "ip_verify_required" {
					endpoint = "/phone/verify-ip"
				}
				out = h.post(t, endpoint, M{"flow": flow, "code": "123456"})
			}
			if out["state"] != "success" || h.app.store.Get("wechat-user")["note"] != "qr-note" {
				t.Fatal(out)
			}
		})
	}
}

func TestManualLoginUsesAndPinsSelectedProxy(t *testing.T) {
	h := newHarness(t, nil)
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			t.Error("request did not use HTTP proxy")
		}
		proxied.Add(1)
		switch r.URL.Path {
		case "/v1/login":
			jsonResponse(w, 200, M{"data": M{"token": fakeToken("proxy-user")}})
		case "/api/user/self":
			jsonResponse(w, 200, M{"data": M{"id": "proxy-user", "username": "proxy-user"}})
		case "/v1/wechat/login/qrcode":
			jsonResponse(w, 200, M{"data": M{"scene": "proxy-qr", "qr_url": "https://example.test/qr"}})
		default:
			jsonResponse(w, 200, M{"code": 200, "data": M{}})
		}
	}))
	defer proxy.Close()
	h.post(t, "/proxy-pool/manage", M{"action": "create", "name": "login proxy", "url": proxy.URL})
	h.post(t, "/phone/code", M{"phone": "13800138000", "captcha": fakeCaptcha, "proxy_id": 1})
	out := h.post(t, "/phone/login", M{"phone": "13800138000", "code": "123456", "proxy_id": 1})
	if out["state"] != "success" || num(arr(obj(obj(out["account"])["proxy"])["ids"])[0]) != 1 {
		t.Fatal("login did not inherit proxy", out)
	}
	qr := h.post(t, "/wechat/qrcode", M{"proxy_id": 1})
	h.post(t, "/proxy-pool/manage", M{"action": "toggle", "id": 1, "enabled": false})
	before := proxied.Load()
	out = h.post(t, "/wechat/status", M{"scene": qr["scene"]})
	if out["state"] != "pending" || text(out["warn"]) == "" || proxied.Load() != before {
		t.Fatal("changed login exit was used", out)
	}
	h.callsMu.Lock()
	defer h.callsMu.Unlock()
	if len(h.calls) != 0 {
		t.Fatal("proxy login fell back to direct", h.calls)
	}
}

func TestLocalClientProfilesAndImport(t *testing.T) {
	h := newHarness(t, nil)
	path := filepath.Join(t.TempDir(), "config.json")
	first, second := fakeToken("local-one"), fakeToken("local-two")
	config := M{"provider": M{"active_profile": "primary", "speedai": M{"token": first, "username": "local-one"}, "provider_profiles": M{"primary": M{"speedai": M{"token": first}}, "secondary": M{"speedai": M{"token": second, "username": "local-two"}}}}}
	if e := writeJSON(path, config); e != nil {
		t.Fatal(e)
	}
	h.app.cfg.LocalConfig = path
	_, raw := h.request(t, "GET", "/local-login", nil, false)
	out := decode(t, raw)
	if len(arr(out["logins"])) != 2 || strings.Contains(string(raw), first) || strings.Contains(string(raw), second) {
		t.Fatal("profiles missing or local tokens leaked", out)
	}
	h.post(t, "/local-login/import", M{"profile": "secondary"})
	if h.app.store.Get("local-two") == nil || h.app.store.Get("local-one") != nil {
		t.Fatal("wrong local profile imported")
	}
	if _, e := os.Stat(path); e != nil {
		t.Fatal("source config was removed")
	}
	if _, e := h.app.manualLogin(context.Background(), "/phone/verify-ip", M{"flow": "expired", "code": "123456"}); e == nil {
		t.Fatal("unknown verification flow accepted")
	}
}
