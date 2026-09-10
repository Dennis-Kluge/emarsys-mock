package httpx

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

type memorySink struct {
	mu      sync.Mutex
	entries []store.RequestLogEntry
}

func (s *memorySink) AppendRequestLog(_ context.Context, e store.RequestLogEntry, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	return nil
}

func (s *memorySink) last(t *testing.T) store.RequestLogEntry {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == 0 {
		t.Fatal("no request was logged")
	}
	return s.entries[len(s.entries)-1]
}

// TestLoggingRestoresRequestBody is the regression test for the subtle failure
// mode of body-capturing middleware: it reads the body to log it and then hands
// the handler an empty one.
func TestLoggingRestoresRequestBody(t *testing.T) {
	sink := &memorySink{}
	const body = `{"key_id":"3","contacts":[{"3":"jane@example.com"}]}`

	var seenByHandler string
	handler := Logging(sink, LogOptions{Max: 100})(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("handler could not read body: %v", err)
			}
			seenByHandler = string(raw)
			api.OK(w, map[string]any{"ids": []int{1}})
		}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v2/contact", strings.NewReader(body)))

	if seenByHandler != body {
		t.Errorf("handler saw %q, want %q", seenByHandler, body)
	}
	entry := sink.last(t)
	if entry.RequestBody != body {
		t.Errorf("logged request body = %q", entry.RequestBody)
	}
	if !strings.Contains(entry.ResponseBody, `"ids":[1]`) {
		t.Errorf("logged response body = %q", entry.ResponseBody)
	}
	if entry.ReplyCode == nil || *entry.ReplyCode != 0 {
		t.Errorf("logged reply code = %v, want 0", entry.ReplyCode)
	}
	if entry.HTTPStatus != http.StatusOK {
		t.Errorf("logged status = %d", entry.HTTPStatus)
	}
}

func TestLoggingTruncatesLargeBodies(t *testing.T) {
	sink := &memorySink{}
	handler := Logging(sink, LogOptions{Max: 100, MaxBodyBytes: 16})(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v2/contact",
		strings.NewReader(strings.Repeat("x", 1000))))

	entry := sink.last(t)
	if !strings.HasSuffix(entry.RequestBody, "(truncated)") {
		t.Errorf("logged request body = %q, want a truncation marker", entry.RequestBody)
	}
	if len(entry.ResponseBody) > 16 {
		t.Errorf("logged response body is %d bytes, want at most 16", len(entry.ResponseBody))
	}
}

func TestLoggingSkipsConfiguredPrefixes(t *testing.T) {
	sink := &memorySink{}
	handler := Logging(sink, LogOptions{
		Max:          100,
		SkipPrefixes: []string{"/_ctl/health"},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_ctl/health", nil))

	if len(sink.entries) != 0 {
		t.Errorf("logged %d entries for a skipped path", len(sink.entries))
	}
}

func TestLoggingRecordsAuthUser(t *testing.T) {
	sink := &memorySink{}
	handler := Logging(sink, LogOptions{Max: 100})(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if rec, ok := w.(AuthUserRecorder); ok {
				rec.RecordAuthUser("mock-api-user")
			}
			w.WriteHeader(http.StatusOK)
		}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v2/field", nil))

	if got := sink.last(t).AuthUser; got != "mock-api-user" {
		t.Errorf("logged auth user = %q", got)
	}
}

func TestRecoverProducesInternalErrorEnvelope(t *testing.T) {
	// A mock that drops the connection on a bug is much harder to diagnose from
	// the client side than one that answers with replyCode 2011.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/field", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"replyCode":2011`) {
		t.Errorf("body = %s, want replyCode 2011", rec.Body)
	}
}

func TestLimitBodyUsesTheContactLimitOnContactPaths(t *testing.T) {
	handler := LimitBody(10<<20, 8<<20)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	cases := []struct {
		name          string
		path          string
		contentLength int64
		wantStatus    int
	}{
		{"contact batch under the limit", "/api/v2/contact", 7 << 20, http.StatusOK},
		{"contact batch over the contact limit", "/api/v2/contact", 9 << 20, http.StatusRequestEntityTooLarge},
		{"v3 contacts over the contact limit", "/api/v3/contacts", 9 << 20, http.StatusRequestEntityTooLarge},
		{"other endpoint under the general limit", "/api/v2/field", 9 << 20, http.StatusOK},
		{"other endpoint over the general limit", "/api/v2/field", 11 << 20, http.StatusRequestEntityTooLarge},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader("{}"))
			req.ContentLength = tc.contentLength
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}
