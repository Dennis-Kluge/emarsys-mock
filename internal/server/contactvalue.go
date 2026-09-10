package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// isoDate is the only date format Emarsys accepts in a contact field. Anything
// else, including the German and US orderings a CSV export tends to produce, is
// rejected rather than stored.
var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// parsedRow is one entry of a contacts[] array after field ids have been
// resolved and values normalised.
type parsedRow struct {
	values map[int]string
	// internalID and uid come from the "id" and "uid" keys, which address a
	// contact directly instead of naming a field value.
	internalID string
	uid        string
}

// unknownFieldError marks a field id that is not in the catalogue. Production
// treats this as a request-level problem rather than a per-row one, because a
// client sending an unknown id is misconfigured for the whole batch.
type unknownFieldError struct{ raw string }

func (e unknownFieldError) Error() string { return "invalid field id: " + e.raw }

// valueError marks a value that does not fit its field's type. It is reported
// per row, so one bad date does not fail the other 999 contacts in a batch.
type valueError struct {
	fieldID int
	value   string
	reason  string
}

func (e valueError) Error() string {
	return fmt.Sprintf("Invalid value for field %d: %q (%s)", e.fieldID, e.value, e.reason)
}

// parseRow turns one contacts[] entry into field values.
func parseRow(raw map[string]json.RawMessage, catalog *store.FieldCatalog) (parsedRow, error) {
	row := parsedRow{values: map[int]string{}}

	for key, value := range raw {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "id":
			row.internalID = scalarToString(value)
			continue
		case "uid":
			row.uid = scalarToString(value)
			continue
		}

		fieldID, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil {
			// A field name where a field id belongs: the single most common
			// mistake when an integration is written against the docs.
			return parsedRow{}, unknownFieldError{raw: key}
		}
		if !catalog.Has(fieldID) {
			return parsedRow{}, unknownFieldError{raw: key}
		}
		row.values[fieldID] = scalarToString(value)
	}
	return row, nil
}

// validateValues checks every value against its field's type.
func validateValues(values map[int]string, catalog *store.FieldCatalog) error {
	for _, fieldID := range sortedKeys(values) {
		if err := validateValue(fieldID, values[fieldID], catalog); err != nil {
			return err
		}
	}
	return nil
}

func validateValue(fieldID int, value string, catalog *store.FieldCatalog) error {
	field, ok := catalog.Get(fieldID)
	if !ok {
		return unknownFieldError{raw: strconv.Itoa(fieldID)}
	}

	// An empty value is always legal, and it clears the field rather than
	// leaving it untouched. This is how an integration that sends a field it
	// did not mean to send wipes consent data in production, so the mock has to
	// reproduce it exactly.
	if value == "" {
		return nil
	}

	switch field.ApplicationType {
	case "date":
		if !isoDate.MatchString(value) {
			return valueError{fieldID, value, "expected YYYY-MM-DD"}
		}
	case "numeric":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return valueError{fieldID, value, "expected a number"}
		}
	case "singlechoice":
		// Opt-in (field 31) is a single-choice field, which is why it accepts
		// only 1 and 2 and rejects the boolean literals a JSON client naturally
		// reaches for.
		if !catalog.HasChoice(fieldID, value) {
			return valueError{fieldID, value, "not a defined choice for this field"}
		}
	case "multichoice", "interests":
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !catalog.HasChoice(fieldID, part) {
				return valueError{fieldID, value, "contains an undefined choice: " + part}
			}
		}
	}
	return nil
}

// keyValueOf returns the value that identifies a row under the batch's key field.
func (r parsedRow) keyValueOf(keyFieldID int) (string, bool) {
	switch keyFieldID {
	case store.KeyFieldInternalID:
		return r.internalID, r.internalID != ""
	case store.KeyFieldUID:
		return r.uid, r.uid != ""
	default:
		v, ok := r.values[keyFieldID]
		return v, ok && v != ""
	}
}

func sortedKeys(m map[int]string) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
