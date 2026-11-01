package gateway

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type detailContextKey struct{}

type detailPart struct {
	method, url string
	status      int
	headers     http.Header
	body        *os.File
}
type detailAttempt struct {
	request, response detailPart
	err               string
}
type detailTrace struct {
	app               *App
	id, tempDir       string
	ts                float64
	request, response detailPart
	attempts          []*detailAttempt
	failed, finished  bool
	pendingRow        M
	writeErr          error
}

// A diagnostic write must never interrupt the request it observes.
type detailSink struct {
	trace *detailTrace
	file  *os.File
}

func (s detailSink) Write(p []byte) (int, error) {
	if s.file != nil && s.trace.writeErr == nil {
		_, s.trace.writeErr = s.file.Write(p)
	}
	return len(p), nil
}

type detailReadCloser struct {
	io.Reader
	io.Closer
}

type detailWriter struct {
	http.ResponseWriter
	trace  *detailTrace
	status int
}

func (w *detailWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *detailWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.trace.response.status = status
	w.trace.response.headers = w.Header().Clone()
	w.ResponseWriter.WriteHeader(status)
}
func (w *detailWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	_, _ = (detailSink{w.trace, w.trace.response.body}).Write(p[:n])
	return n, err
}

func detailFor(ctx context.Context) *detailTrace {
	t, _ := ctx.Value(detailContextKey{}).(*detailTrace)
	return t
}

func (t *detailTrace) spool() *os.File {
	f, err := os.CreateTemp(t.tempDir, "body-")
	if err != nil {
		t.writeErr = err
		return nil
	}
	return f
}

func (a *App) beginDetail(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, *http.Request, func()) {
	if !yes(a.store.Settings()["detailed_debug"]) {
		return w, r, func() {}
	}
	dir := filepath.Join(a.cfg.DataDir, "request-details")
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("detail log: %v", err)
		return w, r, func() {}
	}
	tmp, err := os.MkdirTemp(dir, ".active-")
	if err != nil {
		log.Printf("detail log: %v", err)
		return w, r, func() {}
	}
	t := &detailTrace{app: a, id: random(16), tempDir: tmp, ts: now()}
	t.request = detailPart{method: r.Method, url: r.URL.RequestURI(), headers: r.Header.Clone(), body: t.spool()}
	t.request.headers.Set("Host", r.Host)
	if r.ContentLength >= 0 {
		t.request.headers.Set("Content-Length", strconv.FormatInt(r.ContentLength, 10))
	}
	if len(r.TransferEncoding) > 0 {
		t.request.headers.Set("Transfer-Encoding", strings.Join(r.TransferEncoding, ", "))
	}
	t.response.body = t.spool()
	r.Body = detailReadCloser{Reader: io.TeeReader(r.Body, detailSink{t, t.request.body}), Closer: r.Body}
	r = r.WithContext(context.WithValue(r.Context(), detailContextKey{}, t))
	dw := &detailWriter{ResponseWriter: w, trace: t}
	return dw, r, func() {
		if t.finished {
			return
		}
		row := t.pendingRow
		if row == nil && dw.status >= 400 {
			row = M{"path": r.URL.Path, "source": "api", "status": dw.status, "ok": false, "error": "请求未能进入模型转发"}
		}
		if row != nil {
			if t.finish(row) {
				row["diagnostic_id"] = t.id
			}
			a.usage.Record(row)
		} else {
			t.finish(M{"ok": true})
		}
	}
}

