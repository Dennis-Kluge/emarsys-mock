package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/dennis-kluge/emarsys-mock/internal/config"
	"github.com/dennis-kluge/emarsys-mock/internal/webhook"
)

func TestEventCRUD(t *testing.T) {
	handler, _ := newTestServer(t)

	env := call(t, handler, http.MethodPost, "/api/v2/event", `{"name":"cart_abandoned"}`).expectOK(t)
	var created struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(env.Data, &created); err != nil {
		t.Fatalf("decode: %v (data %s)", err, env.Data)
	}
	if created.ID == 0 || created.Name != "cart_abandoned" {
		t.Fatalf("created = %+v", created)
	}
	id := strconv.FormatInt(created.ID, 10)

	t.Run("duplicate name is refused", func(t *testing.T) {
		call(t, handler, http.MethodPost, "/api/v2/event", `{"name":"cart_abandoned"}`).
			expectError(t, 2011)
	})

	t.Run("list includes the new event", func(t *testing.T) {
		listEnv := call(t, handler, http.MethodGet, "/api/v2/event/", "").expectOK(t)
		var events []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Created string `json:"created"`
		}
		if err := json.Unmarshal(listEnv.Data, &events); err != nil {
			t.Fatalf("decode: %v", err)
		}
		found := false
		for _, e := range events {
			if e.ID == created.ID {
				found = true
				if e.Created == "" {
					t.Error("event has no created timestamp")
				}
			}
		}
		if !found {
			t.Errorf("event %d missing from the list", created.ID)
		}
	})

	t.Run("rename", func(t *testing.T) {
		call(t, handler, http.MethodPost, "/api/v2/event/"+id, `{"name":"cart_recovered"}`).expectOK(t)
		getEnv := call(t, handler, http.MethodGet, "/api/v2/event/"+id, "").expectOK(t)
		if !json.Valid(getEnv.Data) {
			t.Fatal("invalid data")
		}
		var got struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(getEnv.Data, &got); err != nil {
			t.Fatalf("decode: %v (data %s)", err, getEnv.Data)
		}
		if got.Name != "cart_recovered" {
			t.Errorf("name = %q after rename", got.Name)
		}
	})

	t.Run("usages are always empty", func(t *testing.T) {
		// The mock runs no programs, so nothing can ever be using an event.
		usageEnv := call(t, handler, http.MethodGet, "/api/v2/event/"+id+"/usages", "").expectOK(t)
		var usages struct {
			ProgramIDs []int64 `json:"program_ids"`
			EmailIDs   []int64 `json:"email_ids"`
		}
		if err := json.Unmarshal(usageEnv.Data, &usages); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(usages.ProgramIDs) != 0 || len(usages.EmailIDs) != 0 {
			t.Errorf("usages = %+v", usages)
		}
	})

	t.Run("delete", func(t *testing.T) {
		call(t, handler, http.MethodPost, "/api/v2/event/"+id+"/delete", "{}").expectOK(t)
		call(t, handler, http.MethodGet, "/api/v2/event/"+id, "").expectError(t, 2011)
	})
}

