package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/dennis-kluge/emarsys-mock/internal/config"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// createFault installs a rule and returns its id.
func createFault(t *testing.T, handler http.Handler, body string) int64 {
	t.Helper()
	status, raw := ctlCall(t, handler, http.MethodPost, "/_ctl/faults", body)
	if status != http.StatusOK {
		t.Fatalf("create fault: status %d (body %s)", status, raw)
	}
	var rule store.FaultRule
	if err := json.Unmarshal(raw, &rule); err != nil {
		t.Fatalf("decode fault: %v (body %s)", err, raw)
	}
	return rule.ID
}

// TestNextRequestReturns429 is the case the dashboard puts behind a single
// button, and the reason fault injection is a core feature rather than an
// extra: without it a suite only ever exercises the happy path and the retry
// and backoff code is never run.
func TestNextRequestReturns429(t *testing.T) {
	handler, _ := newTestServer(t)

	createFault(t, handler, `{
		"match_path_pattern":"/api/*",
		"http_status":429,
		"reply_code":2011,
		"reply_text":"Rate limit exceeded",
		"remaining_hits":1
	}`)

	first := call(t, handler, http.MethodGet, "/api/v2/field", "")
	if first.Status != http.StatusTooManyRequests {
		t.Fatalf("first status = %d, want 429 (body %s)", first.Status, first.Data)
	}
	if first.ReplyText != "Rate limit exceeded" {
		t.Errorf("replyText = %q", first.ReplyText)
	}

	// The budget was one hit, so the rule has disabled itself.
	second := call(t, handler, http.MethodGet, "/api/v2/field", "").expectOK(t)
	if second.Status != http.StatusOK {
		t.Errorf("second status = %d, want 200", second.Status)
	}
}

func TestFaultMatching(t *testing.T) {
	cases := []struct {
		name        string
		rule        string
		method      string
		target      string
		body        string
		wantFaulted bool
	}{
		{
			name:        "path prefix",
			rule:        `{"match_path_pattern":"/api/v2/contact*","http_status":503}`,
			method:      http.MethodPost,
			target:      "/api/v2/contact",
			body:        `{"key_id":"3","contacts":[{"3":"a@example.com"}]}`,
			wantFaulted: true,
		},
		{
			name:        "path prefix misses another endpoint",
			rule:        `{"match_path_pattern":"/api/v2/contact*","http_status":503}`,
			method:      http.MethodGet,
			target:      "/api/v2/field",
			wantFaulted: false,
		},
		{
			name:        "method narrows the rule",
			rule:        `{"match_method":"PUT","match_path_pattern":"/api/*","http_status":503}`,
			method:      http.MethodGet,
			target:      "/api/v2/field",
			wantFaulted: false,
		},
		{
			name:        "body substring",
			rule:        `{"match_body_contains":"poison@example.com","http_status":503}`,
			method:      http.MethodPost,
			target:      "/api/v2/contact",
			body:        `{"key_id":"3","contacts":[{"3":"poison@example.com"}]}`,
			wantFaulted: true,
		},
		{
			name:        "body substring misses",
			rule:        `{"match_body_contains":"poison@example.com","http_status":503}`,
			method:      http.MethodPost,
			target:      "/api/v2/contact",
			body:        `{"key_id":"3","contacts":[{"3":"clean@example.com"}]}`,
			wantFaulted: false,
		},
		{
			name:        "wildcard in the middle",
			rule:        `{"match_path_pattern":"/api/*/contact","http_status":503}`,
			method:      http.MethodPost,
			target:      "/api/v2/contact",
			body:        `{"key_id":"3","contacts":[{"3":"a@example.com"}]}`,
			wantFaulted: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, _ := newTestServer(t)
			createFault(t, handler, tc.rule)

			env := call(t, handler, tc.method, tc.target, tc.body)
			faulted := env.Status == http.StatusServiceUnavailable
			if faulted != tc.wantFaulted {
				t.Errorf("status = %d, faulted = %v, want faulted = %v",
					env.Status, faulted, tc.wantFaulted)
			}
		})
	}
}

func TestFaultRulesLeaveTheRequestBodyIntact(t *testing.T) {
	// The fault middleware reads the body to match on it and has to put it
	// back, or a rule that does not fire silently empties every request.
	handler, _ := newTestServer(t)
	createFault(t, handler, `{"match_body_contains":"never-matches","http_status":503}`)

	data := call(t, handler, http.MethodPost, "/api/v2/contact",
		`{"key_id":"3","contacts":[{"3":"intact@example.com","1":"Jane"}]}`).expectOK(t).batch(t)
	if len(data.IDs) != 1 {
		t.Fatalf("ids = %v, want one -- the body did not reach the handler", data.IDs)
	}
	if got := readField(t, handler, "intact@example.com", 1); got != "Jane" {
		t.Errorf("first name = %q", got)
	}
}

