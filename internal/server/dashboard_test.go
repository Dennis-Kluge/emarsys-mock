package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/dennis-kluge/emarsys-mock/internal/config"
)

// adminGet fetches a dashboard page from the loopback address.
func adminGet(t *testing.T, handler http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// adminPost submits a dashboard form and follows nothing: the handlers redirect,
// and the redirect target carries the flash message a test wants to read.
func adminPost(t *testing.T, handler http.Handler, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// flashOf reads the message a handler attached to its redirect target.
func flashOf(t *testing.T, rec *httptest.ResponseRecorder) (message, kind string) {
	t.Helper()
	location := rec.Header().Get("Location")
	if location == "" {
		t.Fatalf("no redirect (status %d, body %s)", rec.Code, rec.Body)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("bad redirect target %q: %v", location, err)
	}
	return parsed.Query().Get("msg"), parsed.Query().Get("kind")
}

func TestEveryDashboardViewRenders(t *testing.T) {
	// A template that only fails on a populated page is a template that fails
	// in front of whoever is debugging something, so the fixture covers each
	// view with real rows rather than an empty state.
	handler, _ := newTestServer(t)
	createContact(t, handler,
		`{"key_id":"3","contacts":[{"3":"dash@example.com","1":"Jane","2":"Doe","31":"1"}]}`)
	call(t, handler, http.MethodPut, "/api/v2/contact/",
		`{"key_id":"3","contacts":[{"3":"dash@example.com","31":"2"}]}`).expectOK(t)
	call(t, handler, http.MethodPost, "/api/v2/event/1/trigger",
		`{"key_id":"3","contacts":[{"external_id":"dash@example.com","trigger_id":"d-1"}],"data":{"sku":"A-1"}}`).expectOK(t)
	startExport(t, handler, `{"contact_fields":[1,3],"add_field_names_header":1}`)
	createFault(t, handler, `{"match_path_pattern":"/api/v2/never*","http_status":503}`)
	adminPost(t, handler, "/admin/segments", url.Values{"name": {"VIPs"}})

	views := []string{
		"/admin/requests",
		"/admin/requests?path=/api/v2/contact&method=POST&status=200&reply_code=0",
		"/admin/contacts",
		"/admin/contacts?q=dash@example.com",
		"/admin/contacts/1",
		"/admin/fields",
		"/admin/lists",
		"/admin/events",
		"/admin/exports",
		"/admin/faults",
		"/admin/danger",
	}
	for _, view := range views {
		t.Run(view, func(t *testing.T) {
			rec := adminGet(t, handler, view)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (body %s)", rec.Code, rec.Body)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "<html") || !strings.Contains(body, "emarsys") {
				t.Errorf("response does not look like a page: %.200s", body)
			}
			// An unresolved action or a nil pointer surfaces as a Go error
			// string embedded in the HTML rather than as a failed request.
			for _, marker := range []string{"<no value>", "ZgotmplZ", "template:"} {
				if strings.Contains(body, marker) {
					t.Errorf("template produced %q in the output", marker)
				}
			}
		})
	}
}

func TestDashboardRootRedirectsToTheRequestLog(t *testing.T) {
	// The request log is where anyone debugging an integration ends up.
	handler, _ := newTestServer(t)
	rec := adminGet(t, handler, "/admin/")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/admin/requests" {
		t.Errorf("Location = %q", got)
	}
}

func TestDashboardStaticAssetsAreEmbedded(t *testing.T) {
	// Vendored, not from a CDN: the single-file deployment story has to survive
	// contact with the UI.
	handler, _ := newTestServer(t)

	for _, asset := range []string{"/admin/static/app.css", "/admin/static/htmx.min.js"} {
		rec := adminGet(t, handler, asset)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d", asset, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s is empty", asset)
		}
	}
}

