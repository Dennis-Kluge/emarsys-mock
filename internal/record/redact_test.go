package record

import (
	"encoding/json"
	"strings"
	"testing"
)

func newTestRedactor(mode Mode) *Redactor {
	return NewRedactor(mode, []byte("test-session-key"))
}

// TestCredentialsAreNeverWritten is the assertion that has to hold before this
// tool is pointed at anything real.
func TestCredentialsAreNeverWritten(t *testing.T) {
	r := newTestRedactor(ModeShapes)

	headers := map[string][]string{
		"X-WSSE":                     {`UsernameToken Username="prod-user", PasswordDigest="NzYyYTZjMmE=", Nonce="abc", Created="2026-09-10T12:00:00Z"`},
		"Authorization":              {"Bearer eyJhbGciOiJIUzI1NiJ9.secret.payload"},
		"Cookie":                     {"session=abc123"},
		"Content-Type":               {"application/json"},
		"X-Custom-Internal-Hostname": {"emarsys-prod-01.internal"},
	}

	out := r.Headers(headers)
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(encoded)

	for _, secret := range []string{"prod-user", "NzYyYTZjMmE=", "eyJhbGciOiJIUzI1NiJ9", "abc123"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("a credential survived redaction: %q in %s", secret, rendered)
		}
	}
	// An allowlist cannot be defeated by a header nobody thought of, so an
	// unknown one is dropped rather than redacted.
	if strings.Contains(rendered, "internal") {
		t.Errorf("an unlisted header was kept: %s", rendered)
	}
	if out["Content-Type"] != "application/json" {
		t.Errorf("an allowlisted header was lost: %v", out)
	}
	// The scheme is worth knowing even though the value is not.
	if !strings.Contains(out["X-WSSE"], "UsernameToken") {
		t.Errorf("X-WSSE = %q, want a note that WSSE was used", out["X-WSSE"])
	}
	if !strings.Contains(out["Authorization"], "Bearer") {
		t.Errorf("Authorization = %q, want a note that a bearer token was used", out["Authorization"])
	}
}

func TestShapesModeRemovesPersonalData(t *testing.T) {
	r := newTestRedactor(ModeShapes)

	body := []byte(`{
		"key_id": "3",
		"contacts": [
			{"1": "Ada", "2": "Lovelace", "3": "ada.lovelace@example.com", "4": "1815-12-10", "31": "1"}
		]
	}`)

	out := string(r.Body("application/json", body))

	for _, personal := range []string{"Ada", "Lovelace", "ada.lovelace", "1815-12-10"} {
		if strings.Contains(out, personal) {
			t.Errorf("personal data survived: %q in %s", personal, out)
		}
	}
	// The structure is what a handler is written against, so it has to survive.
	for _, structural := range []string{`"key_id"`, `"contacts"`, `"1"`, `"3"`, `"31"`} {
		if !strings.Contains(out, structural) {
			t.Errorf("structure was lost: %q missing from %s", structural, out)
		}
	}
	// key_id is protocol, not personal.
	if !strings.Contains(out, `"key_id":"3"`) {
		t.Errorf("key_id was pseudonymised although it is protocol: %s", out)
	}
	// The address kept its shape, so a handler still sees an address.
	if !strings.Contains(out, "@example.com") {
		t.Errorf("the address lost its shape: %s", out)
	}
	// The date kept its format, so date validation is still visible.
	if !strings.Contains(out, "1990-01-01") {
		t.Errorf("the date lost its format: %s", out)
	}
}

func TestReplyTextKeepsItsWordingButNotTheAddress(t *testing.T) {
	// Getting the exact error message out of a recording is a large part of why
	// one is made, but production interpolates the contact into it.
	r := newTestRedactor(ModeShapes)

	body := []byte(`{"replyCode":0,"replyText":"OK","data":{"errors":
		{"jane.doe@customer.example":{"2008":"No contact found with the external id: jane.doe@customer.example"}}}}`)

	out := string(r.Body("application/json", body))

	if strings.Contains(out, "jane.doe") {
		t.Errorf("an address survived inside the error: %s", out)
	}
	if !strings.Contains(out, "No contact found with the external id") {
		t.Errorf("the error wording was lost: %s", out)
	}
	if !strings.Contains(out, `"replyCode":0`) {
		t.Errorf("the reply code was lost: %s", out)
	}
	if !strings.Contains(out, `"2008"`) {
		t.Errorf("the per-row error code was lost: %s", out)
	}
}

