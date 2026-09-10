// Package record captures real Emarsys traffic so handlers can be written
// against what production actually sends rather than what the documentation
// says it sends.
//
// Recording runs against a production account, which means the traffic carries
// real people's contact data. Everything here is built around that: values are
// pseudonymised by default and credentials are never written at all. What is
// needed to build a handler is the *shape* of a payload, not its contents, so
// the safe mode is also the useful one.
package record

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Mode decides how much of a payload survives into the recording.
type Mode string

const (
	// ModeShapes keeps structure, keys and value shapes, and replaces the
	// values themselves with stable pseudonyms. This is the default and the
	// only mode that is safe to point at production without a data-protection
	// conversation first.
	ModeShapes Mode = "shapes"

	// ModeRaw keeps payloads verbatim. It captures real personal data and
	// exists only for a deliberately seeded test account.
	ModeRaw Mode = "raw"
)

// headerAllowlist are the response and request headers worth keeping. Anything
// not listed is dropped rather than redacted: an allowlist cannot be defeated
// by a header nobody thought of.
var headerAllowlist = map[string]bool{
	"content-type":          true,
	"content-length":        true,
	"date":                  true,
	"retry-after":           true,
	"x-ratelimit-limit":     true,
	"x-ratelimit-remaining": true,
	"x-ratelimit-reset":     true,
	"x-request-id":          true,
	"location":              true,
}

// credentialHeaders are recorded as a note that they were present, never by value.
var credentialHeaders = map[string]bool{
	"x-wsse":        true,
	"authorization": true,
	"cookie":        true,
	"set-cookie":    true,
	"x-api-key":     true,
}

// Redactor pseudonymises payloads. The same input always maps to the same
// pseudonym within one recording session, so a contact that appears in three
// calls is recognisably the same contact without ever being identifiable.
type Redactor struct {
	Mode Mode
	key  []byte
}

func NewRedactor(mode Mode, sessionKey []byte) *Redactor {
	if mode == "" {
		mode = ModeShapes
	}
	return &Redactor{Mode: mode, key: sessionKey}
}

// Headers reduces a header set to the allowlist, noting which credentials were
// present without recording them.
func (r *Redactor) Headers(in map[string][]string) map[string]string {
	out := map[string]string{}
	for name, values := range in {
		lower := strings.ToLower(name)
		switch {
		case credentialHeaders[lower]:
			out[name] = "<redacted: " + credentialScheme(values) + ">"
		case headerAllowlist[lower]:
			out[name] = strings.Join(values, ", ")
		}
	}
	return out
}

// schemeToken matches an authentication scheme name and nothing else. A cookie
// value like "session=abc123" is also a short first field, and reporting it as
// the "scheme" would leak the session.
var schemeToken = regexp.MustCompile(`^[A-Za-z][A-Za-z-]{1,30}$`)

func credentialScheme(values []string) string {
	if len(values) == 0 {
		return "present"
	}
	fields := strings.Fields(values[0])
	if len(fields) > 1 && schemeToken.MatchString(fields[0]) {
		return fields[0]
	}
	return "present"
}

// Body pseudonymises a payload. Anything that does not parse as JSON is
// replaced wholesale rather than guessed at: a CSV export is contact data by
// definition.
func (r *Redactor) Body(contentType string, raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	if r.Mode == ModeRaw {
		if json.Valid(raw) {
			return json.RawMessage(raw)
		}
		return quoted(string(raw))
	}

	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// Not JSON. CSV exports land here, and they are contact data in bulk.
		return quoted(fmt.Sprintf("<non-JSON body redacted: %d bytes, %s>",
			len(raw), firstToken(contentType)))
	}

	cleaned, err := json.Marshal(r.value(parsed, "", 0))
	if err != nil {
		return quoted("<could not re-encode body>")
	}
	return cleaned
}

// structuralKeys hold protocol values rather than customer data, so they are
// kept verbatim. Getting the reply code and the status literal out of a
// recording is the entire point of making one.
// contactKeyedMaps are objects whose keys are contact key values rather than
// field names.
var contactKeyedMaps = map[string]bool{
	"errors": true, "ids": true, "result": true,
}

