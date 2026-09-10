package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// contactBatchBody is the shared shape of POST and PUT /api/v2/contact.
type contactBatchBody struct {
	KeyID         flexibleInt                  `json:"key_id"`
	ContactListID *int64                       `json:"contact_list_id"`
	Contacts      []map[string]json.RawMessage `json:"contacts"`
}

// batchResult accumulates the response payload of a contact batch.
//
// The ids slice holds one entry per *successful* row, so it is shorter than the
// input whenever anything failed; the two are not index-aligned. Failures are
// keyed by the row's key value, which is why two rows sharing a key value can
// only report one error between them.
type batchResult struct {
	ids    []any
	errors map[string]map[string]string
}

func newBatchResult() *batchResult {
	return &batchResult{ids: []any{}, errors: map[string]map[string]string{}}
}

func (b *batchResult) fail(keyValue string, code api.ReplyCode, text string) {
	b.errors[keyValue] = map[string]string{strconv.Itoa(int(code)): text}
}

func (b *batchResult) payload() map[string]any {
	return map[string]any{"ids": b.ids, "errors": b.errors}
}

// handleContactCreate serves POST /api/v2/contact.
func (s *Server) handleContactCreate(w http.ResponseWriter, r *http.Request) {
	s.runContactBatch(w, r, false, false)
}

// handleContactUpdate serves PUT /api/v2/contact/?create_if_not_exists=0|1.
func (s *Server) handleContactUpdate(w http.ResponseWriter, r *http.Request) {
	create := r.URL.Query().Get("create_if_not_exists")
	s.runContactBatch(w, r, true, create == "1" || create == "true")
}

// runContactBatch implements create, update and upsert.
//
// The rule that matters most here: a batch where individual rows fail is still
// HTTP 200 with replyCode 0, and the failures live in data.errors. A client
// that only checks the top-level reply code reads that as complete success,
// which is precisely the bug these tests need to be able to reproduce.
func (s *Server) runContactBatch(w http.ResponseWriter, r *http.Request, isUpdate, createIfMissing bool) {
	var body contactBatchBody
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

	// Parse every row before touching the database: an unknown field id is a
	// request-level error, and reporting it after half the batch was written
	// would leave a state production could not produce.
	rows := make([]parsedRow, 0, len(body.Contacts))
	for _, raw := range body.Contacts {
		row, parseErr := parseRow(raw, catalog)
		if parseErr != nil {
			var unknown unknownFieldError
			if errors.As(parseErr, &unknown) {
				api.ErrorText(w, api.CodeInvalidFieldID, "Invalid field id: "+unknown.raw)
				return
			}
			s.internalError(w, "parse contact row", parseErr)
			return
		}
		rows = append(rows, row)
	}

	if body.ContactListID != nil {
		exists, listErr := store.ContactListExists(r.Context(), s.db.Read, *body.ContactListID)
		if listErr != nil {
			s.internalError(w, "check contact list", listErr)
			return
		}
		if !exists {
			api.ErrorText(w, api.CodeInternalError,
				"Unknown contact list id: "+strconv.FormatInt(*body.ContactListID, 10))
			return
		}
	}

	result := newBatchResult()
	var touched []int64

	err = s.db.WithTx(r.Context(), func(tx *sql.Tx) error {
		seen := map[string]bool{}

		for _, row := range rows {
			keyValue, present := row.keyValueOf(keyFieldID)
			if !present {
				result.fail("", api.CodeMissingKeyField,
					"Missing value for the key field "+keyFieldName(keyFieldID))
				continue
			}

			// A key value appearing twice in one batch is ambiguous: the two
			// rows would race to define the same contact.
			if seen[keyValue] {
				result.fail(keyValue, api.CodeMultipleContacts,
					"More contacts found with the external ID: "+keyValue+" (duplicated within the batch)")
				continue
			}
			seen[keyValue] = true

			if valErr := validateValues(row.values, catalog); valErr != nil {
				result.fail(keyValue, api.CodeInvalidFieldID, valErr.Error())
				continue
			}

			existing, findErr := store.FindContactIDs(r.Context(), tx, keyFieldID, keyValue)
			if findErr != nil {
				return findErr
			}
			if len(existing) > 1 {
				result.fail(keyValue, api.CodeMultipleContacts,
					"More contacts found with the external ID: "+keyValue)
				continue
			}

			switch {
			case len(existing) == 1 && !isUpdate:
				result.fail(keyValue, api.CodeContactExists,
					"Contact with the external id already exists: "+keyValue)

			case len(existing) == 1:
				if applyErr := store.ApplyContactValues(r.Context(), tx, existing[0], row.values, "api"); applyErr != nil {
					return applyErr
				}
				// An updated contact reports its id as a string, while a newly
				// created one reports an integer. The asymmetry is production's,
				// not ours, and clients that assume one type break on the other.
				result.ids = append(result.ids, strconv.FormatInt(existing[0], 10))
				touched = append(touched, existing[0])

			case isUpdate && !createIfMissing:
				result.fail(keyValue, api.CodeNoContactFound,
					"No contact found with the external id: "+keyValue)

			default:
				values := row.values
				if keyFieldID >= 0 {
					// Make sure the key value itself is stored even when the
					// caller only put it in the addressing position.
					if _, ok := values[keyFieldID]; !ok {
						values[keyFieldID] = keyValue
					}
				}
				id, createErr := store.CreateContact(r.Context(), tx, values, "api")
				if createErr != nil {
					return createErr
				}
				result.ids = append(result.ids, id)
				touched = append(touched, id)
			}
		}

		if body.ContactListID != nil && len(touched) > 0 {
			return store.AddContactsToList(r.Context(), tx, *body.ContactListID, touched)
		}
		return nil
	})
	if err != nil {
		s.internalError(w, "run contact batch", err)
		return
	}

	api.OK(w, result.payload())
}

// resolveKeyField turns the request's key_id into a field id.
//
// key_id names a field by its numeric id, never by its name: "3" is e-mail and
// "email" is an error. The two internal identifiers "id" and "uid" are the only
// non-numeric values production accepts.
func (s *Server) resolveKeyField(w http.ResponseWriter, raw flexibleInt, catalog *store.FieldCatalog) (int, bool) {
	if !raw.Set || raw.Raw == "" {
		api.ErrorText(w, api.CodeMissingKeyField, "Missing key field: key_id")
		return 0, false
	}
	switch raw.Raw {
	case "id":
		return store.KeyFieldInternalID, true
	case "uid":
		return store.KeyFieldUID, true
	}
	if !raw.IsNumeric() {
		api.ErrorText(w, api.CodeInvalidKeyFieldID,
			"Invalid key field id: "+raw.Raw+" (key_id must be a numeric field id, not a field name)")
		return 0, false
	}
	if !catalog.Has(raw.Value) {
		api.ErrorText(w, api.CodeInvalidKeyFieldID, "Invalid key field id: "+raw.Raw)
		return 0, false
	}
	return raw.Value, true
}

func keyFieldName(keyFieldID int) string {
	switch keyFieldID {
	case store.KeyFieldInternalID:
		return "id"
	case store.KeyFieldUID:
		return "uid"
	default:
		return strconv.Itoa(keyFieldID)
	}
}
