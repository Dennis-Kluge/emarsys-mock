package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// The tests in this file cover the behaviours listed in section 8 of the
// briefing: the places where real integrations fail against production. If the
// mock takes the happy path on any of them it is worthless, so each one gets a
// named test rather than being folded into a broader case.

// --- Section 8.1: key_id is a field id, never a field name -------------------

func TestKeyIDMustBeAFieldID(t *testing.T) {
	handler, _ := newTestServer(t)

	cases := []struct {
		name     string
		keyID    string
		wantCode int
		wantHTTP int
	}{
		{"numeric string", `"3"`, 0, http.StatusOK},
		{"bare number", `3`, 0, http.StatusOK},
		{"field name instead of id", `"email"`, 2004, http.StatusBadRequest},
		{"string_id instead of id", `"opt_in"`, 2004, http.StatusBadRequest},
		{"unknown field id", `"9999"`, 2004, http.StatusBadRequest},
		{"internal id is accepted", `"id"`, 0, http.StatusOK},
		{"uid is accepted", `"uid"`, 0, http.StatusOK},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each case uses a distinct address so successful ones do not
			// collide on an already-existing contact.
			body := `{"key_id":` + tc.keyID + `,"contacts":[{"3":"key` +
				strconv.Itoa(i) + `@example.com","id":"","uid":""}]}`
			env := call(t, handler, http.MethodPost, "/api/v2/contact", body)

			if env.ReplyCode != tc.wantCode {
				t.Fatalf("replyCode = %d (%q), want %d", env.ReplyCode, env.ReplyText, tc.wantCode)
			}
			if env.Status != tc.wantHTTP {
				t.Errorf("status = %d, want %d", env.Status, tc.wantHTTP)
			}
		})
	}
}

func TestKeyIDErrorNamesTheProblem(t *testing.T) {
	handler, _ := newTestServer(t)
	env := call(t, handler, http.MethodPost, "/api/v2/contact",
		`{"key_id":"email","contacts":[{"3":"a@example.com"}]}`)

	if !strings.Contains(env.ReplyText, "email") {
		t.Errorf("replyText = %q, want it to name the offending value", env.ReplyText)
	}
}

// --- Section 8.2: opt-in accepts only its defined choices --------------------

func TestOptInRejectsBooleanLiterals(t *testing.T) {
	handler, _ := newTestServer(t)

	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"choice 1", `"1"`, false},
		{"choice 2", `"2"`, false},
		{"numeric choice", `1`, false},
		{"empty clears the field", `""`, false},
		{"null clears the field", `null`, false},
		{"string true", `"true"`, true},
		{"string false", `"false"`, true},
		{"boolean true", `true`, true},
		{"zero", `"0"`, true},
		{"out of range choice", `"3"`, true},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			address := "optin" + strconv.Itoa(i) + "@example.com"
			body := `{"key_id":"3","contacts":[{"3":"` + address + `","31":` + tc.value + `}]}`
			data := call(t, handler, http.MethodPost, "/api/v2/contact", body).expectOK(t).batch(t)

			if tc.wantErr {
				if code := data.rowError(t, address); code != "2006" {
					t.Errorf("error code = %s, want 2006", code)
				}
				if len(data.IDs) != 0 {
					t.Errorf("ids = %v, want none for a rejected row", data.IDs)
				}
				return
			}
			if len(data.Errors) != 0 {
				t.Fatalf("errors = %v, want none", data.Errors)
			}
			if len(data.IDs) != 1 {
				t.Errorf("ids = %v, want exactly one", data.IDs)
			}
		})
	}
}

// --- Section 8.3: an empty string overwrites, it does not skip ---------------