func (t *detailTrace) startAttempt(req *http.Request, raw []byte) *detailAttempt {
	if t == nil {
		return nil
	}
	a := &detailAttempt{request: detailPart{method: req.Method, url: req.URL.String(), headers: req.Header.Clone(), body: t.spool()}}
	a.request.headers.Set("Host", req.URL.Host)
	if req.ContentLength >= 0 {
		a.request.headers.Set("Content-Length", strconv.FormatInt(req.ContentLength, 10))
	}
	_, _ = (detailSink{t, a.request.body}).Write(raw)
	t.attempts = append(t.attempts, a)
	return a
}
func (t *detailTrace) responseAttempt(a *detailAttempt, resp *http.Response) {
	if t == nil || a == nil {
		return
	}
	a.response = detailPart{status: resp.StatusCode, headers: resp.Header.Clone(), body: t.spool()}
	if resp.ContentLength >= 0 {
		a.response.headers.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	if len(resp.TransferEncoding) > 0 {
		a.response.headers.Set("Transfer-Encoding", strings.Join(resp.TransferEncoding, ", "))
	}
	resp.Body = detailReadCloser{Reader: io.TeeReader(resp.Body, detailSink{t, a.response.body}), Closer: resp.Body}
	if resp.StatusCode >= 400 {
		t.failed = true
	}
}
func (t *detailTrace) errorAttempt(a *detailAttempt, err error) {
	if t == nil || err == nil {
		return
	}
	t.failed = true
	if a != nil {
		a.err = err.Error()
	}
}

func writeJSONValue(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
func bodyIsUTF8(f *os.File) (bool, error) {
	if f == nil {
		return true, nil
	}
	if _, err := f.Seek(0, 0); err != nil {
		return false, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		ch, n, err := r.ReadRune()
		if err == io.EOF {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if ch == utf8.RuneError && n == 1 {
			return false, nil
		}
	}
}

func writeBody(w *bufio.Writer, f *os.File, plain bool) error {
	if err := w.WriteByte('"'); err != nil {
		return err
	}
	if f != nil {
		if _, err := f.Seek(0, 0); err != nil {
			return err
		}
		if !plain {
			enc := base64.NewEncoder(base64.StdEncoding, w)
			if _, err := io.Copy(enc, f); err != nil {
				return err
			}
			if err := enc.Close(); err != nil {
				return err
			}
			return w.WriteByte('"')
		}
		r := bufio.NewReaderSize(f, 64<<10)
		for {
			ch, _, err := r.ReadRune()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			switch ch {
			case '"', '\\':
				if err = w.WriteByte('\\'); err == nil {
					_, err = w.WriteRune(ch)
				}
			case '\n':
				_, err = w.WriteString(`\n`)
			case '\r':
				_, err = w.WriteString(`\r`)
			case '\t':
				_, err = w.WriteString(`\t`)
			default:
				if ch < 32 {
					_, err = w.WriteString(fmt.Sprintf(`\u%04x`, ch))
				} else {
					_, err = w.WriteRune(ch)
				}
			}
			if err != nil {
				return err
			}
		}
	}
	return w.WriteByte('"')
}
func writePart(w *bufio.Writer, p detailPart) error {
	plain, err := bodyIsUTF8(p.body)
	if err != nil {
		return err
	}
	_, _ = w.WriteString(`{"method":`)
	if err := writeJSONValue(w, p.method); err != nil {
		return err
	}
	_, _ = w.WriteString(`,"url":`)
	if err := writeJSONValue(w, p.url); err != nil {
		return err
	}
	_, _ = w.WriteString(`,"status":`)
	if err := writeJSONValue(w, p.status); err != nil {
		return err
	}
	_, _ = w.WriteString(`,"headers":`)
	if err := writeJSONValue(w, p.headers); err != nil {
		return err
	}
	_, _ = w.WriteString(`,"body_encoding":`)
	encoding := "utf-8"
	if !plain {
		encoding = "base64"
	}
	if err := writeJSONValue(w, encoding); err != nil {
		return err
	}
	_, _ = w.WriteString(`,"body":`)
	if err := writeBody(w, p.body, plain); err != nil {
		return err
	}
	_, err = w.WriteString(`}`)
	return err
}
func (t *detailTrace) finish(row M) bool {
	if t.finished {
		return false
	}
	t.finished = true
	defer func() {
		files := []*os.File{t.request.body, t.response.body}
		for _, a := range t.attempts {
			files = append(files, a.request.body, a.response.body)
		}
		for _, f := range files {
			if f != nil {
				_ = f.Close()
			}
		}
		base, e1 := filepath.Abs(filepath.Join(t.app.cfg.DataDir, "request-details"))
		target, e2 := filepath.Abs(t.tempDir)
		if e1 == nil && e2 == nil && filepath.Dir(target) == base && strings.HasPrefix(filepath.Base(target), ".active-") {
			_ = os.RemoveAll(target)
		}
	}()
	if yes(row["ok"]) && !t.failed {
		return false
	}
	if t.writeErr != nil {
		log.Printf("detail log: %v", t.writeErr)
		return false
	}
	tmp, err := os.CreateTemp(filepath.Dir(t.tempDir), ".detail-")
	if err != nil {
		log.Printf("detail log: %v", err)
		return false
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	w := bufio.NewWriterSize(tmp, 64<<10)
	_, err = w.WriteString(`{"id":`)
	if err == nil {
		err = writeJSONValue(w, t.id)
	}
	if err == nil {
		_, err = w.WriteString(`,"ts":`)
	}
	if err == nil {
		_, err = w.WriteString(strconv.FormatFloat(t.ts, 'f', 3, 64))
	}
	if err == nil {
		_, err = w.WriteString(`,"request":`)
	}
	if err == nil {
		err = writePart(w, t.request)
	}
	if err == nil {
		_, err = w.WriteString(`,"response":`)
	}
	if err == nil {
		err = writePart(w, t.response)
	}
	if err == nil {
		_, err = w.WriteString(`,"attempts":[`)
	}
	for i, a := range t.attempts {
		if err != nil {
			break
		}
		if i > 0 {
			_, err = w.WriteString(`,`)
		}
		if err == nil {
			_, err = w.WriteString(`{"request":`)
		}
		if err == nil {
			err = writePart(w, a.request)
		}
		if err == nil {
			_, err = w.WriteString(`,"response":`)
		}
		if err == nil {
			err = writePart(w, a.response)
		}
		if err == nil {
			_, err = w.WriteString(`,"error":`)
		}
		if err == nil {
			err = writeJSONValue(w, a.err)
		}
		if err == nil {
			_, err = w.WriteString(`}`)
		}
	}
	if err == nil {
		_, err = w.WriteString(`]}`)
	}
	if err == nil {
		err = w.Flush()
	}
	if e := tmp.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Rename(tmpName, filepath.Join(filepath.Dir(t.tempDir), t.id+".json"))
	}
	if err != nil {
		log.Printf("detail log: %v", err)
		return false
	}
	return true
}
