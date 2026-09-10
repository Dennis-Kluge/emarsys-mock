package record

import (
	"encoding/json"
	"strings"
	"testing"
)

func exchange(method, path, status string, request, response string) Exchange {
	e := Exchange{Method: method, Path: path, Redaction: ModeShapes}
	if request != "" {
		e.RequestBody = json.RawMessage(request)
	}
	if response != "" {
		e.ResponseBody = json.RawMessage(response)
	}
	switch status {
	case "200":
		e.Status = 200
	case "400":
		e.Status = 400
	}
	return e
}

// TestMixedTypesAreSurfaced is the finding a recording exists to produce: an
// API that answers with a string sometimes and an integer other times breaks
// whichever client saw only one of them.
func TestMixedTypesAreSurfaced(t *testing.T) {
	exchanges := []Exchange{
		exchange("PUT", "/v2/contact", "200", "", `{"data":{"ids":[1,2]}}`),
		exchange("PUT", "/v2/contact", "200", "", `{"data":{"ids":["3","4"]}}`),
	}

	report := Report(Summarize(exchanges))

	if !strings.Contains(report, "more than one JSON type") {
		t.Fatalf("the report does not flag the inconsistency:\n%s", report)
	}
	if !strings.Contains(report, "integer | string") {
		t.Errorf("the mixed types are not named:\n%s", report)
	}
	if !strings.Contains(report, "response.data.ids[]") {
		t.Errorf("the report does not say where:\n%s", report)
	}
}

func TestNullIsNotAnInconsistency(t *testing.T) {
	// An absent value is normal. Reporting it as a type conflict would bury the
	// real conflicts in noise.
	exchanges := []Exchange{
		exchange("GET", "/v2/field", "200", "", `{"data":{"name":"E-Mail"}}`),
		exchange("GET", "/v2/field", "200", "", `{"data":{"name":null}}`),
	}
	if report := Report(Summarize(exchanges)); strings.Contains(report, "more than one JSON type") {
		t.Errorf("null was reported as a conflict:\n%s", report)
	}
}

func TestOptionalFieldsAreMarked(t *testing.T) {
	// Which fields are optional is half of writing a handler.
	exchanges := []Exchange{
		exchange("POST", "/v2/contact", "200", `{"key_id":"3","contacts":[]}`, ""),
		exchange("POST", "/v2/contact", "200", `{"key_id":"3","contacts":[],"contact_list_id":1}`, ""),
	}

	report := Report(Summarize(exchanges))
	if !strings.Contains(report, "contact_list_id") {
		t.Fatalf("the optional field is missing:\n%s", report)
	}
	if !strings.Contains(report, "optional, 1/2") {
		t.Errorf("the field is not marked optional:\n%s", report)
	}
}

func TestPathsWithIDsGroupTogether(t *testing.T) {
	// Fifty calls to fifty contacts are one endpoint, not fifty.
	exchanges := []Exchange{
		exchange("POST", "/v2/event/1/trigger", "200", `{"key_id":"3"}`, ""),
		exchange("POST", "/v2/event/2/trigger", "200", `{"key_id":"3"}`, ""),
		exchange("POST", "/v2/event/99/trigger", "400", `{"key_id":"3"}`, ""),
	}

	endpoints := Summarize(exchanges)
	if len(endpoints) != 1 {
		t.Fatalf("got %d endpoints, want them grouped into one: %+v", len(endpoints), endpoints)
	}
	if endpoints[0].Template != "/v2/event/{id}/trigger" {
		t.Errorf("template = %q", endpoints[0].Template)
	}
	if endpoints[0].Calls != 3 {
		t.Errorf("calls = %d, want 3", endpoints[0].Calls)
	}
	// Both outcomes have to show, or a recording only ever documents success.
	if endpoints[0].Statuses[200] != 2 || endpoints[0].Statuses[400] != 1 {
		t.Errorf("statuses = %v", endpoints[0].Statuses)
	}
}

func TestPathTemplate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/api/v2/contact", "/api/v2/contact"},
		{"/api/v2/event/17/trigger", "/api/v2/event/{id}/trigger"},
		{"/api/v2/export/4/data", "/api/v2/export/{id}/data"},
		{"/api/v2/field/31/choice", "/api/v2/field/{id}/choice"},
	}
	for _, tc := range cases {
		if got := PathTemplate(tc.in); got != tc.want {
			t.Errorf("PathTemplate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIntegersAndFloatsAreDistinguished(t *testing.T) {
	// Whether a field is an integer or a decimal decides the type a client
	// declares for it.
	shape := newShape()
	shape.Observe(map[string]any{"id": float64(42), "score": 4.5})

	report := shape.Describe("", 0)
	if !strings.Contains(report, "id: integer") {
		t.Errorf("id was not recognised as an integer:\n%s", report)
	}
	if !strings.Contains(report, "score: number") {
		t.Errorf("score was not recognised as a decimal:\n%s", report)
	}
}
