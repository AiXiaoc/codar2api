package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Route struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
}

func parseProxyID(v any) (int, error) {
	if v == nil {
		return 0, nil
	}
	var n int
	switch x := v.(type) {
	case int:
		n = x
	case float64:
		n = int(x)
		if float64(n) != x {
			return 0, errors.New("代理 ID 必须是非负整数")
		}
	case string:
		var e error
		n, e = strconv.Atoi(x)
		if e != nil {
			return 0, errors.New("代理 ID 格式不正确")
		}
	default:
		return 0, errors.New("代理 ID 格式不正确")
	}
	if n < 0 || n > 1000000000 {
		return 0, errors.New("代理 ID 超出范围")
	}
	return n, nil
}

type ProxyPool struct {
	mu           sync.Mutex
	dir          string
	data         M
	clients      map[string]*http.Client
	checkURL     string
	rotationNext int
	rotationLast string
}

func NewProxyPool(dir, checkURL string) (*ProxyPool, error) {
	p := &ProxyPool{dir: dir, data: M{"next_id": 1, "proxies": []any{}, "accounts": M{}}, clients: map[string]*http.Client{}, checkURL: checkURL}
	if e := loadOptional(filepath.Join(dir, "proxies.json"), &p.data); e != nil {
		return nil, e
	}
	p.data["proxies"] = arr(p.data["proxies"])
	p.data["accounts"] = obj(p.data["accounts"])
	p.data["direct"] = obj(p.data["direct"])
	n := 1
	for _, v := range arr(p.data["proxies"]) {
		if id := int(num(obj(v)["id"])); id >= n {
			n = id + 1
		}
	}
	if num(p.data["next_id"]) < float64(n) {
		p.data["next_id"] = n
	}
	return p, nil
}
func validateProxy(value string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(value))
	if e != nil || u.Hostname() == "" {
		return "", errors.New("代理地址格式错误")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", errors.New("支持 http、https、socks5、socks5h 代理")
	}
	if u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("代理地址不能包含路径或查询参数")
	}
	if u.Port() != "" {
		port, e := strconv.Atoi(u.Port())
		if e != nil || port < 1 || port > 65535 {
			return "", errors.New("代理端口格式错误")
		}
	} else if strings.HasPrefix(u.Scheme, "socks") {
		return "", errors.New("SOCKS5 代理需要指定端口")
	}
	return u.String(), nil
}
func (p *ProxyPool) pinLocked(id int) (Route, string, error) {
	if id == 0 {
		return Route{Name: "直连"}, "", nil
	}
	for _, v := range arr(p.data["proxies"]) {
		m := obj(v)
		if int(num(m["id"])) == id {
			if !yes(m["enabled"]) {
				return Route{}, "", errors.New("指定代理已停用")
			}
			value := text(m["url"])
			return Route{ID: id, Name: text(m["name"]), Fingerprint: digest(value)}, value, nil
		}
	}
	return Route{}, "", errors.New("指定代理已删除或不存在")
}
func (p *ProxyPool) Pin(id int) (Route, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, _, e := p.pinLocked(id)
	return r, e
}
func (p *ProxyPool) accountLocked(uid string, skipBlocked, advance bool) (Route, error) {
	policy := obj(obj(p.data["accounts"])[uid])
	if len(policy) == 0 || policy["mode"] == "direct" {
		return Route{Name: "直连"}, nil
	}
	if policy["mode"] == "round_robin" {
		return p.rotationRouteLocked(skipBlocked, advance)
	}
	ids := arr(policy["ids"])
	if policy["mode"] != "pick" || len(ids) != 1 || num(ids[0]) <= 0 {
		return Route{}, errors.New("账号代理绑定无效，请重新选择")
	}
	r, _, e := p.pinLocked(int(num(ids[0])))
	return r, e
}

func (p *ProxyPool) Account(uid string, skipBlocked ...bool) (Route, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accountLocked(uid, len(skipBlocked) == 0 || skipBlocked[0], true)
}

func (p *ProxyPool) Rotating(uid string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return obj(obj(p.data["accounts"])[uid])["mode"] == "round_robin"
}

func (p *ProxyPool) rotationCandidatesLocked(skipBlocked bool) []int {
	indices := []int{}
	seen := map[string]bool{}
	for i, v := range arr(p.data["proxies"]) {
		m := obj(v)
		if !yes(m["enabled"]) || skipBlocked && yes(m["blocked"]) {
			continue
		}
		fingerprint := digest(text(m["url"]))
		if !seen[fingerprint] {
			indices = append(indices, i)
			seen[fingerprint] = true
		}
	}
	return indices
}

