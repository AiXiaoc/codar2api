package gateway

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func schedulerStore(t *testing.T, weights ...int) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i, weight := range weights {
		uid := fmt.Sprint(i)
		if _, err := s.Upsert(M{"uid": uid, "username": uid, "token": fakeToken(uid), "weight": weight, "balance": M{"agent_token": 100}}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func pickAndRelease(t *testing.T, s *Store, hint string, exclude map[string]bool) string {
	t.Helper()
	a, err := s.Pick("auto", hint, exclude, nil)
	if err != nil {
		t.Fatal(err)
	}
	uid := text(a["uid"])
	s.Release(uid)
	return uid
}

func TestSchedulingRoundRobinWeightedAndSticky(t *testing.T) {
	s := schedulerStore(t, 5, 3, 2)
	if s.Settings()["scheduling_strategy"] != "round_robin" {
		t.Fatal("new installations should default to round robin")
	}
	got := []string{}
	for i := 0; i < 9; i++ {
		got = append(got, pickAndRelease(t, s, "same-session", nil))
	}
	if want := []string{"0", "1", "2", "0", "1", "2", "0", "1", "2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("round robin ignored same-session rotation: %v", got)
	}
	if _, err := s.SaveSettings(M{"scheduling_strategy": "weighted_round_robin"}); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		counts[pickAndRelease(t, s, "same-session", nil)]++
	}
	if want := map[string]int{"0": 50, "1": 30, "2": 20}; !reflect.DeepEqual(counts, want) {
		t.Fatalf("wrong weighted distribution: %v", counts)
	}
	if _, err := s.SaveSettings(M{"scheduling_strategy": "sticky"}); err != nil {
		t.Fatal(err)
	}
	first := pickAndRelease(t, s, "conversation-1", nil)
	for i := 0; i < 10; i++ {
		if uid := pickAndRelease(t, s, "conversation-1", nil); uid != first {
			t.Fatal("sticky conversation moved")
		}
	}
	if next := pickAndRelease(t, s, "conversation-2", nil); next == first {
		t.Fatal("new sticky sessions did not rotate")
	}
	next := pickAndRelease(t, s, "conversation-1", map[string]bool{first: true})
	if next == first || pickAndRelease(t, s, "conversation-1", nil) != next {
		t.Fatal("sticky failover did not update the session")
	}
}

func TestSchedulingLeastConnectionsAndCapacity(t *testing.T) {
	s := schedulerStore(t, 9999, 1, 1)
	if _, err := s.SaveSettings(M{"scheduling_strategy": "least_connections"}); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{"0", "0", "1"} {
		if _, err := s.Reserve(uid, "auto", nil); err != nil {
			t.Fatal(err)
		}
	}
	if uid := pickAndRelease(t, s, "", nil); uid != "2" {
		t.Fatalf("least connections chose %s instead of empty account 2", uid)
	}
	if err := s.Update("2", func(a M) error { a["max_concurrency"] = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve("2", "auto", nil); err != nil {
		t.Fatal(err)
	}
	if uid := pickAndRelease(t, s, "", nil); uid != "1" {
		t.Fatalf("least connections did not skip capped account: %s", uid)
	}
	for _, uid := range []string{"0", "0", "1", "2"} {
		s.Release(uid)
	}
	for _, row := range s.Concurrency() {
		if num(row["current_concurrency"]) != 0 {
			t.Fatal("release leaked inflight requests")
		}
	}
}

func TestEveryStrategySkipsUnavailableAndFullAccounts(t *testing.T) {
	for _, option := range schedulingOptions {
		t.Run(text(option["value"]), func(t *testing.T) {
			s := schedulerStore(t, 1, 1, 1, 1, 1, 1, 1)
			if _, err := s.SaveSettings(M{"scheduling_strategy": option["value"]}); err != nil {
				t.Fatal(err)
			}
			changes := []M{{"enabled": false}, {"restricted": true}, {"token_invalid": true}, {"cooldown_until": now() + 100}, {"max_concurrency": 1}}
			for i, patch := range changes {
				if err := s.Update(fmt.Sprint(i), func(a M) error {
					for k, v := range patch {
						a[k] = v
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Reserve("4", "auto", nil); err != nil {
				t.Fatal(err)
			}
			defer s.Release("4")
			for i := 0; i < 5; i++ {
				a, err := s.Pick("auto", "same-session", nil, func(uid string) bool { return uid == "5" })
				if err != nil || text(a["uid"]) != "6" {
					t.Fatalf("unavailable/full account selected: %v %v", a, err)
				}
				s.Release("6")
			}
		})
	}
}

func TestConcurrencyReservationsAreAtomicAndDefaultUnlimited(t *testing.T) {
	for _, limited := range []bool{true, false} {
		t.Run(fmt.Sprint(limited), func(t *testing.T) {
			s := schedulerStore(t, 1, 1)
			if limited {
				for _, uid := range []string{"0", "1"} {
					if err := s.Update(uid, func(a M) error { a["max_concurrency"] = 2; return nil }); err != nil {
						t.Fatal(err)
					}
				}
			}
			start := make(chan struct{})
			results := make(chan string, 100)
			var wg sync.WaitGroup
			for i := 0; i < 100; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					a, err := s.Pick("auto", "", nil, nil)
					if err != nil && !errors.Is(err, errAccountCapacity) {
						t.Error(err)
					}
					if a != nil {
						results <- text(a["uid"])
					}
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			want := 100
			if limited {
				want = 4
			}
			if len(results) != want {
				t.Fatalf("admitted %d, want %d", len(results), want)
			}
			for _, row := range s.Concurrency() {
				if limited && num(row["current_concurrency"]) > 2 {
					t.Fatal("account concurrency limit exceeded")
				}
			}
			for uid := range results {
				s.Release(uid)
			}
			for _, row := range s.Concurrency() {
				if num(row["current_concurrency"]) != 0 {
					t.Fatal("release did not return to zero")
				}
			}
		})
	}
}

func TestSchedulingHotWeightLimitAndStickyCapacity(t *testing.T) {
	s := schedulerStore(t, 1, 1)
	if _, err := s.SaveSettings(M{"scheduling_strategy": "weighted_round_robin"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		pickAndRelease(t, s, "", nil)
	}
	if err := s.Update("0", func(a M) error { a["weight"] = 9; return nil }); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		counts[pickAndRelease(t, s, "", nil)]++
	}
	if counts["0"] != 90 || counts["1"] != 10 {
		t.Fatalf("hot weight was not applied: %v", counts)
	}
	if _, err := s.SaveSettings(M{"scheduling_strategy": "sticky"}); err != nil {
		t.Fatal(err)
	}
	first := pickAndRelease(t, s, "sticky-conversation", nil)
	for i := 0; i < 2; i++ {
		if _, err := s.Reserve(first, "auto", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Update(first, func(a M) error { a["max_concurrency"] = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := s.Concurrency()[0]; num(got["current_concurrency"]) != 2 {
		t.Fatal("lowering limit changed active requests")
	}
	if uid := pickAndRelease(t, s, "sticky-conversation", nil); uid == first {
		t.Fatal("sticky strategy dispatched a capped account")
	}
	if _, err := s.Reserve(first, "auto", nil); !errors.Is(err, errAccountCapacity) {
		t.Fatal("lowered limit not enforced")
	}
	if err := s.Update(first, func(a M) error { a["max_concurrency"] = 0; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(first, "auto", nil); err != nil {
		t.Fatal("unlimited setting did not take effect")
	}
	for i := 0; i < 3; i++ {
		s.Release(first)
	}
}

func TestAccountSchedulingValidationPersistenceAndImport(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "a", nil)
	for _, patch := range []M{{"weight": 0}, {"weight": 10001}, {"weight": 1.5}, {"weight": "2"}, {"weight": nil}, {"max_concurrency": -1}, {"max_concurrency": 1000001}, {"max_concurrency": true}, {"max_concurrency": 1.5}} {
		before := h.app.store.Get("a")
		patch["uid"], patch["note"] = "a", "should-not-save"
		if r := h.post(t, "/accounts/update", patch); yes(r["ok"]) {
			t.Fatalf("accepted invalid patch %v", patch)
		}
		if !reflect.DeepEqual(before, h.app.store.Get("a")) {
			t.Fatal("invalid patch partially modified account")
		}
	}
	if r := h.post(t, "/accounts/update", M{"uid": "a", "weight": 7, "max_concurrency": 3}); !yes(r["ok"]) {
		t.Fatal(r)
	}
	for _, invalid := range []any{nil, "invalid", true, 4, []any{}, M{}} {
		resp, _ := h.request(t, "POST", "/settings", M{"scheduling_strategy": invalid}, false)
		if resp.StatusCode != 400 {
			t.Fatalf("accepted strategy %v", invalid)
		}
	}
	h.post(t, "/accounts/refresh", M{"uid": "a"})
	h.post(t, "/accounts/add", M{"token": fakeToken("a")})
	a := h.app.store.Get("a")
	if num(a["weight"]) != 7 || num(a["max_concurrency"]) != 3 {
		t.Fatal("refresh or re-import lost local scheduling settings")
	}
	if _, err := h.app.store.Reserve("a", "auto", nil); err != nil {
		t.Fatal(err)
	}
	defer h.app.store.Release("a")
	_, body := h.request(t, "GET", "/accounts/concurrency", nil, false)
	if num(obj(arr(decode(t, body)["accounts"])[0])["current_concurrency"]) != 1 {
		t.Fatal("runtime API omitted active request")
	}
	s, err := NewStore(h.app.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if num(s.Get("a")["weight"]) != 7 || num(s.Get("a")["max_concurrency"]) != 3 || num(s.Concurrency()[0]["current_concurrency"]) != 0 {
		t.Fatal("settings lost on reload or runtime count persisted")
	}
	exported := h.post(t, "/accounts/export", M{})
	row := obj(arr(exported["accounts"])[0])
	if num(row["weight"]) != 7 || num(row["max_concurrency"]) != 3 {
		t.Fatal("export lost scheduling settings")
	}
	other := newHarness(t, nil)
	if r := other.post(t, "/accounts/import", exported); num(r["added"]) != 1 {
		t.Fatal(r)
	}
	if a := other.app.store.Get("a"); num(a["weight"]) != 7 || num(a["max_concurrency"]) != 3 {
		t.Fatal("import lost scheduling settings")
	}
}

func TestLegacyStickyMigrationAndSchedulingDefaults(t *testing.T) {
	for _, sticky := range []bool{true, false} {
		dir := t.TempDir()
		if err := writeJSON(filepath.Join(dir, "settings.json"), M{"session_sticky": sticky}); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(dir, "accounts.json"), []M{{"uid": "a", "token": fakeToken("a"), "current_concurrency": 99}}); err != nil {
			t.Fatal(err)
		}
		s, err := NewStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		want := "round_robin"
		if sticky {
			want = "sticky"
		}
		if s.Settings()["scheduling_strategy"] != want {
			t.Fatal("legacy strategy not migrated")
		}
		if _, exists := s.Settings()["session_sticky"]; exists {
			t.Fatal("legacy toggle still exposed")
		}
		if a := s.Get("a"); num(a["weight"]) != 1 || num(a["max_concurrency"]) != 0 {
			t.Fatal("legacy account defaults missing")
		}
	}
}
