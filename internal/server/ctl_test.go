package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dennis-kluge/emarsys-mock/internal/config"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// ctlCall makes a control-plane request from the loopback address, which is
// what an unconfigured instance accepts.
func ctlCall(t *testing.T, handler http.Handler, method, target, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestControlPlaneIsLocalhostOnlyWithoutAToken(t *testing.T) {
	// An unconfigured mock exposed on a shared network must not be resettable
	// or reprogrammable by whoever finds it.
	handler, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/_ctl/reset", strings.NewReader("{}"))
	req.RemoteAddr = "10.1.2.3:4567"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "CTL_TOKEN") {
		t.Errorf("body = %s, want it to name the way out", rec.Body)
	}
}

func TestControlPlaneTokenOpensItUp(t *testing.T) {
	handler, _, _ := newConfiguredTestServer(t, func(c *config.Config) {
		c.CtlToken = "s3cret"
	})

	cases := []struct {
		name       string
		apply      func(*http.Request)
		wantStatus int
	}{
		{"no token", func(*http.Request) {}, http.StatusUnauthorized},
		{"wrong token", func(r *http.Request) { r.Header.Set("X-Ctl-Token", "nope") }, http.StatusUnauthorized},
		{"header token", func(r *http.Request) { r.Header.Set("X-Ctl-Token", "s3cret") }, http.StatusOK},
		{"bearer token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }, http.StatusOK},
		{"cookie token", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "ctl_token", Value: "s3cret"}) }, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/_ctl/reset", strings.NewReader("{}"))
			// A token must work from anywhere; that is the point of setting one.
			req.RemoteAddr = "10.1.2.3:4567"
			tc.apply(req)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body)
			}
		})
	}
}

