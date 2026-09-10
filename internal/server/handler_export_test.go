package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dennis-kluge/emarsys-mock/internal/auth"
)

// TestExportStatusLiteralHasASpace pins the string that silently breaks polling
// loops: production returns "in progress", not "in_progress".
func TestExportStatusLiteralHasASpace(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"exp@example.com","1":"Jane"}]}`)

	id := startExport(t, handler, `{"contact_fields":[1,3],"add_field_names_header":1}`)
	status := pollExport(t, handler, id)

	if status.Status != "in progress" {
		t.Fatalf("status = %q, want %q", status.Status, "in progress")
	}
	if strings.Contains(status.Status, "_") {
		t.Error("status must not be a slug")
	}
}

func TestExportPollSequence(t *testing.T) {
	// Two polls report "in progress" and the third reports "done". Without that
	// delay nobody's polling loop is ever exercised, which is the only reason
	// to mock an asynchronous endpoint at all.
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"seq@example.com","1":"Jane"}]}`)

	id := startExport(t, handler, `{"contact_fields":[1,3],"add_field_names_header":1}`)

	want := []string{"in progress", "in progress", "done", "done"}
	for i, expected := range want {
		if got := pollExport(t, handler, id).Status; got != expected {
			t.Errorf("poll %d status = %q, want %q", i+1, got, expected)
		}
	}
}

func TestExportDataIsRefusedUntilDone(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"early@example.com","1":"Jane"}]}`)
	id := startExport(t, handler, `{"contact_fields":[1,3],"add_field_names_header":1}`)

	env := call(t, handler, http.MethodGet, "/api/v2/export/"+id+"/data", "")
	if env.ReplyCode == 0 {
		t.Fatalf("data was served before the export finished: %s", env.Data)
	}
	if !strings.Contains(env.ReplyText, "not finished") {
		t.Errorf("replyText = %q", env.ReplyText)
	}
}

func TestExportFlowEndToEnd(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler,
		`{"key_id":"3","contacts":[{"3":"flow@example.com","1":"Jane","2":"Doe"}]}`)

	id := startExport(t, handler,
		`{"contact_fields":[1,2,3],"add_field_names_header":1,"delimiter":","}`)

	for range 3 {
		pollExport(t, handler, id)
	}
	if got := pollExport(t, handler, id).Status; got != "done" {
		t.Fatalf("status = %q, want done", got)
	}

	body := rawGet(t, handler, "/api/v2/export/"+id+"/data")
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 2 {
		t.Fatalf("csv has %d lines, want a header and one row:\n%s", len(lines), body)
	}
	if !strings.HasPrefix(lines[0], "Timestamp,") {
		t.Errorf("header = %q, want a leading Timestamp column", lines[0])
	}
	if !strings.Contains(lines[1], "flow@example.com") || !strings.Contains(lines[1], "Jane") {
		t.Errorf("row = %q", lines[1])
	}
}

// TestExportTimestampsAreViennaLocal covers section 8.9: Emarsys renders export
// timestamps in Vienna local time with no offset, so a client parsing them as
// UTC is one or two hours out all year round.
func TestExportTimestampsAreViennaLocal(t *testing.T) {
	handler, db := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"tz@example.com","1":"Jane"}]}`)

	// Pin the stored change to a known instant so the rendered offset is exact.
	const storedUTC = "2026-07-01T10:00:00Z"
	if _, err := db.Write.Exec(
		`UPDATE contact_field_history SET changed_at = ?`, storedUTC); err != nil {
		t.Fatal(err)
	}

	id := startExport(t, handler, `{"contact_fields":[1],"add_field_names_header":0}`)
	for range 3 {
		pollExport(t, handler, id)
	}

	body := rawGet(t, handler, "/api/v2/export/"+id+"/data")
	// Vienna is UTC+2 on 1 July, so 10:00 UTC renders as 12:00 with no marker.
	if !strings.Contains(body, "2026-07-01 12:00:00") {
		t.Errorf("csv = %q, want the timestamp rendered in Vienna local time", body)
	}
	if strings.Contains(body, "10:00:00") {
		t.Error("timestamp was rendered in UTC")
	}
	if strings.Contains(body, "Z") || strings.Contains(body, "+02:00") {
		t.Error("timestamp carries a zone marker; production emits none")
	}
}

