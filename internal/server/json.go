package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
)

// errBadJSON marks a body the mock could not parse at all.
var errBadJSON = errors.New("malformed JSON body")

// decodeJSON reads a JSON request body into v.
//
// An unparsable body is reported as replyCode 2011 with an explicit message.
// Production does not document a dedicated code for this case, and inventing a
// number would be worse than reusing the generic one with a clear text.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		api.ErrorStatus(w, http.StatusRequestEntityTooLarge, api.CodeInternalError,
			"Request body could not be read: "+err.Error())
		return false
	}
	if len(body) == 0 {
		api.ErrorText(w, api.CodeMissingKeyField, "Empty request body")
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		api.ErrorText(w, api.CodeInternalError, "Invalid JSON in request body: "+err.Error())
		return false
	}
	return true
}

// flexibleInt accepts a JSON value that may arrive as a number or as a string.
//
// Emarsys clients are inconsistent about this and the API tolerates both:
// key_id is documented as "3" but a numeric 3 works just as well.
type flexibleInt struct {
	Value int
	Set   bool
	Raw   string
}

func (f *flexibleInt) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	if raw == "null" {
		return nil
	}
	f.Set = true
	f.Raw = strings.Trim(raw, `"`)

	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		if i, convErr := strconv.Atoi(n.String()); convErr == nil {
			f.Value = i
			return nil
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		f.Raw = s
		if i, convErr := strconv.Atoi(strings.TrimSpace(s)); convErr == nil {
			f.Value = i
		}
		return nil
	}
	return nil
}

// IsNumeric reports whether the raw value was a plain integer, which is what
// separates a field id from a field name.
func (f flexibleInt) IsNumeric() bool {
	if !f.Set || f.Raw == "" {
		return false
	}
	_, err := strconv.Atoi(strings.TrimSpace(f.Raw))
	return err == nil
}

// flexibleStrings accepts a JSON value that may be a single scalar or an array
// of them, and normalises it to a slice of strings. Several Emarsys endpoints
// document an array but accept a bare value.
type flexibleStrings struct {
	Values []string
	Set    bool
	IsList bool
}

func (f *flexibleStrings) UnmarshalJSON(b []byte) error {
	f.Set = true
	var list []json.RawMessage
	if err := json.Unmarshal(b, &list); err == nil {
		f.IsList = true
		for _, item := range list {
			f.Values = append(f.Values, scalarToString(item))
		}
		return nil
	}
	if strings.TrimSpace(string(b)) == "null" {
		f.Set = false
		return nil
	}
	f.Values = append(f.Values, scalarToString(b))
	return nil
}

// scalarToString renders a JSON scalar the way Emarsys stores it: strings
// verbatim, numbers in their literal form, null and objects as empty.
func scalarToString(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" || trimmed == "" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		if b {
			return "true"
		}
		return "false"
	}
	return ""
}
