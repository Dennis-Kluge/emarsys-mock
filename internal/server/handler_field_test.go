package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

func TestFieldListShape(t *testing.T) {
	handler, _ := newTestServer(t)

	for _, target := range []string{"/api/v2/field", "/api/v2/field/", "/api/v2/field/translate/en"} {
		t.Run(target, func(t *testing.T) {
			env := call(t, handler, http.MethodGet, target, "").expectOK(t)

			var fields []fieldJSON
			if err := json.Unmarshal(env.Data, &fields); err != nil {
				t.Fatalf("decode: %v (data %s)", err, env.Data)
			}
			if len(fields) == 0 {
				t.Fatal("field list is empty")
			}

			byID := map[int]fieldJSON{}
			for _, f := range fields {
				byID[f.ID] = f
			}
			// Integrations hardcode these, so they have to be exactly here.
			if got := byID[3]; got.Name != "E-Mail" || got.ApplicationType != "shorttext" {
				t.Errorf("field 3 = %+v", got)
			}
			if got := byID[31]; got.Name != "Opt-in" || got.ApplicationType != "singlechoice" {
				t.Errorf("field 31 = %+v", got)
			}
		})
	}
}

func TestFieldCreateAndDelete(t *testing.T) {
	handler, _ := newTestServer(t)

	env := call(t, handler, http.MethodPost, "/api/v2/field",
		`{"name":"Loyalty tier","application_type":"shorttext"}`).expectOK(t)

	var created struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("created field has no id")
	}

	// The new field must be usable immediately, addressed by its numeric id.
	createContact(t, handler,
		`{"key_id":"3","contacts":[{"3":"tiered@example.com","`+strconv.Itoa(created.ID)+`":"gold"}]}`)
	if got := readField(t, handler, "tiered@example.com", created.ID); got != "gold" {
		t.Errorf("custom field value = %q, want gold", got)
	}

	call(t, handler, http.MethodDelete, "/api/v2/field/"+strconv.Itoa(created.ID), "").expectOK(t)
	call(t, handler, http.MethodDelete, "/api/v2/field/"+strconv.Itoa(created.ID), "").
		expectError(t, 2006)
}

func TestFieldCreateValidation(t *testing.T) {
	handler, _ := newTestServer(t)

	cases := []struct{ name, body string }{
		{"no name", `{"application_type":"shorttext"}`},
		{"blank name", `{"name":"   ","application_type":"shorttext"}`},
		{"unknown type", `{"name":"X","application_type":"quantum"}`},
		{"missing type", `{"name":"X"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call(t, handler, http.MethodPost, "/api/v2/field", tc.body).expectError(t, 2006)
		})
	}
}

func TestSystemFieldsCannotBeDeleted(t *testing.T) {
	// Deleting e-mail or opt-in would put the mock in a state no account can
	// reach and quietly break every test that follows.
	handler, _ := newTestServer(t)

	for _, id := range []string{"3", "31"} {
		env := call(t, handler, http.MethodDelete, "/api/v2/field/"+id, "").expectError(t, 2006)
		if env.ReplyText == "" {
			t.Error("reply text should explain why the delete was refused")
		}
	}
}

func TestFieldChoices(t *testing.T) {
	handler, _ := newTestServer(t)

	t.Run("per field", func(t *testing.T) {
		env := call(t, handler, http.MethodGet, "/api/v2/field/31/choice", "").expectOK(t)

		var choices []choiceJSON
		if err := json.Unmarshal(env.Data, &choices); err != nil {
			t.Fatalf("decode: %v (data %s)", err, env.Data)
		}
		if len(choices) != 2 {
			t.Fatalf("opt-in choices = %v, want two", choices)
		}
		if choices[0].Choice == "" {
			t.Error("choice label is empty")
		}
	})

	t.Run("translated variant", func(t *testing.T) {
		call(t, handler, http.MethodGet, "/api/v2/field/31/choice/translate/en", "").expectOK(t)
	})

	t.Run("bulk", func(t *testing.T) {
		env := call(t, handler, http.MethodGet, "/api/v2/field/choices?fields=5,31&language=en", "").
			expectOK(t)

		var byField map[string][]choiceJSON
		if err := json.Unmarshal(env.Data, &byField); err != nil {
			t.Fatalf("decode: %v (data %s)", err, env.Data)
		}
		if len(byField["5"]) != 2 || len(byField["31"]) != 2 {
			t.Errorf("choices = %v, want two per field", byField)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		call(t, handler, http.MethodGet, "/api/v2/field/424242/choice", "").expectError(t, 2006)
		call(t, handler, http.MethodGet, "/api/v2/field/choices?fields=424242", "").expectError(t, 2006)
	})
}

func TestCreatedFieldCanBeMadeQueryable(t *testing.T) {
	// contact/query is only allowed on indexed fields, which is a provisioning
	// decision in production. The mock exposes it at creation time so a test can
	// arrange both the working and the replyCode 2015 case.
	handler, _ := newTestServer(t)

	env := call(t, handler, http.MethodPost, "/api/v2/field",
		`{"name":"Customer number","application_type":"shorttext","indexed":true}`).expectOK(t)
	var created struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &created); err != nil {
		t.Fatal(err)
	}
	id := strconv.Itoa(created.ID)

	createContact(t, handler,
		`{"key_id":"3","contacts":[{"3":"cn@example.com","`+id+`":"C-1"}]}`)

	call(t, handler, http.MethodGet, "/api/v2/contact/query/?"+id+"=C-1&return=3", "").expectOK(t)
}