func (p *ProxyPool) rotationRouteLocked(skipBlocked, advance bool) (Route, error) {
	indices := p.rotationCandidatesLocked(skipBlocked)
	if len(indices) < 2 {
		return Route{}, fmt.Errorf("轮询代理至少需要两条已启用的不同代理，当前可用 %d 条，请检查代理池", len(indices))
	}
	rows := arr(p.data["proxies"])
	start := p.rotationNext % len(rows)
	chosen, distance := -1, len(rows)
	for _, idx := range indices {
		m := obj(rows[idx])
		if digest(text(m["url"])) == p.rotationLast {
			continue
		}
		if d := (idx - start + len(rows)) % len(rows); d < distance {
			chosen, distance = idx, d
		}
	}
	if chosen < 0 {
		return Route{}, errors.New("轮询代理没有可切换的出口，请检查代理池")
	}
	route, _, err := p.pinLocked(int(num(obj(rows[chosen])["id"])))
	if err == nil && advance {
		p.rotationNext = (chosen + 1) % len(rows)
		p.rotationLast = route.Fingerprint
	}
	return route, err
}

func (p *ProxyPool) Policy(uid string, skipBlocked ...bool) M {
	p.mu.Lock()
	defer p.mu.Unlock()
	policy := clone(obj(obj(p.data["accounts"])[uid]))
	if len(policy) == 0 {
		policy = M{"mode": "direct", "ids": []any{}}
	}
	skip := len(skipBlocked) == 0 || skipBlocked[0]
	r, e := p.accountLocked(uid, skip, false)
	policy["available"] = e == nil
	if policy["mode"] == "round_robin" {
		policy["name"] = "轮询代理"
		policy["available_count"] = len(p.rotationCandidatesLocked(skip))
		if e != nil {
			policy["error"] = e.Error()
		}
		if len(p.rotationCandidatesLocked(false)) > 0 && len(p.rotationCandidatesLocked(true)) == 0 {
			policy["blocked"], policy["blocked_reason"] = true, "轮询代理的全部出口均被封控，请在代理池检查"
		}
		return policy
	}
	if e != nil {
		policy["error"] = e.Error()
		policy["name"] = "指定代理不可用"
	} else {
		policy["name"] = r.Name
		if b := p.blockLocked(r.ID); b != nil {
			policy["blocked"], policy["blocked_reason"], policy["blocked_at"] = true, b["blocked_reason"], b["blocked_at"]
		}
	}
	return policy
}

// An exit is one proxy, or the gateway's own IP for direct routes (ID 0). It is
// blocked when the upstream edge refused that IP; see ipBlocked.
func (p *ProxyPool) blockLocked(id int) M {
	if id == 0 {
		if d := obj(p.data["direct"]); yes(d["blocked"]) {
			return d
		}
		return nil
	}
	for _, v := range arr(p.data["proxies"]) {
		if m := obj(v); int(num(m["id"])) == id && yes(m["blocked"]) {
			return m
		}
	}
	return nil
}

// ExitBlocked reports whether the account's current exit is blocked.
func (p *ProxyPool) ExitBlocked(uid string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if obj(obj(p.data["accounts"])[uid])["mode"] == "round_robin" {
		return len(p.rotationCandidatesLocked(false)) > 0 && len(p.rotationCandidatesLocked(true)) == 0
	}
	r, e := p.accountLocked(uid, true, false)
	if e != nil {
		return false
	}
	return p.blockLocked(r.ID) != nil
}
func (p *ProxyPool) AnyBlocked() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.blockLocked(0) != nil {
		return true
	}
	for _, v := range arr(p.data["proxies"]) {
		if yes(obj(v)["blocked"]) {
			return true
		}
	}
	return false
}

// Block marks the exit a request used. A proxy whose address changed since the
// request started is left alone, because the blocked IP is no longer in use.
func (p *ProxyPool) Block(r Route, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := clone(p.data)
	target := obj(next["direct"])
	if r.ID > 0 {
		target = nil
		for _, v := range arr(next["proxies"]) {
			if m := obj(v); int(num(m["id"])) == r.ID && digest(text(m["url"])) == r.Fingerprint {
				target = m
			}
		}
		if target == nil {
			return nil
		}
	}
	if !yes(target["blocked"]) {
		target["blocked_at"] = now()
	}
	target["blocked"], target["blocked_reason"] = true, reason
	if r.ID == 0 {
		next["direct"] = target
	}
	if e := writeJSON(filepath.Join(p.dir, "proxies.json"), next); e != nil {
		return e
	}
	p.data = next
	return nil
}

