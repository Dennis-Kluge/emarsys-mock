package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dennis-kluge/emarsys-mock/internal/auth"
	"github.com/dennis-kluge/emarsys-mock/internal/config"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

func newTestServer(t *testing.T) (http.Handler, *store.DB) {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatalf("Europe/Vienna unavailable: %v", err)
	}

	cfg := config.Config{
		WSSESkew:            5 * time.Minute,
		OAuthTokenTTL:       time.Hour,
		OAuthSigningKey:     []byte("test-signing-key"),
		RateLimitPerMinute:  1000,
		MaxBatchContacts:    1000,
		MaxBodyBytes:        10 << 20,
		MaxContactBodyBytes: 8 << 20,
		RequestLogMax:       1000,
		ExportLocation:      loc,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, db, logger).Handler(), db
}

// wsseRequest builds a request signed with the seeded credentials.
func wsseRequest(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("X-WSSE", auth.BuildHeader(
		"mock-api-user", "mock-secret", time.Now(), "a1b2c3d4e5f60718293a4b5c6d7e8f90"))
	return req
}

func TestHealth(t *testing.T) {
	handler, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/_ctl/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var env struct {
		ReplyCode int `json:"replyCode"`
		Data      struct {
			Status string `json:"status"`
			Fields int    `json:"fields"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Status != "ok" || env.Data.Fields == 0 {
		t.Errorf("health payload = %+v", env.Data)
	}
}

func TestAPIRequiresAuthentication(t *testing.T) {
	handler, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v2/field", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"replyCode":1`) {
		t.Errorf("body = %s, want replyCode 1", rec.Body)
	}
}

func TestUnimplementedEndpointReturnsEnvelope(t *testing.T) {
	// A client hitting an endpoint the mock does not cover yet must be able to
	// tell that apart from a transport failure, so even a 404 is an envelope.
	handler, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, wsseRequest(t, http.MethodGet, "/api/v2/not-built-yet", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
	var env struct {
		ReplyText string `json:"replyText"`
		Data      any    `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(env.ReplyText, "not implemented") {
		t.Errorf("replyText = %q", env.ReplyText)
	}
	if env.Data != "" {
		t.Errorf("data = %v, want empty string", env.Data)
	}
}

func TestOAuthTokenUnlocksAPI(t *testing.T) {
	handler, _ := newTestServer(t)

	tokenReq := httptest.NewRequest(http.MethodPost, "/oauth2/token",
		strings.NewReader("grant_type=client_credentials"))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenReq.SetBasicAuth("mock-client", "mock-client-secret")
	tokenRec := httptest.NewRecorder()
	handler.ServeHTTP(tokenRec, tokenReq)

	if tokenRec.Code != http.StatusOK {
		t.Fatalf("token status = %d, want 200 (body %s)", tokenRec.Code, tokenRec.Body)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(tokenRec.Body.Bytes(), &token); err != nil {
		t.Fatalf("decode token: %v", err)
	}

	apiReq := httptest.NewRequest(http.MethodGet, "/api/v3/contacts", nil)
	apiReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
	apiRec := httptest.NewRecorder()
	handler.ServeHTTP(apiRec, apiReq)

	// The endpoint itself is not built yet; what matters here is that the token
	// got past authentication rather than being rejected with 401.
	if apiRec.Code == http.StatusUnauthorized {
		t.Fatalf("bearer token was rejected: %s", apiRec.Body)
	}
}

func TestRequestsAreLogged(t *testing.T) {
	handler, db := newTestServer(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, wsseRequest(t, http.MethodPost, "/api/v2/contact",
		strings.NewReader(`{"key_id":"3","contacts":[{"3":"jane@example.com"}]}`)))

	var (
		method, path, authUser, reqBody, respBody string
		status                                    int
		replyCode                                 *int
	)
	err := db.Read.QueryRow(
		`SELECT method, path, auth_user, http_status, reply_code, request_body, response_body
		 FROM request_log ORDER BY id DESC LIMIT 1`,
	).Scan(&method, &path, &authUser, &status, &replyCode, &reqBody, &respBody)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}

	if method != http.MethodPost || path != "/api/v2/contact" {
		t.Errorf("logged %s %s", method, path)
	}
	if authUser != "mock-api-user" {
		t.Errorf("auth_user = %q, want the resolved principal", authUser)
	}
	if !strings.Contains(reqBody, "jane@example.com") {
		t.Errorf("request_body = %q", reqBody)
	}
	if respBody == "" {
		t.Error("response_body was not captured")
	}
	if replyCode == nil {
		t.Error("reply_code was not captured")
	}
}

func TestUnauthenticatedRequestsAreLoggedToo(t *testing.T) {
	handler, db := newTestServer(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/field", nil))

	var status int
	var replyCode *int
	err := db.Read.QueryRow(
		`SELECT http_status, reply_code FROM request_log ORDER BY id DESC LIMIT 1`).
		Scan(&status, &replyCode)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Errorf("logged status = %d, want 401", status)
	}
	if replyCode == nil || *replyCode != 1 {
		t.Errorf("logged reply code = %v, want 1", replyCode)
	}
}

func TestHealthIsNotLogged(t *testing.T) {
	handler, db := newTestServer(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_ctl/health", nil))

	var n int
	if err := db.Read.QueryRow(`SELECT COUNT(*) FROM request_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("request_log has %d rows; probe traffic should not fill it", n)
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	handler, _ := newTestServer(t)

	req := wsseRequest(t, http.MethodPost, "/api/v2/contact", strings.NewReader("{}"))
	req.ContentLength = 9 << 20 // above the 8 MB contact limit, below the 10 MB general one
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %s)", rec.Code, rec.Body)
	}
}
