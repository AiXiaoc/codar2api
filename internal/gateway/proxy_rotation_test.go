package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

func rotationPool(t *testing.T, count int) *ProxyPool {
	t.Helper()
	p, err := NewProxyPool(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	for i := 1; i <= count; i++ {
		if _, err := p.Action(M{"action": "create", "name": fmt.Sprint("proxy-", i), "url": fmt.Sprintf("http://127.0.0.1:%d", 10000+i)}); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestProxyRotationSequenceReadOnlyViewsAndPersistence(t *testing.T) {
	p := rotationPool(t, 3)
	if err := p.BindRotation([]string{"a", "b"}, true); err != nil {
		t.Fatal(err)
	}
	request := 0
	for i := 0; i < 12; i++ {
		for _, uid := range []string{"a", "b"} {
			for n := 0; n < 3; n++ {
				policy := p.Policy(uid)
				if policy["mode"] != "round_robin" || policy["name"] != "轮询代理" || !yes(policy["available"]) || num(policy["available_count"]) != 3 || p.ExitBlocked(uid) {
					t.Fatalf("invalid policy %v", policy)
				}
			}
			route, err := p.Account(uid)
			if err != nil || route.ID != request%3+1 {
				t.Fatalf("%s request %d route=%v err=%v", uid, request, route, err)
			}
			request++
		}
	}
	reloaded, err := NewProxyPool(p.dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if !reloaded.Rotating("a") || !reloaded.Rotating("b") {
		t.Fatal("rotation binding not persisted")
	}
	r, err := reloaded.Account("a")
	if err != nil || r.ID <= 0 {
		t.Fatal("reloaded rotation became direct")
	}
	if fixed, _ := p.Pin(2); p.Bind([]string{"a"}, fixed) != nil {
		t.Fatal("could not restore fixed proxy")
	}
	for i := 0; i < 3; i++ {
		if r, err := p.Account("a"); err != nil || r.ID != 2 {
			t.Fatal("fixed proxy changed")
		}
	}
}

func TestProxyRotationChangesBlocksAndInsufficientPool(t *testing.T) {
	p := rotationPool(t, 3)
	if err := p.BindRotation([]string{"a"}, true); err != nil {
		t.Fatal(err)
	}
	r1, _ := p.Account("a")
	r2, _ := p.Pin(2)
	if err := p.Block(r2, "blocked exit"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		r, err := p.Account("a")
		if err != nil || r.ID == 2 || r.Fingerprint == r1.Fingerprint {
			t.Fatalf("did not rotate around blocked proxy: %v %v", r, err)
		}
		r1 = r
	}
	if _, err := p.Action(M{"action": "toggle", "id": 3, "enabled": false}); err != nil {
		t.Fatal(err)
	}
	if r, err := p.Account("a"); err == nil || r.ID != 0 || yes(p.Policy("a")["available"]) {
		t.Fatal("single remaining proxy was reused or became direct")
	}
	if _, err := p.Action(M{"action": "toggle", "id": 3, "enabled": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Action(M{"action": "delete", "id": 2}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		r, err := p.Account("a")
		if err != nil || r.ID == 2 || r.Fingerprint == r1.Fingerprint {
			t.Fatalf("pool deletion broke rotation: %v %v", r, err)
		}
		r1 = r
	}
	for _, id := range []int{1, 3} {
		r, _ := p.Pin(id)
		if err := p.Block(r, "blocked"); err != nil {
			t.Fatal(err)
		}
	}
	if !p.ExitBlocked("a") || !yes(p.Policy("a")["blocked"]) {
		t.Fatal("all blocked exits hidden")
	}
	if _, err := p.Account("a"); err == nil {
		t.Fatal("blocked proxies selected")
	}
	if r, err := p.Account("a", false); err != nil || r.ID <= 0 {
		t.Fatal("disabled IP blocking still filtered proxies")
	}
}

func TestProxyRotationDuplicateAddressesAndConcurrentAllocation(t *testing.T) {
	p := rotationPool(t, 1)
	if _, err := p.Action(M{"action": "create", "name": "duplicate", "url": "http://127.0.0.1:10001"}); err != nil {
		t.Fatal(err)
	}
	if err := p.BindRotation([]string{"a"}, true); err == nil {
		t.Fatal("same proxy counted as two exits")
	}
	if p.Policy("a")["mode"] != "direct" {
		t.Fatal("failed binding changed account mode")
	}
	if _, err := p.Action(M{"action": "create", "name": "other", "url": "http://127.0.0.1:10003"}); err != nil {
		t.Fatal(err)
	}
	if err := p.BindRotation([]string{"a"}, true); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan int, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := p.Account("a")
			if err != nil {
				t.Error(err)
				return
			}
			results <- r.ID
		}()
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for id := range results {
		counts[id]++
	}
	if !reflect.DeepEqual(counts, map[int]int{1: 50, 3: 50}) {
		t.Fatalf("unbalanced concurrent proxy selection: %v", counts)
	}
}

func TestActualRequestsRotateProxiesAndReimportPreservesMode(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "a", nil)
	h.add(t, "b", nil)
	seen := make(chan int, 128)
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	for i := 1; i <= 3; i++ {
		id := i
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- id
			forward := r.Clone(r.Context())
			forward.RequestURI = ""
			resp, err := transport.RoundTrip(forward)
			if err != nil {
				http.Error(w, "forward failed", 502)
				return
			}
			defer resp.Body.Close()
			for k, values := range resp.Header {
				for _, v := range values {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
		}))
		t.Cleanup(proxy.Close)
		h.post(t, "/proxy-pool/manage", M{"action": "create", "name": fmt.Sprint("proxy-", i), "url": proxy.URL})
	}
	h.post(t, "/accounts/proxy", M{"all": true, "proxy_mode": "round_robin"})
	for i, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/count_tokens", "/v1/chat/completions", "/v1/responses"} {
		h.request(t, "GET", "/accounts", nil, false)
		h.request(t, "GET", "/proxy-pool", nil, false)
		resp, _ := h.request(t, "POST", path, M{"model": "auto"}, true)
		if resp.StatusCode != 200 {
			t.Fatalf("%s failed: %d", path, resp.StatusCode)
		}
		if got := <-seen; got != i%3+1 {
			t.Fatalf("%s routed via %d, want %d", path, got, i%3+1)
		}
	}
	if err := h.app.store.Update("a", func(current M) error {
		current["enabled"] = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if r := h.post(t, "/accounts/test", M{"uid": "a", "model": "auto"}); !yes(r["ok"]) {
		t.Fatal(r)
	}
	if got := <-seen; got != 1 {
		t.Fatal("panel test did not rotate")
	}
	if _, err := h.app.requestJSON(context.Background(), "GET", h.upstream.URL+"/api/user/self", h.app.store.Get("a"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != 2 {
		t.Fatal("account request did not rotate")
	}
	if r := h.post(t, "/accounts/add", M{"token": fakeToken("a")}); !yes(r["updated"]) {
		t.Fatal(r)
	}
	if !h.app.proxies.Rotating("a") {
		t.Fatal("reimport replaced rotation mode with fixed route")
	}
	last := 2
	for len(seen) > 0 {
		next := <-seen
		if next != last%3+1 {
			t.Fatalf("reimport requests did not rotate: %d -> %d", last, next)
		}
		last = next
	}
	for _, id := range []int{2, 3} {
		h.post(t, "/proxy-pool/manage", M{"action": "toggle", "id": id, "enabled": false})
	}
	h.callsMu.Lock()
	before := len(h.calls)
	h.callsMu.Unlock()
	resp, _ := h.request(t, "POST", "/v1/chat/completions", M{"model": "auto"}, true)
	h.callsMu.Lock()
	after := len(h.calls)
	h.callsMu.Unlock()
	if resp.StatusCode != 503 || after != before || len(seen) != 0 {
		t.Fatal("insufficient rotating pool fell back to direct or reused proxy")
	}
}

func TestProxyRotationBindingValidationAndBatch(t *testing.T) {
	h := newHarness(t, nil)
	h.add(t, "a", nil)
	h.add(t, "b", nil)
	if resp, _ := h.request(t, "POST", "/accounts/proxy", M{"uid": "a", "proxy_mode": "round_robin"}, false); resp.StatusCode != 400 {
		t.Fatal("empty pool accepted")
	}
	for i := 1; i <= 2; i++ {
		h.post(t, "/proxy-pool/manage", M{"action": "create", "name": fmt.Sprint(i), "url": fmt.Sprintf("http://127.0.0.1:%d", 10000+i)})
	}
	for _, body := range []M{{"uid": "a", "proxy_mode": "bad"}, {"uid": "a", "proxy_mode": true}, {"uid": "a", "proxy_mode": "round_robin", "proxy_id": 1}, {"uid": "missing", "proxy_mode": "round_robin"}} {
		resp, _ := h.request(t, "POST", "/accounts/proxy", body, false)
		if resp.StatusCode != 400 && resp.StatusCode != 404 {
			t.Fatal("invalid rotating binding accepted")
		}
	}
	if r := h.post(t, "/accounts/proxy", M{"all": true, "proxy_mode": "round_robin"}); num(r["updated"]) != 2 {
		t.Fatal(r)
	}
	if !h.app.proxies.Rotating("a") || !h.app.proxies.Rotating("b") {
		t.Fatal("batch mode was not saved")
	}
}