func TestDashboardShowsFieldIDsNextToNames(t *testing.T) {
	// The id is what an integration sends and what an error message names, so
	// it has to be visible without a detour through the field catalogue.
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"ids@example.com"}]}`)

	body := adminGet(t, handler, "/admin/contacts").Body.String()
	if !strings.Contains(body, `<span class="field-id">3</span>`) {
		t.Error("contact list does not show the e-mail field id")
	}
	if !strings.Contains(body, `<span class="field-id">31</span>`) {
		t.Error("contact list does not show the opt-in field id")
	}
}

func TestDashboardContactEditWritesThroughTheSameValidation(t *testing.T) {
	// An edit the API would have rejected must be rejected here too, or the
	// dashboard can put the mock into a state production could not produce.
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"edit@example.com","31":"1"}]}`)

	t.Run("rejects an undefined choice", func(t *testing.T) {
		rec := adminPost(t, handler, "/admin/contacts/1/field",
			url.Values{"field_id": {"31"}, "value": {"true"}})
		message, kind := flashOf(t, rec)
		if kind != "error" {
			t.Fatalf("kind = %q, message = %q; want an error", kind, message)
		}
		if got := readField(t, handler, "edit@example.com", 31); got != "1" {
			t.Errorf("opt-in = %q, want it unchanged", got)
		}
	})

	t.Run("rejects a malformed date", func(t *testing.T) {
		rec := adminPost(t, handler, "/admin/contacts/1/field",
			url.Values{"field_id": {"4"}, "value": {"31.01.1990"}})
		if _, kind := flashOf(t, rec); kind != "error" {
			t.Error("a German-ordered date was accepted")
		}
	})

	t.Run("accepts a valid value and records the change", func(t *testing.T) {
		adminPost(t, handler, "/admin/contacts/1/field",
			url.Values{"field_id": {"31"}, "value": {"2"}})
		if got := readField(t, handler, "edit@example.com", 31); got != "2" {
			t.Errorf("opt-in = %q, want 2", got)
		}

		body := adminGet(t, handler, "/admin/contacts/1").Body.String()
		if !strings.Contains(body, "dashboard") {
			t.Error("the history does not record the dashboard as the source")
		}
	})

	t.Run("clearing a value is spelled out", func(t *testing.T) {
		rec := adminPost(t, handler, "/admin/contacts/1/field",
			url.Values{"field_id": {"31"}, "value": {""}})
		message, kind := flashOf(t, rec)
		if kind == "error" {
			t.Fatalf("clearing was refused: %q", message)
		}
		if !strings.Contains(message, "Cleared") {
			t.Errorf("message = %q, want it to say the field was cleared", message)
		}
		if got := readField(t, handler, "edit@example.com", 31); got != "" {
			t.Errorf("opt-in = %q, want it cleared", got)
		}
	})
}

func TestDashboardFieldManagement(t *testing.T) {
	handler, _ := newTestServer(t)

	rec := adminPost(t, handler, "/admin/fields", url.Values{
		"name":             {"Loyalty tier"},
		"application_type": {"shorttext"},
		"indexed":          {"1"},
	})
	message, kind := flashOf(t, rec)
	if kind == "error" {
		t.Fatalf("create failed: %q", message)
	}
	if !strings.Contains(message, "id") {
		t.Errorf("message = %q, want it to name the new field id", message)
	}

	t.Run("system fields are protected", func(t *testing.T) {
		rec := adminPost(t, handler, "/admin/fields/3/delete", nil)
		if _, kind := flashOf(t, rec); kind != "error" {
			t.Error("the e-mail field was deletable from the dashboard")
		}
	})

	t.Run("the index flag toggles queryability", func(t *testing.T) {
		createContact(t, handler, `{"key_id":"3","contacts":[{"3":"idx@example.com","1":"Jane"}]}`)
		call(t, handler, http.MethodGet, "/api/v2/contact/query/?1=Jane&return=3", "").
			expectError(t, 2015)

		adminPost(t, handler, "/admin/fields/1/index", url.Values{"indexed": {"1"}})
		call(t, handler, http.MethodGet, "/api/v2/contact/query/?1=Jane&return=3", "").expectOK(t)
	})

	t.Run("choices can be added", func(t *testing.T) {
		adminPost(t, handler, "/admin/fields/31/choices",
			url.Values{"choice_id": {"3"}, "label": {"Pending"}})
		// The new choice is immediately valid on the API side.
		createContact(t, handler, `{"key_id":"3","contacts":[{"3":"pending@example.com","31":"3"}]}`)
		if got := readField(t, handler, "pending@example.com", 31); got != "3" {
			t.Errorf("opt-in = %q, want the newly defined choice", got)
		}
	})
}