func TestCtlResetRestoresTheSeedState(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"gone@example.com"}]}`)

	status, body := ctlCall(t, handler, http.MethodPost, "/_ctl/reset", "{}")
	if status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", status, body)
	}

	// The contact is gone and the seeded catalogue is back.
	data := call(t, handler, http.MethodPost, "/api/v2/contact/getdata",
		`{"keyId":3,"keyValues":["gone@example.com"],"fields":[1]}`).expectOK(t)
	if !strings.Contains(string(data.Data), "2008") {
		t.Errorf("contact survived the reset: %s", data.Data)
	}
	call(t, handler, http.MethodGet, "/api/v2/field", "").expectOK(t)
}

func TestCtlSeedLoadsAProductionFieldCatalogue(t *testing.T) {
	// The seed format matches a GET /v2/field response on purpose: a real
	// account's catalogue can be dumped and loaded here unchanged, which beats
	// the mock guessing at a customer's custom fields.
	handler, _ := newTestServer(t)

	status, body := ctlCall(t, handler, http.MethodPost, "/_ctl/seed", `{
		"fields":[
			{"id":45,"name":"Loyalty tier","application_type":"singlechoice","string_id":"loyalty_tier",
			 "choices":[{"id":1,"choice":"Silver"},{"id":2,"choice":"Gold"}]},
			{"id":46,"name":"Customer number","application_type":"shorttext","indexed":true}
		],
		"events":[{"name":"tier_upgraded"}],
		"contact_lists":[{"name":"Gold members"}],
		"contacts":[{"3":"seeded@example.com","45":"2","46":"C-9"}]
	}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", status, body)
	}

	var result struct {
		Created map[string]int `json:"created"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	if result.Created["fields"] != 2 || result.Created["contacts"] != 1 {
		t.Errorf("created = %v", result.Created)
	}

	// The seeded field validates like any other: 2 is a defined choice, 3 is not.
	data := call(t, handler, http.MethodPut, "/api/v2/contact/?create_if_not_exists=1",
		`{"key_id":"3","contacts":[{"3":"tier@example.com","45":"3"}]}`).expectOK(t).batch(t)
	if code := data.rowError(t, "tier@example.com"); code != "2006" {
		t.Errorf("error code = %s, want 2006 for an undefined choice", code)
	}

	// The indexed flag came across, so the field is queryable.
	call(t, handler, http.MethodGet, "/api/v2/contact/query/?46=C-9&return=3", "").expectOK(t)
}

func TestCtlRequestsBackAssertions(t *testing.T) {
	// This is how a test asserts what an integration actually called, rather
	// than trusting that it did.
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"logged@example.com"}]}`)

	status, body := ctlCall(t, handler, http.MethodGet,
		"/_ctl/requests?path=/api/v2/contact&method=POST", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", status, body)
	}

	var payload struct {
		Requests []store.RequestLogEntry `json:"requests"`
		Count    int                     `json:"count"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	if payload.Count == 0 {
		t.Fatal("no requests recorded")
	}
	last := payload.Requests[len(payload.Requests)-1]
	if !strings.Contains(last.RequestBody, "logged@example.com") {
		t.Errorf("request body = %q", last.RequestBody)
	}
	if last.AuthUser != "mock-api-user" {
		t.Errorf("auth user = %q", last.AuthUser)
	}
}

func TestCtlTriggersAreQueryable(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"ct@example.com"}]}`)
	call(t, handler, http.MethodPost, "/api/v2/event/1/trigger",
		`{"key_id":"3","contacts":[{"external_id":"ct@example.com"}],"data":{"sku":"A-1"}}`).expectOK(t)

	status, body := ctlCall(t, handler, http.MethodGet, "/_ctl/events/triggers", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", status, body)
	}

	var payload struct {
		Triggers []store.EventTrigger `json:"triggers"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Triggers) != 1 {
		t.Fatalf("triggers = %v, want one", payload.Triggers)
	}
	got := payload.Triggers[0]
	if got.ExternalID != "ct@example.com" || !strings.Contains(got.Payload, "sku") {
		t.Errorf("trigger = %+v", got)
	}
}

func TestCtlExportStatusCanBeForced(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"forced@example.com","1":"Jane"}]}`)
	id := startExport(t, handler, `{"contact_fields":[1,3],"add_field_names_header":1}`)

	status, body := ctlCall(t, handler, http.MethodPost,
		"/_ctl/exports/"+id+"/status", `{"status":"done"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d (body %s)", status, body)
	}
	if got := pollExport(t, handler, id).Status; got != "done" {
		t.Errorf("status = %q, want done without polling", got)
	}

	t.Run("rejects an invented status", func(t *testing.T) {
		status, _ := ctlCall(t, handler, http.MethodPost,
			"/_ctl/exports/"+id+"/status", `{"status":"in_progress"}`)
		if status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 for a slug that is not the literal", status)
		}
	})
}

func TestReadOnlyModeBlocksControlPlaneWrites(t *testing.T) {
	handler, _, _ := newConfiguredTestServer(t, func(c *config.Config) {
		c.ReadOnly = true
	})

	writes := []struct{ method, target, body string }{
		{http.MethodPost, "/_ctl/reset", "{}"},
		{http.MethodPost, "/_ctl/seed", `{"events":[{"name":"x"}]}`},
		{http.MethodPost, "/_ctl/faults", `{"http_status":429}`},
	}
	for _, tc := range writes {
		t.Run(tc.target, func(t *testing.T) {
			status, body := ctlCall(t, handler, tc.method, tc.target, tc.body)
			if status != http.StatusForbidden {
				t.Errorf("status = %d, want 403 (body %s)", status, body)
			}
		})
	}

	t.Run("reads still work", func(t *testing.T) {
		if status, _ := ctlCall(t, handler, http.MethodGet, "/_ctl/faults", ""); status != http.StatusOK {
			t.Errorf("status = %d, want 200", status)
		}
	})

	t.Run("the Emarsys surface is unaffected", func(t *testing.T) {
		// Read-only guards our own surface. Blocking contact writes too would
		// make the mock useless for the tests it exists to serve.
		createContact(t, handler, `{"key_id":"3","contacts":[{"3":"ro@example.com"}]}`)
	})
}
