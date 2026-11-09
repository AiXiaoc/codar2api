package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPointSpendingOfficialBillsPaginationAndFailure(t *testing.T) {
	h := newHarness(t, nil)
	at := now()
	if _, err := h.app.store.Upsert(M{"uid": "a", "username": "point-account", "token": fakeToken("point-account"), "added_at": at - 25*86400, "enabled": false}); err != nil {
		t.Fatal(err)
	}
	bill := func(id string, seconds, change float64, note string) M {
		return M{"id": id, "created_at": time.UnixMilli(int64((at - seconds) * 1000)).UTC().Format(time.RFC3339Nano), "change": change, "point_type": "agent", "note": note}
	}
	var calls, mode atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/agent/ledger" {
			t.Error("unexpected upstream endpoint", r.URL.Path)
		}
		if mode.Load() == 1 {
			jsonResponse(w, 502, M{"message": "offline ledger unavailable"})
			return
		}
		if mode.Load() == 2 {
			jsonResponse(w, 200, M{"data": M{"items": "invalid"}})
			return
		}
		if mode.Load() == 3 {
			jsonResponse(w, 200, M{"data": M{"total": 1000, "items": []any{bill("recent", 60, -2.5, "对话扣费")}}})
			return
		}
		if mode.Load() == 4 {
			jsonResponse(w, 200, M{"data": M{"total": 1, "items": []any{M{"change": -1}}}})
			return
		}
		var body M
		json.NewDecoder(r.Body).Decode(&body)
		if num(body["page"]) == 1 {
			jsonResponse(w, 200, M{"data": M{"total": 101, "items": []any{bill("recent", 60, -2.5, "对话扣费"), bill("gift", 70, 100, "签到赠送"), bill("expire", 80, -50, "积分到期")}}})
			return
		}
		jsonResponse(w, 200, M{"data": M{"total": 101, "items": []any{bill("old", 20*86400, -7, "对话扣费"), bill("before-import", 26*86400, -100, "对话扣费")}}})
	}))
	defer upstream.Close()
	h.app.cfg.PayBase = upstream.URL
	read := func(force bool) M {
		path := "/accounts/spending"
		if force {
			path += "?force=1"
		}
		response, raw := h.request(t, "GET", path, nil, false)
		var value M
		if response.StatusCode != 200 || json.Unmarshal(raw, &value) != nil {
			t.Fatalf("spending endpoint status=%d", response.StatusCode)
		}
		return obj(arr(value["accounts"])[0])
	}
	r := read(false)
	if num(r["spent_total"]) != 9.5 || num(r["spent_week"]) != 2.5 || num(r["spent_hour"]) != 2.5 || text(r["uid"]) != "a" {
		t.Fatal("wrong actual point charges or import boundary", r)
	}
	if calls.Load() != 2 {
		t.Fatal("did not read the second page", calls.Load())
	}
	read(false)
	if calls.Load() != 2 {
		t.Fatal("per-account cache was bypassed")
	}
	mode.Store(1)
	r = read(true)
	if num(r["spent_total"]) != 9.5 || text(r["error"]) == "" {
		t.Fatal("a failed fetch replaced valid history", r)
	}
	mode.Store(2)
	r = read(true)
	if num(r["spent_total"]) != 9.5 || text(r["error"]) == "" {
		t.Fatal("malformed data became zero", r)
	}
	for _, invalid := range []int32{3, 4} {
		mode.Store(invalid)
		r = read(true)
		if num(r["spent_total"]) != 9.5 || text(r["error"]) == "" {
			t.Fatal("repeated pagination or invalid timestamps replaced complete history", invalid, r)
		}
	}
	mode.Store(0)
	r = read(true)
	if num(r["spent_total"]) != 9.5 || text(r["error"]) != "" {
		t.Fatal("incremental rescan duplicated the old charge", r)
	}
}

func TestPointChargeExcludesIncomeExpirationAndRefund(t *testing.T) {
	if !pointCharge(-1, "agent", "对话扣费") || pointCharge(1, "agent", "") || pointCharge(-5, "expire", "") || pointCharge(-5, "", "积分到期") || pointCharge(-5, "refund", "") {
		t.Fatal("non-consumption ledger entries were included")
	}
}