func TestDashboardListAndSegmentMembership(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"member@example.com"}]}`)

	t.Run("a contact can be addressed by e-mail", func(t *testing.T) {
		rec := adminPost(t, handler, "/admin/lists/1/members",
			url.Values{"contact": {"member@example.com"}, "action": {"add"}})
		if message, kind := flashOf(t, rec); kind == "error" {
			t.Fatalf("add failed: %q", message)
		}
		body := adminGet(t, handler, "/admin/lists").Body.String()
		if !strings.Contains(body, "Newsletter") {
			t.Error("the seeded list is missing from the view")
		}
	})

	t.Run("an unknown reference is refused", func(t *testing.T) {
		rec := adminPost(t, handler, "/admin/lists/1/members",
			url.Values{"contact": {"nobody@example.com"}, "action": {"add"}})
		if _, kind := flashOf(t, rec); kind != "error" {
			t.Error("an unknown contact was silently accepted")
		}
	})

	t.Run("segments are managed the same way", func(t *testing.T) {
		rec := adminPost(t, handler, "/admin/segments", url.Values{"name": {"VIPs"}})
		message, kind := flashOf(t, rec)
		if kind == "error" {
			t.Fatalf("create failed: %q", message)
		}
		// The message has to say what a segment is here, because a static
		// member list is not what the name suggests.
		if !strings.Contains(message, "criteria") {
			t.Errorf("message = %q, want it to say membership is manual", message)
		}
		adminPost(t, handler, "/admin/segments/1/members",
			url.Values{"contact": {"member@example.com"}, "action": {"add"}})
		call(t, handler, http.MethodGet, "/api/v2/filter/1/contacts/count", "").expectOK(t)
	})
}

func TestDashboardQuickFaultButton(t *testing.T) {
	handler, _ := newTestServer(t)

	rec := adminPost(t, handler, "/admin/faults/quick", url.Values{"preset": {"429"}})
	if message, kind := flashOf(t, rec); kind == "error" {
		t.Fatalf("quick fault failed: %q", message)
	}

	if env := call(t, handler, http.MethodGet, "/api/v2/field", ""); env.Status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", env.Status)
	}
	// It fires once and retires itself.
	call(t, handler, http.MethodGet, "/api/v2/field", "").expectOK(t)
}

func TestDashboardExportDownload(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"dl@example.com","1":"Jane"}]}`)
	id := startExport(t, handler, `{"contact_fields":[1,3],"add_field_names_header":1}`)

	t.Run("an unfinished export cannot be downloaded", func(t *testing.T) {
		rec := adminPost(t, handler, "/admin/exports/"+id+"/download", nil)
		_ = rec
		got := adminGet(t, handler, "/admin/exports/"+id+"/download")
		if got.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want a redirect with an explanation", got.Code)
		}
	})

	t.Run("forcing done makes it downloadable", func(t *testing.T) {
		adminPost(t, handler, "/admin/exports/"+id+"/status", url.Values{"status": {"done"}})

		rec := adminGet(t, handler, "/admin/exports/"+id+"/download")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (body %s)", rec.Code, rec.Body)
		}
		if disposition := rec.Header().Get("Content-Disposition"); !strings.Contains(disposition, ".csv") {
			t.Errorf("Content-Disposition = %q", disposition)
		}
		if !strings.Contains(rec.Body.String(), "dl@example.com") {
			t.Errorf("csv = %q", rec.Body)
		}
	})
}

