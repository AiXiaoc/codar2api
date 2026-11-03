package gateway

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBatchConcurrencyScopeValidationAndPersistence(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "a", M{"weight": 7, "max_concurrency": 2, "note": "keep", "restricted": true})
	h.add(t, "b", M{"weight": 3, "max_concurrency": 4, "enabled": false})
	h.add(t, "c", M{"max_concurrency": 6})
	before := h.app.store.List()
	for _, value := range []any{nil, "3", true, -1, 1.5, 1000001} {
		resp, _ := h.request(t, "POST", "/accounts/batch", M{"action": "set_concurrency", "all": true, "max_concurrency": value}, false)
		if resp.StatusCode != 400 {
			t.Fatalf("accepted invalid limit %v", value)
		}
	}
	for _, patch := range []M{
		{"action": "set_concurrency", "all": true},
		{"action": "set_concurrency", "max_concurrency": 3, "uids": []any{"a", "missing"}},
		{"action": "set_concurrency", "max_concurrency": 3, "uids": []any{}},
		{"action": "set_concurrency", "max_concurrency": 3, "all": true, "uids": []any{"a"}},
	} {
		resp, _ := h.request(t, "POST", "/accounts/batch", patch, false)
		if resp.StatusCode != 400 {
			t.Fatalf("accepted invalid batch %v", patch)
		}
	}
	if !reflect.DeepEqual(before, h.app.store.List()) {
		t.Fatal("invalid batch partially changed accounts")
	}
	result := h.post(t, "/accounts/batch", M{"action": "set_concurrency", "uids": []any{"a", "b", "a"}, "max_concurrency": 3})
	if !yes(result["ok"]) || num(result["count"]) != 2 || num(result["updated"]) != 2 {
		t.Fatal("selected batch did not deduplicate accounts", result)
	}
	for _, uid := range []string{"a", "b"} {
		if num(h.app.store.Get(uid)["max_concurrency"]) != 3 {
			t.Fatalf("account %s limit not changed", uid)
		}
	}
	if num(h.app.store.Get("c")["max_concurrency"]) != 6 || !yes(h.app.store.Get("a")["restricted"]) || yes(h.app.store.Get("b")["enabled"]) || num(h.app.store.Get("a")["weight"]) != 7 || h.app.store.Get("a")["note"] != "keep" {
		t.Fatal("batch changed untargeted accounts or unrelated preferences")
	}
	result = h.post(t, "/accounts/batch", M{"action": "set_concurrency", "all": true, "max_concurrency": 1000000})
	if num(result["count"]) != 3 || num(result["updated"]) != 3 {
		t.Fatal(result)
	}
	result = h.post(t, "/accounts/batch", M{"action": "set_concurrency", "all": true, "max_concurrency": 0})
	if !yes(result["ok"]) || num(result["count"]) != 3 {
		t.Fatal("unlimited limit rejected", result)
	}
	reopened, err := NewStore(h.app.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range reopened.List() {
		if num(account["max_concurrency"]) != 0 {
			t.Fatal("batch concurrency not persisted")
		}
	}
}

func TestBatchConcurrencyRetainsInflightAndAppliesImmediately(t *testing.T) {
	s := schedulerStore(t, 1, 1)
	for i := 0; i < 2; i++ {
		if _, err := s.Reserve("0", "auto", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Batch(M{"action": "set_concurrency", "all": true, "max_concurrency": 1}); err != nil {
		t.Fatal(err)
	}
	if num(s.Concurrency()[0]["current_concurrency"]) != 2 {
		t.Fatal("lowering limits changed running reservations")
	}
	if _, err := s.Reserve("0", "auto", nil); !errors.Is(err, errAccountCapacity) {
		t.Fatal("new reservation bypassed batch limit", err)
	}
	if _, err := s.Batch(M{"action": "set_concurrency", "uids": []any{"0"}, "max_concurrency": 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve("0", "auto", nil); err != nil {
		t.Fatal("unlimited batch did not take immediate effect", err)
	}
	for i := 0; i < 3; i++ {
		s.Release("0")
	}
}

func TestBatchConcurrencyPersistenceFailureRollsBack(t *testing.T) {
	s := schedulerStore(t, 1, 1)
	before := s.List()
	target := filepath.Join(s.dir, "accounts.json")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Batch(M{"action": "set_concurrency", "all": true, "max_concurrency": 5}); err == nil {
		t.Fatal("save failure was not reported")
	}
	if !reflect.DeepEqual(before, s.List()) {
		t.Fatal("save failure left partial changes in memory")
	}
}
