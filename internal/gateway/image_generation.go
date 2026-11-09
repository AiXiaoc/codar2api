package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	codarImageGenerationPath = "/v1/images/generations"
	codarResearchPath        = "/v1/research/tools/call"
	codarImageDeadline       = 6 * time.Minute
)

var codarImageTaskID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func codarImageURL(s string) bool {
	if len(s) > 8192 {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Hostname() != "" && u.User == nil && (u.Scheme == "https" || u.Scheme == "http")
}

func parseCodarImage(p M) (M, error) {
	for k := range p {
		switch k {
		case "model", "prompt", "size", "resolution", "n", "response_format", "image_urls", "mask_url", "async":
		default:
			return nil, errors.New("Codar 生图不支持参数 " + k)
		}
	}
	args := M{"model": defaultCodarImageModel, "size": "16:9", "resolution": "1K"}
	for _, k := range []string{"model", "size", "resolution", "prompt", "mask_url"} {
		if v, exists := p[k]; exists {
			s, ok := v.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return nil, errors.New(k + " 必须是非空字符串")
			}
			args[k] = strings.TrimSpace(s)
		}
	}
	model := text(args["model"])
	if !isCodarImageModel(model) {
		return nil, errors.New("不支持的 Codar 生图模型 " + model)
	}
	if count := utf8.RuneCountInString(text(args["prompt"])); count == 0 || count > 16000 {
		return nil, errors.New("prompt 必须包含 1 到 16000 个字符")
	}
	if len(text(args["size"])) > 32 {
		return nil, errors.New("size 过长，请填写宽高比或尺寸，如 16:9")
	}
	switch res := strings.ToUpper(text(args["resolution"])); res {
	case "1K", "2K", "4K":
		args["resolution"] = res
	default:
		return nil, errors.New("resolution 仅支持 1K、2K、4K")
	}
	if n, exists := p["n"]; exists && n != float64(1) && n != 1 {
		return nil, errors.New("Codar 生图一次生成一张图片，n 必须为 1")
	}
	if v, exists := p["response_format"]; exists && v != "url" && v != "b64_json" {
		return nil, errors.New("response_format 仅接受 url 或 b64_json，实际返回格式由网关设置决定")
	}
	if v, exists := p["image_urls"]; exists {
		images, ok := v.([]any)
		if !ok || len(images) > 16 {
			return nil, errors.New("image_urls 必须是 URL 数组，最多 16 项")
		}
		for _, image := range images {
			s, ok := image.(string)
			if !ok || !codarImageURL(s) {
				return nil, errors.New("image_urls 仅接受 HTTP(S) 图片链接")
			}
		}
		if len(images) > 0 {
			args["image_urls"] = images
		}
	}
	if mask := text(args["mask_url"]); mask != "" {
		if (model != "gpt-image-2" && model != "gpt-image-2.5") || len(arr(args["image_urls"])) == 0 {
			return nil, errors.New("mask_url 仅用于带参考图的 GPT Image 2 或 2.5 请求")
		}
		if !codarImageURL(mask) {
			return nil, errors.New("mask_url 必须是 HTTP(S) 链接")
		}
	}
	return args, nil
}

func (a *App) callCodarResearch(ctx context.Context, account M, route *Route, tool string, args M) (M, error) {
	user := str(jwt(text(account["token"]))["username"], text(account["username"]))
	if user == "" {
		return nil, &UpstreamError{Status: 401, Message: "Codar 账号缺少用户名，请重新导入"}
	}
	out, err := a.requestJSON(ctx, http.MethodPost, a.cfg.PayBase+codarResearchPath, account, route,
		M{"tool_name": tool, "username": user, "arguments": args})
	if err != nil {
		return nil, err
	}
	if num(out["code"]) != 200 || len(obj(out["data"])) == 0 {
		return nil, &UpstreamError{Status: 502, Message: "Codar 生图工具返回格式无效"}
	}
	return obj(out["data"]), nil
}

