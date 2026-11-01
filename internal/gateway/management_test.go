package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalManagementRequest(t *testing.T) {
	for _, tc := range []struct {
		peer, host, header, value string
		want                      bool
	}{
		{"127.0.0.1:1234", "127.0.0.1:8801", "", "", true},
		{"127.0.0.2:1234", "localhost:8801", "", "", true},
		{"[::1]:1234", "[::1]:8801", "", "", true},
		{"[::ffff:127.0.0.1]:1234", "LOCALHOST.", "", "", true},
		{"198.51.100.8:1234", "localhost:8801", "X-Forwarded-For", "127.0.0.1", false},
		{"192.168.1.8:1234", "127.0.0.1:8801", "", "", false},
		{"[2001:db8::1]:1234", "localhost:8801", "", "", false},
		{"bad-address", "localhost:8801", "", "", false},
		{"127.0.0.1:1234", "panel.example.com", "", "", false},
		{"127.0.0.1:1234", "localhost:8801", "X-Forwarded-For", "198.51.100.8", false},
		{"127.0.0.1:1234", "localhost:8801", "X-Real-IP", "198.51.100.8", false},
		{"127.0.0.1:1234", "localhost:8801", "Forwarded", "for=198.51.100.8", false},
	} {
		r := httptest.NewRequest("GET", "http://localhost/", nil)
		r.RemoteAddr, r.Host = tc.peer, tc.host
		if tc.header != "" {
			r.Header.Set(tc.header, tc.value)
		}
		if got := localManagementRequest(r); got != tc.want {
			t.Errorf("peer=%s host=%s %s=%s: local=%v", tc.peer, tc.host, tc.header, tc.value, got)
		}
	}
}

func TestRemoteManagementToggleAndPublicAPI(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, M{"choices": []any{M{"message": M{"content": "ok"}}}})
	})
	h.add(t, "a", nil)
	u, _ := url.Parse(h.server.URL)
	call := func(method, path, body string, admin, key bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://codar.example.com"+path, strings.NewReader(body))
		r.RemoteAddr = "198.51.100.8:1234"
		if admin {
			for _, cookie := range h.client.Jar.Cookies(u) {
				r.AddCookie(cookie)
			}
		}
		if key {
			r.Header.Set("Authorization", "Bearer "+h.key)
		}
		w := httptest.NewRecorder()
		h.app.ServeHTTP(w, r)
		return w
	}
	if !yes(h.app.store.Settings()["allow_remote_management"]) {
		t.Fatal("remote management should default to on")
	}
	if w := call("GET", "/settings", "", true, false); w.Code != http.StatusOK {
		t.Fatalf("default remote administrator denied: %d", w.Code)
	}
	if w := call("GET", "/settings", "", false, true); w.Code != http.StatusUnauthorized {
		t.Fatal("default remote management bypassed administrator authentication")
	}
	h.post(t, "/settings", M{"allow_remote_management": false})
	for _, path := range []string{"/", "/dashboard", "/ui", "/login", "/assets/pages.js", "/auth/session", "/accounts", "/accounts/export", "/settings", "/keys", "/proxy-pool"} {
		for _, method := range []string{"GET", "POST"} {
			if w := call(method, path, `{}`, true, true); w.Code != http.StatusForbidden {
				t.Fatalf("remote %s %s accepted with management off: %d", method, path, w.Code)
			}
		}
	}
	if w := call("POST", "/auth/login", `{"password":"test-admin-password"}`, false, false); w.Code != 403 {
		t.Fatal("remote login allowed with management off")
	}
	for _, enabled := range []bool{false, true, false} {
		h.post(t, "/settings", M{"allow_remote_management": enabled})
		for _, path := range []string{"/ping", "/health", "/v1/models"} {
			if w := call("GET", path, "", false, true); w.Code != 200 {
				t.Fatalf("enabled=%v public %s failed: %d %s", enabled, path, w.Code, w.Body.String())
			}
		}
		if w := call("POST", "/v1/chat/completions", `{"model":"auto"}`, false, true); w.Code != 200 {
			t.Fatalf("enabled=%v API Key call failed: %d %s", enabled, w.Code, w.Body.String())
		}
		if w := call("POST", "/v1/chat/completions", `{"model":"auto"}`, true, false); w.Code != 401 {
			t.Fatal("admin session bypassed public API Key check")
		}
		if enabled {
			if w := call("GET", "/settings", "", true, false); w.Code != 200 {
				t.Fatalf("enabled remote admin denied: %d", w.Code)
			}
			if w := call("GET", "/settings", "", false, true); w.Code != 401 {
				t.Fatal("API key bypassed remote admin authentication")
			}
			if w := call("POST", "/auth/login", `{"password":"test-admin-password"}`, false, false); w.Code != 200 {
				t.Fatalf("remote login denied with management enabled: %d", w.Code)
			}
			r := httptest.NewRequest("POST", "http://codar.example.com/settings", strings.NewReader(`{"allow_remote_management":false}`))
			r.RemoteAddr = "198.51.100.8:1234"
			r.Header.Set("Origin", "https://other.example.com")
			for _, cookie := range h.client.Jar.Cookies(u) {
				r.AddCookie(cookie)
			}
			w := httptest.NewRecorder()
			h.app.ServeHTTP(w, r)
			if w.Code != 403 || !yes(h.app.store.Settings()["allow_remote_management"]) {
				t.Fatal("remote management bypassed same-origin protection")
			}
			if w := call("POST", "/settings", `{"allow_remote_management":false}`, true, false); w.Code != 200 {
				t.Fatal("remote administrator could not disable remote access")
			}
			if w := call("GET", "/settings", "", true, false); w.Code != 403 {
				t.Fatal("existing remote session was not blocked immediately")
			}
		} else if w := call("GET", "/settings", "", true, false); w.Code != 403 {
			t.Fatal("remote session remained usable after management disabled")
		}
	}
	if w := call("POST", "/settings", `{"allow_remote_management":true}`, true, false); w.Code != 403 {
		t.Fatal("remote request re-enabled its own disabled access")
	}
	if resp, _ := h.request(t, "POST", "/settings", M{"allow_remote_management": "yes"}, false); resp.StatusCode != 400 {
		t.Fatal("non-boolean remote management setting accepted")
	}
}

