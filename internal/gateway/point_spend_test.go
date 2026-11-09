package gateway

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newPointTestLog(t *testing.T, source string) *pointSpendLog {
	t.Helper()
	p, err := newPointSpendLog(t.TempDir(), source)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPointSpendingWindowsAndImportBoundary(t *testing.T) {
	p := newPointTestLog(t, "ledger")
	// 00:30 in Beijing: rolling one hour crosses yesterday's midnight.
	at := float64(time.Date(2026, 11, 4, 0, 30, 0, 0, time.FixedZone("CST", 8*3600)).Unix())
	since := at - 30*86400
	p.data.Accounts["a"] = &pointSpendAccount{Since: since, At: at, Debits: []pointDebit{
		{At: at - 1800, Points: 1}, {At: at - 3600, Points: 2},
		{At: at - 3600.001, Points: 4}, {At: at - 86400, Points: 8},
		{At: at - 86400.001, Points: 16}, {At: at - 7*86400, Points: 32},
		{At: at - 7*86400.001, Points: 64}, {At: since - 1, Points: 128},
		{At: at + 1, Points: 256},
	}}
	r := p.view("a", since, at)
	for key, expected := range map[string]float64{"spent_hour": 3, "spent_today": 1, "spent_day": 15, "spent_week": 63, "spent_total": 127} {
		if num(r[key]) != expected {
			t.Fatalf("%s=%v, want %v", key, r[key], expected)
		}
	}
	if p.view("b", since, at)["spent_total"] != nil {
		t.Fatal("unknown account reported another account's points")
	}
	if p.view("a", since+1, at)["spent_total"] != nil {
		t.Fatal("changed import boundary reused a different lifetime")
	}
}

func TestPointSpendingRefreshOverlapPersistenceAndFailure(t *testing.T) {
	p := newPointTestLog(t, "ledger")
	since, at := now()-30*86400, now()
	old := pointDebit{ID: "old", At: since + 1, Points: 7}
	recent := pointDebit{ID: "recent", At: at - 60, Points: 3}
	calls := 0
	fetch := func(_ context.Context, from float64) ([]pointDebit, error) {
		calls++
		if from != since {
			t.Fatal("first query did not start at import", from, since)
		}
		return []pointDebit{old, recent, recent}, nil
	}
	r := p.refresh(context.Background(), "a", since, false, fetch)
	if num(r["spent_total"]) != 10 || !yes(r["complete"]) {
		t.Fatal("deduplication failed", r)
	}
	p.refresh(context.Background(), "a", since, false, fetch)
	if calls != 1 {
		t.Fatal("fresh results were fetched again")
	}
	recent.Points = 4
	late := pointDebit{ID: "late", At: at - 2*3600, Points: 5}
	fetch = func(_ context.Context, from float64) ([]pointDebit, error) {
		if from < at-86401 || from > at-86399 {
			t.Fatal("refresh did not overlap the prior day", from)
		}
		return []pointDebit{recent, late}, nil
	}
	r = p.refresh(context.Background(), "a", since, true, fetch)
	if num(r["spent_total"]) != 16 || num(r["spent_day"]) != 9 {
		t.Fatal("overlap lost old history or double counted a charge", r)
	}
	r = p.refresh(context.Background(), "a", since, true, func(context.Context, float64) ([]pointDebit, error) {
		return []pointDebit{{At: at - 1, Points: 1000}}, errors.New("second page failed")
	})
	if num(r["spent_total"]) != 16 || text(r["error"]) == "" {
		t.Fatal("failed query replaced a complete snapshot", r)
	}
	r = p.refresh(context.Background(), "a", since, true, fetch)
	if text(r["error"]) != "" || num(r["spent_total"]) != 16 {
		t.Fatal("recovery lost the cumulative amount", r)
	}
	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newPointSpendLog(filepath.Dir(p.path), "ledger")
	if err != nil || num(reopened.view("a", since, now())["spent_total"]) != 16 {
		t.Fatal("lifetime points did not survive restart", err)
	}
}

func TestPointSpendingUnknownZeroAndInvalidData(t *testing.T) {
	p := newPointTestLog(t, "ledger")
	since := now() - 1000
	r := p.refresh(context.Background(), "a", since, true, func(context.Context, float64) ([]pointDebit, error) {
		return nil, errors.New("upstream unavailable")
	})
	if r["spent_total"] != nil || text(r["error"]) == "" {
		t.Fatal("failed initial fetch looked like zero spending", r)
	}
	r = p.refresh(context.Background(), "a", since, true, func(context.Context, float64) ([]pointDebit, error) {
		return nil, nil
	})
	if r["spent_total"] == nil || num(r["spent_total"]) != 0 || text(r["error"]) != "" {
		t.Fatal("empty but complete ledger did not report zero", r)
	}
	r = p.refresh(context.Background(), "a", since, true, func(context.Context, float64) ([]pointDebit, error) {
		return []pointDebit{{At: 0, Points: 100}}, nil
	})
	if num(r["spent_total"]) != 0 || text(r["error"]) == "" {
		t.Fatal("invalid debit corrupted the snapshot", r)
	}
}

func TestPointSpendingGatewayReplayAndAccountIsolation(t *testing.T) {
	p := newPointTestLog(t, "gateway")
	at := now() - 60
	legacy := M{"uid": "a", "ts": at, "credit": 0.25}
	p.recordUsage(legacy)
	p.recordUsage(legacy)
	p.recordUsage(M{"uid": "a", "ts": at, "credit": 0.25, "point_id": "different"})
	p.recordUsage(M{"uid": "b", "ts": at, "credit": 9})
	p.recordUsage(M{"uid": "a", "ts": at, "credit": -100})
	p.recordUsage(M{"uid": "a", "ts": at})
	r := p.view("a", at-1, now())
	if num(r["spent_total"]) != 0.5 || !yes(r["complete"]) {
		t.Fatal("replayed or missing credit changed the amount", r)
	}
	if num(p.view("b", at-1, now())["spent_total"]) != 9 {
		t.Fatal("account usage leaked")
	}
	if yes(p.view("a", p.data.Coverage-1, now())["complete"]) {
		t.Fatal("incomplete old request history was called complete")
	}
	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestPointSpendingCoverageSurvivesRestartWithoutCharges(t *testing.T) {
	p := newPointTestLog(t, "gateway")
	coverage := p.data.Coverage
	if err := p.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newPointSpendLog(filepath.Dir(p.path), "gateway")
	if err != nil || reopened.data.Coverage != coverage {
		t.Fatal("zero-usage coverage was reset on restart", err)
	}
}