func TestNonJSONBodiesAreNotRecorded(t *testing.T) {
	// A CSV export is contact data in bulk and has no shape worth keeping.
	r := newTestRedactor(ModeShapes)

	csv := []byte("Timestamp,First Name,E-Mail\n2026-07-01 12:00:00,Ada,ada@example.com\n")
	out := string(r.Body("text/csv;charset=utf-8", csv))

	if strings.Contains(out, "ada@example.com") || strings.Contains(out, "Ada") {
		t.Errorf("CSV contents survived: %s", out)
	}
	if !strings.Contains(out, "text/csv") || !strings.Contains(out, "redacted") {
		t.Errorf("the recording does not say what was dropped: %s", out)
	}
}

func TestPseudonymsAreStableWithinASessionAndNotAcross(t *testing.T) {
	// A contact appearing in three calls has to be recognisably the same
	// contact, without ever being identifiable.
	a := newTestRedactor(ModeShapes)
	b := NewRedactor(ModeShapes, []byte("a-different-session"))

	first := string(a.Body("application/json", []byte(`{"x":"ada@example.com"}`)))
	again := string(a.Body("application/json", []byte(`{"x":"ada@example.com"}`)))
	other := string(b.Body("application/json", []byte(`{"x":"ada@example.com"}`)))

	if first != again {
		t.Errorf("the same value produced two pseudonyms: %s vs %s", first, again)
	}
	if first == other {
		t.Error("two sessions produced the same pseudonym, so recordings can be correlated")
	}
}

func TestRawModeIsVerbatim(t *testing.T) {
	// Raw mode exists for a self-seeded account, and it has to be honest about
	// keeping everything.
	r := newTestRedactor(ModeRaw)
	out := string(r.Body("application/json", []byte(`{"3":"ada@example.com"}`)))
	if !strings.Contains(out, "ada@example.com") {
		t.Errorf("raw mode dropped data: %s", out)
	}
}

func TestNumbersSurviveRedaction(t *testing.T) {
	// An id is a number, and whether the API returns a string or an integer is
	// exactly the sort of thing a recording exists to settle.
	r := newTestRedactor(ModeShapes)
	out := string(r.Body("application/json", []byte(`{"ids":[1,2],"errors":{}}`)))
	if !strings.Contains(out, "[1,2]") {
		t.Errorf("numeric ids were altered: %s", out)
	}
}

// TestArrayElementsAreNotTreatedAsContactKeyedMaps is a regression test for a
// bug an end-to-end recording found that the unit tests had missed.
//
// data.result is an array of contact rows in getdata and a map keyed by e-mail
// in last_change. Treating both the same pseudonymised the field ids out of the
// getdata rows, destroying the structure the recording exists to capture.
func TestArrayElementsAreNotTreatedAsContactKeyedMaps(t *testing.T) {
	r := newTestRedactor(ModeShapes)

	getdata := []byte(`{"replyCode":0,"replyText":"OK","data":{"result":[
		{"id":1,"uid":"deadbeef","1":"Ada","31":"1"}
	],"errors":{}}}`)

	out := string(r.Body("application/json", getdata))

	// The field ids are the structure. They have to survive.
	for _, key := range []string{`"1":`, `"31":`, `"id":`, `"uid":`} {
		if !strings.Contains(out, key) {
			t.Errorf("field key %s was pseudonymised away: %s", key, out)
		}
	}
	// The values still must not.
	if strings.Contains(out, "Ada") || strings.Contains(out, "deadbeef") {
		t.Errorf("values survived: %s", out)
	}
	// The internal id is a number and stays one, because whether an id comes
	// back as a string or an integer is a thing a recording has to settle.
	if !strings.Contains(out, `"id":1`) {
		t.Errorf("the numeric id was altered: %s", out)
	}
}

// TestLastChangeResultKeysAreStillPseudonymised guards the other half: there
// the same field really is a map keyed by the contact's address.
func TestLastChangeResultKeysAreStillPseudonymised(t *testing.T) {
	r := newTestRedactor(ModeShapes)

	lastChange := []byte(`{"data":{"result":{
		"ada.lovelace@example.com":{"old_value":"1","current_value":"2","time":"2026-09-10 12:00:00"}
	}}}`)

	out := string(r.Body("application/json", lastChange))
	if strings.Contains(out, "ada.lovelace") {
		t.Errorf("the address survived as a map key: %s", out)
	}
	if !strings.Contains(out, "@example.com") {
		t.Errorf("the key lost its shape: %s", out)
	}
}
