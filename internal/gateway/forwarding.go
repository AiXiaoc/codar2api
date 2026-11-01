package gateway

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

type originalModelBodyKey struct{}

func forwardedBody(r *http.Request, p M, passthrough, changed bool) []byte {
	if passthrough && !changed {
		if raw, ok := r.Context().Value(originalModelBodyKey{}).([]byte); ok {
			return raw
		}
	}
	raw, _ := json.Marshal(p)
	return raw
}

func localCredentialParameter(key string) bool {
	switch strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(key)) {
	case "key", "apikey", "accesskey", "token", "accesstoken", "authorization":
		return true
	}
	return false
}

// Preserve the caller's query order, repeated parameters and encoding while
// keeping credentials out of the upstream URL.
func forwardedQuery(raw string) string {
	parts := []string{}
	for _, part := range strings.Split(raw, "&") {
		key, _, _ := strings.Cut(part, "=")
		key, err := url.QueryUnescape(key)
		if err != nil || localCredentialParameter(key) {
			continue
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "&")
}

func copyForwardedHeaders(dst, src http.Header) {
	blocked := map[string]bool{
		"connection": true, "proxy-connection": true, "keep-alive": true,
		"te": true, "trailer": true, "transfer-encoding": true, "upgrade": true,
		"authorization": true, "proxy-authorization": true, "www-authenticate": true,
		"proxy-authenticate": true, "x-api-key": true, "api-key": true,
		"x-auth-token": true, "cookie": true, "cookie2": true,
		"host": true, "content-length": true, "content-encoding": true,
		"accept-encoding": true, "forwarded": true, "x-real-ip": true,
	}
	for _, connection := range src.Values("Connection") {
		for _, name := range strings.Split(connection, ",") {
			blocked[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	for name, values := range src {
		lower := strings.ToLower(name)
		if blocked[lower] || strings.HasPrefix(lower, "x-codar-") || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "sec-") {
			continue
		}
		dst[name] = append([]string(nil), values...)
	}
}
