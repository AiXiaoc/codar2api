package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

func sessionHint(r *http.Request, p M) string {
	for _, k := range []string{"X-Session-Id", "X-Conversation-Id", "Session-Id"} {
		if s := r.Header.Get(k); s != "" {
			return digest(s)
		}
	}
	for _, k := range []string{"session_id", "conversation_id", "prompt_cache_key"} {
		if s := text(p[k]); s != "" {
			return digest(s)
		}
	}
	if user := text(obj(p["metadata"])["user_id"]); user != "" {
		return digest(user)
	}
	// Without an explicit id, the first user message stands for the conversation.
	// The Responses API sends it in "input", as text or a list of items.
	if s, ok := p["input"].(string); ok && s != "" {
		b, _ := json.Marshal(s)
		return digest(string(b))
	}
	for _, list := range []any{p["messages"], p["input"]} {
		for _, v := range arr(list) {
			if obj(v)["role"] == "user" {
				b, _ := json.Marshal(obj(v)["content"])
				return digest(string(b))
			}
		}
	}
	return ""
}
func retryable(status int, body []byte) bool {
	var d M
	_ = json.Unmarshal(body, &d)
	if restriction(d) != "" {
		return true
	}
	if status == 503 && strings.Contains(strings.ToLower(string(body)), "no available channel") {
		return false
	}
	return status == 0 || status == 401 || status == 402 || status == 429 || status >= 500
}
func (a *App) noteFailure(account M, status int, body []byte) {
	uid := text(account["uid"])
	var d M
	_ = json.Unmarshal(body, &d)
	if reason := restriction(d); reason != "" {
		a.logPersistence(a.store.Restrict(uid, reason))
		return
	}
	settings := a.store.Settings()
	message := safeMessage(string(body))
	if len(d) > 0 {
		message = safeMessage(errorText(d))
	}
	// 401 always means the token is no longer accepted; re-import or renewal clears it.
	if status == 401 {
		a.logPersistence(a.store.Update(uid, func(current M) error {
			current["token_invalid"] = true
			current["last_error"] = message
			current["last_error_at"] = now()
			return nil
		}))
		return
	}
	seconds, kind, reason := num(settings["cooldown_error_seconds"]), "error", "上游错误"
	lower := strings.ToLower(string(body))
	if status == 429 {
		seconds = num(settings["cooldown_429_seconds"])
		kind = "rate"
		reason = "请求限流"
	} else if status == 402 || strings.Contains(lower, "余额") || strings.Contains(lower, "insufficient") || strings.Contains(lower, "quota") || strings.Contains(lower, "balance") {
		seconds = num(settings["cooldown_balance_seconds"])
		kind = "balance"
		reason = "余额不足"
	}
	a.logPersistence(a.store.Update(uid, func(current M) error {
		current["cooldown_until"] = now() + seconds
		current["cooldown_kind"] = kind
		current["cooldown_reason"] = reason
		if !yes(current["restricted"]) {
			current["last_error"] = message
			current["last_error_at"] = now()
		}
		return nil
	}))
}
func (a *App) relay(w http.ResponseWriter, r *http.Request, p, key, explicit M) {
	t0 := time.Now()
	requestedModel := text(p["model"])
	model := strings.TrimSpace(requestedModel)
	if model == "" {
		apiError(w, 400, "请指定 model")
		return
	}
	settings := a.store.Settings()
	passthrough := yes(settings["auto_passthrough"])
	bodyChanged := transformIdentityPrompts(r.URL.Path, p, yes(settings["dsh_identity_rewrite"]), yes(settings["identity_first_line_fill"]))
	isStream := yes(p["stream"])
	if !passthrough && r.URL.Path == "/v1/chat/completions" && isStream {
		opts := obj(p["stream_options"])
		opts["include_usage"] = true
		p["stream_options"] = opts
	}
	raw := forwardedBody(r, p, passthrough, bodyChanged)
	row := M{"path": r.URL.Path, "model": model, "requested_model": requestedModel, "stream": isStream, "source": "api", "attempts": 0}
	if explicit != nil {
		row["source"] = "panel"
	}
	counted := r.URL.Path != "/v1/messages/count_tokens"
	var chosen M
	var usage M
	// Attempts that failed before switching accounts are kept in "tries" so each
	// account's request records show its own failure, not only the final result.
	tries, switched, pending := []any{}, false, false
	failTry := func(status int, msg string) {
		if detail := detailFor(r.Context()); detail != nil {
			detail.failed = true
		}
		try := M{"uid": chosen["uid"], "account": chosen["username"], "status": status, "ok": false, "error": safeMessage(msg)}
		if model, ok := row["response_model"].(string); ok {
			try["response_model"] = model
		}
		tries = append(tries, try)
		switched, pending = true, false
	}
	defer func() {
		row["ms"] = time.Since(t0).Milliseconds()
		if chosen != nil {
			row["uid"] = chosen["uid"]
			row["account"] = chosen["username"]
		}
		if switched {
			if pending {
				tries = append(tries, M{"uid": chosen["uid"], "account": chosen["username"], "status": row["status"], "ok": yes(row["ok"]), "error": row["error"]})
			}
			row["tries"] = tries
		}
		if counted {
			if detail := detailFor(r.Context()); detail != nil && detail.finish(row) {
				row["diagnostic_id"] = detail.id
			}
			a.usage.Record(row)
		} else if detail := detailFor(r.Context()); detail != nil {
			if detail.finish(row) {
				row["diagnostic_id"] = detail.id
				a.usage.Record(row)
			}
		}
		if trace := diagnosticFor(r); trace != nil {
			trace.record("relay_result", row)
		}
		if key != nil {
			a.auth.Count(text(key["id"]), num(usage["prompt"])+num(usage["completion"]))
		}
	}()
	fail := func(status int, msg string) {
		row["status"] = status
		row["ok"] = false
		row["error"] = safeMessage(msg)
		apiError(w, status, msg)
	}
	tried := map[string]bool{}
	attempts := max(1, int(num(settings["max_attempts"])))
	if explicit != nil {
		attempts = 1
	}
	// Requests with no session at all get no hint, so they rotate through the pool.
	hint := ""
	if s := sessionHint(r, p); s != "" {
		hint = text(key["id"]) + ":" + s
	}
	var lastBody []byte
	lastStatus := 503
	lastType := "application/json"
	lastError := "没有可用的 Codar 账号"
	ipBlock := yes(settings["ip_block"])
	var skip func(string) bool
	if ipBlock {
		skip = a.proxies.ExitBlocked
		if a.proxies.AnyBlocked() {
			lastError += "（有出口 IP 处于封控状态）"
		}
	}
	reservedUID := ""
	release := func() {
		if reservedUID != "" {
			a.store.Release(reservedUID)
			reservedUID = ""
		}
	}
	defer release()
	for attempt := 0; attempt < attempts; attempt++ {
		release()
		if r.Context().Err() != nil {
			row["error"] = "客户端已取消请求"
			row["client_aborted"] = true
			return
		}
		var next M
		var pickErr error
		if explicit != nil {
			next, pickErr = a.store.ReserveManual(text(explicit["uid"]), model, skip)
		} else {
			next, pickErr = a.store.Pick(model, hint, tried, skip)
		}
		if next == nil {
			if errors.Is(pickErr, errAccountCapacity) && len(tried) == 0 {
				w.Header().Set("Retry-After", "1")
				fail(http.StatusTooManyRequests, pickErr.Error())
				return
			}
			if explicit != nil && pickErr != nil {
				fail(http.StatusForbidden, pickErr.Error())
				return
			}
			break
		}
		uid := text(next["uid"])
		reservedUID = uid
		tried[uid] = true
		row["attempts"] = attempt + 1
		// Recheck the live persisted flag immediately before every upstream dispatch.
		current := a.store.Get(uid)
		if current == nil {
			continue
		}
		if reason := modelUnavailable(current, model, settings, explicit != nil); reason != "" {
			lastError = reason
			if explicit != nil {
				chosen = current
				fail(403, reason)
				return
			}
			continue
		}
		chosen, pending = current, true
		delete(row, "response_model")
		route, e := a.proxies.Account(uid, ipBlock)
		if e != nil {
			lastError = e.Error()
			if explicit != nil {
				fail(502, lastError)
				return
			}
			failTry(0, lastError)
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
		target := a.cfg.APIBase + r.URL.Path
		if passthrough {
			if query := forwardedQuery(r.URL.RawQuery); query != "" {
				target += "?" + query
			}
		}
		req, e := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(raw))
		if e != nil {
			cancel()
			fail(500, "上游地址配置无效")
			return
		}
		if passthrough {
			copyForwardedHeaders(req.Header, r.Header)
		}
		req.Header.Set("Authorization", "Bearer "+text(chosen["token"]))
		req.Header.Set("Content-Type", str(req.Header.Get("Content-Type"), "application/json"))
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Set("User-Agent", modelUserAgent(settings, r.UserAgent()))
		if !passthrough {
			req.Header.Set("Accept", str(r.Header.Get("Accept"), "*/*"))
			for _, h := range []string{"anthropic-version", "anthropic-beta"} {
				if value := r.Header.Get(h); value != "" {
					req.Header.Set(h, value)
				}
			}
		}
		if req.Header.Get("Accept") == "" {
			req.Header.Set("Accept", "*/*")
		}
		if strings.HasPrefix(r.URL.Path, "/v1/messages") && req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		trace := diagnosticFor(r)
		detail := detailFor(r.Context())
		var detailAttempt *detailAttempt
		if detail != nil {
			detailAttempt = detail.startAttempt(req, raw)
		}
		if trace != nil {
			trace.secrets = append(trace.secrets, text(chosen["token"]))
			trace.record("upstream_request", M{"attempt": attempt + 1, "account_uid": uid, "api_key_name": key["name"], "url": req.URL.Scheme + "://" + req.URL.Host + req.URL.Path, "headers": diagnosticHeaders(req.Header), "body": p, "json_bytes": len(raw), "json_sha256": digest(string(raw)), "route_id": route.ID})
		}
		resp, e := a.proxies.Do(req, route)
		if e != nil {
			if detail != nil {
				detail.errorAttempt(detailAttempt, e)
			}
			if trace != nil {
				trace.record("upstream_network_error", M{"attempt": attempt + 1, "error": e.Error()})
			}
			cancel()
			if r.Context().Err() != nil {
				row["error"] = "客户端已取消请求"
				row["client_aborted"] = true
				return
			}
			lastError = e.Error()
			lastBody = nil
			a.noteFailure(chosen, 0, []byte(lastError))
			failTry(0, lastError)
			continue
		}
		if detail != nil {
			detail.responseAttempt(detailAttempt, resp)
		}
		if trace != nil {
			trace.record("upstream_response", M{"attempt": attempt + 1, "status": resp.StatusCode, "headers": diagnosticHeaders(resp.Header)})
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
			if detail != nil && e == nil && len(body) > 1<<20 {
				_, _ = io.Copy(io.Discard, resp.Body)
			}
			resp.Body.Close()
			cancel()
			if trace != nil {
				trace.record("upstream_error_body", M{"attempt": attempt + 1, "body": string(body)})
			}
			if e != nil || len(body) > 1<<20 {
				lastError = "读取上游错误响应失败"
				lastBody = nil
				failTry(resp.StatusCode, lastError)
				continue
			}
			lastBody, lastStatus, lastType = body, resp.StatusCode, str(resp.Header.Get("Content-Type"), "application/json")
			var d M
			_ = json.Unmarshal(body, &d)
			if model, ok := responseModel(d); ok {
				row["response_model"] = model
			}
			lastError = errorText(d)
			if reason := ipBlocked(lastStatus, body, resp.Header.Get("Server")); reason != "" {
				lastError = ipBlockedMessage(route, reason)
				if ipBlock {
					// The IP, not the account, was refused: stop using this exit and
					// answer with JSON rather than the edge server's HTML page.
					a.logPersistence(a.proxies.Block(route, reason))
					lastBody = nil
					failTry(lastStatus, lastError)
					continue
				}
			}
			if retryable(lastStatus, body) {
				a.noteFailure(chosen, lastStatus, body)
				failTry(lastStatus, lastError)
				continue
			}
			row["ok"] = false
			row["status"] = lastStatus
			row["error"] = safeMessage(lastError)
			w.Header().Set("Content-Type", lastType)
			w.WriteHeader(lastStatus)
			_, _ = w.Write(lastBody)
			return
		}
		usage = a.pipe(w, r, chosen, resp, row, t0)
		cancel()
		return
	}
	if lastBody != nil {
		row["ok"] = false
		row["status"] = lastStatus
		row["error"] = safeMessage(lastError)
		w.Header().Set("Content-Type", lastType)
		w.WriteHeader(lastStatus)
		_, _ = w.Write(lastBody)
		return
	}
	fail(503, lastError)
}
func (a *App) pipe(w http.ResponseWriter, r *http.Request, account M, resp *http.Response, row M, t0 time.Time) M {
	defer resp.Body.Close()
	ctype := str(resp.Header.Get("Content-Type"), "application/json")
	stream := strings.Contains(ctype, "event-stream")
	uid := text(account["uid"])
	tap := newTap(func(v M) {
		if reason := restriction(v); reason != "" {
			a.logPersistence(a.store.Restrict(uid, reason))
		}
	})
	row["ok"] = true
	row["status"] = resp.StatusCode
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Codar-Account", text(account["username"]))
	for _, h := range []string{"X-Request-Id", "Cache-Control"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if stream {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(resp.StatusCode)
		controller := http.NewResponseController(w)
		_ = controller.Flush()
		buf := make([]byte, 32<<10)
		first := true
		var readError error
		var writeError error
		var streamBytes int64
		for {
			n, e := resp.Body.Read(buf)
			readError = e
			if n > 0 {
				streamBytes += int64(n)
				if first {
					row["ttft_ms"] = time.Since(t0).Milliseconds()
					first = false
				}
				tap.Feed(buf[:n])
				if _, we := w.Write(buf[:n]); we != nil {
					writeError = we
					break
				}
				if fe := controller.Flush(); fe != nil {
					writeError = fe
					break
				}
			}
			// SSE completion is independent of the HTTP connection closing. Once
			// delivered, do not wait for another read that can race a client cancel.
			if tap.terminal != "" || e != nil {
				break
			}
		}
		tap.Finish()
		row["stream_completed"] = tap.terminal != ""
		if tap.terminal != "" {
			row["stream_end"] = tap.terminal
		}
		if writeError != nil || tap.terminal == "" && r.Context().Err() != nil {
			row["ok"] = false
			row["client_aborted"] = true
			row["error"] = "客户端已断开"
		} else if tap.terminal == "" {
			row["ok"] = false
			row["error"] = "上游流提前断开（未收到结束标记）"
		}
		if trace := diagnosticFor(r); trace != nil {
			details := M{"completed": tap.terminal != "", "terminal": tap.terminal, "bytes": streamBytes, "protocol": resp.Proto}
			for k, e := range map[string]error{"read_error": readError, "write_error": writeError, "client_context_error": r.Context().Err()} {
				if e != nil {
					details[k] = e.Error()
				}
			}
			trace.record("stream_termination", details)
		}
	} else {
		body, e := io.ReadAll(io.LimitReader(resp.Body, 32<<20+1))
		if detail := detailFor(r.Context()); detail != nil && e == nil && len(body) > 32<<20 {
			_, _ = io.Copy(io.Discard, resp.Body)
		}
		if e != nil || len(body) > 32<<20 {
			row["ok"] = false
			row["status"] = 502
			row["error"] = "读取上游响应失败或响应过大"
			apiError(w, 502, text(row["error"]))
			return nil
		}
		var v M
		_ = json.Unmarshal(body, &v)
		tap.inspect(v)
		if reason := restriction(v); reason != "" {
			a.logPersistence(a.store.Restrict(uid, reason))
			tap.err = "账号受限：" + reason
		}
		w.WriteHeader(resp.StatusCode)
		if _, e = w.Write(body); e != nil {
			row["ok"] = false
			row["client_aborted"] = true
			row["error"] = "客户端已断开"
		}
	}
	if tap.hasModel {
		row["response_model"] = tap.model
	}
	if tap.err != "" {
		row["ok"] = false
		row["error"] = tap.err
	}
	usage := normalizeUsage(tap.raw)
	for k, v := range usage {
		row[k] = v
	}
	if cost := tap.billing["cost_cny"]; cost != nil {
		row["cost_cny"] = cost
	}
	a.store.mu.Lock()
	for _, current := range a.store.accounts {
		if current["uid"] == uid {
			stats := obj(current["stats"])
			stats["requests"] = num(stats["requests"]) + 1
			stats["last_used"] = now()
			stats["last_model"] = row["model"]
			stats["cost_cny"] = num(stats["cost_cny"]) + num(row["cost_cny"])
			current["stats"] = stats
			if !yes(current["restricted"]) {
				current["last_error"] = ""
			}
			a.store.dirty = true
			break
		}
	}
	a.store.mu.Unlock()
	return usage
}

// testAccount sends one non-streaming chat request from the panel. The quick
// test sends "Hi" with a 16-token cap; the call dialog passes its own prompt.
func (a *App) testAccount(ctx context.Context, account M, model, prompt string, maxTokens int) M {
	if prompt == "" {
		prompt = "Hi"
	}
	if maxTokens <= 0 {
		maxTokens = 16
	}
	if model == "" {
		free := freeModels(account, now())
		if len(free) > 0 {
			model = free[0]
		} else {
			model = "auto"
		}
	}
	var skip func(string) bool
	if yes(a.store.Settings()["ip_block"]) {
		skip = a.proxies.ExitBlocked
	}
	requestedAccount := account
	account, err := a.store.ReserveManual(text(account["uid"]), model, skip)
	if err != nil {
		if detail := detailFor(ctx); detail != nil {
			detail.pendingRow = M{"path": "/v1/chat/completions", "source": "panel", "model": model, "uid": requestedAccount["uid"], "account": requestedAccount["username"], "status": 403, "ok": false, "error": err.Error()}
		}
		return M{"ok": false, "error": err.Error(), "model": model}
	}
	defer a.store.Release(text(account["uid"]))
	t := time.Now()
	d, e := a.requestJSONWithin(ctx, 3*time.Minute, "POST", a.cfg.APIBase+"/v1/chat/completions", account, nil, M{"model": model, "messages": []any{M{"role": "user", "content": prompt}}, "max_tokens": maxTokens, "stream": false})
	row := M{"path": "/v1/chat/completions", "source": "panel", "model": model, "requested_model": model, "uid": account["uid"], "account": account["username"], "stream": false, "ms": time.Since(t).Milliseconds(), "status": 200, "ok": e == nil}
	result := M{"ok": e == nil, "model": model, "elapsed_ms": row["ms"]}
	if e != nil {
		result["error"] = safeMessage(e.Error())
		row["error"] = result["error"]
		row["status"] = statusOf(e)
		var upstream *UpstreamError
		if errors.As(e, &upstream) {
			if returned, ok := responseModel(upstream.Body); ok {
				row["response_model"] = returned
			}
		}
	} else {
		reply := ""
		for _, v := range arr(d["choices"]) {
			reply += text(obj(obj(v)["message"])["content"])
		}
		if reply == "" {
			for _, v := range arr(d["content"]) {
				reply += text(obj(v)["text"])
			}
		}
		result["reply"] = reply
		tap := newTap(nil)
		tap.inspect(d)
		if tap.hasModel {
			row["response_model"] = tap.model
		}
		usage := normalizeUsage(tap.raw)
		for k, v := range usage {
			row[k] = v
		}
		result["usage"] = usage
	}
	if detail := detailFor(ctx); detail != nil {
		detail.pendingRow = row
	} else {
		a.usage.Record(row)
	}
	return result
}