func TestEmptyStringOverwritesAnExistingValue(t *testing.T) {
	// This is the case that silently destroys consent data in production: an
	// integration includes the opt-in field without meaning to, sends an empty
	// value, and the contact is unsubscribed with no error anywhere.
	handler, _ := newTestServer(t)
	const address = "consent@example.com"

	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"`+address+`","31":"1"}]}`)
	if got := readField(t, handler, address, 31); got != "1" {
		t.Fatalf("opt-in after create = %q, want 1", got)
	}

	call(t, handler, http.MethodPut, "/api/v2/contact/",
		`{"key_id":"3","contacts":[{"3":"`+address+`","31":""}]}`).expectOK(t)

	if got := readField(t, handler, address, 31); got != "" {
		t.Errorf("opt-in after empty write = %q, want it to have been cleared", got)
	}
}

func TestEmptyStringChangeIsRecordedInHistory(t *testing.T) {
	// The audit trail is the only way to reconstruct who cleared a consent
	// flag and when, because Emarsys itself offers no endpoint for it.
	handler, db := newTestServer(t)
	const address = "audit@example.com"

	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"`+address+`","31":"1"}]}`)
	call(t, handler, http.MethodPut, "/api/v2/contact/",
		`{"key_id":"3","contacts":[{"3":"`+address+`","31":""}]}`).expectOK(t)

	var oldValue, newValue *string
	err := db.Read.QueryRow(
		`SELECT old_value, new_value FROM contact_field_history
		 WHERE field_id = 31 ORDER BY id DESC LIMIT 1`).Scan(&oldValue, &newValue)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if oldValue == nil || *oldValue != "1" {
		t.Errorf("old_value = %v, want 1", oldValue)
	}
	if newValue == nil || *newValue != "" {
		t.Errorf("new_value = %v, want an empty string", newValue)
	}
}

// --- Section 8.4: an update replaces only the fields it was sent -------------

func TestUpdateLeavesUnsentFieldsAlone(t *testing.T) {
	handler, _ := newTestServer(t)
	const address = "partial@example.com"

	createContact(t, handler,
		`{"key_id":"3","contacts":[{"3":"`+address+`","1":"Jane","2":"Doe"}]}`)

	call(t, handler, http.MethodPut, "/api/v2/contact/",
		`{"key_id":"3","contacts":[{"3":"`+address+`","1":"Janet"}]}`).expectOK(t)

	if got := readField(t, handler, address, 1); got != "Janet" {
		t.Errorf("first name = %q, want Janet", got)
	}
	if got := readField(t, handler, address, 2); got != "Doe" {
		t.Errorf("last name = %q, want it untouched", got)
	}
}

// --- Section 8.5: an upsert reports created and updated ids differently ------

func TestUpsertIDTypesDifferForCreatedAndUpdated(t *testing.T) {
	// Production returns a string id for a contact that already existed and an
	// integer for one it just created. A client that assumes a single type
	// breaks on whichever case it did not see first.
	handler, _ := newTestServer(t)
	const existing = "known@example.com"

	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"`+existing+`"}]}`)

	data := call(t, handler, http.MethodPut, "/api/v2/contact/?create_if_not_exists=1",
		`{"key_id":"3","contacts":[{"3":"`+existing+`"},{"3":"fresh@example.com"}]}`).
		expectOK(t).batch(t)

	if len(data.IDs) != 2 {
		t.Fatalf("ids = %v, want two", data.IDs)
	}
	if !strings.HasPrefix(string(data.IDs[0]), `"`) {
		t.Errorf("updated contact id = %s, want a JSON string", data.IDs[0])
	}
	if strings.HasPrefix(string(data.IDs[1]), `"`) {
		t.Errorf("created contact id = %s, want a JSON number", data.IDs[1])
	}
}

func TestUpdateWithoutCreateFlagReportsMissingContacts(t *testing.T) {
	handler, _ := newTestServer(t)

	data := call(t, handler, http.MethodPut, "/api/v2/contact/",
		`{"key_id":"3","contacts":[{"3":"ghost@example.com"}]}`).expectOK(t).batch(t)

	if code := data.rowError(t, "ghost@example.com"); code != "2008" {
		t.Errorf("error code = %s, want 2008", code)
	}
}

// --- Section 8.6: dates are ISO-8601 calendar dates only ---------------------

func TestDateFieldAcceptsOnlyISODates(t *testing.T) {
	handler, _ := newTestServer(t)

	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"iso date", "1990-01-31", false},
		{"empty", "", false},
		{"german ordering", "31.01.1990", true},
		{"us ordering", "01/31/1990", true},
		{"iso timestamp", "1990-01-31T00:00:00Z", true},
		{"year only", "1990", true},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			address := "date" + strconv.Itoa(i) + "@example.com"
			body := `{"key_id":"3","contacts":[{"3":"` + address + `","4":"` + tc.value + `"}]}`
			data := call(t, handler, http.MethodPost, "/api/v2/contact", body).expectOK(t).batch(t)

			if tc.wantErr {
				if code := data.rowError(t, address); code != "2006" {
					t.Errorf("error code = %s, want 2006", code)
				}
				return
			}
			if len(data.Errors) != 0 {
				t.Errorf("errors = %v, want none", data.Errors)
			}
		})
	}
}