func TestExportPollsBeforeDoneIsOverridablePerJob(t *testing.T) {
	// A test that only needs the file should not have to poll three times for
	// it, and one testing the polling loop needs the delay. Both must be
	// arrangeable without reconfiguring the service.
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"imm@example.com","1":"Jane"}]}`)

	id := startExport(t, handler, `{"contact_fields":[1],"polls_before_done":0}`)
	if got := pollExport(t, handler, id).Status; got != "done" {
		t.Errorf("status on first poll = %q, want done", got)
	}
}

func TestExportRequiresFields(t *testing.T) {
	handler, _ := newTestServer(t)
	call(t, handler, http.MethodPost, "/api/v2/contact/getchanges",
		`{"contact_fields":[]}`).expectError(t, 2014)
	call(t, handler, http.MethodPost, "/api/v2/contact/getchanges",
		`{"contact_fields":[424242]}`).expectError(t, 2006)
}

func TestExportTimeRangeFiltersChanges(t *testing.T) {
	handler, db := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"old@example.com","1":"Old"}]}`)
	if _, err := db.Write.Exec(
		`UPDATE contact_field_history SET changed_at = '2020-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"new@example.com","1":"New"}]}`)

	id := startExport(t, handler,
		`{"contact_fields":[1,3],"time_range":["2026-01-01","2030-01-01"],"add_field_names_header":0}`)
	for range 3 {
		pollExport(t, handler, id)
	}

	body := rawGet(t, handler, "/api/v2/export/"+id+"/data")
	if strings.Contains(body, "old@example.com") {
		t.Errorf("csv includes a change outside the window:\n%s", body)
	}
	if !strings.Contains(body, "new@example.com") {
		t.Errorf("csv is missing the change inside the window:\n%s", body)
	}
}

func TestExportUnknownIDs(t *testing.T) {
	handler, _ := newTestServer(t)
	call(t, handler, http.MethodGet, "/api/v2/export/9999", "").expectError(t, 2011)
	call(t, handler, http.MethodGet, "/api/v2/export/9999/data", "").expectError(t, 2011)
}

func TestLastChangeReadsTheAuditTrail(t *testing.T) {
	// Emarsys has no endpoint for a field's history. This one reads the log the
	// mock keeps, which is the same table a consent audit trail would use.
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"hist@example.com","31":"1"}]}`)
	call(t, handler, http.MethodPut, "/api/v2/contact/",
		`{"key_id":"3","contacts":[{"3":"hist@example.com","31":"2"}]}`).expectOK(t)

	env := call(t, handler, http.MethodPost, "/api/v2/contact/last_change",
		`{"keyId":3,"keyValues":["hist@example.com"],"fieldId":31}`).expectOK(t)

	var payload struct {
		Result map[string]struct {
			OldValue     string `json:"old_value"`
			CurrentValue string `json:"current_value"`
			Time         string `json:"time"`
		} `json:"result"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("decode: %v (data %s)", err, env.Data)
	}
	entry, ok := payload.Result["hist@example.com"]
	if !ok {
		t.Fatalf("result = %v", payload.Result)
	}
	if entry.OldValue != "1" || entry.CurrentValue != "2" {
		t.Errorf("change = %+v, want 1 -> 2", entry)
	}
	if _, err := time.Parse(emarsysTimeLayout, entry.Time); err != nil {
		t.Errorf("time = %q, want the Emarsys layout %q", entry.Time, emarsysTimeLayout)
	}
}

// --- helpers ---------------------------------------------------------------

func startExport(t *testing.T, handler http.Handler, body string) string {
	t.Helper()
	env := call(t, handler, http.MethodPost, "/api/v2/contact/getchanges", body).expectOK(t)

	var payload struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("decode export id: %v (data %s)", err, env.Data)
	}
	if payload.ID == 0 {
		t.Fatalf("export id = 0 (data %s)", env.Data)
	}
	return strconv.FormatInt(payload.ID, 10)
}

type exportStatus struct {
	ID       string `json:"id"`
	Created  string `json:"created"`
	Status   string `json:"status"`
	Type     string `json:"type"`
	FileName string `json:"file_name"`
}

func pollExport(t *testing.T, handler http.Handler, id string) exportStatus {
	t.Helper()
	env := call(t, handler, http.MethodGet, "/api/v2/export/"+id, "").expectOK(t)

	var status exportStatus
	if err := json.Unmarshal(env.Data, &status); err != nil {
		t.Fatalf("decode export status: %v (data %s)", err, env.Data)
	}
	// The status endpoint reports the id as a string although getchanges handed
	// out a number for the same job.
	if status.ID != id {
		t.Errorf("status id = %q, want the string %q", status.ID, id)
	}
	return status
}

// rawGet fetches a body that is not an envelope, such as export CSV.
func rawGet(t *testing.T, handler http.Handler, target string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("X-WSSE", auth.BuildHeader(
		"mock-api-user", "mock-secret", time.Now(), "a1b2c3d4e5f60718293a4b5c6d7e8f90"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	return rec.Body.String()
}
