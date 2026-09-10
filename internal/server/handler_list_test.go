package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestContactListLifecycle(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[
		{"3":"a@example.com","1":"Ann"},
		{"3":"b@example.com","1":"Bob"},
		{"3":"c@example.com","1":"Cid"}
	]}`)

	env := call(t, handler, http.MethodPost, "/api/v2/contactlist",
		`{"name":"Autumn campaign","key_id":"3","external_ids":["a@example.com","nope@example.com"]}`).
		expectOK(t)

	var created struct {
		ID     int64                        `json:"id"`
		Errors map[string]map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(env.Data, &created); err != nil {
		t.Fatalf("decode: %v (data %s)", err, env.Data)
	}
	if created.ID == 0 {
		t.Fatal("list has no id")
	}
	if _, ok := created.Errors["nope@example.com"]["2008"]; !ok {
		t.Errorf("errors = %v, want 2008 for the unknown contact", created.Errors)
	}
	list := strconv.FormatInt(created.ID, 10)

	t.Run("add reports how many were new", func(t *testing.T) {
		// a@example.com is already on the list, so only b counts as inserted.
		env := call(t, handler, http.MethodPost, "/api/v2/contactlist/"+list+"/add",
			`{"key_id":"3","external_ids":["a@example.com","b@example.com"]}`).expectOK(t)

		var payload struct {
			Inserted int `json:"inserted_contacts"`
		}
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Inserted != 1 {
			t.Errorf("inserted_contacts = %d, want 1", payload.Inserted)
		}
	})

	t.Run("count is a bare number", func(t *testing.T) {
		env := call(t, handler, http.MethodGet, "/api/v2/contactlist/"+list+"/count", "").expectOK(t)
		var count int
		if err := json.Unmarshal(env.Data, &count); err != nil {
			t.Fatalf("count payload is not a number: %s", env.Data)
		}
		if count != 2 {
			t.Errorf("count = %d, want 2", count)
		}
	})

	t.Run("members are bare contact ids", func(t *testing.T) {
		env := call(t, handler, http.MethodGet, "/api/v2/contactlist/"+list+"/", "").expectOK(t)
		var ids []int64
		if err := json.Unmarshal(env.Data, &ids); err != nil {
			t.Fatalf("members payload is not an id array: %s", env.Data)
		}
		if len(ids) != 2 {
			t.Errorf("members = %v, want two", ids)
		}
	})

	t.Run("contact data is nested under fields", func(t *testing.T) {
		env := call(t, handler, http.MethodGet,
			"/api/v2/contactlist/"+list+"/contacts/data?fields=1,3", "").expectOK(t)

		var payload map[string]struct {
			Fields map[string]json.RawMessage `json:"fields"`
		}
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("decode: %v (data %s)", err, env.Data)
		}
		if len(payload) != 2 {
			t.Fatalf("payload has %d contacts, want 2: %s", len(payload), env.Data)
		}
		for id, entry := range payload {
			if _, ok := entry.Fields["1"]; !ok {
				t.Errorf("contact %s is missing field 1: %v", id, entry.Fields)
			}
			if _, ok := entry.Fields["id"]; !ok {
				t.Errorf("contact %s is missing its id: %v", id, entry.Fields)
			}
		}
	})

	t.Run("replace swaps the whole membership", func(t *testing.T) {
		call(t, handler, http.MethodPost, "/api/v2/contactlist/"+list+"/replace",
			`{"key_id":"3","external_ids":["c@example.com"]}`).expectOK(t)

		env := call(t, handler, http.MethodGet, "/api/v2/contactlist/"+list+"/count", "").expectOK(t)
		var count int
		json.Unmarshal(env.Data, &count)
		if count != 1 {
			t.Errorf("count after replace = %d, want 1", count)
		}
	})

	t.Run("delete removes contacts from the list, not the list", func(t *testing.T) {
		env := call(t, handler, http.MethodPost, "/api/v2/contactlist/"+list+"/delete",
			`{"key_id":"3","external_ids":["c@example.com"]}`).expectOK(t)

		var payload struct {
			Deleted int `json:"deleted_contacts"`
		}
		if err := json.Unmarshal(env.Data, &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Deleted != 1 {
			t.Errorf("deleted_contacts = %d, want 1", payload.Deleted)
		}
		// The list itself must still be there.
		call(t, handler, http.MethodGet, "/api/v2/contactlist/"+list+"/count", "").expectOK(t)
	})

	t.Run("rename", func(t *testing.T) {
		call(t, handler, http.MethodPost, "/api/v2/contactlist/"+list+"/rename",
			`{"name":"Winter campaign"}`).expectOK(t)

		env := call(t, handler, http.MethodGet, "/api/v2/contactlist", "").expectOK(t)
		var lists []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(env.Data, &lists); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, l := range lists {
			if l.ID == created.ID && l.Name != "Winter campaign" {
				t.Errorf("name = %q after rename", l.Name)
			}
		}
	})

	t.Run("deletelist removes the list itself", func(t *testing.T) {
		call(t, handler, http.MethodPost, "/api/v2/contactlist/"+list+"/deletelist", "{}").expectOK(t)
		call(t, handler, http.MethodGet, "/api/v2/contactlist/"+list+"/count", "").expectError(t, 2011)
	})
}

func TestContactListDataRequiresFields(t *testing.T) {
	handler, _ := newTestServer(t)
	call(t, handler, http.MethodGet, "/api/v2/contactlist/1/contacts/data", "").expectError(t, 2014)
}

func TestUnknownContactListIsRejected(t *testing.T) {
	handler, _ := newTestServer(t)
	for _, target := range []string{
		"/api/v2/contactlist/9999/",
		"/api/v2/contactlist/9999/count",
	} {
		call(t, handler, http.MethodGet, target, "").expectError(t, 2011)
	}
	call(t, handler, http.MethodPost, "/api/v2/contactlist/9999/add",
		`{"key_id":"3","external_ids":["a@example.com"]}`).expectError(t, 2011)
}

func TestContactListExportUsesTheSameJobMachinery(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler,
		`{"key_id":"3","contact_list_id":1,"contacts":[{"3":"listed@example.com","1":"Jane"}]}`)

	env := call(t, handler, http.MethodPost, "/api/v2/contactlist/1/export",
		`{"contact_fields":[1,3],"add_field_names_header":1}`).expectOK(t)

	var payload struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	id := strconv.FormatInt(payload.ID, 10)

	for range 3 {
		pollExport(t, handler, id)
	}
	body := rawGet(t, handler, "/api/v2/export/"+id+"/data")
	if !strings.Contains(body, "listed@example.com") {
		t.Errorf("csv = %q", body)
	}
}
