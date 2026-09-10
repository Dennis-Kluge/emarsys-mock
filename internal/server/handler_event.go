package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
	"github.com/dennis-kluge/emarsys-mock/internal/webhook"
)

// handleEventList serves GET /api/v2/event.
func (s *Server) handleEventList(w http.ResponseWriter, r *http.Request) {
	events, err := s.db.Events(r.Context())
	if err != nil {
		s.internalError(w, "list events", err)
		return
	}
	out := make([]store.Event, 0, len(events))
	for _, e := range events {
		e.Created = s.localTime(e.Created)
		out = append(out, e)
	}
	api.OK(w, out)
}

// handleEventGet serves GET /api/v2/event/{eventId}.
func (s *Server) handleEventGet(w http.ResponseWriter, r *http.Request) {
	event, ok := s.lookupEvent(w, r)
	if !ok {
		return
	}
	event.Created = s.localTime(event.Created)
	api.OK(w, event)
}

// handleEventCreate serves POST /api/v2/event.
func (s *Server) handleEventCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		api.ErrorText(w, api.CodeMissingKeyField, "Event name must not be empty")
		return
	}

	event, err := s.db.CreateEvent(r.Context(), body.Name)
	if err != nil {
		if errors.Is(err, store.ErrEventExists) {
			api.ErrorText(w, api.CodeInternalError, "An event named "+body.Name+" already exists")
			return
		}
		s.internalError(w, "create event", err)
		return
	}
	api.OK(w, map[string]any{"id": event.ID, "name": event.Name})
}

// handleEventRename serves POST /api/v2/event/{eventId}.
func (s *Server) handleEventRename(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "eventId")
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		api.ErrorText(w, api.CodeMissingKeyField, "Event name must not be empty")
		return
	}

	event, err := s.db.RenameEvent(r.Context(), id, body.Name)
	switch {
	case err == nil:
		api.OK(w, map[string]any{"id": event.ID, "name": event.Name})
	case errors.Is(err, store.ErrNoEvent):
		s.unknownEvent(w, id)
	default:
		s.internalError(w, "rename event", err)
	}
}

// handleEventDelete serves POST /api/v2/event/{eventId}/delete.
func (s *Server) handleEventDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "eventId")
	if !ok {
		return
	}
	switch err := s.db.DeleteEvent(r.Context(), id); {
	case err == nil:
		api.OK(w, "")
	case errors.Is(err, store.ErrNoEvent):
		s.unknownEvent(w, id)
	default:
		s.internalError(w, "delete event", err)
	}
}

// handleEventUsages serves GET /api/v2/event/{eventId}/usages.
//
// The mock runs no programs and sends no e-mail, so an event is never in use.
// The endpoint exists so a client that checks before deleting gets an answer
// with the right shape rather than a 404.
func (s *Server) handleEventUsages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupEvent(w, r); !ok {
		return
	}
	api.OK(w, map[string]any{
		"program_ids":              []int64{},
		"email_ids":                []int64{},
		"interactions_program_ids": []string{},
	})
}

// triggerContact is one entry of the contacts array of a trigger request.
//
// event_time and trigger_id live inside each contact, not at the top level.
// Getting that wrong is silent: the fields are simply ignored.
type triggerContact struct {
	ExternalID json.RawMessage `json:"external_id"`
	EventTime  string          `json:"event_time"`
	TriggerID  string          `json:"trigger_id"`
	Attachment json.RawMessage `json:"attachment"`
}

