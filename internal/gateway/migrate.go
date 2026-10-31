package gateway

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ImportPython copies only Codar data, never credentials from other Hub platforms.
// It validates the source before creating the destination and never overwrites data.
func ImportPython(source, target, hubProxyPath string) error {
	source, e := filepath.Abs(source)
	if e != nil {
		return e
	}
	target, e = filepath.Abs(target)
	if e != nil {
		return e
	}
	if strings.EqualFold(source, target) {
		return errors.New("源目录与目标目录不能相同")
	}
	rel, e := filepath.Rel(source, target)
	if e == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
		return errors.New("目标目录不能放在源数据目录内")
	}
	entries, e := os.ReadDir(target)
	if e == nil && len(entries) > 0 {
		return errors.New("目标数据目录非空，请选择新的目录")
	}
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	var accounts []M
	if e = readJSON(filepath.Join(source, "accounts.json"), &accounts); e != nil {
		return fmt.Errorf("读取源 accounts.json: %w", e)
	}
	seen := map[string]bool{}
	for _, acc := range accounts {
		uid := text(acc["uid"])
		if uid == "" || seen[uid] {
			return errors.New("源数据存在空账号 ID 或重复 ID")
		}
		seen[uid] = true
	}
	files := []string{"accounts.json", "settings.json", "catalog.json", "prices.json", "usage.jsonl"}
	proxy := M{"next_id": 1, "proxies": []any{}, "accounts": M{}}
	if hubProxyPath != "" {
		var hub M
		if e = readJSON(hubProxyPath, &hub); e != nil {
			return e
		}
		proxy["proxies"] = arr(hub["proxies"])
		proxy["next_id"] = hub["next_id"]
		bindings := M{}
		for k, v := range obj(hub["accounts"]) {
			if strings.HasPrefix(k, "codar:") {
				bindings[strings.TrimPrefix(k, "codar:")] = v
			}
		}
		proxy["accounts"] = bindings
	}
	// Stage next to the target so a successful import is a single directory rename.
	parent := filepath.Dir(target)
	if e = os.MkdirAll(parent, 0700); e != nil {
		return e
	}
	stage, e := os.MkdirTemp(parent, ".codar-import-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	for _, name := range files {
		path := filepath.Join(source, name)
		info, e := os.Lstat(path)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("不支持的源文件类型：%s", name)
		}
		if strings.HasSuffix(name, ".json") {
			var v any
			if e = readJSON(path, &v); e != nil {
				return e
			}
		}
		dest := filepath.Join(stage, name)
		if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
			return e
		}
		in, e := os.Open(path)
		if e != nil {
			return e
		}
		out, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			in.Close()
			return e
		}
		_, e = io.Copy(out, in)
		in.Close()
		if e == nil {
			e = out.Sync()
		}
		ce := out.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			return e
		}
	}
	if e = writeJSON(filepath.Join(stage, "proxies.json"), proxy); e != nil {
		return e
	}
	if _, e = NewStore(stage); e != nil {
		return e
	}
	if _, e = NewProxyPool(stage, "https://api.claw.speedai.com/v1/codar/models"); e != nil {
		return e
	}
	if _, e = os.Stat(target); e == nil {
		if e = os.Remove(target); e != nil {
			return e
		}
	}
	if e = os.Rename(stage, target); e != nil {
		return e
	}
	fmt.Printf("已迁移 %d 个账号到 %s，原数据未改动。\n", len(accounts), target)
	return nil
}
