package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// applicationTypes are the field types Emarsys accepts when a custom field is
// created. Anything else is rejected rather than silently stored, because a
// field with a nonsense type would never validate values the way production does.
var applicationTypes = map[string]bool{
	"shorttext":    true,
	"longtext":     true,
	"largetext":    true,
	"date":         true,
	"url":          true,
	"numeric":      true,
	"interests":    true,
	"singlechoice": true,
	"multichoice":  true,
}

type fieldJSON struct {
	ID              int    `json:"id"`
	Name            string `json:"name"`
	ApplicationType string `json:"application_type"`
	StringID        string `json:"string_id"`
}

type choiceJSON struct {
	ID          int    `json:"id"`
	Choice      string `json:"choice"`
	BitPosition int    `json:"bit_position"`
}

// handleFieldList serves GET /api/v2/field and its translated variant.
func (s *Server) handleFieldList(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.db.LoadFieldCatalog(r.Context())
	if err != nil {
		s.internalError(w, "load field catalog", err)
		return
	}

	out := make([]fieldJSON, 0, len(catalog.All()))
	for _, f := range catalog.All() {
		out = append(out, fieldJSON{
			ID:              f.ID,
			Name:            f.Name,
			ApplicationType: f.ApplicationType,
			StringID:        f.StringID,
		})
	}
	api.OK(w, out)
}

// handleFieldCreate serves POST /api/v2/field.
func (s *Server) handleFieldCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string `json:"name"`
		ApplicationType string `json:"application_type"`
		StringID        string `json:"string_id"`
		// Indexed has no counterpart in production, where indexing is a
		// provisioning decision. The mock exposes it so a test can set up the
		// replyCode 2015 case without touching the database directly.
		Indexed bool `json:"indexed"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		api.ErrorText(w, api.CodeInvalidFieldID, "Field name must not be empty")
		return
	}
	if !applicationTypes[body.ApplicationType] {
		api.ErrorText(w, api.CodeInvalidFieldID,
			"Unknown application_type: "+body.ApplicationType)
		return
	}

	id, err := s.db.CreateField(r.Context(), body.Name, body.ApplicationType, body.StringID, body.Indexed)
	if err != nil {
		s.internalError(w, "create field", err)
		return
	}
	api.OK(w, map[string]any{"id": id})
}

// handleFieldDelete serves DELETE /api/v2/field/{fieldId}.
func (s *Server) handleFieldDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "fieldId", api.CodeInvalidFieldID)
	if !ok {
		return
	}

	switch err := s.db.DeleteField(r.Context(), id); {
	case err == nil:
		// Production answers a delete with a string payload, not an object.
		api.OK(w, "")
	case errors.Is(err, store.ErrNoField):
		api.ErrorText(w, api.CodeInvalidFieldID, "Invalid field id: "+strconv.Itoa(id))
	case errors.Is(err, store.ErrSystemField):
		api.ErrorText(w, api.CodeInvalidFieldID,
			"Field "+strconv.Itoa(id)+" is a system field and cannot be deleted")
	default:
		s.internalError(w, "delete field", err)
	}
}

// handleFieldSubpath dispatches the two five-segment GET routes under /field
// that ServeMux cannot tell apart on its own.
func (s *Server) handleFieldSubpath(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.PathValue("first") == "translate":
		s.handleFieldList(w, r)
	case r.PathValue("second") == "choice":
		s.fieldChoiceByID(w, r, r.PathValue("first"))
	default:
		s.handleNotImplemented(w, r)
	}
}

// handleFieldChoice serves GET /api/v2/field/{fieldId}/choice/translate/{lang}.
func (s *Server) handleFieldChoice(w http.ResponseWriter, r *http.Request) {
	s.fieldChoiceByID(w, r, r.PathValue("fieldId"))
}

func (s *Server) fieldChoiceByID(w http.ResponseWriter, r *http.Request, rawID string) {
	id, err := strconv.Atoi(rawID)
	if err != nil {
		api.ErrorText(w, api.CodeInvalidFieldID, "Invalid field id: "+rawID)
		return
	}
	catalog, catErr := s.db.LoadFieldCatalog(r.Context())
	if catErr != nil {
		s.internalError(w, "load field catalog", catErr)
		return
	}
	if !catalog.Has(id) {
		api.ErrorText(w, api.CodeInvalidFieldID, "Invalid field id: "+strconv.Itoa(id))
		return
	}
	api.OK(w, choicesJSON(catalog, id))
}

// handleFieldChoices serves GET /api/v2/field/choices?fields=1,2&language=en.
func (s *Server) handleFieldChoices(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.db.LoadFieldCatalog(r.Context())
	if err != nil {
		s.internalError(w, "load field catalog", err)
		return
	}

	raw := r.URL.Query().Get("fields")
	var ids []int
	if strings.TrimSpace(raw) == "" {
		for _, f := range catalog.All() {
			if len(catalog.Choices(f.ID)) > 0 {
				ids = append(ids, f.ID)
			}
		}
	} else {
		for _, part := range strings.Split(raw, ",") {
			id, convErr := strconv.Atoi(strings.TrimSpace(part))
			if convErr != nil || !catalog.Has(id) {
				api.ErrorText(w, api.CodeInvalidFieldID, "Invalid field id: "+strings.TrimSpace(part))
				return
			}
			ids = append(ids, id)
		}
	}

	// Keyed by field id as a string, matching the shape in the collection.
	out := map[string][]choiceJSON{}
	for _, id := range ids {
		out[strconv.Itoa(id)] = choicesJSON(catalog, id)
	}
	api.OK(w, out)
}

func choicesJSON(catalog *store.FieldCatalog, fieldID int) []choiceJSON {
	choices := catalog.Choices(fieldID)
	out := make([]choiceJSON, 0, len(choices))
	for _, c := range choices {
		out = append(out, choiceJSON{ID: c.ID, Choice: c.Label, BitPosition: c.ID})
	}
	return out
}

// pathInt reads an integer path parameter, answering with the given reply code
// when it is not a number.
func pathInt(w http.ResponseWriter, r *http.Request, name string, code api.ReplyCode) (int, bool) {
	raw := r.PathValue(name)
	id, err := strconv.Atoi(raw)
	if err != nil {
		api.ErrorText(w, code, "Invalid "+name+": "+raw)
		return 0, false
	}
	return id, true
}
