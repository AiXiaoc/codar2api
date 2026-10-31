package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
)

// Diagnostics are opt-in and stay in the local data directory. Keep one rotated
// file so a long debugging session cannot grow the log without a limit.
type requestDiagnosticLog struct {
	mu   sync.Mutex
	path string
}

type diagnosticContextKey struct{}

type requestDiagnostic struct {
	log     *requestDiagnosticLog
	id      string
	secrets []string
}

var diagnosticAPIKey = regexp.MustCompile(`(?i)\bsk-[a-z0-9_-]{8,}`)
var diagnosticBearer = regexp.MustCompile(`(?i)\bBearer\s+[^\s"'<>]+`)
var diagnosticProxyAuth = regexp.MustCompile(`(https?|socks5h?)://[^\s/@]+:[^\s/@]+@`)

func diagnosticSensitive(key string) bool {
	k := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(key))
	return strings.Contains(k, "authorization") || strings.Contains(k, "cookie") ||
		strings.Contains(k, "apikey") || strings.Contains(k, "password") ||
		strings.Contains(k, "secret") || strings.Contains(k, "credential") ||
		strings.Contains(k, "privatekey") || strings.HasSuffix(k, "token")
}

func (t *requestDiagnostic) cleanString(s string) string {
	for _, secret := range t.secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
	}
	s = tokenPattern.ReplaceAllString(s, "[REDACTED JWT]")
	s = diagnosticAPIKey.ReplaceAllString(s, "[REDACTED API KEY]")
	s = diagnosticBearer.ReplaceAllString(s, "Bearer [REDACTED]")
	return diagnosticProxyAuth.ReplaceAllString(s, "$1://[REDACTED]@")
}

func (t *requestDiagnostic) clean(v any) any {
	switch x := v.(type) {
	case M:
		out := M{}
		for k, value := range x {
			if diagnosticSensitive(k) {
				out[k] = "[REDACTED]"
			} else {
				out[k] = t.clean(value)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = t.clean(value)
		}
		return out
	case string:
		return t.cleanString(x)
	default:
		return v
	}
}

func diagnosticHeaders(h http.Header) M {
	out := M{}
	for k, values := range h {
		out[k] = strings.Join(values, ", ")
	}
	return out
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (t *requestDiagnostic) record(event string, fields M) {
	row := M{"id": t.id, "ts": now(), "event": event, "details": t.clean(fields)}
	b, err := json.Marshal(row)
	if err != nil {
		log.Printf("request diagnostic encoding failed: %v", err)
		return
	}
	if len(b) > 16<<20 {
		b = mustMarshal(M{"id": t.id, "ts": now(), "event": event, "truncated": true, "encoded_bytes": len(b), "prefix": string(b[:1<<20])})
	}
	l := t.log
	l.mu.Lock()
	defer l.mu.Unlock()
	if info, e := os.Stat(l.path); e == nil && info.Size()+int64(len(b))+1 > 64<<20 {
		if e = os.Remove(l.path + ".1"); e != nil && !os.IsNotExist(e) {
			log.Printf("request diagnostic rotation failed: %v", e)
			return
		}
		if e = os.Rename(l.path, l.path+".1"); e != nil {
			log.Printf("request diagnostic rotation failed: %v", e)
			return
		}
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
		if e := f.Close(); err == nil {
			err = e
		}
	}
	if err != nil {
		log.Printf("request diagnostic write failed: %v", err)
	}
}

func diagnosticFor(r *http.Request) *requestDiagnostic {
	t, _ := r.Context().Value(diagnosticContextKey{}).(*requestDiagnostic)
	return t
}

type diagnosticWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	error  bytes.Buffer
}

func (w *diagnosticWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *diagnosticWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *diagnosticWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if w.status >= 400 && w.error.Len() < 32<<10 {
		n := min(len(p), (32<<10)-w.error.Len())
		w.error.Write(p[:n])
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (l *requestDiagnosticLog) begin(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, *http.Request, func()) {
	t := &requestDiagnostic{log: l, id: random(8)}
	for k, values := range r.Header {
		if diagnosticSensitive(k) {
			for _, v := range values {
				t.secrets = append(t.secrets, v)
				if parts := strings.Fields(v); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
					t.secrets = append(t.secrets, parts[1])
				}
			}
		}
	}
	r = r.WithContext(context.WithValue(r.Context(), diagnosticContextKey{}, t))
	w.Header().Set("X-Codar-Diagnostic-Id", t.id)
	dw := &diagnosticWriter{ResponseWriter: w}
	t.record("incoming", M{"method": r.Method, "path": r.URL.Path, "host": r.Host, "content_length": r.ContentLength, "headers": diagnosticHeaders(r.Header)})
	start := now()
	return dw, r, func() {
		status := dw.status
		if status == 0 {
			status = 200
		}
		t.record("gateway_response", M{"status": status, "bytes": dw.bytes, "ms": (now() - start) * 1000, "error_body": dw.error.String()})
	}
}