func TestDashboardDangerZone(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"doomed@example.com"}]}`)

	t.Run("seed reports what it loaded", func(t *testing.T) {
		rec := adminPost(t, handler, "/admin/danger/seed", url.Values{
			"payload": {`{"fields":[{"id":45,"name":"Loyalty tier","application_type":"shorttext"}],
			              "events":[{"name":"tier_upgraded"}]}`},
		})
		message, kind := flashOf(t, rec)
		if kind == "error" {
			t.Fatalf("seed failed: %q", message)
		}
		if !strings.Contains(message, "1 fields") || !strings.Contains(message, "1 events") {
			t.Errorf("message = %q, want a count of what was loaded", message)
		}
	})

	t.Run("malformed JSON is reported, not swallowed", func(t *testing.T) {
		rec := adminPost(t, handler, "/admin/danger/seed", url.Values{"payload": {`{"fields":`}})
		if _, kind := flashOf(t, rec); kind != "error" {
			t.Error("invalid JSON was accepted")
		}
	})

	t.Run("reset clears everything", func(t *testing.T) {
		adminPost(t, handler, "/admin/danger/reset", nil)
		body := adminGet(t, handler, "/admin/contacts").Body.String()
		if strings.Contains(body, "doomed@example.com") {
			t.Error("a contact survived the reset")
		}
	})
}

func TestReadOnlyDashboardHidesAndBlocksWrites(t *testing.T) {
	handler, _, _ := newConfiguredTestServer(t, func(c *config.Config) {
		c.ReadOnly = true
	})

	t.Run("the flag is visible", func(t *testing.T) {
		body := adminGet(t, handler, "/admin/requests").Body.String()
		if !strings.Contains(body, "read-only") {
			t.Error("nothing on the page says the instance is read-only")
		}
	})

	t.Run("write controls are not rendered", func(t *testing.T) {
		for _, view := range []string{"/admin/fields", "/admin/lists", "/admin/faults", "/admin/danger"} {
			body := adminGet(t, handler, view).Body.String()
			if strings.Contains(body, `type="submit"`) {
				t.Errorf("%s still renders a submit button", view)
			}
		}
	})

	t.Run("writes are blocked server-side too", func(t *testing.T) {
		// Hiding a button is presentation. The refusal has to be real.
		posts := []struct {
			target string
			form   url.Values
		}{
			{"/admin/fields", url.Values{"name": {"X"}, "application_type": {"shorttext"}}},
			{"/admin/lists", url.Values{"name": {"X"}}},
			{"/admin/events", url.Values{"name": {"X"}}},
			{"/admin/faults/quick", nil},
			{"/admin/danger/reset", nil},
		}
		for _, tc := range posts {
			rec := adminPost(t, handler, tc.target, tc.form)
			message, kind := flashOf(t, rec)
			if kind != "error" || !strings.Contains(message, "read-only") {
				t.Errorf("%s: message = %q, kind = %q; want a read-only refusal",
					tc.target, message, kind)
			}
		}
	})
}

func TestDashboardIsGuardedLikeTheControlPlane(t *testing.T) {
	// The dashboard shows and edits contact data, so it must not be laxer than
	// /_ctl about who may reach it.
	handler, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/contacts", nil)
	req.RemoteAddr = "10.1.2.3:4567"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 from a remote address", rec.Code)
	}
}

func TestDashboardTokenAuth(t *testing.T) {
	handler, _, _ := newConfiguredTestServer(t, func(c *config.Config) {
		c.CtlToken = "s3cret"
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/contacts", nil)
	req.RemoteAddr = "10.1.2.3:4567"
	req.AddCookie(&http.Cookie{Name: "ctl_token", Value: "s3cret"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a valid cookie (body %s)", rec.Code, rec.Body)
	}
}

func TestDashboardTrafficDoesNotFloodTheRequestLog(t *testing.T) {
	// The log is the debugging view; filling it with the dashboard's own page
	// loads would make it useless.
	handler, db := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"quiet@example.com"}]}`)

	for range 5 {
		adminGet(t, handler, "/admin/requests")
		adminGet(t, handler, "/admin/contacts")
	}

	var logged int
	if err := db.Read.QueryRow(
		`SELECT COUNT(*) FROM request_log WHERE path LIKE '/admin%'`).Scan(&logged); err != nil {
		t.Fatal(err)
	}
	if logged != 0 {
		t.Errorf("%d dashboard requests ended up in the log", logged)
	}
	_ = strconv.Itoa(logged)
}