func TestRemoteManagementInitialValueAndPersistence(t *testing.T) {
	dir := t.TempDir()
	s, err := newStore(dir, true)
	if err != nil || !yes(s.Settings()["allow_remote_management"]) {
		t.Fatalf("initial remote setting was not applied: %v", err)
	}
	if _, err = s.SaveSettings(M{"allow_remote_management": false}); err != nil {
		t.Fatal(err)
	}
	s, err = newStore(dir, true)
	if err != nil || yes(s.Settings()["allow_remote_management"]) {
		t.Fatalf("initial setting overrode saved disabled value: %v", err)
	}
	if _, err = s.SaveSettings(M{"allow_remote_management": true}); err != nil {
		t.Fatal(err)
	}
	s, err = NewStore(dir)
	if err != nil || !yes(s.Settings()["allow_remote_management"]) {
		t.Fatalf("saved enabled setting did not survive reload: %v", err)
	}
}

func TestRemoteManagementDefaultsAndExplicitOverrides(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value *bool
		want  bool
	}{
		{"default", nil, true},
		{"explicitly disabled", new(false), false},
		{"explicitly enabled", new(true), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{AllowRemoteManagement: tc.value}
			cfg.defaults()
			s, err := newStore(t.TempDir(), *cfg.AllowRemoteManagement)
			if err != nil || yes(s.Settings()["allow_remote_management"]) != tc.want {
				t.Fatalf("remote management initial value: %v, %v", s, err)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		saved M
		want  bool
	}{
		{"new install", nil, true},
		{"older settings without remote option", M{"auto_sign": false}, true},
		{"saved disabled", M{"allow_remote_management": false}, false},
		{"saved enabled", M{"allow_remote_management": true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.saved != nil {
				if err := writeJSON(filepath.Join(dir, "settings.json"), tc.saved); err != nil {
					t.Fatal(err)
				}
			}
			s, err := NewStore(dir)
			if err != nil || yes(s.Settings()["allow_remote_management"]) != tc.want {
				t.Fatalf("remote management saved value: %v, %v", s, err)
			}
		})
	}
	if !yes(defaults()["allow_remote_management"]) {
		t.Fatal("schema default must enable remote management")
	}
}
