package gateway

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed web/* defaults.json
var assets embed.FS

var Version = "0.1.2"

type Config struct {
	Addr, DataDir, Password, APIBase, PayBase, WebBase, CheckURL, LocalConfig string
	// DefaultPassword is used on first start when Password is empty (Windows: "admin").
	DefaultPassword            string
	SecureCookie, NoBackground bool
	RequestDiagnostics         bool
	MaxConcurrent              int
}

func (c *Config) defaults() {
	if c.Addr == "" {
		c.Addr = "127.0.0.1:8801"
	}
	if c.DataDir == "" {
		c.DataDir = "data"
	}
	if c.APIBase == "" {
		c.APIBase = "https://api.claw.speedai.com"
	}
	if c.PayBase == "" {
		c.PayBase = "https://api.kuaipaper.com"
	}
	if c.WebBase == "" {
		c.WebBase = "https://codar-web.speedai.com"
	}
	if c.CheckURL == "" {
		c.CheckURL = c.APIBase + "/v1/codar/models"
	}
	if c.LocalConfig == "" {
		home, _ := os.UserHomeDir()
		c.LocalConfig = filepath.Join(home, ".codar", "config.json")
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = 128
	}
	c.APIBase = strings.TrimRight(c.APIBase, "/")
	c.PayBase = strings.TrimRight(c.PayBase, "/")
	c.WebBase = strings.TrimRight(c.WebBase, "/")
}

type App struct {
	cfg           Config
	store         *Store
	auth          *Auth
	proxies       *ProxyPool
	usage         *UsageLog
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	gates         chan struct{}
	ops           sync.Map
	catalogMu     sync.Mutex
	annMu         sync.Mutex
	ann           M
	rateMu        sync.Mutex
	rate          M
	loginMu       sync.Mutex
	flows         map[string]M
	qr            map[string]M
	priceDefaults M
	diagnostics   *requestDiagnosticLog
}

func New(cfg Config) (*App, string, error) {
	cfg.defaults()
	store, e := NewStore(cfg.DataDir)
	if e != nil {
		return nil, "", e
	}
	auth, initial, e := NewAuth(cfg.DataDir, cfg.Password, cfg.DefaultPassword, cfg.SecureCookie)
	if e != nil {
		return nil, "", e
	}
	proxies, e := NewProxyPool(cfg.DataDir, cfg.CheckURL)
	if e != nil {
		return nil, "", e
	}
	usage, e := NewUsageLog(cfg.DataDir)
	if e != nil {
		return nil, "", e
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{cfg: cfg, store: store, auth: auth, proxies: proxies, usage: usage, ctx: ctx, cancel: cancel, gates: make(chan struct{}, cfg.MaxConcurrent), ann: M{"items": []any{}}, rate: M{}, flows: map[string]M{}, qr: map[string]M{}}
	if cfg.RequestDiagnostics {
		a.diagnostics = &requestDiagnosticLog{path: filepath.Join(cfg.DataDir, "request-diagnostics.jsonl")}
		log.Printf("request diagnostics enabled: %s", a.diagnostics.path)
	}
	b, _ := assets.ReadFile("defaults.json")
	var d M
	_ = json.Unmarshal(b, &d)
	a.priceDefaults = obj(d["PRICE_DEFAULTS"])
	a.wg.Add(1)
	go a.maintenance()
	return a, initial, nil
}
func (a *App) Close() error {
	a.cancel()
	a.wg.Wait()
	a.proxies.Close()
	return errors.Join(a.store.Flush(), a.auth.Flush(), a.usage.Flush())
}
func (a *App) lockAccount(uid string) func() {
	m, _ := a.ops.LoadOrStore(uid, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
func (a *App) public(account M) M {
	policy := a.proxies.Policy(text(account["uid"]))
	d := a.store.Public(account, policy)
	if yes(policy["blocked"]) && yes(a.store.Settings()["ip_block"]) && yes(d["ready"]) {
		d["ready"], d["reason"] = false, "出口 IP 封控"
	}
	return d
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func apiError(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, M{"ok": false, "error": M{"message": msg, "type": "gateway_error"}})
}
func payload(w http.ResponseWriter, r *http.Request) (M, error) {
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	defer r.Body.Close()
	var m M
	dec := json.NewDecoder(r.Body)
	if e := dec.Decode(&m); e != nil || m == nil {
		return nil, errors.New("请求体必须是 JSON 对象（最多 8MB）")
	}
	if e := dec.Decode(new(any)); e != io.EOF {
		return nil, errors.New("请求体只能包含一个 JSON 对象")
	}
	if trace := diagnosticFor(r); trace != nil {
		b := mustMarshal(m)
		trace.record("incoming_body", M{"body": m, "json_bytes": len(b), "json_sha256": digest(string(b))})
	}
	return m, nil
}
func isChat(path string) bool {
	switch path {
	case "/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/count_tokens":
		return true
	}
	return false
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if a.diagnostics != nil && isChat(path) && r.Method == "POST" {
		var finish func()
		w, r, finish = a.diagnostics.begin(w, r)
		defer finish()
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	defer func() {
		if e := recover(); e != nil {
			log.Printf("request handler panic (%s)", r.URL.Path)
			apiError(w, 500, "内部错误，请检查服务日志")
		}
	}()
	if isChat(path) || path == "/v1/models" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id, X-Codar-Account")
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Api-Key, Anthropic-Version, Anthropic-Beta, Anthropic-Dangerous-Direct-Browser-Access, OpenAI-Beta, X-Session-Id, X-Conversation-Id")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if r.Method == "GET" && (path == "/ping" || path == "/health") {
		jsonResponse(w, 200, M{"ok": true, "platform": "codar", "version": Version})
		return
	}
	if r.Method == "GET" && (path == "/" || path == "/dashboard" || path == "/ui" || path == "/login") {
		name := "web/dashboard.html"
		if path == "/login" || !a.auth.Admin(r) {
			name = "web/login.html"
		}
		b, e := assets.ReadFile(name)
		if e != nil {
			apiError(w, 500, "页面缺失")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		_, _ = w.Write(b)
		return
	}
	if path == "/favicon.ico" {
		w.WriteHeader(204)
		return
	}
	if path == "/auth/login" && r.Method == "POST" {
		if !sameOrigin(r) {
			apiError(w, 403, "仅允许同源操作")
			return
		}
		p, e := payload(w, r)
		if e != nil {
			apiError(w, 400, e.Error())
			return
		}
		password := text(p["password"])
		if len(password) > 1024 {
			apiError(w, 400, "密码过长")
			return
		}
		if e = a.auth.Login(w, r, password); e != nil {
			apiError(w, 401, e.Error())
			return
		}
		jsonResponse(w, 200, M{"ok": true})
		return
	}
	if (isChat(path) && r.Method == "POST") || (path == "/v1/models" && r.Method == "GET") {
		// Every public model endpoint requires an API key, even with an admin cookie.
		key, e := a.auth.API(r, "")
		if e != nil {
			apiError(w, 401, e.Error())
			return
		}
		if path == "/v1/models" {
			models, e := a.catalog(r.Context(), false)
			if e != nil && len(models) == 0 {
				apiError(w, 502, e.Error())
				return
			}
			out := []any{}
			for _, v := range models {
				m := obj(v)
				allowed := len(arr(key["models"])) == 0
				for _, id := range arr(key["models"]) {
					if id == m["id"] {
						allowed = true
					}
				}
				if allowed {
					out = append(out, M{"id": m["id"], "object": "model", "owned_by": "codar"})
				}
			}
			jsonResponse(w, 200, M{"object": "list", "data": out})
			return
		}
		p, e := payload(w, r)
		if e != nil {
			apiError(w, 400, e.Error())
			return
		}
		if _, e = a.auth.API(r, text(p["model"])); e != nil {
			apiError(w, 403, e.Error())
			return
		}
		select {
		case a.gates <- struct{}{}:
			defer func() { <-a.gates }()
		default:
			apiError(w, 429, "网关并发已达上限，请稍后重试")
			return
		}
		a.relay(w, r, p, key, nil)
		return
	}
	if !a.auth.Admin(r) {
		apiError(w, 401, "请先登录管理面板")
		return
	}
	if r.Method == "GET" && strings.HasPrefix(path, "/assets/") {
		name := strings.TrimPrefix(path, "/assets/")
		ctype := map[string]string{".js": "application/javascript; charset=utf-8", ".css": "text/css; charset=utf-8"}[filepath.Ext(name)]
		b, e := assets.ReadFile("web/" + name)
		if ctype == "" || strings.Contains(name, "/") || e != nil {
			apiError(w, 404, "资源不存在")
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
		return
	}
	if r.Method != "GET" && !sameOrigin(r) {
		apiError(w, 403, "仅允许同源操作")
		return
	}
	if path == "/auth/logout" && r.Method == "POST" {
		a.auth.Logout(w, r)
		jsonResponse(w, 200, M{"ok": true})
		return
	}
	if path == "/auth/session" && r.Method == "GET" {
		jsonResponse(w, 200, M{"ok": true, "version": Version})
		return
	}
	var p M
	var e error
	if r.Method == "POST" {
		p, e = payload(w, r)
		if e != nil {
			apiError(w, 400, e.Error())
			return
		}
	} else if r.Method != "GET" {
		apiError(w, 405, "不支持的请求方法")
		return
	}
	result, status, e := a.panel(r, path, p)
	if e != nil {
		apiError(w, status, e.Error())
		return
	}
	jsonResponse(w, 200, result)
}
func (a *App) maintenance() {
	defer a.wg.Done()
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	last := time.Time{}
	var background sync.WaitGroup
	done := make(chan struct{}, 1)
	defer background.Wait()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-tick.C:
			if e := a.usage.Flush(); e != nil {
				log.Printf("usage flush failed: %v", e)
			}
			if e := a.store.Flush(); e != nil {
				log.Printf("accounts flush failed: %v", e)
			}
			if e := a.auth.Flush(); e != nil {
				log.Printf("keys flush failed: %v", e)
			}
			if !a.cfg.NoBackground && time.Since(last) >= 30*time.Second {
				select {
				case done <- struct{}{}:
					last = time.Now()
					background.Add(1)
					go func() { defer background.Done(); defer func() { <-done }(); a.refreshBackground() }()
				default:
				}
			}
		}
	}
}
func (a *App) refreshBackground() {
	settings := a.store.Settings()
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, account := range a.store.List() {
		if !yes(account["enabled"]) {
			continue
		}
		select {
		case <-a.ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(uid string) {
			defer wg.Done()
			defer func() { <-sem }()
			unlock := a.lockAccount(uid)
			defer unlock()
			current := a.store.Get(uid)
			if current == nil {
				return
			}
			ctx, cancel := context.WithTimeout(a.ctx, 90*time.Second)
			defer cancel()
			a.autoActions(ctx, current)
			if now()-num(current["refreshed_at"]) >= num(settings["refresh_minutes"])*60 {
				a.refresh(ctx, current, []string{"profile", "balance", "free", "mine", "invite", "sign"})
			} else if now()-num(current["free_checked_at"]) >= num(settings["free_check_minutes"])*60 {
				a.refresh(ctx, current, []string{"free", "mine"})
			}
			a.saveBusiness(current)
		}(text(account["uid"]))
	}
	wg.Wait()
}
func statusOf(err error) int {
	if err == nil {
		return 200
	}
	var ue *UpstreamError
	if errors.As(err, &ue) {
		if ue.Status >= 400 && ue.Status <= 599 {
			return ue.Status
		}
		return 502
	}
	return 400
}
func (a *App) logPersistence(err error) {
	if err != nil {
		log.Printf("persistent state update failed: %v", err)
	}
}
func (a *App) saveBusiness(v M) error {
	fields := []string{"token", "username", "display_name", "profile", "balance", "free", "activities", "invite", "sign", "redeems", "renew", "refreshed_at", "free_checked_at", "token_invalid", "web_id"}
	return a.store.Update(text(v["uid"]), func(current M) error {
		for _, k := range fields {
			if val, ok := v[k]; ok {
				current[k] = val
			}
		}
		if !yes(current["restricted"]) && num(v["last_error_at"]) >= num(current["last_error_at"]) {
			current["last_error"] = v["last_error"]
			current["last_error_at"] = v["last_error_at"]
		}
		return nil
	})
}
func (a *App) account(uid string) (M, error) {
	v := a.store.Get(uid)
	if v == nil {
		return nil, fmt.Errorf("未找到账号 %s", uid)
	}
	return v, nil
}
