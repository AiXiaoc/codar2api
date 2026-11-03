package gateway

import (
	"errors"
	"net/http"
)

// Only failures before HTTP obtained a connection qualify for proxy retries.
// Do not retain the transport error here: proxy URLs may contain credentials.
type proxyConnectError struct{}

func (*proxyConnectError) Error() string {
	return "指定代理连接上游失败，请检查代理；当前请求未切换为直连"
}

type proxyConnectPolicy struct {
	retry, switchProxy bool
}

func proxyPolicy(settings M) proxyConnectPolicy {
	return proxyConnectPolicy{yes(settings["proxy_connect_retry"]), yes(settings["proxy_connect_switch"])}
}

type proxyAttemptObserver func(*http.Request, Route, int) func(*http.Response, error)

// Snapshot a finite list. Later pool additions cannot extend a running request
// indefinitely. Every route is rechecked immediately before use.
func (p *ProxyPool) fallbackRoutes(initial Route) []Route {
	p.mu.Lock()
	defer p.mu.Unlock()
	rows := arr(p.data["proxies"])
	start := 0
	for i, v := range rows {
		if int(num(obj(v)["id"])) == initial.ID {
			start = i + 1
			break
		}
	}
	seen := map[string]bool{initial.Fingerprint: true}
	routes := []Route{}
	for n := 0; n < len(rows); n++ {
		m := obj(rows[(start+n)%len(rows)])
		id := int(num(m["id"]))
		if id == initial.ID || !yes(m["enabled"]) || yes(m["blocked"]) {
			continue
		}
		route, _, err := p.pinLocked(id)
		if err == nil && !seen[route.Fingerprint] {
			seen[route.Fingerprint] = true
			routes = append(routes, route)
		}
	}
	return routes
}

func (p *ProxyPool) activateFallback(route Route) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	current, _, err := p.pinLocked(route.ID)
	if err != nil || current.Fingerprint != route.Fingerprint || p.blockLocked(route.ID) != nil {
		return false
	}
	for i, v := range arr(p.data["proxies"]) {
		if int(num(obj(v)["id"])) == route.ID {
			p.rotationNext = i + 1
			p.rotationLast = route.Fingerprint
			break
		}
	}
	return true
}

// Proxy attempts stay inside the same account reservation. Return the actual
// route so an HTTP IP block is attributed to the exit that returned it.
func (p *ProxyPool) doWithPolicy(req *http.Request, initial Route, policy proxyConnectPolicy, observe proxyAttemptObserver) (*http.Response, Route, error) {
	route := initial
	var fallback []Route
	loadedFallback := false
	proxyAttempt, sameProxyRetries := 0, 0
	canReplay := req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
	for {
		if err := req.Context().Err(); err != nil {
			return nil, route, err
		}
		attemptReq := req
		if proxyAttempt > 0 {
			attemptReq = req.Clone(req.Context())
			if req.Body != nil && req.Body != http.NoBody {
				body, err := req.GetBody()
				if err != nil {
					return nil, route, errors.New("重建代理重试请求失败")
				}
				attemptReq.Body = body
			}
		}
		proxyAttempt++
		var finish func(*http.Response, error)
		if observe != nil {
			finish = observe(attemptReq, route, proxyAttempt)
		}
		resp, err := p.Do(attemptReq, route)
		if finish != nil {
			finish(resp, err)
		}
		var connectErr *proxyConnectError
		if !errors.As(err, &connectErr) || route.ID <= 0 || req.Context().Err() != nil || !canReplay {
			return resp, route, err
		}
		if policy.retry && sameProxyRetries == 0 {
			sameProxyRetries++
			continue
		}
		if !policy.switchProxy {
			return nil, route, err
		}
		if !loadedFallback {
			fallback, loadedFallback = p.fallbackRoutes(initial), true
		}
		switched := false
		for len(fallback) > 0 {
			next := fallback[0]
			fallback = fallback[1:]
			if p.activateFallback(next) {
				route, sameProxyRetries, switched = next, 0, true
				break
			}
		}
		if !switched {
			return nil, route, err
		}
	}
}
