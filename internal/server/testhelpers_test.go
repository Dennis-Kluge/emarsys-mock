package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dennis-kluge/emarsys-mock/internal/auth"
)

// envelope is the decoded Emarsys response, with data left raw so a test can
// assert on the exact JSON types production returns.
type envelope struct {
	Status    int
	ReplyCode int             `json:"replyCode"`
	ReplyText string          `json:"replyText"`
	Data      json.RawMessage `json:"data"`
}

// call makes an authenticated request against the full middleware stack.
func call(t *testing.T, handler http.Handler, method, target, body string) envelope {
	t.Helper()

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WSSE", auth.BuildHeader(
		"mock-api-user", "mock-secret", time.Now(), "a1b2c3d4e5f60718293a4b5c6d7e8f90"))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	env := envelope{Status: rec.Code}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s %s: response is not an envelope: %v (body %s)", method, target, err, rec.Body)
	}
	return env
}

// batchData is the payload of a contact batch response.
type batchData struct {
	IDs    []json.RawMessage            `json:"ids"`
	Errors map[string]map[string]string `json:"errors"`
}

func (e envelope) batch(t *testing.T) batchData {
	t.Helper()
	var d batchData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("data is not a batch payload: %v (data %s)", err, e.Data)
	}
	return d
}

// expectOK asserts the transport-level success that every batch returns, even
// when individual rows failed.
func (e envelope) expectOK(t *testing.T) envelope {
	t.Helper()
	if e.Status != http.StatusOK || e.ReplyCode != 0 {
		t.Fatalf("status/replyCode = %d/%d, want 200/0 (replyText %q)", e.Status, e.ReplyCode, e.ReplyText)
	}
	return e
}

// expectError asserts a hard error with the given reply code.
func (e envelope) expectError(t *testing.T, code int) envelope {
	t.Helper()
	if e.ReplyCode != code {
		t.Fatalf("replyCode = %d (%q), want %d", e.ReplyCode, e.ReplyText, code)
	}
	return e
}

// rowError returns the reply code recorded for one key value in data.errors.
func (d batchData) rowError(t *testing.T, keyValue string) string {
	t.Helper()
	entry, ok := d.Errors[keyValue]
	if !ok {
		t.Fatalf("no error recorded for %q; errors = %v", keyValue, d.Errors)
	}
	for code := range entry {
		return code
	}
	t.Fatalf("empty error entry for %q", keyValue)
	return ""
}

// createContact is the arrange step most tests start from.
func createContact(t *testing.T, handler http.Handler, body string) envelope {
	t.Helper()
	return call(t, handler, http.MethodPost, "/api/v2/contact", body).expectOK(t)
}

// newSignedRequest builds an authenticated request without sending it, for the
// tests that need to inspect response headers rather than the envelope.
func newSignedRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WSSE", auth.BuildHeader(
		"mock-api-user", "mock-secret", time.Now(), "a1b2c3d4e5f60718293a4b5c6d7e8f90"))
	return req
}

func recordRequest(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
