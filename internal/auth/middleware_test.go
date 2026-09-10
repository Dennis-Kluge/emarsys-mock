package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeDirectory keeps the middleware tests free of a database.
type fakeDirectory struct {
	users  map[string]*Principal
	nonces map[string]bool
}

func newFakeDirectory(p ...*Principal) *fakeDirectory {
	d := &fakeDirectory{users: map[string]*Principal{}, nonces: map[string]bool{}}
	for _, principal := range p {
		d.users[principal.Username] = principal
	}
	return d
}

func (d *fakeDirectory) ByUsername(_ context.Context, username string) (*Principal, error) {
	if p, ok := d.users[username]; ok {
		return p, nil
	}
	return nil, ErrUnknownPrincipal
}

func (d *fakeDirectory) ByClientID(_ context.Context, clientID string) (*Principal, error) {
	for _, p := range d.users {
		if p.ClientID == clientID {
			return p, nil
		}
	}
	return nil, ErrUnknownPrincipal
}

func (d *fakeDirectory) RememberNonce(_ context.Context, nonce string, _ time.Time) (bool, error) {
	if d.nonces[nonce] {
		return false, nil
	}
	d.nonces[nonce] = true
	return true, nil
}

var (
	testNow  = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	testUser = &Principal{
		Username:     "mock-api-user",
		Secret:       "mock-secret",
		ClientID:     "mock-client",
		ClientSecret: "mock-client-secret",
		Permissions:  []string{"*"},
	}
)

