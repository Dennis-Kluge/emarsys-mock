// Package httpx holds transport-level middleware: request logging, body limits
// and panic recovery.
package httpx

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// LogSink receives finished request records.
type LogSink interface {
	AppendRequestLog(ctx context.Context, e store.RequestLogEntry, max int) error
}

type LogOptions struct {
	Max int
	// MaxBodyBytes caps how much of each body is kept. Bodies are stored for
	// debugging, not for replay, so truncating a 5 MB import is fine.
	MaxBodyBytes int
	// SkipPrefixes are paths that are not worth recording, such as the
	// dashboard's own polling.
	SkipPrefixes []string
	Now          func() time.Time
}

// recorder captures the status, reply code and response body of a handler, and
// lets middleware annotate the entry with the authenticated user.
type recorder struct {
	http.ResponseWriter
	status    int
	replyCode *int
	authUser  string
	body      *bytes.Buffer
	limit     int
}

func (rec *recorder) WriteHeader(status int) {
	if rec.status == 0 {
		rec.status = status
	}
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *recorder) Write(p []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	if rec.body != nil && rec.body.Len() < rec.limit {
		room := rec.limit - rec.body.Len()
		if room > len(p) {
			room = len(p)
		}
		rec.body.Write(p[:room])
	}
	return rec.ResponseWriter.Write(p)
}

// RecordReplyCode satisfies api.ReplyCodeRecorder.
func (rec *recorder) RecordReplyCode(code api.ReplyCode) {
	c := int(code)
	rec.replyCode = &c
}

// RecordAuthUser satisfies AuthUserRecorder.
func (rec *recorder) RecordAuthUser(user string) { rec.authUser = user }

// AuthUserRecorder lets the authentication middleware name the principal it
// resolved, so the log shows who called rather than who claimed to.
type AuthUserRecorder interface {
	RecordAuthUser(string)
}

// Logging records every request and response into the sink.
func Logging(sink LogSink, opts LogOptions) func(http.Handler) http.Handler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 64 << 10
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range opts.SkipPrefixes {
				if strings.HasPrefix(r.URL.Path, p) {
					next.ServeHTTP(w, r)
					return
				}
			}

			start := opts.Now()
			reqBody := drainBody(r, opts.MaxBodyBytes)

			rec := &recorder{
				ResponseWriter: w,
				body:           &bytes.Buffer{},
				limit:          opts.MaxBodyBytes,
			}
			next.ServeHTTP(rec, r)

			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			entry := store.RequestLogEntry{
				TS:           start,
				Method:       r.Method,
				Path:         r.URL.Path,
				Query:        r.URL.RawQuery,
				AuthUser:     rec.authUser,
				HTTPStatus:   rec.status,
				ReplyCode:    rec.replyCode,
				RequestBody:  reqBody,
				ResponseBody: rec.body.String(),
				DurationMS:   opts.Now().Sub(start).Milliseconds(),
			}
			// A logging failure must never turn a good response into a bad one;
			// the response has already been written by this point anyway.
			_ = sink.AppendRequestLog(context.WithoutCancel(r.Context()), entry, opts.Max)
		})
	}
}

// drainBody reads the request body so it can be logged and puts it back for the
// handler.
func drainBody(r *http.Request, limit int) string {
	if r.Body == nil {
		return ""
	}
	raw, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(nil))
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if len(raw) > limit {
		return string(raw[:limit]) + "…(truncated)"
	}
	return string(raw)
}
