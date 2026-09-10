package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// listMembershipBody is the shared shape of add, delete and replace.
type listMembershipBody struct {
	KeyID       flexibleInt     `json:"key_id"`
	ExternalIDs flexibleStrings `json:"external_ids"`
}

// handleContactListCreate serves POST /api/v2/contactlist.
func (s *Server) handleContactListCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		KeyID       flexibleInt     `json:"key_id"`
		ExternalIDs flexibleStrings `json:"external_ids"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		api.ErrorText(w, api.CodeMissingKeyField, "Contact list name must not be empty")
		return
	}

	listID, err := s.db.CreateContactList(r.Context(), body.Name)
	if err != nil {
		s.internalError(w, "create contact list", err)
		return
	}

	// A list can be populated at creation time, in which case unknown contacts
	// are reported the same way a later add would report them.
	failures := map[string]map[string]string{}
	if len(body.ExternalIDs.Values) > 0 {
		ids, resolveErr := s.resolveExternalIDs(w, r, body.KeyID, body.ExternalIDs.Values, failures)
		if resolveErr {
			return
		}
		if txErr := s.db.WithTx(r.Context(), func(tx *sql.Tx) error {
			_, addErr := store.AddToListCounting(r.Context(), tx, listID, ids)
			return addErr
		}); txErr != nil {
			s.internalError(w, "populate contact list", txErr)
			return
		}
	}

	api.OK(w, map[string]any{"id": listID, "errors": failures})
}

// handleContactListList serves GET /api/v2/contactlist.
func (s *Server) handleContactListList(w http.ResponseWriter, r *http.Request) {
	lists, err := s.db.ContactLists(r.Context())
	if err != nil {
		s.internalError(w, "list contact lists", err)
		return
	}
	out := make([]map[string]any, 0, len(lists))
	for _, l := range lists {
		out = append(out, map[string]any{"id": l.ID, "name": l.Name})
	}
	api.OK(w, out)
}

// handleContactListMembers serves GET /api/v2/contactlist/{listId}/, which
// returns bare contact ids.
func (s *Server) handleContactListMembers(w http.ResponseWriter, r *http.Request) {
	listID, ok := s.lookupList(w, r)
	if !ok {
		return
	}
	limit, offset := s.paging(r)

	ids, err := s.db.ListMembers(r.Context(), listID, limit, offset)
	if err != nil {
		s.internalError(w, "read list members", err)
		return
	}
	api.OK(w, ids)
}

// handleContactListCount serves GET /api/v2/contactlist/{listId}/count, whose
// payload is a bare number rather than an object.
func (s *Server) handleContactListCount(w http.ResponseWriter, r *http.Request) {
	listID, ok := s.lookupList(w, r)
	if !ok {
		return
	}
	count, err := s.db.ListMemberCount(r.Context(), listID)
	if err != nil {
		s.internalError(w, "count list members", err)
		return
	}
	api.OK(w, count)
}

// handleContactListData serves GET /api/v2/contactlist/{listId}/contacts/data.
//
// The payload is keyed by contact id, with the values nested one level deeper
// under "fields".
func (s *Server) handleContactListData(w http.ResponseWriter, r *http.Request) {
	listID, ok := s.lookupList(w, r)
	if !ok {
		return
	}
	catalog, err := s.db.LoadFieldCatalog(r.Context())
	if err != nil {
		s.internalError(w, "load field catalog", err)
		return
	}

	var fieldIDs []int
	if raw := strings.TrimSpace(r.URL.Query().Get("fields")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			id, convErr := strconv.Atoi(strings.TrimSpace(part))
			if convErr != nil || !catalog.Has(id) {
				api.ErrorText(w, api.CodeInvalidFieldID, "Invalid field id: "+strings.TrimSpace(part))
				return
			}
			fieldIDs = append(fieldIDs, id)
		}
	}
	if len(fieldIDs) == 0 {
		api.Error(w, api.CodeNoFieldToReturn)
		return
	}

	limit, offset := s.paging(r)
	ids, err := s.db.ListMembers(r.Context(), listID, limit, offset)
	if err != nil {
		s.internalError(w, "read list members", err)
		return
	}
	contacts, err := store.LoadContacts(r.Context(), s.db.Read, ids, fieldIDs)
	if err != nil {
		s.internalError(w, "load contacts", err)
		return
	}

	out := map[string]any{}
	for _, id := range ids {
		contact, found := contacts[id]
		if !found {
			continue
		}
		out[strconv.FormatInt(id, 10)] = map[string]any{"fields": contactRowJSON(contact, fieldIDs)}
	}
	api.OK(w, out)
}

// handleContactListAdd serves POST /api/v2/contactlist/{listId}/add.
func (s *Server) handleContactListAdd(w http.ResponseWriter, r *http.Request) {
	s.changeListMembership(w, r, "add")
}

// handleContactListRemove serves POST /api/v2/contactlist/{listId}/delete,
// which removes contacts from the list rather than deleting the list.
func (s *Server) handleContactListRemove(w http.ResponseWriter, r *http.Request) {
	s.changeListMembership(w, r, "remove")
}

// handleContactListReplace serves POST /api/v2/contactlist/{listId}/replace.
func (s *Server) handleContactListReplace(w http.ResponseWriter, r *http.Request) {
	s.changeListMembership(w, r, "replace")
}

func (s *Server) changeListMembership(w http.ResponseWriter, r *http.Request, mode string) {
	listID, ok := s.lookupList(w, r)
	if !ok {
		return
	}
	var body listMembershipBody
	if !decodeJSON(w, r, &body) {
		return
	}

	failures := map[string]map[string]string{}
	ids, bail := s.resolveExternalIDs(w, r, body.KeyID, body.ExternalIDs.Values, failures)
	if bail {
		return
	}

	var changed int
	err := s.db.WithTx(r.Context(), func(tx *sql.Tx) error {
		switch mode {
		case "remove":
			n, err := store.RemoveFromList(r.Context(), tx, listID, ids)
			changed = n
			return err
		case "replace":
			if err := store.ClearList(r.Context(), tx, listID); err != nil {
				return err
			}
			n, err := store.AddToListCounting(r.Context(), tx, listID, ids)
			changed = n
			return err
		default:
			n, err := store.AddToListCounting(r.Context(), tx, listID, ids)
			changed = n
			return err
		}
	})
	if err != nil {
		s.internalError(w, "change list membership", err)
		return
	}

	countKey := "inserted_contacts"
	if mode == "remove" {
		countKey = "deleted_contacts"
	}
	api.OK(w, map[string]any{countKey: changed, "errors": failures})
}

// handleContactListRename serves POST /api/v2/contactlist/{listId}/rename.
func (s *Server) handleContactListRename(w http.ResponseWriter, r *http.Request) {
	listID, ok := s.lookupList(w, r)
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
		api.ErrorText(w, api.CodeMissingKeyField, "Contact list name must not be empty")
		return
	}
	if err := s.db.RenameContactList(r.Context(), listID, body.Name); err != nil {
		s.internalError(w, "rename contact list", err)
		return
	}
	api.OK(w, map[string]any{"id": listID, "name": body.Name})
}

// handleContactListDelete serves POST /api/v2/contactlist/{listId}/deletelist.
func (s *Server) handleContactListDelete(w http.ResponseWriter, r *http.Request) {
	listID, ok := s.lookupList(w, r)
	if !ok {
		return
	}
	if err := s.db.DeleteContactList(r.Context(), listID); err != nil {
		s.internalError(w, "delete contact list", err)
		return
	}
	api.OK(w, "")
}

// resolveExternalIDs maps key values to contact ids, recording unresolvable
// ones in failures. The bool reports whether a hard error was already written.
func (s *Server) resolveExternalIDs(w http.ResponseWriter, r *http.Request, keyID flexibleInt, values []string, failures map[string]map[string]string) ([]int64, bool) {
	catalog, err := s.db.LoadFieldCatalog(r.Context())
	if err != nil {
		s.internalError(w, "load field catalog", err)
		return nil, true
	}
	keyFieldID, ok := s.resolveKeyField(w, keyID, catalog)
	if !ok {
		return nil, true
	}
	if len(values) > s.cfg.MaxBatchContacts {
		api.Error(w, api.CodeExternalIDsTooBig)
		return nil, true
	}

	var ids []int64
	for _, value := range values {
		found, findErr := store.FindContactIDs(r.Context(), s.db.Read, keyFieldID, value)
		if findErr != nil {
			s.internalError(w, "find contacts", findErr)
			return nil, true
		}
		switch {
		case len(found) == 0:
			failures[value] = map[string]string{
				strconv.Itoa(int(api.CodeNoContactFound)): "No contact found with the external id: " + value,
			}
		case len(found) > 1:
			failures[value] = map[string]string{
				strconv.Itoa(int(api.CodeMultipleContacts)): "More contacts found with the external ID: " + value,
			}
		default:
			ids = append(ids, found[0])
		}
	}
	return ids, false
}

func (s *Server) lookupList(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := pathInt64(w, r, "listId")
	if !ok {
		return 0, false
	}
	if _, err := s.db.ContactList(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNoList) {
			api.ErrorText(w, api.CodeInternalError,
				"Unknown contact list id: "+strconv.FormatInt(id, 10))
			return 0, false
		}
		s.internalError(w, "load contact list", err)
		return 0, false
	}
	return id, true
}

// paging reads the limit and offset parameters shared by the list endpoints.
func (s *Server) paging(r *http.Request) (limit, offset int) {
	limit = 10000
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}
