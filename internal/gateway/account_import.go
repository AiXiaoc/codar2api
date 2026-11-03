package gateway

import (
	"errors"
	"strings"
)

func (a *App) importRoute(p M) (*Route, error) {
	if _, ok := p["proxy_id"]; !ok {
		return nil, nil
	}
	id, err := parseProxyID(p["proxy_id"])
	if err != nil {
		return nil, err
	}
	route, err := a.proxies.Pin(id)
	if err != nil {
		return nil, err
	}
	return &route, nil
}

// importStoredToken writes an account without contacting any upstream service.
func (a *App) importStoredToken(token, note, via string, metadata M) (M, error) {
	return a.importStoredTokenAt(token, note, via, metadata, nil)
}

func (a *App) importStoredTokenAt(token, note, via string, metadata M, route *Route) (M, error) {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	if token == "" || tokenPattern.FindString(token) != token {
		return nil, errors.New("令牌格式不正确，请填写完整 JWT")
	}
	claims := jwt(token)
	username := str(claims["username"], text(metadata["username"]))
	uid := str(metadata["uid"], str(claims["id"], str(claims["user_id"], str(claims["sub"], username))))
	if uid == "" {
		uid = "import-" + digest(token)[:24]
	}
	// Resolve the identity and save serially so concurrent files do not create
	// duplicate rows for the same account under different exported IDs.
	a.importMu.Lock()
	defer a.importMu.Unlock()
	for _, existing := range a.store.List() {
		if text(existing["token"]) == token || username != "" && text(existing["username"]) == username {
			uid = text(existing["uid"])
			break
		}
	}
	unlock := a.lockAccount(uid)
	defer unlock()
	candidate := a.store.Get(uid)
	if candidate == nil {
		candidate = M{"uid": uid, "note": note, "login_via": via, "profile_pending": true}
		if username != "" {
			candidate["username"] = username
		}
		if name := text(metadata["display_name"]); name != "" {
			candidate["display_name"] = name
		}
	}
	candidate["token"] = token
	candidate["token_invalid"] = false
	updated, err := a.store.Upsert(candidate)
	if err != nil {
		return nil, err
	}
	if route != nil {
		if err := a.proxies.Bind([]string{uid}, *route); err != nil {
			return nil, err
		}
	}
	return M{"ok": true, "updated": updated, "account": a.public(a.store.Get(uid))}, nil
}