func TestFaultRulesNeverBreakTheControlPlane(t *testing.T) {
	// A rule that could break /_ctl would make the mock unrecoverable: the
	// endpoint needed to delete the rule would be the one failing.
	handler, _ := newTestServer(t)
	id := createFault(t, handler, `{"http_status":500}`)

	if status, _ := ctlCall(t, handler, http.MethodGet, "/_ctl/faults", ""); status != http.StatusOK {
		t.Fatalf("control plane status = %d, want 200", status)
	}
	status, body := ctlCall(t, handler, http.MethodDelete,
		"/_ctl/faults/"+strconv.FormatInt(id, 10), "")
	if status != http.StatusOK {
		t.Fatalf("delete status = %d (body %s)", status, body)
	}
	call(t, handler, http.MethodGet, "/api/v2/field", "").expectOK(t)
}

func TestFaultRuleWithoutMatchersBreaksEverything(t *testing.T) {
	handler, _ := newTestServer(t)
	createFault(t, handler, `{"http_status":500,"reply_code":2011,"reply_text":"Internal error"}`)

	for _, target := range []string{"/api/v2/field", "/api/v2/event/"} {
		env := call(t, handler, http.MethodGet, target, "")
		if env.Status != http.StatusInternalServerError {
			t.Errorf("%s status = %d, want 500", target, env.Status)
		}
	}
}

func TestFaultedRequestsStillAppearInTheLog(t *testing.T) {
	// The log has to show what the client saw, otherwise a failing test cannot
	// be told apart from a broken mock.
	handler, _ := newTestServer(t)
	createFault(t, handler, `{"match_path_pattern":"/api/*","http_status":503,"remaining_hits":1}`)
	call(t, handler, http.MethodGet, "/api/v2/field", "")

	_, body := ctlCall(t, handler, http.MethodGet, "/_ctl/requests?path=/api/v2/field", "")
	var payload struct {
		Requests []store.RequestLogEntry `json:"requests"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Requests) == 0 {
		t.Fatal("faulted request was not logged")
	}
	if got := payload.Requests[0].HTTPStatus; got != http.StatusServiceUnavailable {
		t.Errorf("logged status = %d, want 503", got)
	}
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/api/v2/contact", "/api/v2/contact", true},
		{"/api/v2/contact", "/api/v2/contact/getdata", false},
		{"/api/*", "/api/v2/anything/at/all", true},
		{"/api/v2/contact*", "/api/v2/contact/getdata", true},
		{"/api/v2/contact*", "/api/v2/field", false},
		{"*getdata", "/api/v2/contact/getdata", true},
		{"/api/*/contact", "/api/v2/contact", true},
		{"/api/*/contact", "/api/v2/field", false},
		{"*", "/anything", true},
	}
	for _, tc := range cases {
		if got := matchPattern(tc.pattern, tc.path); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestRateLimitReturns429WithHeaders(t *testing.T) {
	// CI lowers the limit deliberately so retry and backoff logic gets
	// exercised without sending a thousand requests.
	handler, _, _ := newConfiguredTestServer(t, func(c *config.Config) {
		c.RateLimitPerMinute = 3
	})

	for i := range 3 {
		if env := call(t, handler, http.MethodGet, "/api/v2/field", ""); env.Status != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i+1, env.Status)
		}
	}

	req := newSignedRequest(t, http.MethodGet, "/api/v2/field", "")
	rec := recordRequest(handler, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body %s)", rec.Code, rec.Body)
	}
	for _, header := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After"} {
		if rec.Header().Get(header) == "" {
			t.Errorf("%s header is missing", header)
		}
	}
	if got := rec.Header().Get("X-RateLimit-Limit"); got != "3" {
		t.Errorf("X-RateLimit-Limit = %q, want 3", got)
	}
	if !strings.Contains(rec.Body.String(), "Rate limit exceeded") {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestRateLimitIsPerCaller(t *testing.T) {
	handler, _, _ := newConfiguredTestServer(t, func(c *config.Config) {
		c.RateLimitPerMinute = 2
	})

	for range 2 {
		call(t, handler, http.MethodGet, "/api/v2/field", "").expectOK(t)
	}
	if env := call(t, handler, http.MethodGet, "/api/v2/field", ""); env.Status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", env.Status)
	}

	// A different credential has its own budget. The bearer path is enough to
	// prove the bucket is the caller and not the process.
	req := newSignedRequest(t, http.MethodGet, "/api/v2/field", "")
	req.Header.Del("X-WSSE")
	req.Header.Set("Authorization", "Bearer some-other-caller")
	rec := recordRequest(handler, req)
	if rec.Code == http.StatusTooManyRequests {
		t.Error("a second caller shared the first caller's budget")
	}
}

func TestControlPlaneIsNotRateLimited(t *testing.T) {
	// A rate-limited control plane would lock a test out of the reset it needs
	// exactly when it has run into the limit.
	handler, _, _ := newConfiguredTestServer(t, func(c *config.Config) {
		c.RateLimitPerMinute = 1
	})
	call(t, handler, http.MethodGet, "/api/v2/field", "").expectOK(t)
	call(t, handler, http.MethodGet, "/api/v2/field", "")

	for range 5 {
		if status, body := ctlCall(t, handler, http.MethodGet, "/_ctl/faults", ""); status != http.StatusOK {
			t.Fatalf("control plane status = %d (body %s)", status, body)
		}
	}
}