func (a *App) awaitCodarImage(ctx context.Context, account M, route *Route, taskID string) (string, error) {
	consecutiveErrors := 0
	for {
		data, err := a.callCodarResearch(ctx, account, route, "get_tool_task_result", M{"task_id": taskID})
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if status := statusOf(err); status < 500 && status != 429 {
				return "", err
			}
			consecutiveErrors++
			if consecutiveErrors >= 3 {
				return "", err
			}
		} else {
			consecutiveErrors = 0
			result := obj(data["result"])
			switch strings.ToLower(text(result["status"])) {
			case "succeeded":
				imageURL := text(result["url"])
				if !codarImageURL(imageURL) {
					return "", &UpstreamError{Status: 502, Message: "Codar 生图已完成但未返回有效图片链接"}
				}
				return imageURL, nil
			case "queued", "pending", "processing", "running", "submitted", "in_progress":
			case "failed", "error", "canceled", "cancelled", "expired", "timeout", "timed_out":
				msg := str(result["error"], errorText(result))
				return "", &UpstreamError{Status: 502, Message: "Codar 生图任务失败：" + msg}
			default:
				return "", &UpstreamError{Status: 502, Message: "Codar 生图工具未返回可识别的任务状态"}
			}
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func codarImageWaitError(err error) (int, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return 504, "等待 Codar 生图超时，任务可能仍在运行"
	}
	if errors.Is(err, context.Canceled) {
		return 499, "等待 Codar 生图已取消"
	}
	return statusOf(err), safeMessage(err.Error())
}

func codarImageError(w http.ResponseWriter, status int, msg, taskID string) {
	if taskID != "" {
		jsonResponse(w, status, M{"error": M{"message": msg, "type": "upstream_error", "task_id": taskID}})
	} else {
		apiError(w, status, msg)
	}
}

func (a *App) recordImageRequest(row M, keyID string, start time.Time, detail *detailTrace) {
	row["ms"] = time.Since(start).Milliseconds()
	if detail != nil && detail.finish(row) {
		row["diagnostic_id"] = detail.id
	}
	a.usage.Record(row)
	if keyID != "" {
		a.auth.Count(keyID, 0)
	}
}

func (a *App) imageSucceeded(row M) {
	row["ok"], row["status"], row["response_model"] = true, 200, row["model"]
	a.logPersistence(a.store.Update(text(row["uid"]), func(c M) error {
		stats := obj(c["stats"])
		stats["requests"], stats["last_used"], stats["last_model"] = num(stats["requests"])+1, now(), row["model"]
		c["stats"] = stats
		return nil
	}))
}

func (a *App) serveImage(w http.ResponseWriter, r *http.Request, p, key, manualAccount M) {
	manual := manualAccount != nil
	model := text(p["model"])
	respondFailure := func(status int, msg, taskID string) {
		if manual {
			out := M{"ok": false, "model": model, "error": msg}
			if taskID != "" {
				out["task_id"] = taskID
			}
			jsonResponse(w, 200, out)
			return
		}
		codarImageError(w, status, msg, taskID)
	}
	async := false
	if v, exists := p["async"]; exists {
		var ok bool
		if async, ok = v.(bool); !ok {
			respondFailure(400, "async 必须是布尔值", "")
			return
		}
	}
	if async && manual {
		respondFailure(400, "账号调用只支持等待图片生成完成", "")
		return
	}
	args, err := parseCodarImage(p)
	if err != nil {
		respondFailure(400, err.Error(), "")
		return
	}
	model = text(args["model"])
	if !manual {
		if _, err = a.auth.API(r, model); err != nil {
			apiError(w, 403, err.Error())
			return
		}
	}
	select {
	case a.gates <- struct{}{}:
	default:
		respondFailure(429, "网关并发已达上限，请稍后重试", "")
		return
	}
	t0 := time.Now()
	keyID := text(key["id"])
	uid, localTaskID := "", ""
	transferred := false
	release := func() {
		if uid != "" {
			a.store.Release(uid)
		}
		<-a.gates
	}
	row := M{"path": codarImageGenerationPath, "model": model, "requested_model": text(p["model"]), "source": "api", "attempts": 0}
	if manual {
		row["source"], row["uid"], row["account"] = "panel", manualAccount["uid"], manualAccount["username"]
	}
	defer func() {
		if !transferred {
			release()
			if localTaskID != "" {
				a.imageTasks.remove(localTaskID)
			}
			a.recordImageRequest(row, keyID, t0, detailFor(r.Context()))
		}
	}()
	fail := func(status int, msg string, taskID string) {
		msg = safeMessage(msg)
		row["ok"], row["status"], row["error"] = false, status, msg
		if r.Context().Err() != nil {
			row["client_aborted"] = true
			return
		}
		respondFailure(status, msg, taskID)
	}
	settings := a.store.Settings()
	var skip func(string) bool
	if yes(settings["ip_block"]) {
		skip = a.proxies.ExitBlocked
	}
	var account M
	if manual {
		account, err = a.store.ReserveManual(text(manualAccount["uid"]), model, skip)
	} else {
		account, err = a.store.Pick(model, "", nil, skip)
	}
	if err != nil {
		if errors.Is(err, errAccountCapacity) {
			w.Header().Set("Retry-After", "1")
			fail(429, err.Error(), "")
		} else {
			fail(503, err.Error(), "")
		}
		return
	}
	uid = text(account["uid"])
	row["uid"], row["account"] = uid, account["username"]
	current := a.store.Get(uid)
	if current == nil || modelUnavailable(current, model, settings, manual) != "" {
		fail(503, "所选 Codar 账号不再可用", "")
		return
	}
	account = current
	route, err := a.route(account, nil)
	if err != nil {
		fail(502, err.Error(), "")
		return
	}
	if async {
		var ok bool
		if localTaskID, ok = a.imageTasks.reserve(keyID, model, t0); !ok {
			w.Header().Set("Retry-After", "2")
			fail(429, "生图任务队列已满，请稍后重试", "")
			return
		}
	}
	ctx, cancel := context.WithDeadline(r.Context(), t0.Add(codarImageDeadline))
	defer cancel()
	row["attempts"] = 1
	// Submission may be billable. Do not submit again after an ambiguous error.
	data, err := a.callCodarResearch(ctx, account, &route, "image_gen", args)
	if err != nil {
		status, msg := statusOf(err), err.Error()
		// A Researcher billing error is returned to the caller; it does not
		// imply that the account's separate chat balance is exhausted.
		if status == 401 || status == 429 {
			if r.Context().Err() == nil {
				var ue *UpstreamError
				if errors.As(err, &ue) && len(ue.Body) > 0 {
					body, _ := json.Marshal(ue.Body)
					a.noteFailure(account, status, body)
				}
			}
		} else if status >= 500 {
			msg = "无法确认 Codar 是否已收到生图任务，请先核对再重试：" + msg
		}
		fail(status, msg, "")
		return
	}
	taskID := str(data["task_id"], text(data["result"]))
	if !codarImageTaskID.MatchString(taskID) {
		fail(502, "Codar 没有返回有效任务 ID，请先核对任务是否已提交", "")
		return
	}
	if async {
		// The background worker owns the account/global slots after acceptance.
		// It must not inherit the request context or touch its detail capture.
		ready := make(chan struct{})
		if !a.imageTasks.start(func() {
			defer release()
			<-ready
			a.runImageTask(localTaskID, taskID, t0, account, route, row, keyID)
		}) {
			fail(503, "网关正在关闭，Codar 生图任务可能已提交", taskID)
			return
		}
		transferred = true
		defer close(ready)
		row["task_id"] = localTaskID
		w.Header().Set("X-Codar-Account", uid)
		w.Header().Set("Location", codarImageTaskPath+localTaskID)
		w.Header().Set("Retry-After", "2")
		jsonResponse(w, http.StatusAccepted, M{"task_id": localTaskID, "status": "queued", "created": t0.Unix()})
		if detail := detailFor(r.Context()); detail != nil && detail.finish(M{"ok": true, "status": 202, "model": model, "uid": uid, "path": r.URL.Path, "source": "api"}) {
			row["diagnostic_id"] = detail.id
		}
		return
	}
	imageURL, err := a.awaitCodarImage(ctx, account, &route, taskID)
	if err != nil {
		status, msg := codarImageWaitError(err)
		fail(status, msg, taskID)
		return
	}
	w.Header().Set("X-Codar-Account", uid)
	out := M{"created": t0.Unix()}
	if manual {
		out["ok"], out["model"], out["elapsed_ms"] = true, model, time.Since(t0).Milliseconds()
	}
	if err = a.writeCodarImage(w, r, out, imageURL); err != nil {
		fail(statusOf(err), "Codar 生图已完成："+err.Error(), taskID)
		return
	}
	a.imageSucceeded(row)
}
