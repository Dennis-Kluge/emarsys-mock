package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// handleContactGetData serves POST /api/v2/contact/getdata.
//
// Note the casing: this endpoint takes keyId and keyValues while create and
// update take key_id. The inconsistency is production's and a client that
// normalises it gets an empty result instead of an error.
func (s *Server) handleContactGetData(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ContactListID *int64            `json:"contact_list_id"`
		KeyID         flexibleInt       `json:"keyId"`
		KeyValues     flexibleStrings   `json:"keyValues"`
		Fields        []json.RawMessage `json:"fields"`
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
	if len(body.Fields) == 0 {
		api.Error(w, api.CodeNoFieldToReturn)
		return
	}
	fieldIDs, ok := s.resolveFieldList(w, body.Fields, catalog)
	if !ok {
		return
	}
	if !body.KeyValues.Set || !body.KeyValues.IsList {
		api.Error(w, api.CodeExternalIDsType)
		return
	}
	if len(body.KeyValues.Values) > s.cfg.MaxBatchContacts {
		api.Error(w, api.CodeExternalIDsTooBig)
		return
	}

	result := []map[string]any{}
	failures := map[string]map[string]string{}

	for _, keyValue := range body.KeyValues.Values {
		ids, findErr := store.FindContactIDs(r.Context(), s.db.Read, keyFieldID, keyValue)
		if findErr != nil {
			s.internalError(w, "find contacts", findErr)
			return
		}
		switch {
		case len(ids) == 0:
			failures[keyValue] = map[string]string{
				strconv.Itoa(int(api.CodeNoContactFound)): "No contact found with the external id: " + keyValue,
			}
			continue
		case len(ids) > 1:
			failures[keyValue] = map[string]string{
				strconv.Itoa(int(api.CodeMultipleContacts)): "More contacts found with the external ID: " + keyValue,
			}
			continue
		}

		contacts, loadErr := store.LoadContacts(r.Context(), s.db.Read, ids, fieldIDs)
		if loadErr != nil {
			s.internalError(w, "load contacts", loadErr)
			return
		}
		contact, found := contacts[ids[0]]
		if !found {
			continue
		}
		result = append(result, contactRowJSON(contact, fieldIDs))
	}

	api.OK(w, map[string]any{"result": result, "errors": failures})
}

// contactRowJSON renders one contact the way getdata does: the internal id and
// uid alongside the requested fields, each keyed by its field id as a string.
func contactRowJSON(contact *store.ContactRow, fieldIDs []int) map[string]any {
	row := map[string]any{
		"id":  contact.ID,
		"uid": contact.UID,
	}
	for _, id := range fieldIDs {
		row[strconv.Itoa(id)] = contact.Values[id]
	}
	return row
}

// queryReservedParams are the query-string keys that are not field selectors.
var queryReservedParams = map[string]bool{
	"return": true, "limit": true, "offset": true, "excludeempty": true,
}

// handleContactQuery serves GET /api/v2/contact/query/?{fieldId}=value&return={fieldId}.
//
// Unlike getdata this only works on indexed fields, which is a provisioning
// decision in production and a per-field flag here.
func (s *Server) handleContactQuery(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.db.LoadFieldCatalog(r.Context())
	if err != nil {
		s.internalError(w, "load field catalog", err)
		return
	}
	params := r.URL.Query()

	var (
		queryFieldRaw string
		queryValue    string
		selectors     int
	)
	for key, values := range params {
		if queryReservedParams[strings.ToLower(key)] {
			continue
		}
		selectors++
		queryFieldRaw = key
		if len(values) > 0 {
			queryValue = values[0]
		}
	}
	if selectors != 1 {
		api.ErrorText(w, api.CodeMissingKeyField,
			"Exactly one field must be given to query on, got "+strconv.Itoa(selectors))
		return
	}

	queryFieldID, convErr := strconv.Atoi(queryFieldRaw)
	if convErr != nil || !catalog.Has(queryFieldID) {
		api.ErrorText(w, api.CodeInvalidFieldID, "Invalid field id: "+queryFieldRaw)
		return
	}
	field, _ := catalog.Get(queryFieldID)
	if !field.IsIndexed {
		api.ErrorText(w, api.CodeNoIndexOnColumn,
			"No index on the column: "+field.Name+" (field "+queryFieldRaw+")")
		return
	}

	returnRaw := params.Get("return")
	if strings.TrimSpace(returnRaw) == "" {
		api.Error(w, api.CodeNoFieldToReturn)
		return
	}
	returnFieldID, convErr := strconv.Atoi(returnRaw)
	if convErr != nil || !catalog.Has(returnFieldID) {
		api.ErrorText(w, api.CodeInvalidFieldID, "Invalid field id: "+returnRaw)
		return
	}

	limit, ok := s.queryLimit(w, params.Get("limit"))
	if !ok {
		return
	}
	offset := 0
	if raw := params.Get("offset"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			offset = n
		}
	}
	excludeEmpty := params.Get("excludeempty") == "true" || params.Get("excludeempty") == "1"

	ids, err := store.QueryContactsByField(r.Context(), s.db.Read, queryFieldID, queryValue, excludeEmpty, limit, offset)
	if err != nil {
		s.internalError(w, "query contacts", err)
		return
	}
	contacts, err := store.LoadContacts(r.Context(), s.db.Read, ids, []int{returnFieldID})
	if err != nil {
		s.internalError(w, "load contacts", err)
		return
	}

	result := []map[string]any{}
	for _, id := range ids {
		contact, found := contacts[id]
		if !found {
			continue
		}
		result = append(result, map[string]any{
			strconv.Itoa(returnFieldID): contact.Values[returnFieldID],
			"id":                        contact.ID,
		})
	}
	api.OK(w, map[string]any{"result": result})
}