var structuralKeys = map[string]bool{
	"replycode": true, "replytext": true, "status": true, "type": true,
	"application_type": true, "string_id": true, "key_id": true, "keyid": true,
	"grant_type": true, "token_type": true, "expires_in": true,
	"distribution_method": true, "delimiter": true, "add_field_names_header": true,
	"error": true, "error_description": true, "file_name": true,
}

// value walks a decoded payload.
//
// key is the field name the value sat under.
//
// errorBudget counts how many levels remain in which a numeric key means a
// reply code rather than a contact field id. data.errors is shaped
// {keyValue: {replyCode: message}}, so the message sits two levels down; the
// budget is set there and spent on the way in. A counter rather than a flag,
// because a contact object is also keyed by numbers, and letting the rule leak
// further down would keep exactly the values it is meant to remove.
func (r *Redactor) value(v any, key string, errorBudget int) any {
	switch typed := v.(type) {
	case map[string]any:
		// Some Emarsys maps are keyed by the contact's own key value, so the
		// keys are personal data: data.errors is keyed by e-mail address.
		keysArePersonal := contactKeyedMaps[strings.ToLower(key)]

		childBudget := errorBudget - 1
		if keysArePersonal {
			childBudget = 2
		}

		out := make(map[string]any, len(typed))
		for k, child := range typed {
			outKey := k
			if keysArePersonal || emailPattern.MatchString(k) {
				outKey = r.pseudonym(k)
			}
			out[outKey] = r.value(child, k, childBudget)
		}
		return out

	case []any:
		// The elements carry a distinct key, so an array never looks like a
		// contact-keyed map. data.result is an array of contact rows in
		// getdata and a map keyed by e-mail in last_change; without this the
		// first case has its field ids pseudonymised away, which destroys
		// exactly the structure the recording is for.
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = r.value(child, key+"[]", errorBudget)
		}
		return out

	case string:
		switch {
		case structuralKeys[strings.ToLower(key)]:
			// Even a reply text carries an address: "No contact found with the
			// external id: jane@example.com".
			return r.scrubAddresses(typed)
		case errorBudget > 0 && allDigits.MatchString(key):
			// A reply message keyed by its reply code. The wording is a large
			// part of why a recording is made; the address inside it is not.
			return r.scrubAddresses(typed)
		default:
			return r.pseudonym(typed)
		}

	default:
		// Numbers, booleans and null are structural enough to keep. An id is a
		// number, and losing it would hide whether the API returns strings or
		// integers -- one of the details a recording exists to settle.
		return v
	}
}

var (
	emailPattern = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`)
	isoDate      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	isoDateTime  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}`)
	allDigits    = regexp.MustCompile(`^\d+$`)
)

// pseudonym replaces a value while preserving the shape a handler has to cope
// with: an address stays an address, a date stays a date in the same format.
func (r *Redactor) pseudonym(value string) string {
	if value == "" {
		return ""
	}
	switch {
	case emailPattern.MatchString(value):
		return "user-" + r.tag(value) + "@example.com"
	case isoDateTime.MatchString(value):
		// Keep the separator and the presence of a zone marker: those are
		// exactly the details that differ between endpoints.
		return regexp.MustCompile(`\d`).ReplaceAllString(value[:19], "0") + value[19:]
	case isoDate.MatchString(value):
		return "1990-01-01"
	case allDigits.MatchString(value):
		return strings.Repeat("0", len(value))
	case len(value) <= 24 && !strings.ContainsAny(value, " @"):
		// Short tokens are usually enums, ids or field names, which are shape.
		return "tok-" + r.tag(value)
	default:
		return "str-" + r.tag(value) + "-len" + strconv.Itoa(len(value))
	}
}

// scrubAddresses removes addresses from a string that is otherwise kept.
func (r *Redactor) scrubAddresses(value string) string {
	return emailPattern.ReplaceAllStringFunc(value, func(match string) string {
		return "user-" + r.tag(match) + "@example.com"
	})
}

// tag is a stable short pseudonym. It is keyed per session, so a recording
// cannot be correlated back to a person by rainbow-tabling the hash.
func (r *Redactor) tag(value string) string {
	mac := hmac.New(sha256.New, r.key)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))[:8]
}

func quoted(s string) json.RawMessage {
	out, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`"<unencodable>"`)
	}
	return out
}

func firstToken(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i > 0 {
		return contentType[:i]
	}
	if contentType == "" {
		return "unknown content type"
	}
	return contentType
}
