package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TestBatchPartialFailureIsAnHTTP200 pins the single most important rule of the
// whole contract.
//
// A batch where some rows failed comes back as HTTP 200 with replyCode 0 and
// the failures buried in data.errors. A client that checks only the top-level
// reply code reads that as complete success and silently drops contacts, which
// is exactly the bug an integration test needs to be able to trigger.
func TestBatchPartialFailureIsAnHTTP200(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"taken@example.com"}]}`)

	env := call(t, handler, http.MethodPost, "/api/v2/contact",
		`{"key_id":"3","contacts":[
			{"3":"new@example.com","1":"Jane"},
			{"3":"taken@example.com","1":"Clash"},
			{"3":"alsonew@example.com","1":"John"}
		 ]}`)

	if env.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- partial failures are not transport failures", env.Status)
	}
	if env.ReplyCode != 0 {
		t.Fatalf("replyCode = %d, want 0 -- the batch as a whole succeeded", env.ReplyCode)
	}

	data := env.batch(t)
	if len(data.IDs) != 2 {
		t.Errorf("ids = %v, want one per successful row", data.IDs)
	}
	if len(data.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", data.Errors)
	}
	if code := data.rowError(t, "taken@example.com"); code != "2009" {
		t.Errorf("error code = %s, want 2009", code)
	}
	if msg := data.Errors["taken@example.com"]["2009"]; !strings.Contains(msg, "taken@example.com") {
		t.Errorf("error message = %q, want it to name the contact", msg)
	}
}

func TestBatchSizeLimit(t *testing.T) {
	handler, _ := newTestServer(t)

	rows := make([]string, 0, 1001)
	for i := range 1001 {
		rows = append(rows, `{"3":"bulk`+strconv.Itoa(i)+`@example.com"}`)
	}
	body := `{"key_id":"3","contacts":[` + strings.Join(rows, ",") + `]}`

	env := call(t, handler, http.MethodPost, "/api/v2/contact", body).expectError(t, 1000)
	if env.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", env.Status)
	}
}

func TestUnknownFieldIDFailsTheWholeRequest(t *testing.T) {
	// An unknown field id means the client is misconfigured for every row, not
	// just the one it appeared in, so it is a request-level error.
	handler, _ := newTestServer(t)

	cases := []struct{ name, field string }{
		{"numeric but unknown", `"424242"`},
		{"field name instead of id", `"email"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"key_id":"3","contacts":[{"3":"x@example.com",` + tc.field + `:"v"}]}`
			env := call(t, handler, http.MethodPost, "/api/v2/contact", body).expectError(t, 2006)
			if env.Status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", env.Status)
			}
		})
	}
}

func TestBatchRequiresContactsAndKeyID(t *testing.T) {
	handler, _ := newTestServer(t)

	cases := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"no key_id", `{"contacts":[{"3":"a@example.com"}]}`, 2005},
		{"no contacts", `{"key_id":"3"}`, 2005},
		{"empty contacts", `{"key_id":"3","contacts":[]}`, 2005},
		{"empty body", ``, 2005},
		{"malformed json", `{"key_id":`, 2011},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call(t, handler, http.MethodPost, "/api/v2/contact", tc.body).expectError(t, tc.wantCode)
		})
	}
}

func TestRowWithoutKeyValueIsReportedUnderTheEmptyKey(t *testing.T) {
	handler, _ := newTestServer(t)

	data := call(t, handler, http.MethodPost, "/api/v2/contact",
		`{"key_id":"3","contacts":[{"1":"Nameless"}]}`).expectOK(t).batch(t)

	if code := data.rowError(t, ""); code != "2005" {
		t.Errorf("error code = %s, want 2005", code)
	}
}

func TestGetDataUsesCamelCaseKeys(t *testing.T) {
	// create/update take key_id while getdata takes keyId. Normalising the two
	// gets an empty result rather than an error, so the mock has to be as picky
	// as production is.
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"case@example.com","1":"Jane"}]}`)

	t.Run("camelCase works", func(t *testing.T) {
		env := call(t, handler, http.MethodPost, "/api/v2/contact/getdata",
			`{"keyId":3,"keyValues":["case@example.com"],"fields":[1]}`).expectOK(t)

		var payload struct {
			Result []map[string]json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(payload.Result) != 1 {
			t.Fatalf("result = %v, want one row", payload.Result)
		}
		row := payload.Result[0]
		for _, key := range []string{"id", "uid", "1"} {
			if _, ok := row[key]; !ok {
				t.Errorf("result row is missing %q: %v", key, row)
			}
		}
	})

	t.Run("snake_case is not accepted", func(t *testing.T) {
		call(t, handler, http.MethodPost, "/api/v2/contact/getdata",
			`{"key_id":3,"key_values":["case@example.com"],"fields":[1]}`).expectError(t, 2005)
	})
}

