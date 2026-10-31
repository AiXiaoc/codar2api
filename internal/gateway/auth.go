package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Passwords use PBKDF2-HMAC-SHA256. API keys and session IDs have 256 random bits;
// keys are matched by their SHA-256 and also kept in keys.json for copying.
func passwordHash(password, salt string) string {
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write([]byte(salt))
	mac.Write([]byte{0, 0, 0, 1})
	u := mac.Sum(nil)
	out := append([]byte{}, u...)
	for i := 1; i < 210000; i++ {
		mac.Reset()
		mac.Write(u)
		u = mac.Sum(nil)
		for j := range out {
			out[j] ^= u[j]
		}
	}
	return hex.EncodeToString(out)
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

type attempt struct {
	count int
	until time.Time
}
type Auth struct {
	mu          sync.Mutex
	dir         string
	credentials M
	keys        []M
	sessions    map[string]time.Time
	attempts    map[string]attempt
	dirty       bool
	secure      bool
}

// NewAuth creates the admin credentials on first start: the given password, else
// fallback (exempt from the length rule), else a random one returned as initial.
func NewAuth(dir, password, fallback string, secure bool) (*Auth, string, error) {
	a := &Auth{dir: dir, credentials: M{}, keys: []M{}, sessions: map[string]time.Time{}, attempts: map[string]attempt{}, secure: secure}
	initial := ""
	if e := loadOptional(filepath.Join(dir, "admin.json"), &a.credentials); e != nil {
		return nil, "", e
	}
	if text(a.credentials["hash"]) == "" {
		short := password != "" && len(password) < 10
		if password == "" && fallback != "" {
			password = fallback
			initial = password
		}
		if password == "" {
			password = random(12)
			initial = password
		}
		if short {
			return nil, "", errors.New("初始管理员密码至少 10 个字符")
		}
		salt := random(16)
		a.credentials = M{"salt": salt, "hash": passwordHash(password, salt)}
		if e := writeJSON(filepath.Join(dir, "admin.json"), a.credentials); e != nil {
			return nil, "", e
		}
	}
	if e := loadOptional(filepath.Join(dir, "keys.json"), &a.keys); e != nil {
		return nil, "", e
	}
	return a, initial, nil
}
func (a *Auth) Admin(r *http.Request) bool {
	c, e := r.Cookie("codar_session")
	if e != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := digest(c.Value)
	t, ok := a.sessions[key]
	if !ok {
		return false
	}
	if time.Now().After(t) {
		delete(a.sessions, key)
		return false
	}
	return true
}
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, e := url.Parse(o)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host)
}
func (a *Auth) Login(w http.ResponseWriter, r *http.Request, password string) error {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	a.mu.Lock()
	at := a.attempts[ip]
	if time.Now().Before(at.until) && at.count >= 5 {
		a.mu.Unlock()
		return errors.New("尝试次数过多，请 5 分钟后再试")
	}
	if time.Now().After(at.until) {
		at = attempt{until: time.Now().Add(5 * time.Minute)}
	}
	at.count++
	a.attempts[ip] = at
	creds := clone(a.credentials)
	a.mu.Unlock()
	hash := passwordHash(password, text(creds["salt"]))
	if !equal(hash, text(creds["hash"])) {
		return errors.New("管理员密码错误")
	}
	token := random(32)
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.attempts, ip)
	for k, t := range a.sessions {
		if time.Now().After(t) {
			delete(a.sessions, k)
		}
	}
	if len(a.sessions) > 1000 {
		a.sessions = map[string]time.Time{}
	}
	if len(a.attempts) > 10000 {
		for k, v := range a.attempts {
			if time.Now().After(v.until) {
				delete(a.attempts, k)
			}
		}
	}
	a.sessions[digest(token)] = time.Now().Add(24 * time.Hour)
	http.SetCookie(w, &http.Cookie{Name: "codar_session", Value: token, Path: "/", HttpOnly: true, Secure: a.secure || r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
	return nil
}
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, e := r.Cookie("codar_session"); e == nil {
		delete(a.sessions, digest(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "codar_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.secure || r.TLS != nil, SameSite: http.SameSiteStrictMode})
}
func (a *Auth) ChangePassword(old, password string) error {
	if len(password) < 10 || len(password) > 1024 {
		return errors.New("密码长度应为 10 至 1024 个字符")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !equal(passwordHash(old, text(a.credentials["salt"])), text(a.credentials["hash"])) {
		return errors.New("当前密码错误")
	}
	salt := random(16)
	next := M{"salt": salt, "hash": passwordHash(password, salt)}
	if e := writeJSON(filepath.Join(a.dir, "admin.json"), next); e != nil {
		return e
	}
	a.credentials = next
	a.sessions = map[string]time.Time{}
	return nil
}

// publicKey hides the hash and the stored key. Keys keep their full value so the
// panel can copy them again; keys from older versions only have the hash.
func publicKey(k M) M {
	d := clone(k)
	d["copyable"] = text(k["token"]) != ""
	delete(d, "hash")
	delete(d, "token")
	return d
}
func (a *Auth) Keys() []M {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []M{}
	for _, k := range a.keys {
		out = append(out, publicKey(k))
	}
	return out
}
func (a *Auth) KeysAction(p M) (M, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	action := text(p["action"])
	if action == "reveal" {
		for _, k := range a.keys {
			if k["id"] == p["id"] {
				if text(k["token"]) == "" {
					return nil, errors.New("这个 Key 由旧版本创建，只保存了哈希，无法再复制，请新建一个")
				}
				return M{"ok": true, "token": k["token"]}, nil
			}
		}
		return nil, errors.New("未找到 API Key")
	}
	next := clone(a.keys)
	var result M
	switch action {
	case "create":
		name := strings.TrimSpace(text(p["name"]))
		if name == "" || len(name) > 100 {
			return nil, errors.New("请输入 1 至 100 字符的 Key 名称")
		}
		exp := num(p["expires_at"])
		if exp != 0 && exp <= now() {
			return nil, errors.New("到期时间必须晚于当前时间")
		}
		models, e := keyModels(p["models"])
		if e != nil {
			return nil, e
		}
		token := "sk-codar-" + random(32)
		k := M{"id": random(8), "name": name, "hash": digest(token), "token": token, "prefix": token[:17], "enabled": true, "created_at": now(), "expires_at": exp, "models": models, "requests": 0, "tokens": 0}
		next = append(next, k)
		result = M{"ok": true, "key": publicKey(k), "token": token}
	case "update", "delete":
		found := false
		for i, k := range next {
			if k["id"] != p["id"] {
				continue
			}
			found = true
			if action == "delete" {
				next = append(next[:i], next[i+1:]...)
			} else {
				if v, ok := p["enabled"]; ok {
					b, ok := v.(bool)
					if !ok {
						return nil, errors.New("enabled 必须是布尔值")
					}
					k["enabled"] = b
				}
				if v, ok := p["name"]; ok {
					n := strings.TrimSpace(text(v))
					if n == "" || len(n) > 100 {
						return nil, errors.New("Key 名称格式不对")
					}
					k["name"] = n
				}
				if v, ok := p["models"]; ok {
					models, e := keyModels(v)
					if e != nil {
						return nil, e
					}
					k["models"] = models
				}
			}
			break
		}
		if !found {
			return nil, errors.New("未找到 API Key")
		}
		result = M{"ok": true}
	default:
		return nil, errors.New("未知的 Key 操作")
	}
	if e := writeJSON(filepath.Join(a.dir, "keys.json"), next); e != nil {
		return nil, e
	}
	a.keys = next
	return result, nil
}

// keyModels validates a key's model scope; an empty list allows every model.
func keyModels(v any) ([]any, error) {
	if v == nil {
		return []any{}, nil
	}
	list, ok := v.([]any)
	if !ok || len(list) > 500 {
		return nil, errors.New("模型范围必须是模型 ID 列表")
	}
	out, seen := []any{}, map[string]bool{}
	for _, m := range list {
		s, ok := m.(string)
		if s = strings.TrimSpace(s); !ok || s == "" || len(s) > 100 {
			return nil, errors.New("模型 ID 格式不对")
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}
func (a *Auth) API(r *http.Request, model string) (M, error) {
	tok := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
		tok = strings.TrimSpace(h[7:])
	}
	if tok == "" {
		tok = r.Header.Get("x-api-key")
	}
	if tok == "" {
		return nil, errors.New("请提供 API Key")
	}
	hash := digest(tok)
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range a.keys {
		if !equal(hash, text(k["hash"])) {
			continue
		}
		if !yes(k["enabled"]) {
			return nil, errors.New("API Key 已停用")
		}
		if n := num(k["expires_at"]); n > 0 && n <= now() {
			return nil, errors.New("API Key 已过期")
		}
		if model != "" && len(arr(k["models"])) > 0 {
			allowed := false
			for _, m := range arr(k["models"]) {
				if text(m) == model {
					allowed = true
				}
			}
			if !allowed {
				return nil, fmt.Errorf("API Key 无权使用模型 %s", model)
			}
		}
		return clone(k), nil
	}
	return nil, errors.New("API Key 无效")
}
func (a *Auth) Count(id string, tokens float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range a.keys {
		if k["id"] == id {
			k["requests"] = num(k["requests"]) + 1
			k["tokens"] = num(k["tokens"]) + tokens
			k["last_used"] = now()
			a.dirty = true
			break
		}
	}
}
func (a *Auth) Flush() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.dirty {
		return nil
	}
	if e := writeJSON(filepath.Join(a.dir, "keys.json"), a.keys); e != nil {
		return e
	}
	a.dirty = false
	return nil
}