func TestRequestLogLiveMode(t *testing.T) {
	// The request log is the view anyone debugging an integration keeps open,
	// so it can tail rather than needing a reload. Only the table refreshes:
	// swapping the whole page would steal focus from the filter inputs every
	// few seconds.
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"live@example.com"}]}`)

	t.Run("live mode arms the poll", func(t *testing.T) {
		body := adminGet(t, handler, "/admin/requests?live=1").Body.String()
		// The poll URL has to carry live=1 itself. Without it the first swap
		// returns a table with no trigger on it and the tail stops after one
		// refresh -- a failure that looks exactly like nothing happening.
		if !strings.Contains(body, `hx-get="/admin/requests/table?live=1"`) {
			t.Errorf("poll URL does not keep live mode on; page = %s",
				betweenMarkers(body, "hx-get=", ">"))
		}
		if !strings.Contains(body, `hx-trigger="every 3s"`) {
			t.Error("live page has no refresh trigger")
		}
	})

	t.Run("the refreshed table stays live", func(t *testing.T) {
		body := adminGet(t, handler, "/admin/requests/table?live=1").Body.String()
		if !strings.Contains(body, "hx-trigger") {
			t.Error("the swapped-in table carries no trigger, so the tail stops after one refresh")
		}
	})

	t.Run("it is off by default", func(t *testing.T) {
		body := adminGet(t, handler, "/admin/requests").Body.String()
		if strings.Contains(body, "hx-trigger") {
			t.Error("the log polls without being asked to")
		}
	})

	t.Run("the fragment is a fragment", func(t *testing.T) {
		rec := adminGet(t, handler, "/admin/requests/table?live=1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "<html") || strings.Contains(body, "<nav") {
			t.Error("the fragment carries the whole layout")
		}
		if !strings.Contains(body, "live@example.com") {
			t.Error("the fragment does not contain the logged request")
		}
	})

	t.Run("filters survive the refresh", func(t *testing.T) {
		body := adminGet(t, handler, "/admin/requests?live=1&path=/api/v2/contact").Body.String()
		if !strings.Contains(body, "live=1") || !strings.Contains(body, "path=%2Fapi%2Fv2%2Fcontact") {
			t.Error("the poll URL drops the active filter or live mode")
		}
	})

	t.Run("the toggle turns it off again", func(t *testing.T) {
		body := adminGet(t, handler, "/admin/requests?live=1").Body.String()
		if !strings.Contains(body, `href="/admin/requests?"`) {
			t.Error("no way back out of live mode")
		}
	})
}

// betweenMarkers pulls a fragment out of a page for a failure message, so a
// diff shows the attribute rather than the whole document.
func betweenMarkers(body, start, end string) string {
	i := strings.Index(body, start)
	if i < 0 {
		return "(marker not found)"
	}
	rest := body[i:]
	if j := strings.Index(rest, end); j > 0 {
		return rest[:j]
	}
	return rest[:min(len(rest), 120)]
}
