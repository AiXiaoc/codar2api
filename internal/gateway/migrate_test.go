package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPythonMigrationPreservesAccountsAndProxyBindings(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "python"), filepath.Join(root, "go")
	if e := os.MkdirAll(source, 0700); e != nil {
		t.Fatal(e)
	}
	accounts := []M{{"uid": "u1", "username": "user", "token": fakeToken("user"), "enabled": false, "restricted": true, "restricted_reason": "original restriction", "stats": M{"requests": 42}}}
	if e := writeJSON(filepath.Join(source, "accounts.json"), accounts); e != nil {
		t.Fatal(e)
	}
	hub := filepath.Join(root, "hub-proxies.json")
	writeJSON(hub, M{"next_id": 3, "proxies": []any{M{"id": 2, "name": "proxy", "url": "http://alice:password@127.0.0.1:1080", "enabled": true}}, "accounts": M{"codar:u1": M{"mode": "pick", "ids": []any{2}}, "raccoon:unrelated": M{"mode": "pick", "ids": []any{2}}}})
	before, e := os.ReadFile(filepath.Join(source, "accounts.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = ImportPython(source, target, hub); e != nil {
		t.Fatal(e)
	}
	store, e := NewStore(target)
	if e != nil {
		t.Fatal(e)
	}
	acc := store.Get("u1")
	if yes(acc["enabled"]) || !yes(acc["restricted"]) || num(obj(acc["stats"])["requests"]) != 42 {
		t.Fatal("account migration lost state")
	}
	pool, e := NewProxyPool(target, "")
	if e != nil {
		t.Fatal(e)
	}
	route, e := pool.Account("u1")
	if e != nil || route.ID != 2 {
		t.Fatal("proxy binding migration failed")
	}
	if _, ok := obj(pool.data["accounts"])["raccoon:unrelated"]; ok {
		t.Fatal("unrelated bindings imported")
	}
	after, _ := os.ReadFile(filepath.Join(source, "accounts.json"))
	if string(before) != string(after) {
		t.Fatal("source changed")
	}
	if e = ImportPython(source, target, hub); e == nil {
		t.Fatal("existing data overwritten")
	}
}
func TestMigrationRejectsMalformedDataAndNestedTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "python")
	os.MkdirAll(source, 0700)
	writeJSON(filepath.Join(source, "accounts.json"), []M{{"uid": "a"}})
	if e := ImportPython(source, filepath.Join(source, "nested"), ""); e == nil {
		t.Fatal("nested target allowed")
	}
	writeJSON(filepath.Join(source, "settings.json"), M{"refresh_minutes": 30})
	os.WriteFile(filepath.Join(source, "prices.json"), []byte("broken"), 0600)
	target := filepath.Join(root, "go")
	if e := ImportPython(source, target, ""); e == nil {
		t.Fatal("bad JSON accepted")
	}
	if _, e := os.Stat(target); !os.IsNotExist(e) {
		t.Fatal("partial migration left destination")
	}
}
func TestPersistenceFailuresRollBackBatchAndFailClosedRestriction(t *testing.T) {
	dir := t.TempDir()
	store, e := NewStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	_, e = store.Upsert(M{"uid": "a", "username": "a", "token": fakeToken("a")})
	if e != nil {
		t.Fatal(e)
	}
	os.Remove(filepath.Join(dir, "accounts.json"))
	os.Mkdir(filepath.Join(dir, "accounts.json"), 0700)
	if _, e = store.Batch(M{"action": "disable", "uids": []any{"a"}}); e == nil {
		t.Fatal("save failure not reported")
	}
	if !yes(store.Get("a")["enabled"]) {
		t.Fatal("batch memory not rolled back")
	}
	if e = store.Restrict("a", "restricted"); e == nil {
		t.Fatal("restriction save failure not reported")
	}
	if !yes(store.Get("a")["restricted"]) {
		t.Fatal("restriction failed open after disk error")
	}
	if picked, _ := store.Pick("auto", "", map[string]bool{}, nil); picked != nil {
		t.Fatal("unsaved restriction permitted dispatch")
	}
}