func TestEventTriggerRecordsPerContactControlFields(t *testing.T) {
	// event_time and trigger_id live inside each contacts[] entry, not at the
	// top level. A client that puts them at the top level gets no error at all,
	// so the mock has to store them from the right place to make that visible.
	handler, db := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"trigger@example.com"}]}`)

	call(t, handler, http.MethodPost, "/api/v2/event/1/trigger", `{
		"key_id":"3",
		"contacts":[{
			"external_id":"trigger@example.com",
			"event_time":"2026-09-10 12:00:00",
			"trigger_id":"order-4711",
			"attachment":{"order_total":"42.50"}
		}],
		"data":{"campaign":"autumn"}
	}`).expectOK(t)

	var eventTime, triggerID, payload string
	err := db.Read.QueryRow(
		`SELECT COALESCE(event_time,''), COALESCE(trigger_id,''), payload_json
		 FROM event_triggers ORDER BY id DESC LIMIT 1`).Scan(&eventTime, &triggerID, &payload)
	if err != nil {
		t.Fatalf("read trigger: %v", err)
	}
	if eventTime != "2026-09-10 12:00:00" {
		t.Errorf("event_time = %q", eventTime)
	}
	if triggerID != "order-4711" {
		t.Errorf("trigger_id = %q", triggerID)
	}
	// The batch-level data and the contact's own attachment are merged.
	for _, want := range []string{"order_total", "campaign"} {
		if !json.Valid([]byte(payload)) {
			t.Fatalf("payload is not JSON: %s", payload)
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(payload), &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields[want]; !ok {
			t.Errorf("payload is missing %q: %s", want, payload)
		}
	}
}

func TestEventTriggerDeduplicatesByTriggerID(t *testing.T) {
	// trigger_id is Emarsys' idempotency key: a client that retries after a
	// timeout must not cause a second send.
	handler, db := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"idem@example.com"}]}`)

	body := `{"key_id":"3","contacts":[{"external_id":"idem@example.com","trigger_id":"once"}]}`
	for range 3 {
		call(t, handler, http.MethodPost, "/api/v2/event/1/trigger", body).expectOK(t)
	}

	var n int
	if err := db.Read.QueryRow(
		`SELECT COUNT(*) FROM event_triggers WHERE trigger_id = 'once'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("stored %d triggers for one trigger_id, want 1", n)
	}
}

func TestEventTriggerReportsUnknownContacts(t *testing.T) {
	handler, _ := newTestServer(t)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"real@example.com"}]}`)

	env := call(t, handler, http.MethodPost, "/api/v2/event/1/trigger", `{
		"key_id":"3",
		"contacts":[{"external_id":"real@example.com"},{"external_id":"ghost@example.com"}]
	}`).expectOK(t)

	var payload struct {
		Errors map[string]map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("decode: %v (data %s)", err, env.Data)
	}
	if _, ok := payload.Errors["ghost@example.com"]["2008"]; !ok {
		t.Errorf("errors = %v, want 2008 for the unknown contact", payload.Errors)
	}
	if len(payload.Errors) != 1 {
		t.Errorf("errors = %v, want only the unknown contact to fail", payload.Errors)
	}
}

func TestEventTriggerRejectsAnUnknownEvent(t *testing.T) {
	handler, _ := newTestServer(t)
	call(t, handler, http.MethodPost, "/api/v2/event/9999/trigger",
		`{"key_id":"3","contacts":[{"external_id":"a@example.com"}]}`).expectError(t, 2011)
}

func TestEventTriggerPostsToTheWebhook(t *testing.T) {
	// The outbound webhook is what makes the full loop testable in CI without
	// pulling a cloud dependency into the mock.
	var (
		mu       sync.Mutex
		received []webhook.TriggerPayload
	)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload webhook.TriggerPayload
		if err := json.Unmarshal(body, &payload); err == nil {
			mu.Lock()
			received = append(received, payload)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	handler, _, srv := newConfiguredTestServer(t, func(c *config.Config) {
		c.WebhookURL = target.URL
	})
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"hook@example.com"}]}`)

	call(t, handler, http.MethodPost, "/api/v2/event/1/trigger", `{
		"key_id":"3",
		"contacts":[{"external_id":"hook@example.com","trigger_id":"hook-1"}],
		"data":{"sku":"A-1"}
	}`).expectOK(t)

	srv.Shutdown() // waits for in-flight deliveries

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("webhook received %d payloads, want 1", len(received))
	}
	got := received[0]
	if got.Type != "event.trigger" || got.ExternalID != "hook@example.com" {
		t.Errorf("payload = %+v", got)
	}
	if got.EventName == "" || got.TriggerID != "hook-1" {
		t.Errorf("payload = %+v", got)
	}
	if !json.Valid(got.Data) {
		t.Errorf("payload data is not JSON: %s", got.Data)
	}
}

func TestWebhookIsSilentWhenNotConfigured(t *testing.T) {
	handler, _, srv := newConfiguredTestServer(t, nil)
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"nohook@example.com"}]}`)

	call(t, handler, http.MethodPost, "/api/v2/event/1/trigger",
		`{"key_id":"3","contacts":[{"external_id":"nohook@example.com"}]}`).expectOK(t)
	srv.Shutdown()
}

func TestWebhookFailureDoesNotFailTheRequest(t *testing.T) {
	// A mock whose own notifications can break a client's request is worse than
	// one that sends none.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer target.Close()

	handler, _, srv := newConfiguredTestServer(t, func(c *config.Config) {
		c.WebhookURL = target.URL
	})
	createContact(t, handler, `{"key_id":"3","contacts":[{"3":"broken@example.com"}]}`)

	call(t, handler, http.MethodPost, "/api/v2/event/1/trigger",
		`{"key_id":"3","contacts":[{"external_id":"broken@example.com"}]}`).expectOK(t)
	srv.Shutdown()
}