func newTestAuth(t *testing.T, dir Directory, mutate func(*Options)) *Authenticator {
	t.Helper()
	opts := Options{
		Skew:       5 * time.Minute,
		SigningKey: []byte("test-signing-key"),
		TokenTTL:   time.Hour,
		Now:        func() time.Time { return testNow },
	}
	if mutate != nil {
		mutate(&opts)
	}
	return New(dir, opts)
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"replyCode":0,"replyText":"OK","data":{}}`))
	})
}

func TestMiddlewareWSSE(t *testing.T) {
	a := newTestAuth(t, newFakeDirectory(testUser), nil)
	handler := a.Middleware(okHandler())

	cases := []struct {
		name       string
		header     string
		wantStatus int
	}{
		{
			name:       "valid credentials",
			header:     BuildHeader("mock-api-user", "mock-secret", testNow, "a1b2c3d4e5f60718293a4b5c6d7e8f90"),
			wantStatus: http.StatusOK,
		},
		{
			name:       "wrong secret",
			header:     BuildHeader("mock-api-user", "wrong-secret", testNow, "a1b2c3d4e5f60718293a4b5c6d7e8f90"),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "unknown user",
			header:     BuildHeader("nobody", "mock-secret", testNow, "a1b2c3d4e5f60718293a4b5c6d7e8f90"),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "stale created",
			header:     BuildHeader("mock-api-user", "mock-secret", testNow.Add(-time.Hour), "a1b2c3d4e5f60718293a4b5c6d7e8f90"),
			wantStatus: http.StatusUnauthorized,
		},
		{name: "no header", header: "", wantStatus: http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v2/field", nil)
			if tc.header != "" {
				req.Header.Set("X-WSSE", tc.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body)
			}
			if tc.wantStatus == http.StatusUnauthorized {
				assertReplyCode(t, rec.Body.Bytes(), 1)
			}
		})
	}
}

func TestMiddlewareRejectsNonceReuse(t *testing.T) {
	dir := newFakeDirectory(testUser)
	a := newTestAuth(t, dir, func(o *Options) { o.RejectNonceReuse = true })
	handler := a.Middleware(okHandler())
	header := BuildHeader("mock-api-user", "mock-secret", testNow, "a1b2c3d4e5f60718293a4b5c6d7e8f90")

	call := func() int {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/field", nil)
		req.Header.Set("X-WSSE", header)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := call(); got != http.StatusOK {
		t.Fatalf("first call status = %d, want 200", got)
	}
	if got := call(); got != http.StatusUnauthorized {
		t.Fatalf("replayed call status = %d, want 401", got)
	}
}

func TestMiddlewareAllowsNonceReuseByDefault(t *testing.T) {
	// A client that retries a failed request with the same nonce is realistic,
	// so replay rejection is opt-in.
	a := newTestAuth(t, newFakeDirectory(testUser), nil)
	handler := a.Middleware(okHandler())
	header := BuildHeader("mock-api-user", "mock-secret", testNow, "a1b2c3d4e5f60718293a4b5c6d7e8f90")

	for i := range 2 {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/field", nil)
		req.Header.Set("X-WSSE", header)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d status = %d, want 200", i, rec.Code)
		}
	}
}

func TestMiddlewarePermissions(t *testing.T) {
	limited := &Principal{
		Username:    "limited",
		Secret:      "s3cret",
		Permissions: []string{"/api/v2/contact*"},
	}
	a := newTestAuth(t, newFakeDirectory(limited), nil)
	handler := a.Middleware(okHandler())

	cases := []struct {
		path       string
		wantStatus int
	}{
		{"/api/v2/contact", http.StatusOK},
		{"/api/v2/contact/getdata", http.StatusOK},
		{"/api/v2/field", http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("X-WSSE", BuildHeader("limited", "s3cret", testNow, "a1b2c3d4e5f60718293a4b5c6d7e8f90"))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestPrincipalAllows(t *testing.T) {
	cases := []struct {
		name        string
		permissions []string
		path        string
		want        bool
	}{
		{"wildcard", []string{"*"}, "/api/v2/anything", true},
		{"prefix", []string{"/api/v2/contact*"}, "/api/v2/contact/getdata", true},
		{"prefix miss", []string{"/api/v2/contact*"}, "/api/v2/event", false},
		{"exact hit", []string{"/api/v2/field"}, "/api/v2/field", true},
		{"exact miss", []string{"/api/v2/field"}, "/api/v2/field/1/choice", false},
		{"none", nil, "/api/v2/field", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Principal{Permissions: tc.permissions}
			if got := p.Allows(tc.path); got != tc.want {
				t.Errorf("Allows(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestMiddlewareBearer(t *testing.T) {
	a := newTestAuth(t, newFakeDirectory(testUser), nil)
	handler := a.Middleware(okHandler())

	token, err := SignJWT(Claims{
		ClientID: "mock-client",
		Subject:  "mock-api-user",
		Expires:  testNow.Add(time.Hour).Unix(),
	}, []byte("test-signing-key"))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		token      string
		wantStatus int
	}{
		{"valid token", token, http.StatusOK},
		{"garbage token", "abc.def.ghi", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v3/contacts", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body)
			}
		})
	}
}

func TestTokenHandler(t *testing.T) {
	a := newTestAuth(t, newFakeDirectory(testUser), nil)
	handler := a.TokenHandler()

	t.Run("issues a usable token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/oauth2/token",
			formBody("grant_type=client_credentials"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth("mock-client", "mock-client-secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
		}
		var resp struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
			ExpiresIn   int    `json:"expires_in"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.TokenType != "Bearer" || resp.ExpiresIn != 3600 {
			t.Errorf("response = %+v", resp)
		}
		if _, err := ParseJWT(resp.AccessToken, []byte("test-signing-key"), testNow); err != nil {
			t.Errorf("issued token does not verify: %v", err)
		}
	})

	t.Run("rejects a wrong client secret", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/oauth2/token", nil)
		req.SetBasicAuth("mock-client", "nope")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("rejects an unsupported grant type", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/oauth2/token", formBody("grant_type=password"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth("mock-client", "mock-client-secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
}

func assertReplyCode(t *testing.T, body []byte, want int) {
	t.Helper()
	var env struct {
		ReplyCode int `json:"replyCode"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (body %s)", err, body)
	}
	if env.ReplyCode != want {
		t.Errorf("replyCode = %d, want %d", env.ReplyCode, want)
	}
}