// --- Section 8.7: an ambiguous key value is replyCode 2010 -------------------

func TestDuplicateKeyValueWithinOneBatch(t *testing.T) {
	handler, _ := newTestServer(t)

	data := call(t, handler, http.MethodPost, "/api/v2/contact",
		`{"key_id":"3","contacts":[
			{"3":"twice@example.com","1":"First"},
			{"3":"twice@example.com","1":"Second"}
		 ]}`).expectOK(t).batch(t)

	if len(data.IDs) != 1 {
		t.Errorf("ids = %v, want the first row only", data.IDs)
	}
	if code := data.rowError(t, "twice@example.com"); code != "2010" {
		t.Errorf("error code = %s, want 2010", code)
	}
}

func TestAmbiguousKeyValueInTheDatabase(t *testing.T) {
	// Emarsys does not enforce uniqueness on key fields, so a value can end up
	// on two contacts. Addressing it afterwards is an error, not a coin flip.
	handler, db := newTestServer(t)

	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"dup@example.com"}]}`)
	// Reach past the API to build the state a bulk import or a merge can create.
	if _, err := db.Write.Exec(
		`INSERT INTO contacts (uid, created_at, updated_at) VALUES ('dup-uid','now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write.Exec(
		`INSERT INTO contact_values (contact_id, field_id, value, updated_at)
		 SELECT id, 3, 'dup@example.com', 'now' FROM contacts WHERE uid = 'dup-uid'`); err != nil {
		t.Fatal(err)
	}

	data := call(t, handler, http.MethodPut, "/api/v2/contact/",
		`{"key_id":"3","contacts":[{"3":"dup@example.com","1":"Jane"}]}`).expectOK(t).batch(t)

	if code := data.rowError(t, "dup@example.com"); code != "2010" {
		t.Errorf("error code = %s, want 2010", code)
	}
}

// --- Section 8.8: contact/query only works on indexed fields -----------------

func TestQueryRequiresAnIndexedField(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"q@example.com","1":"Jane"}]}`)

	t.Run("indexed field succeeds", func(t *testing.T) {
		env := call(t, handler, http.MethodGet,
			"/api/v2/contact/query/?3=q@example.com&return=1", "").expectOK(t)

		var payload struct {
			Result []map[string]json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("decode: %v (data %s)", err, env.Data)
		}
		if len(payload.Result) != 1 {
			t.Fatalf("result = %v, want one row", payload.Result)
		}
		if got := string(payload.Result[0]["1"]); got != `"Jane"` {
			t.Errorf("returned field = %s, want \"Jane\"", got)
		}
		if _, ok := payload.Result[0]["id"]; !ok {
			t.Error("result row is missing the contact id")
		}
	})

	t.Run("unindexed field is rejected", func(t *testing.T) {
		call(t, handler, http.MethodGet, "/api/v2/contact/query/?1=Jane&return=3", "").
			expectError(t, 2015)
	})

	t.Run("missing return field", func(t *testing.T) {
		call(t, handler, http.MethodGet, "/api/v2/contact/query/?3=q@example.com", "").
			expectError(t, 2014)
	})

	t.Run("limit out of range", func(t *testing.T) {
		call(t, handler, http.MethodGet,
			"/api/v2/contact/query/?3=q@example.com&return=1&limit=10001", "").
			expectError(t, 2016)
		call(t, handler, http.MethodGet,
			"/api/v2/contact/query/?3=q@example.com&return=1&limit=0", "").
			expectError(t, 2016)
	})
}

// readField is a small read-back helper built on getdata.
func readField(t *testing.T, handler http.Handler, address string, fieldID int) string {
	t.Helper()
	env := call(t, handler, http.MethodPost, "/api/v2/contact/getdata",
		`{"keyId":3,"keyValues":["`+address+`"],"fields":[`+strconv.Itoa(fieldID)+`]}`).expectOK(t)

	var payload struct {
		Result []map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("decode getdata: %v (data %s)", err, env.Data)
	}
	if len(payload.Result) == 0 {
		t.Fatalf("getdata returned no row for %s", address)
	}
	raw, ok := payload.Result[0][strconv.Itoa(fieldID)]
	if !ok {
		t.Fatalf("getdata result has no field %d: %v", fieldID, payload.Result[0])
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("field %d is not a string: %s", fieldID, raw)
	}
	return value
}