func (p *ProxyPool) BindRotation(uids []string, skipBlocked bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.rotationCandidatesLocked(skipBlocked)) < 2 {
		return errors.New("轮询代理至少需要两条已启用的不同代理，请先在代理池添加或启用")
	}
	next := clone(p.data)
	accounts := obj(next["accounts"])
	for _, uid := range uids {
		accounts[uid] = M{"mode": "round_robin", "ids": []any{}}
	}
	next["accounts"] = accounts
	if err := writeJSON(filepath.Join(p.dir, "proxies.json"), next); err != nil {
		return err
	}
	p.data = next
	return nil
}
func unblock(m M) { delete(m, "blocked"); delete(m, "blocked_at"); delete(m, "blocked_reason") }
func (p *ProxyPool) View(admin bool) M {
	p.mu.Lock()
	defer p.mu.Unlock()
	rows := []any{}
	for _, v := range arr(p.data["proxies"]) {
		m := clone(obj(v))
		delete(m, "url")
		if u, e := url.Parse(text(obj(v)["url"])); e == nil {
			m["address"] = u.Scheme + "://" + u.Host
			m["authenticated"] = u.User != nil
		}
		rows = append(rows, m)
	}
	return M{"ok": true, "supported": true, "proxies": rows, "direct": clone(obj(p.data["direct"])), "error": ""}
}
func (p *ProxyPool) Bind(uids []string, r Route) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	current, _, e := p.pinLocked(r.ID)
	if e != nil {
		return e
	}
	if current.Fingerprint != r.Fingerprint {
		return errors.New("登录期间代理地址发生变更，请重新登录")
	}
	next := clone(p.data)
	accounts := obj(next["accounts"])
	for _, uid := range uids {
		ids := []any{}
		mode := "direct"
		if r.ID > 0 {
			ids = append(ids, r.ID)
			mode = "pick"
		}
		accounts[uid] = M{"mode": mode, "ids": ids}
	}
	next["accounts"] = accounts
	if e = writeJSON(filepath.Join(p.dir, "proxies.json"), next); e != nil {
		return e
	}
	p.data = next
	return nil
}
func (p *ProxyPool) Action(body M) (M, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := clone(p.data)
	rows := arr(next["proxies"])
	action := text(body["action"])
	out := M{"ok": true}
	if action == "create" || action == "update" {
		name := strings.TrimSpace(text(body["name"]))
		if name == "" || len(name) > 100 {
			return nil, errors.New("请输入代理名称（最多 100 字符）")
		}
		value := text(body["url"])
		var err error
		if value != "" {
			value, err = validateProxy(value)
			if err != nil {
				return nil, err
			}
		}
		if action == "create" {
			if value == "" {
				return nil, errors.New("请输入代理地址")
			}
			id := int(num(next["next_id"]))
			if id < 1 {
				id = 1
			}
			next["next_id"] = id + 1
			rows = append(rows, M{"id": id, "name": name, "url": value, "enabled": true, "created_at": now()})
		} else {
			found := false
			for _, v := range rows {
				m := obj(v)
				if num(m["id"]) == num(body["id"]) {
					found = true
					m["name"] = name
					if value != "" && value != text(m["url"]) {
						// A new address is a new exit IP, so an earlier block no longer applies.
						m["url"] = value
						unblock(m)
					}
					break
				}
			}
			if !found {
				return nil, errors.New("未找到代理")
			}
		}
	} else if action == "toggle" || action == "delete" {
		found := false
		for i, v := range rows {
			m := obj(v)
			if num(m["id"]) != num(body["id"]) {
				continue
			}
			found = true
			if action == "delete" {
				rows = append(rows[:i], rows[i+1:]...)
			} else {
				v, ok := body["enabled"].(bool)
				if !ok {
					return nil, errors.New("enabled 必须是布尔值")
				}
				m["enabled"] = v
			}
			break
		}
		if !found {
			return nil, errors.New("未找到代理")
		}
	} else if action == "import" {
		results, e := importProxies(next, &rows, text(body["text"]), text(body["scheme"]))
		if e != nil {
			return nil, e
		}
		out = results
	} else if action == "unblock" {
		id, e := parseProxyID(body["id"])
		if e != nil {
			return nil, e
		}
		found := id == 0
		if found {
			d := obj(next["direct"])
			unblock(d)
			next["direct"] = d
		}
		for _, v := range rows {
			if m := obj(v); int(num(m["id"])) == id {
				found = true
				unblock(m)
			}
		}
		if !found {
			return nil, errors.New("未找到代理")
		}
	} else {
		return nil, errors.New("未知的代理操作")
	}
	next["proxies"] = rows
	if e := writeJSON(filepath.Join(p.dir, "proxies.json"), next); e != nil {
		return nil, e
	}
	p.data = next
	// In-use connections finish normally. New requests obtain a fresh isolated pool.
	for k, c := range p.clients {
		c.CloseIdleConnections()
		delete(p.clients, k)
	}
	return out, nil
}

