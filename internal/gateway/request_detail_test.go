package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequestDetailsRequireAdminAndPreserveFullContent(t *testing.T) {
	h := newHarness(t, nil)
	id := random(16)
	body := "{\"text\":\"原始正文\\nsecond line\"}"
	detail := M{"id": id, "request": M{"headers": M{"Authorization": []any{"Bearer full-token"}}, "body": body}, "response": M{"status": 403, "headers": M{"X-Upstream": []any{"present"}}, "body": "entire upstream response"}, "attempts": []any{}}
	if err := writeJSON(filepath.Join(h.app.cfg.DataDir, "request-details", id+".json"), detail); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(h.server.URL + "/stats/request-detail?id=" + id)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("request details were exposed without admin login")
	}
	resp, b := h.request(t, "GET", "/stats/request-detail?id="+id, nil, false)
	var out M
	if resp.StatusCode != 200 || json.Unmarshal(b, &out) != nil || !yes(out["ok"]) {
		t.Fatalf("authorized detail unavailable: %d %s", resp.StatusCode, b)
	}
	d := obj(out["detail"])
	if text(obj(d["request"])["body"]) != body || text(obj(d["response"])["body"]) != "entire upstream response" || text(arr(obj(obj(d["request"])["headers"])["Authorization"])[0]) != "Bearer full-token" {
		t.Fatal("stored detail was truncated or changed")
	}
	for _, id := range []string{"", "../../settings", strings.Repeat("a", 31), strings.Repeat("A", 32), strings.Repeat("z", 32), random(16)} {
		resp, _ := h.request(t, "GET", "/stats/request-detail?id="+url.QueryEscape(id), nil, false)
		if resp.StatusCode != 404 {
			t.Fatalf("invalid detail identifier accepted: %q (%d)", id, resp.StatusCode)
		}
	}
	h.app.usage.Record(M{"uid": "a", "account": "a", "model": "auto", "ok": false, "status": 403, "diagnostic_id": detail["id"]})
	resp, b = h.request(t, "GET", "/stats", nil, false)
	if resp.StatusCode != 200 || !strings.Contains(string(b), id) {
		t.Fatal("recent requests omitted diagnostic reference")
	}
	if rows := arr(h.app.usage.Requests("a", "a", false, 1, 20)["items"]); len(rows) != 1 || text(obj(rows[0])["diagnostic_id"]) != id {
		t.Fatal("account history omitted diagnostic reference")
	}
}

func TestRequestDetailsRetentionSkipsInProgressFiles(t *testing.T) {
	dir := t.TempDir()
	details := filepath.Join(dir, "request-details")
	if err := os.MkdirAll(details, 0700); err != nil {
		t.Fatal(err)
	}
	oldID, currentID := random(16), random(16)
	for _, id := range []string{oldID, currentID} {
		if err := writeJSON(filepath.Join(details, id+".json"), M{"id": id}); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(details, oldID+".json"), old, old); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(details, ".active-temp")
	if err := os.Mkdir(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	inProgress := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(tmp, inProgress, inProgress); err != nil {
		t.Fatal(err)
	}
	u, err := NewUsageLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(details, oldID+".json")); !os.IsNotExist(err) {
		t.Fatal("expired detail not removed")
	}
	if _, err := u.RequestDetail(currentID); err != nil {
		t.Fatal("current detail removed", err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatal("in-progress directory removed", err)
	}
}