// queryLimit validates the limit parameter. Emarsys caps it at 10000 and
// answers replyCode 2016 for anything outside that.
func (s *Server) queryLimit(w http.ResponseWriter, raw string) (int, bool) {
	const maxLimit = 10000
	if strings.TrimSpace(raw) == "" {
		return maxLimit, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maxLimit {
		api.Error(w, api.CodeInvalidLimit)
		return 0, false
	}
	return limit, true
}

// handleContactCheckIDs serves POST /api/v2/contact/checkids and the legacy
// /getid path.
func (s *Server) handleContactCheckIDs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeyID          flexibleInt     `json:"key_id"`
		ExternalIDs    flexibleStrings `json:"external_ids"`
		KeyValues      flexibleStrings `json:"key_values"`
		KeyValue       flexibleStrings `json:"key_value"`
		GetMultipleIDs bool            `json:"get_multiple_ids"`
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

	// The documented parameter is external_ids; key_values and key_value are
	// accepted because the older /getid path uses them.
	values := body.ExternalIDs.Values
	if len(values) == 0 {
		values = body.KeyValues.Values
	}
	if len(values) == 0 {
		values = body.KeyValue.Values
	}
	if len(values) > s.cfg.MaxBatchContacts {
		api.Error(w, api.CodeExternalIDsTooBig)
		return
	}

	// ids is an object keyed by external id, not an array.
	ids := map[string]any{}
	failures := map[string]map[string]string{}

	for _, keyValue := range values {
		found, findErr := store.FindContactIDs(r.Context(), s.db.Read, keyFieldID, keyValue)
		if findErr != nil {
			s.internalError(w, "find contacts", findErr)
			return
		}
		switch {
		case len(found) == 0:
			failures[keyValue] = map[string]string{
				strconv.Itoa(int(api.CodeNoContactFound)): "No contact found with the external id: " + keyValue,
			}
		case len(found) > 1 && !body.GetMultipleIDs:
			failures[keyValue] = map[string]string{
				strconv.Itoa(int(api.CodeMultipleContacts)): "More contacts found with the external ID: " + keyValue,
			}
		case body.GetMultipleIDs:
			ids[keyValue] = found
		default:
			ids[keyValue] = found[0]
		}
	}

	api.OK(w, map[string]any{"ids": ids, "errors": failures})
}

// handleContactDelete serves POST /api/v2/contact/delete.
//
// The request shape is unusual: the key values arrive under a JSON key whose
// name is the key field id, so {"key_id":3,"3":["jane@example.com"]}.
func (s *Server) handleContactDelete(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return
	}

	catalog, err := s.db.LoadFieldCatalog(r.Context())
	if err != nil {
		s.internalError(w, "load field catalog", err)
		return
	}

	var keyID flexibleInt
	if v, ok := raw["key_id"]; ok {
		_ = keyID.UnmarshalJSON(v)
	}
	keyFieldID, ok := s.resolveKeyField(w, keyID, catalog)
	if !ok {
		return
	}

	var values flexibleStrings
	if v, ok := raw[keyFieldName(keyFieldID)]; ok {
		_ = values.UnmarshalJSON(v)
	}
	if len(values.Values) == 0 {
		api.ErrorText(w, api.CodeMissingKeyField,
			"No key values given under \""+keyFieldName(keyFieldID)+"\"")
		return
	}

	failures := map[string]map[string]string{}
	err = s.db.WithTx(r.Context(), func(tx *sql.Tx) error {
		for _, keyValue := range values.Values {
			ids, findErr := store.FindContactIDs(r.Context(), tx, keyFieldID, keyValue)
			if findErr != nil {
				return findErr
			}
			if len(ids) == 0 {
				failures[keyValue] = map[string]string{
					strconv.Itoa(int(api.CodeNoContactFound)): "No contact found with the external id: " + keyValue,
				}
				continue
			}
			for _, id := range ids {
				if delErr := store.DeleteContact(r.Context(), tx, id); delErr != nil {
					return delErr
				}
			}
		}
		return nil
	})
	if err != nil {
		s.internalError(w, "delete contacts", err)
		return
	}

	api.OK(w, map[string]any{"errors": failures})
}

// resolveFieldList validates a list of field ids from a request body.
func (s *Server) resolveFieldList(w http.ResponseWriter, raw []json.RawMessage, catalog *store.FieldCatalog) ([]int, bool) {
	out := make([]int, 0, len(raw))
	for _, item := range raw {
		asString := scalarToString(item)
		id, err := strconv.Atoi(strings.TrimSpace(asString))
		if err != nil || !catalog.Has(id) {
			api.ErrorText(w, api.CodeInvalidFieldID, "Invalid field id: "+asString)
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}