// handleEventTrigger serves POST /api/v2/event/{eventId}/trigger.
//
// The mock records the trigger, acknowledges it, and optionally posts it to the
// configured webhook. What Emarsys would do afterwards -- evaluate a program,
// render a template, send an e-mail -- does not happen and is out of scope.
func (s *Server) handleEventTrigger(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "eventId")
	if !ok {
		return
	}
	event, err := s.db.Event(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNoEvent) {
			s.unknownEvent(w, id)
			return
		}
		s.internalError(w, "load event", err)
		return
	}

	var body struct {
		KeyID    flexibleInt                  `json:"key_id"`
		Contacts []map[string]json.RawMessage `json:"contacts"`
		Data     json.RawMessage              `json:"data"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	catalog, err := s.db.LoadFieldCatalog(r.Context())
	if err != nil {
		s.internalError(w, "load field catalog", err)
		return
	}
	keyFieldID, ok := s.resolveKeyField(w, body.KeyID, catalog)
	if !ok {
		return
	}
	if len(body.Contacts) == 0 {
		api.ErrorText(w, api.CodeMissingKeyField, "No contacts given")
		return
	}
	if len(body.Contacts) > s.cfg.MaxBatchContacts {
		api.ErrorText(w, api.CodeBatchTooLarge,
			"Limit of "+strconv.Itoa(s.cfg.MaxBatchContacts)+" contacts exceeded")
		return
	}

	failures := map[string]map[string]string{}

	for _, raw := range body.Contacts {
		entry, externalID := parseTriggerContact(raw, keyFieldID)
		if externalID == "" {
			failures[""] = map[string]string{
				strconv.Itoa(int(api.CodeMissingKeyField)): "Missing external_id for the key field " +
					keyFieldName(keyFieldID),
			}
			continue
		}

		ids, findErr := store.FindContactIDs(r.Context(), s.db.Read, keyFieldID, externalID)
		if findErr != nil {
			s.internalError(w, "find contacts", findErr)
			return
		}
		if len(ids) == 0 {
			failures[externalID] = map[string]string{
				strconv.Itoa(int(api.CodeNoContactFound)): "No contact found with the external id: " + externalID,
			}
			continue
		}
		if len(ids) > 1 {
			failures[externalID] = map[string]string{
				strconv.Itoa(int(api.CodeMultipleContacts)): "More contacts found with the external ID: " + externalID,
			}
			continue
		}

		payload := mergeTriggerPayload(body.Data, entry.Attachment)
		contactID := ids[0]
		recorded, fresh, recErr := s.db.RecordTrigger(r.Context(), store.EventTrigger{
			EventID:    event.ID,
			ContactID:  &contactID,
			ExternalID: externalID,
			Payload:    string(payload),
			EventTime:  entry.EventTime,
			TriggerID:  entry.TriggerID,
		})
		if recErr != nil {
			s.internalError(w, "record trigger", recErr)
			return
		}

		// A repeated trigger_id is Emarsys' idempotency key: acknowledged, but
		// neither stored again nor announced again.
		if !fresh {
			continue
		}
		_ = recorded

		s.webhook.Send(webhook.TriggerPayload{
			Type:       "event.trigger",
			EventID:    event.ID,
			EventName:  event.Name,
			ContactID:  &contactID,
			ExternalID: externalID,
			KeyID:      keyFieldName(keyFieldID),
			Data:       payload,
			EventTime:  entry.EventTime,
			TriggerID:  entry.TriggerID,
			ReceivedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}

	api.OK(w, map[string]any{"errors": failures})
}

// parseTriggerContact pulls the per-contact control fields out of one entry and
// returns the external id, which may be given as "external_id" or under the
// numeric key field id.
func parseTriggerContact(raw map[string]json.RawMessage, keyFieldID int) (triggerContact, string) {
	var entry triggerContact
	if v, ok := raw["external_id"]; ok {
		entry.ExternalID = v
	}
	if v, ok := raw["event_time"]; ok {
		entry.EventTime = scalarToString(v)
	}
	if v, ok := raw["trigger_id"]; ok {
		entry.TriggerID = scalarToString(v)
	}
	if v, ok := raw["attachment"]; ok {
		entry.Attachment = v
	}

	externalID := scalarToString(entry.ExternalID)
	if externalID == "" {
		if v, ok := raw[keyFieldName(keyFieldID)]; ok {
			externalID = scalarToString(v)
		}
	}
	return entry, externalID
}

// mergeTriggerPayload combines the batch-level data object with a contact's own
// attachment, with the attachment winning on conflicting keys.
func mergeTriggerPayload(batch, attachment json.RawMessage) []byte {
	merged := map[string]any{}
	for _, source := range []json.RawMessage{batch, attachment} {
		if len(source) == 0 {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(source, &fields); err != nil {
			// A non-object attachment is kept verbatim under its own key rather
			// than dropped, so a test can still see what was sent.
			merged["attachment"] = json.RawMessage(source)
			continue
		}
		for k, v := range fields {
			merged[k] = v
		}
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return []byte("{}")
	}
	return out
}

func (s *Server) lookupEvent(w http.ResponseWriter, r *http.Request) (store.Event, bool) {
	id, ok := pathInt64(w, r, "eventId")
	if !ok {
		return store.Event{}, false
	}
	event, err := s.db.Event(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNoEvent) {
			s.unknownEvent(w, id)
			return store.Event{}, false
		}
		s.internalError(w, "load event", err)
		return store.Event{}, false
	}
	return event, true
}

func (s *Server) unknownEvent(w http.ResponseWriter, id int64) {
	api.ErrorText(w, api.CodeInternalError,
		"Unknown external event id: "+strconv.FormatInt(id, 10))
}

func pathInt64(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		api.ErrorText(w, api.CodeInternalError, "Invalid "+name+": "+raw)
		return 0, false
	}
	return id, true
}
