package record

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeUpstream stands in for the real API and reports what it received, so the
// test can prove the proxy forwarded faithfully.
type fakeUpstream struct {
	server    *httptest.Server
	gotAuth   string
	gotBody   string
	gotPath   string
	gotQuery  string
	gotMethod string
}

func newFakeUpstream(t *testing.T, respond func(w http.ResponseWriter)) *fakeUpstream {
	t.Helper()
	up := &fakeUpstream{}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		up.gotAuth = r.Header.Get("X-WSSE")
		up.gotBody = string(body)
		up.gotPath = r.URL.Path
		up.gotQuery = r.URL.RawQuery
		up.gotMethod = r.Method
		respond(w)
	}))
	t.Cleanup(up.server.Close)
	return up
}

func newTestProxy(t *testing.T, upstream string, mode Mode) (*Proxy, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recording.jsonl")
	writer, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })

	proxy, err := NewProxy(writer, ProxyOptions{
		Upstream: upstream,
		Mode:     mode,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return proxy, path
}

func readRecording(t *testing.T, path string) []Exchange {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	exchanges, err := Read(file)
	if err != nil {
		t.Fatal(err)
	}
	return exchanges
}

func TestProxyForwardsFaithfully(t *testing.T) {
	// The client must reach production unchanged. WSSE in particular has to
	// arrive verbatim -- the digest covers the nonce, the timestamp and the
	// secret, so the proxy hop is invisible to it, but only if the header is
	// passed through untouched.
	upstream := newFakeUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"replyCode":0,"replyText":"OK","data":{"ids":[1]}}`))
	})
	proxy, path := newTestProxy(t, upstream.server.URL, ModeShapes)

	const auth = `UsernameToken Username="u", PasswordDigest="d", Nonce="n", Created="c"`
	req := httptest.NewRequest(http.MethodPost, "/api/v2/contact?dry=1",
		strings.NewReader(`{"key_id":"3","contacts":[{"3":"ada@example.com"}]}`))
	req.Header.Set("X-WSSE", auth)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if upstream.gotAuth != auth {
		t.Errorf("the upstream saw a different X-WSSE: %q", upstream.gotAuth)
	}
	if !strings.Contains(upstream.gotBody, "ada@example.com") {
		t.Errorf("the upstream saw a modified body: %q", upstream.gotBody)
	}
	if upstream.gotPath != "/api/v2/contact" || upstream.gotQuery != "dry=1" {
		t.Errorf("upstream got %s?%s", upstream.gotPath, upstream.gotQuery)
	}
	if upstream.gotMethod != http.MethodPost {
		t.Errorf("upstream method = %s", upstream.gotMethod)
	}

	// The client sees the real response, unaltered.
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ids":[1]`) {
		t.Errorf("client got %d %s", rec.Code, rec.Body)
	}

	exchanges := readRecording(t, path)
	if len(exchanges) != 1 {
		t.Fatalf("recorded %d exchanges, want 1", len(exchanges))
	}
	got := exchanges[0]
	if got.Method != http.MethodPost || got.Path != "/api/v2/contact" || got.Status != 200 {
		t.Errorf("recorded %+v", got)
	}
	if got.Query["dry"] != "1" {
		t.Errorf("query was not recorded: %v", got.Query)
	}
	// And the recording itself carries neither the credential nor the address.
	rendered, _ := json.Marshal(got)
	for _, secret := range []string{"PasswordDigest=\\\"d\\\"", "ada@example.com"} {
		if strings.Contains(string(rendered), secret) {
			t.Errorf("the recording leaked %q: %s", secret, rendered)
		}
	}
}

func TestProxyRecordsFailuresToo(t *testing.T) {
	// A 400 from production is worth more than a 200: it documents the error
	// shape, which is the half of a contract nobody writes down.
	upstream := newFakeUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"replyCode":2004,"replyText":"Invalid key field id","data":""}`))
	})
	proxy, path := newTestProxy(t, upstream.server.URL, ModeShapes)

	req := httptest.NewRequest(http.MethodPost, "/api/v2/contact", strings.NewReader(`{"key_id":"email"}`))
	proxy.ServeHTTP(httptest.NewRecorder(), req)

	exchanges := readRecording(t, path)
	if len(exchanges) != 1 || exchanges[0].Status != http.StatusBadRequest {
		t.Fatalf("recorded %+v", exchanges)
	}
	if !strings.Contains(string(exchanges[0].ResponseBody), "Invalid key field id") {
		t.Errorf("the reply text was lost: %s", exchanges[0].ResponseBody)
	}
	if !strings.Contains(string(exchanges[0].ResponseBody), "2004") {
		t.Errorf("the reply code was lost: %s", exchanges[0].ResponseBody)
	}
}

func TestProxyRecordsAnUnreachableUpstream(t *testing.T) {
	// A failed call is still evidence, and the client needs a real error rather
	// than a hang.
	proxy, path := newTestProxy(t, "http://127.0.0.1:1", ModeShapes)

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/field", nil))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	exchanges := readRecording(t, path)
	if len(exchanges) != 1 || exchanges[0].Error == "" {
		t.Fatalf("the failure was not recorded: %+v", exchanges)
	}
}

func TestProxyRejectsAnUpstreamWithoutAHost(t *testing.T) {
	writer, err := NewWriter(filepath.Join(t.TempDir(), "r.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	if _, err := NewProxy(writer, ProxyOptions{Upstream: "api.emarsys.net"}); err == nil {
		t.Error("an upstream with no scheme was accepted; it would silently record nothing")
	}
}
