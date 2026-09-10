package server

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files in testdata")

// uidPattern masks the random contact uid so the golden files stay stable.
var uidPattern = regexp.MustCompile(`"uid":"[0-9a-f]{32}"`)

// assertGolden compares a response body against testdata/<name>.json.
//
// These files pin the exact wire format, not just the values: key order, the
// JSON type of every id, and the difference between an empty object and an
// empty array. Regenerate them with `go test ./internal/server -update` and read
// the diff before committing it -- a change here is a change to the contract.
func assertGolden(t *testing.T, name string, env envelope) {
	t.Helper()

	raw, err := json.Marshal(struct {
		HTTPStatus int             `json:"http_status"`
		ReplyCode  int             `json:"replyCode"`
		ReplyText  string          `json:"replyText"`
		Data       json.RawMessage `json:"data"`
	}{env.Status, env.ReplyCode, env.ReplyText, env.Data})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	raw = uidPattern.ReplaceAll(raw, []byte(`"uid":"<uid>"`))

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatalf("indent: %v", err)
	}
	pretty.WriteByte('\n')

	path := filepath.Join("testdata", name+".json")
	if *updateGolden {
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run: go test ./internal/server -update)", path, err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(pretty.Bytes())) {
		t.Errorf("%s does not match the golden file.\n got: %s\nwant: %s", name, pretty.Bytes(), want)
	}
}

// TestGoldenResponses records one response shape per endpoint, arranged so the
// ids are deterministic: the server starts from a fresh seeded database and
// every contact is created in a fixed order.
func TestGoldenResponses(t *testing.T) {
	handler, _ := newTestServer(t)

	// Arrange: two contacts, one of which is opted in.
	assertGolden(t, "contact_create", call(t, handler, http.MethodPost, "/api/v2/contact",
		`{"key_id":"3","contacts":[
			{"3":"ada@example.com","1":"Ada","2":"Lovelace","31":"1"},
			{"3":"grace@example.com","1":"Grace","2":"Hopper"}
		 ]}`))

	assertGolden(t, "contact_create_duplicate", call(t, handler, http.MethodPost, "/api/v2/contact",
		`{"key_id":"3","contacts":[
			{"3":"ada@example.com","1":"Ada"},
			{"3":"alan@example.com","1":"Alan"}
		 ]}`))

	assertGolden(t, "contact_upsert", call(t, handler, http.MethodPut,
		"/api/v2/contact/?create_if_not_exists=1",
		`{"key_id":"3","contacts":[
			{"3":"ada@example.com","1":"Ada A."},
			{"3":"katherine@example.com","1":"Katherine"}
		 ]}`))

	assertGolden(t, "contact_update_missing", call(t, handler, http.MethodPut, "/api/v2/contact/",
		`{"key_id":"3","contacts":[{"3":"nobody@example.com","1":"Nobody"}]}`))

	assertGolden(t, "contact_getdata", call(t, handler, http.MethodPost, "/api/v2/contact/getdata",
		`{"keyId":3,"keyValues":["ada@example.com","nobody@example.com"],"fields":[1,31]}`))

	assertGolden(t, "contact_query", call(t, handler, http.MethodGet,
		"/api/v2/contact/query/?3=grace@example.com&return=1", ""))

	assertGolden(t, "contact_checkids", call(t, handler, http.MethodPost, "/api/v2/contact/checkids",
		`{"key_id":"3","external_ids":["ada@example.com","nobody@example.com"]}`))

	assertGolden(t, "contact_delete", call(t, handler, http.MethodPost, "/api/v2/contact/delete",
		`{"key_id":3,"3":["alan@example.com","nobody@example.com"]}`))

	assertGolden(t, "field_list", call(t, handler, http.MethodGet, "/api/v2/field", ""))
	assertGolden(t, "field_choice", call(t, handler, http.MethodGet, "/api/v2/field/31/choice", ""))
	assertGolden(t, "field_choices_bulk", call(t, handler, http.MethodGet,
		"/api/v2/field/choices?fields=5,31&language=en", ""))

	// Error shapes matter as much as success shapes.
	assertGolden(t, "error_invalid_key_field", call(t, handler, http.MethodPost, "/api/v2/contact",
		`{"key_id":"email","contacts":[{"3":"x@example.com"}]}`))
	assertGolden(t, "error_unindexed_query", call(t, handler, http.MethodGet,
		"/api/v2/contact/query/?1=Ada&return=3", ""))
	assertGolden(t, "error_no_return_field", call(t, handler, http.MethodGet,
		"/api/v2/contact/query/?3=ada@example.com", ""))
}