func TestGetDataValidation(t *testing.T) {
	handler, _ := newTestServer(t)

	cases := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"no fields", `{"keyId":3,"keyValues":["a@example.com"],"fields":[]}`, 2014},
		{"unknown field", `{"keyId":3,"keyValues":["a@example.com"],"fields":[424242]}`, 2006},
		{"key values not a list", `{"keyId":3,"keyValues":"a@example.com","fields":[1]}`, 2003},
		{"unknown key field", `{"keyId":424242,"keyValues":["a@example.com"],"fields":[1]}`, 2004},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call(t, handler, http.MethodPost, "/api/v2/contact/getdata", tc.body).
				expectError(t, tc.wantCode)
		})
	}
}

func TestGetDataReportsMissingContactsPerKey(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"here@example.com","1":"Jane"}]}`)

	env := call(t, handler, http.MethodPost, "/api/v2/contact/getdata",
		`{"keyId":3,"keyValues":["here@example.com","gone@example.com"],"fields":[1]}`).expectOK(t)

	var payload struct {
		Result []map[string]json.RawMessage `json:"result"`
		Errors map[string]map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Result) != 1 {
		t.Errorf("result = %v, want the one contact that exists", payload.Result)
	}
	if _, ok := payload.Errors["gone@example.com"]["2008"]; !ok {
		t.Errorf("errors = %v, want 2008 for the missing contact", payload.Errors)
	}
}

func TestCheckIDsReturnsAnObjectNotAnArray(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"known@example.com"}]}`)

	env := call(t, handler, http.MethodPost, "/api/v2/contact/checkids",
		`{"key_id":"3","external_ids":["known@example.com","missing@example.com"]}`).expectOK(t)

	var payload struct {
		IDs    map[string]json.RawMessage   `json:"ids"`
		Errors map[string]map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("decode: %v (data %s)", err, env.Data)
	}
	if _, ok := payload.IDs["known@example.com"]; !ok {
		t.Errorf("ids = %v, want the known address keyed by its value", payload.IDs)
	}
	if _, ok := payload.Errors["missing@example.com"]["2008"]; !ok {
		t.Errorf("errors = %v, want 2008 for the unknown address", payload.Errors)
	}
}

func TestContactDeleteTakesKeyValuesUnderTheFieldIDKey(t *testing.T) {
	// The request shape is unusual enough to be worth its own test: the key
	// values arrive under a JSON key named after the key field id.
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"delete-me@example.com"}]}`)

	env := call(t, handler, http.MethodPost, "/api/v2/contact/delete",
		`{"key_id":3,"3":["delete-me@example.com","never-existed@example.com"]}`).expectOK(t)

	var payload struct {
		Errors map[string]map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := payload.Errors["never-existed@example.com"]["2008"]; !ok {
		t.Errorf("errors = %v, want 2008 for the unknown address", payload.Errors)
	}

	gone := call(t, handler, http.MethodPost, "/api/v2/contact/getdata",
		`{"keyId":3,"keyValues":["delete-me@example.com"],"fields":[1]}`).expectOK(t)
	if !strings.Contains(string(gone.Data), "2008") {
		t.Errorf("contact still readable after delete: %s", gone.Data)
	}
}

func TestContactBatchHonoursContactListID(t *testing.T) {
	handler, db := newTestServer(t)

	// List 1 is seeded as "Newsletter".
	createContact(t, handler,
		`{"key_id":"3","contact_list_id":1,"contacts":[{"3":"listed@example.com"}]}`)

	var members int
	if err := db.Read.QueryRow(
		`SELECT COUNT(*) FROM contact_list_members WHERE list_id = 1`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 1 {
		t.Errorf("list members = %d, want 1", members)
	}
}

func TestContactBatchRejectsAnUnknownContactList(t *testing.T) {
	handler, _ := newTestServer(t)
	call(t, handler, http.MethodPost, "/api/v2/contact",
		`{"key_id":"3","contact_list_id":9999,"contacts":[{"3":"a@example.com"}]}`).
		expectError(t, 2011)
}

func TestTrailingSlashVariantsAreEquivalent(t *testing.T) {
	// Clients differ on the trailing slash and both forms reach production.
	handler, _ := newTestServer(t)

	for i, target := range []string{"/api/v2/contact", "/api/v2/contact/"} {
		body := `{"key_id":"3","contacts":[{"3":"slash` + strconv.Itoa(i) + `@example.com"}]}`
		call(t, handler, http.MethodPost, target, body).expectOK(t)
	}
}
