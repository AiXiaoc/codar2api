package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func recentTestCost(string, float64, float64, float64) any { return float64(0) }

func TestRecentErrorsSurviveNormalTrafficRestartAndSeparateStorage(t *testing.T) {
	dir := t.TempDir()
	u, err := NewUsageLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 65; i++ {
		u.Record(M{"ok": false, "status": 502, "error": fmt.Sprintf("failure-%d", i), "account": "first", "model": "m", "diagnostic_id": "keep-detail"})
	}
	u.Record(M{"ok": true, "status": 200, "account": "second", "model": "m", "tries": []any{
		M{"ok": false, "status": 502, "account": "first", "error": "retry cause"}, M{"ok": true, "status": 200, "account": "second"},
	}})
	for i := 0; i < 100; i++ {
		u.Record(M{"ok": true, "status": 200, "model": "m"})
	}
	page := u.RecentRequests(0, true, 1, 30, recentTestCost)
	items := arr(page["items"])
	if num(page["total"]) != 66 || num(page["pages"]) != 3 || len(items) != 30 || !yes(obj(items[0])["recovered"]) {
		t.Fatal("errors were hidden by normal traffic or retries", page)
	}
	if cause := obj(arr(obj(items[0])["error_attempts"])[0]); text(cause["account"]) != "first" || text(cause["error"]) != "retry cause" || num(cause["status"]) != 502 {
		t.Fatal("failed retry lost its upstream error", cause)
	}
	last := u.RecentRequests(0, true, 99999999, 30, recentTestCost)
	if num(last["page"]) != 3 || len(arr(last["items"])) != 6 || text(obj(arr(last["items"])[5])["error"]) != "failure-0" || text(obj(arr(last["items"])[5])["diagnostic_id"]) != "keep-detail" {
		t.Fatal("last page or original detail reference lost", last)
	}
	all := u.RecentRequests(0, false, 1, 30, recentTestCost)
	if num(all["total"]) != 166 || yes(obj(arr(all["items"])[0])["had_error"]) {
		t.Fatal("all requests no longer lists the latest normal traffic", all)
	}
	stats := obj(u.Stats(0, recentTestCost)["total"])
	if num(stats["requests"]) != 166 || num(stats["failed"]) != 65 {
		t.Fatal("separate errors changed request totals", stats)
	}
	for i := 0; i < 3; i++ {
		if err := u.Flush(); err != nil {
			t.Fatal(err)
		}
		u, err = NewUsageLog(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := u.RecentRequests(0, true, 1, 100, recentTestCost); num(got["total"]) != 66 {
			t.Fatal("restart duplicated or lost errors", got)
		}
	}
	before, err := os.ReadFile(filepath.Join(dir, "usage-errors.jsonl"))
	if err != nil || bytes.Count(before, []byte{'\n'}) != 66 {
		t.Fatal("independent error file does not contain one row per request", err)
	}
	u.Record(M{"ok": true, "status": 200, "model": "m"})
	if err := u.Flush(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "usage-errors.jsonl"))
	if !bytes.Equal(before, after) {
		t.Fatal("normal traffic wrote to the error file")
	}
	if err := os.Remove(filepath.Join(dir, "usage.jsonl")); err != nil {
		t.Fatal(err)
	}
	u, err = NewUsageLog(dir)
	if err != nil || num(u.RecentRequests(0, true, 1, 100, recentTestCost)["total"]) != 66 || num(u.RecentRequests(0, false, 1, 30, recentTestCost)["total"]) != 0 {
		t.Fatal("error history still depends on the normal log", err)
	}
}

func TestRecentErrorsMigrateWithoutPendingUsageAndKeepTimestamp(t *testing.T) {
	dir := t.TempDir()
	older := M{"ts": now() - 7200, "ok": false, "status": 502, "error": "legacy failure"}
	newer := M{"ts": now() - 1800, "ok": true, "status": 200, "tries": []any{M{"ok": false, "status": 429, "error": "recovered"}}}
	var buf bytes.Buffer
	for _, row := range []M{{"ts": now() - 8*86400, "ok": false, "error": "expired"}, older, {"ts": now() - 3600, "ok": true, "status": 200}, newer} {
		if err := json.NewEncoder(&buf).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "usage.jsonl"), buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	// An existing newer error with no final newline must not swallow the older
	// row added by migration. The result must remain chronologically ordered.
	raw, _ := json.Marshal(newer)
	if err := os.WriteFile(filepath.Join(dir, "usage-errors.jsonl"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	u, err := NewUsageLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	page := u.RecentRequests(0, true, -3, 1000, recentTestCost)
	items := arr(page["items"])
	if num(page["total"]) != 2 || num(page["page_size"]) != 100 || !yes(obj(items[0])["recovered"]) || num(obj(items[1])["ts"]) != num(older["ts"]) {
		t.Fatal("migration changed timestamps, ordering or retention", page)
	}
	if num(u.RecentRequests(now()-3600, true, 1, 30, recentTestCost)["total"]) != 1 {
		t.Fatal("time filter no longer applies to error history")
	}
	if err := u.Flush(); err != nil {
		t.Fatal(err)
	}
	u, err = NewUsageLog(dir)
	if err != nil || num(u.RecentRequests(0, true, 1, 100, recentTestCost)["total"]) != 2 {
		t.Fatal("migration was not persisted without new normal requests", err)
	}
}

func TestRecentErrorsRetryFailedFlushAndPruneWhileIdle(t *testing.T) {
	u, err := NewUsageLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u.Record(M{"ok": false, "status": 502, "error": "persist me"})
	path := u.requestErrors.path
	u.requestErrors.path = filepath.Dir(path) // Opening a directory for append fails.
	if err := u.Flush(); err == nil || len(u.requestErrors.pending) != 1 || len(u.pending) != 1 {
		t.Fatal("failed persistence discarded pending errors", err)
	}
	u.requestErrors.path = path
	if err := u.Flush(); err != nil {
		t.Fatal(err)
	}
	u.requestErrors.rows[0]["ts"] = now() - 8*86400
	u.requestErrors.lastCompact = time.Now().Add(-2 * time.Hour)
	if err := u.Flush(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) != 0 || len(u.requestErrors.rows) != 0 || len(u.requestErrors.seen) != 0 {
		t.Fatal("idle errors were not pruned after seven days", err)
	}
}

func TestRecentErrorsEndpointAdminAndPagination(t *testing.T) {
	h := newHarness(t, nil)
	resp, err := http.Get(h.server.URL + "/stats/recent?errors_only=1")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("recent errors were exposed without admin login", resp.StatusCode)
	}
	h.app.usage.Record(M{"ok": false, "status": 502, "error": "endpoint failure", "diagnostic_id": "detail-reference"})
	h.app.usage.Record(M{"ok": true, "status": 200})
	for _, query := range []string{"?errors_only=1&hours=9999&page=999&page_size=999", "?errors_only=1&hours=-3&page=-4&page_size=-1"} {
		value, status, err := h.app.panel(httptest.NewRequest("GET", "/stats/recent"+query, nil), "/stats/recent", nil)
		out := obj(value)
		if err != nil || status != 200 || num(out["total"]) != 1 || num(out["page"]) != 1 || len(arr(out["items"])) != 1 || text(obj(arr(out["items"])[0])["diagnostic_id"]) != "detail-reference" {
			t.Fatal("error endpoint lost metadata or pagination limits", out, status, err)
		}
	}
}