// proxyLine reads one line of a batch import: an optional name, then the proxy
// as a URL, host:port or host:port:user:pass. The last two use scheme.
func proxyLine(line, scheme string) (string, string, error) {
	fields := strings.Fields(line)
	raw, name := fields[len(fields)-1], strings.Join(fields[:len(fields)-1], " ")
	if !strings.Contains(raw, "://") {
		parts := strings.Split(raw, ":")
		switch len(parts) {
		case 2:
			raw = scheme + "://" + raw
		case 4:
			raw = scheme + "://" + url.UserPassword(parts[2], parts[3]).String() + "@" + parts[0] + ":" + parts[1]
		default:
			return "", "", errors.New("格式应为 协议://地址、host:port 或 host:port:用户名:密码")
		}
	}
	value, e := validateProxy(raw)
	if e != nil {
		return "", "", e
	}
	if name == "" {
		u, _ := url.Parse(value)
		name = u.Host
	}
	if len(name) > 100 {
		return "", "", errors.New("代理名称最多 100 字符")
	}
	return name, value, nil
}

// importProxies adds every valid line and reports each line by number. Results
// never echo the input, which may contain proxy passwords.
func importProxies(next M, rows *[]any, input, scheme string) (M, error) {
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" && scheme != "socks5" && scheme != "socks5h" {
		return nil, errors.New("默认协议只能是 http、https、socks5 或 socks5h")
	}
	lines := strings.Split(input, "\n")
	if len(lines) > 500 {
		return nil, errors.New("一次最多添加 500 个代理")
	}
	seen := map[string]bool{}
	for _, v := range *rows {
		seen[text(obj(v)["url"])] = true
	}
	results, added := []any{}, 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		r := M{"line": i + 1, "ok": false}
		name, value, e := proxyLine(line, scheme)
		if e == nil && seen[value] {
			e = errors.New("代理池里已经有这个地址")
		}
		if e != nil {
			r["error"] = e.Error()
		} else {
			id := max(1, int(num(next["next_id"])))
			next["next_id"] = id + 1
			*rows = append(*rows, M{"id": id, "name": name, "url": value, "enabled": true, "created_at": now()})
			seen[value] = true
			added++
			u, _ := url.Parse(value)
			r["ok"], r["id"], r["name"], r["address"] = true, id, name, u.Scheme+"://"+u.Host
		}
		results = append(results, r)
	}
	if len(results) == 0 {
		return nil, errors.New("请至少填写一个代理")
	}
	return M{"ok": true, "added": added, "failed": len(results) - added, "results": results}, nil
}
func (p *ProxyPool) Client(route Route) (*http.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current, value, e := p.pinLocked(route.ID)
	if e != nil {
		return nil, e
	}
	if current.Fingerprint != route.Fingerprint {
		return nil, errors.New("登录期间代理地址发生变更，请重新开始")
	}
	key := fmt.Sprintf("%d:%s", route.ID, current.Fingerprint)
	if c := p.clients[key]; c != nil {
		return c, nil
	}
	tr := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 128, MaxIdleConnsPerHost: 32, MaxConnsPerHost: 128, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 90 * time.Second, ExpectContinueTimeout: time.Second}
	if value != "" {
		u, e := url.Parse(value)
		if e != nil {
			return nil, errors.New("代理配置无效")
		}
		tr.Proxy = http.ProxyURL(u)
	}
	c := &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	p.clients[key] = c
	return c, nil
}
func (p *ProxyPool) Do(req *http.Request, r Route) (*http.Response, error) {
	c, e := p.Client(r)
	if e != nil {
		return nil, e
	}
	resp, e := c.Do(req)
	if e != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		if r.ID > 0 {
			return nil, errors.New("指定代理连接上游失败，请检查代理；当前请求未切换为直连")
		}
		return nil, errors.New("连接上游失败或超时")
	}
	return resp, nil
}
func (p *ProxyPool) Check(ctx context.Context, id int) (M, error) {
	r, e := p.Pin(id)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", p.checkURL, nil)
	if e != nil {
		return nil, e
	}
	t := time.Now()
	resp, err := p.Do(req, r)
	result := M{"at": now(), "ok": false, "latency_ms": time.Since(t).Milliseconds(), "error": ""}
	if err != nil {
		result["error"] = err.Error()
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
		resp.Body.Close()
		result["ok"] = resp.StatusCode >= 200 && resp.StatusCode < 400
		result["status"] = resp.StatusCode
		if !yes(result["ok"]) {
			result["error"] = fmt.Sprintf("检测目标返回 HTTP %d", resp.StatusCode)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	next := clone(p.data)
	for _, v := range arr(next["proxies"]) {
		if num(obj(v)["id"]) == float64(id) {
			obj(v)["check"] = result
		}
	}
	if e = writeJSON(filepath.Join(p.dir, "proxies.json"), next); e != nil {
		return nil, e
	}
	p.data = next
	return result, nil
}
func (p *ProxyPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.clients {
		c.CloseIdleConnections()
	}
}
