package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            int
		valid           bool
	}{
		{"0.1.3", "v0.1.3", 0, true},
		{"v0.1.3", "0.1.4", -1, true},
		{"0.9.0", "0.10.0", -1, true},
		{"1.10.0", "1.9.99", 1, true},
		{"1.0.0-rc.1", "1.0.0", -1, true},
		{"1.0.0", "1.0.0-rc.1", 1, true},
		{"1.0.0-alpha.9", "1.0.0-alpha.10", -1, true},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1, true},
		{"1.0.0-alpha.1", "1.0.0-alpha", 1, true},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1, true},
		{"1.0.0-alpha.beta", "1.0.0-alpha.1", 1, true},
		{"1.0.0-beta", "1.0.0-rc", -1, true},
		{"1.0.0+build.1", "v1.0.0+other.2", 0, true},
		{"99999999999999999999999.0.0", "100000000000000000000000.0.0", -1, true},
		{"dev", "v1.0.0", 0, false},
		{"1.0", "1.0.0", 0, false},
		{"1.01.0", "1.1.0", 0, false},
		{"1.0.0-alpha.01", "1.0.0", 0, false},
		{"1.0.0", "javascript:alert(1)", 0, false},
		{" 1.0.0", "1.0.0", 0, false},
	} {
		t.Run(tc.current+"/"+tc.latest, func(t *testing.T) {
			got, valid := compareVersions(tc.current, tc.latest)
			if valid != tc.valid || got != tc.want {
				t.Fatalf("got (%d, %v), want (%d, %v)", got, valid, tc.want, tc.valid)
			}
		})
	}
}

func TestAboutAndUpdateCheck(t *testing.T) {
	h := newHarness(t, nil)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Api-Key") != "" {
			t.Error("update check forwarded credentials or used the wrong method")
		}
		if r.UserAgent() != "Codar2API/"+Version || r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("missing update request headers")
		}
		jsonResponse(w, 200, M{"tag_name": "v9.0.0", "html_url": "javascript:alert(1)", "draft": false, "prerelease": false})
	}))
	defer upstream.Close()
	h.app.updates.endpoint = upstream.URL
	response, body := h.request(t, "GET", "/about", nil, false)
	var about M
	if err := json.Unmarshal(body, &about); err != nil || response.StatusCode != 200 || about["current_version"] != Version || calls.Load() != 0 {
		t.Fatalf("about should show the local version without contacting GitHub: %s", body)
	}
	for range 2 {
		response, body = h.request(t, "POST", "/about/check-update", M{}, true)
		var result updateResult
		if err := json.Unmarshal(body, &result); err != nil || response.StatusCode != 200 {
			t.Fatalf("update check failed: %s", body)
		}
		if result.Status != "available" || result.CurrentVersion != Version || result.LatestVersion != "v9.0.0" || result.CheckedAt == 0 || result.ReleaseURL != projectRepositoryURL+"/releases/tag/v9.0.0" {
			t.Fatalf("incorrect update result: %+v", result)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("successful check was not cached")
	}
	h.app.updates.mu.Lock()
	h.app.updates.checkedAt = time.Now().Add(-2 * time.Minute)
	h.app.updates.mu.Unlock()
	h.post(t, "/about/check-update", M{})
	if calls.Load() != 2 {
		t.Fatal("expired update result was not refreshed")
	}
	u, _ := url.Parse(h.server.URL)
	r := httptest.NewRequest("POST", h.server.URL+"/about/check-update", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://other.example")
	for _, cookie := range h.client.Jar.Cookies(u) {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.app.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || calls.Load() != 2 {
		t.Fatal("cross-origin update check bypassed protection")
	}
	for _, path := range []string{"/about", "/about/check-update"} {
		method, body := "GET", ""
		if strings.HasSuffix(path, "check-update") {
			method, body = "POST", `{}`
		}
		r := httptest.NewRequest(method, h.server.URL+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+h.key)
		w := httptest.NewRecorder()
		h.app.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("non-administrator accessed %s: %d", path, w.Code)
		}
	}
}

func TestUpdateResultsAndConcurrentChecks(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		io.WriteString(w, `{"tag_name":"v1.0.0"}`)
	}))
	defer server.Close()
	checker := newReleaseChecker()
	checker.endpoint = server.URL
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			result, err := checker.check(t.Context(), "0.9.0")
			if err != nil || result.Status != "available" {
				t.Errorf("concurrent update check failed: %+v, %v", result, err)
			}
		})
	}
	wg.Wait()
	for version, want := range map[string]string{"1.0.0": "current", "1.1.0": "newer", "dev": "unknown"} {
		result, err := checker.check(t.Context(), version)
		if err != nil || result.Status != want {
			t.Errorf("version %s: got %+v, %v", version, result, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent requests repeated the GitHub request")
	}
}

func TestUpdateCheckErrorsAndRetry(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"not found", "private response", 404},
		{"forbidden", "private response", 403},
		{"rate limited", "private response", 429},
		{"server error", "private response", 500},
		{"redirect", "private response", 302},
		{"invalid JSON", "private response", 200},
		{"missing version", `{}`, 200},
		{"draft", `{"tag_name":"v1.0.0","draft":true}`, 200},
		{"prerelease", `{"tag_name":"v1.0.0-beta","prerelease":true}`, 200},
		{"invalid version", `{"tag_name":"javascript:alert(1)"}`, 200},
		{"too large", strings.Repeat(" ", (1<<20)+1), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
					return
				}
				io.WriteString(w, `{"tag_name":"v1.0.0"}`)
			}))
			defer server.Close()
			checker := newReleaseChecker()
			checker.endpoint = server.URL
			if _, err := checker.check(t.Context(), "0.1.3"); err == nil || strings.Contains(err.Error(), "private response") {
				t.Fatalf("unexpected error: %v", err)
			}
			if result, err := checker.check(t.Context(), "0.1.3"); err != nil || result.Status != "available" || calls.Load() != 2 {
				t.Fatalf("retry failed: %+v, %v", result, err)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer server.Close()
		checker := newReleaseChecker()
		checker.endpoint, checker.client.Timeout = server.URL, 30*time.Millisecond
		if _, err := checker.check(t.Context(), "1.0.0"); err == nil || !strings.Contains(err.Error(), "超时") {
			t.Fatalf("timeout was not reported: %v", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := newReleaseChecker().check(ctx, "1.0.0"); err == nil {
			t.Fatal("canceled check was not rejected")
		}
	})
}
