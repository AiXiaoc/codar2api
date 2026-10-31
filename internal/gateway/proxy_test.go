package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPProxyBindingAndFailClosed(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "a", nil)
	var hits atomic.Int32
	var proxyAuth string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		proxyAuth = r.Header.Get("Proxy-Authorization")
		if r.Header.Get("Authorization") != "Bearer "+fakeToken("a") {
			t.Error("upstream authorization missing")
		}
		jsonResponse(w, 200, M{"usage": M{"prompt_tokens": 10, "completion_tokens": 2}, "choices": []any{}})
	}))
	defer proxy.Close()
	u, _ := url.Parse(proxy.URL)
	u.User = url.UserPassword("proxyuser", "secretpass")
	h.post(t, "/proxy-pool/manage", M{"action": "create", "name": "test proxy", "url": u.String()})
	h.post(t, "/accounts/proxy", M{"uid": "a", "proxy_id": 1})
	resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
	if resp.StatusCode != 200 || hits.Load() != 1 || proxyAuth == "" {
		t.Fatal("proxy not used")
	}
	_, body := h.request(t, "GET", "/proxy-pool", nil, false)
	if strings.Contains(string(body), "secretpass") {
		t.Fatal("proxy password exposed")
	}
	h.post(t, "/proxy-pool/manage", M{"action": "toggle", "id": 1, "enabled": false})
	before := len(h.calls)
	resp, _ = h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
	if resp.StatusCode != 503 || hits.Load() != 1 || len(h.calls) != before {
		t.Fatal("unavailable proxy fell back to direct")
	}
	h.post(t, "/proxy-pool/manage", M{"action": "delete", "id": 1})
	if _, e := h.app.proxies.Account("a"); e == nil {
		t.Fatal("deleted proxy became direct")
	}
}
func TestIPBlockStopsSchedulingThatExit(t *testing.T) {
	const page = "<html>\r\n<head><title>403 Forbidden</title></head>\r\n<body>\r\n<center><h1>403 Forbidden</h1></center>\r\n<hr><center>nginx/1.24.0 (Ubuntu)</center>\r\n</body>\r\n</html>\r\n"
	var direct atomic.Int32
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		direct.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(403)
		io.WriteString(w, page)
	})
	// The "proxy" answers model requests itself, standing in for a different exit IP.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, M{"choices": []any{}, "usage": M{"prompt_tokens": 5, "completion_tokens": 1}})
	}))
	defer proxy.Close()
	h.add(t, "a", nil)
	h.add(t, "b", nil)
	h.post(t, "/proxy-pool/manage", M{"action": "create", "name": "other exit", "url": proxy.URL})
	h.post(t, "/accounts/proxy", M{"uid": "b", "proxy_id": 1})
	ask := func(text string) int {
		resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto", "messages": []any{M{"role": "user", "content": text}}}, true)
		return resp.StatusCode
	}
	if code := ask("one"); code != 200 || direct.Load() != 1 {
		t.Fatalf("blocked direct exit not retried elsewhere: HTTP %d, %d direct calls", code, direct.Load())
	}
	view := h.app.proxies.View(true)
	if d := obj(view["direct"]); !yes(d["blocked"]) || !strings.Contains(text(d["blocked_reason"]), "nginx/1.24.0 (Ubuntu)") {
		t.Fatalf("direct exit not marked blocked: %v", d)
	}
	if rec := h.app.usage.Requests("a", "a", true, 1, 20); num(rec["failed"]) != 1 || !strings.Contains(text(obj(arr(rec["items"])[0])["error"]), "403 Forbidden") {
		t.Fatalf("blocked attempt missing from account records: %v", rec)
	}
	if code := ask("two"); code != 200 || direct.Load() != 1 {
		t.Fatal("blocked exit still scheduled")
	}
	if pub := h.app.public(h.app.store.Get("a")); yes(pub["ready"]) || !yes(obj(pub["proxy"])["blocked"]) {
		t.Fatalf("account view hides blocked exit: %v", pub)
	}
	if reloaded, e := NewProxyPool(h.app.cfg.DataDir, ""); e != nil || !reloaded.ExitBlocked("a") {
		t.Fatal("block not persisted")
	}
	h.post(t, "/proxy-pool/manage", M{"action": "unblock", "id": 0})
	if h.app.proxies.ExitBlocked("a") {
		t.Fatal("unblock failed")
	}
	h.post(t, "/accounts/update", M{"uid": "b", "enabled": false})
	h.post(t, "/settings", M{"ip_block": false})
	if code := ask("three"); code != 403 || h.app.proxies.ExitBlocked("a") {
		t.Fatalf("disabled option still blocks: HTTP %d", code)
	}
}
func TestBatchProxyImport(t *testing.T) {
	h := newHarness(t, nil)
	h.post(t, "/proxy-pool/manage", M{"action": "create", "name": "old", "url": "http://10.0.0.9:8080"})
	input := strings.Join([]string{"东京 01 socks5h://u1:p1@10.0.0.1:1080", "", "10.0.0.2:3128", "10.0.0.3:1080:u3:secret3", "not a proxy", "http://10.0.0.9:8080", "10.0.0.2:3128", "ftp://10.0.0.4:21"}, "\n")
	resp, b := h.request(t, "POST", "/proxy-pool/manage", M{"action": "import", "text": input, "scheme": "socks5"}, false)
	if resp.StatusCode != 200 {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, b)
	}
	d := decode(t, b)
	if num(d["added"]) != 3 || num(d["failed"]) != 4 || bytes.Contains(b, []byte("secret3")) || bytes.Contains(b, []byte("p1@")) {
		t.Fatalf("unexpected import result: %s", b)
	}
	lines := []float64{}
	for _, v := range arr(d["results"]) {
		lines = append(lines, num(obj(v)["line"]))
	}
	if fmt.Sprint(lines) != "[1 3 4 5 6 7 8]" {
		t.Fatalf("line numbers wrong: %v", lines)
	}
	want := map[string]string{"东京 01": "socks5h://u1:p1@10.0.0.1:1080", "10.0.0.2:3128": "socks5://10.0.0.2:3128", "10.0.0.3:1080": "socks5://u3:secret3@10.0.0.3:1080"}
	h.app.proxies.mu.Lock()
	for _, v := range arr(h.app.proxies.data["proxies"]) {
		m := obj(v)
		if u, ok := want[text(m["name"])]; ok && u == text(m["url"]) {
			delete(want, text(m["name"]))
		}
	}
	h.app.proxies.mu.Unlock()
	if len(want) != 0 {
		t.Fatalf("proxies not stored as expected: %v", want)
	}
	if resp, _ := h.request(t, "POST", "/proxy-pool/manage", M{"action": "import", "text": " \n "}, false); resp.StatusCode != 400 {
		t.Fatal("empty import accepted")
	}
}
func TestHTTPSProxyTLSAndConnect(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "secure origin") }))
	defer upstream.Close()
	var connects atomic.Int32
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" {
			t.Error("expected CONNECT")
			http.Error(w, "CONNECT required", 400)
			return
		}
		connects.Add(1)
		target, e := net.DialTimeout("tcp", r.Host, time.Second)
		if e != nil {
			http.Error(w, "dial failed", 502)
			return
		}
		client, buf, e := w.(http.Hijacker).Hijack()
		if e != nil {
			target.Close()
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buf.Flush()
		go func() { defer target.Close(); _, _ = io.Copy(target, client) }()
		defer client.Close()
		_, _ = io.Copy(client, target)
	}))
	defer proxy.Close()
	pool, e := NewProxyPool(t.TempDir(), upstream.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	_, e = pool.Action(M{"action": "create", "name": "TLS proxy", "url": proxy.URL})
	if e != nil {
		t.Fatal(e)
	}
	route, _ := pool.Pin(1)
	client, e := pool.Client(route)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(proxy.Certificate())
	roots.AddCert(upstream.Certificate())
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	req, _ := http.NewRequest("GET", upstream.URL, nil)
	resp, e := pool.Do(req, route)
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || string(body) != "secure origin" || connects.Load() != 1 {
		t.Fatal("HTTPS proxy tunnel failed")
	}
}
func TestSOCKS5AuthenticatedRemoteDNS(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "socks ok") }))
	defer origin.Close()
	targetURL, _ := url.Parse(origin.URL)
	targetPort, _ := strconv.Atoi(targetURL.Port())
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	errChan := make(chan error, 1)
	go func() {
		defer wg.Done()
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(conn)
		head := make([]byte, 2)
		if _, e = io.ReadFull(reader, head); e != nil {
			errChan <- e
			return
		}
		methods := make([]byte, int(head[1]))
		_, _ = io.ReadFull(reader, methods)
		_, _ = conn.Write([]byte{5, 2})
		_, _ = io.ReadFull(reader, head)
		user := make([]byte, int(head[1]))
		_, _ = io.ReadFull(reader, user)
		plen, _ := reader.ReadByte()
		pass := make([]byte, int(plen))
		_, _ = io.ReadFull(reader, pass)
		if string(user) != "alice" || string(pass) != "password" {
			t.Error("SOCKS authentication failed")
		}
		_, _ = conn.Write([]byte{1, 0})
		request := make([]byte, 4)
		_, _ = io.ReadFull(reader, request)
		if request[3] != 3 {
			t.Error("expected remote domain resolution")
		}
		n, _ := reader.ReadByte()
		domain := make([]byte, int(n))
		_, _ = io.ReadFull(reader, domain)
		portBytes := make([]byte, 2)
		_, _ = io.ReadFull(reader, portBytes)
		if string(domain) != "not-resolvable.invalid" || int(binary.BigEndian.Uint16(portBytes)) != targetPort {
			t.Error("wrong SOCKS target")
		}
		target, e := net.Dial("tcp", targetURL.Host)
		if e != nil {
			errChan <- e
			return
		}
		defer target.Close()
		_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		go func() { defer target.Close(); _, _ = io.Copy(target, reader) }()
		_, _ = io.Copy(conn, target)
	}()
	pool, _ := NewProxyPool(t.TempDir(), origin.URL)
	_, e = pool.Action(M{"action": "create", "name": "SOCKS", "url": "socks5h://alice:password@" + listener.Addr().String()})
	if e != nil {
		t.Fatal(e)
	}
	route, _ := pool.Pin(1)
	req, _ := http.NewRequest("GET", "http://not-resolvable.invalid:"+strconv.Itoa(targetPort), nil)
	resp, e := pool.Do(req, route)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	pool.Close()
	if e != nil || string(b) != "socks ok" {
		t.Fatal("SOCKS forwarding failed")
	}
	wg.Wait()
	select {
	case e := <-errChan:
		t.Fatal(e)
	default:
	}
}
func TestPinnedProxyMutationAndConnectionReuse(t *testing.T) {
	var connections atomic.Int32
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	origin.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	origin.Start()
	defer origin.Close()
	pool, _ := NewProxyPool(t.TempDir(), origin.URL)
	defer pool.Close()
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest("GET", origin.URL, nil)
		resp, e := pool.Do(req, Route{Name: "直连"})
		if e != nil {
			t.Fatal(e)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if connections.Load() != 1 {
		t.Fatal("HTTP connection pool not reused")
	}
	_, _ = pool.Action(M{"action": "create", "name": "old", "url": "http://127.0.0.1:10001"})
	route, _ := pool.Pin(1)
	_, _ = pool.Action(M{"action": "update", "id": 1, "name": "changed", "url": "http://127.0.0.1:10002"})
	req, _ := http.NewRequestWithContext(context.Background(), "GET", origin.URL, nil)
	if _, e := pool.Do(req, route); e == nil {
		t.Fatal("pinned login proxy silently changed")
	}
}
