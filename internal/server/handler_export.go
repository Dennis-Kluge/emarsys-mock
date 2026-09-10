package server

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// exportRequest is the shape shared by getchanges, getregistrations and the
// contact list export.
type exportRequest struct {
	DistributionMethod  string            `json:"distribution_method"`
	Origin              string            `json:"origin"`
	TimeRange           []string          `json:"time_range"`
	ContactFields       []json.RawMessage `json:"contact_fields"`
	Delimiter           string            `json:"delimiter"`
	AddFieldNamesHeader int               `json:"add_field_names_header"`
	Language            string            `json:"language"`
	WithTimestamp       int               `json:"with_timestamp"`
	NotificationURL     string            `json:"notification_url"`
	// PollsBeforeDone has no counterpart in production. It lets one test ask
	// for an export that is ready immediately and another for one that takes
	// several polls, without reconfiguring the whole service.
	PollsBeforeDone *int `json:"polls_before_done"`
}

// handleGetChanges serves POST /api/v2/contact/getchanges.
func (s *Server) handleGetChanges(w http.ResponseWriter, r *http.Request) {
	s.startExport(w, r, "changes")
}

// handleGetRegistrations serves POST /api/v2/contact/getregistrations.
func (s *Server) handleGetRegistrations(w http.ResponseWriter, r *http.Request) {
	s.startExport(w, r, "registrations")
}

// handleContactListExport serves POST /api/v2/contactlist/{listId}/export.
func (s *Server) handleContactListExport(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.lookupList(w, r); !ok {
		return
	}
	s.startExport(w, r, "contactlist")
}

func (s *Server) startExport(w http.ResponseWriter, r *http.Request, exportType string) {
	var body exportRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	catalog, err := s.db.LoadFieldCatalog(r.Context())
	if err != nil {
		s.internalError(w, "load field catalog", err)
		return
	}
	fieldIDs, ok := s.resolveFieldList(w, body.ContactFields, catalog)
	if !ok {
		return
	}
	if len(fieldIDs) == 0 {
		api.Error(w, api.CodeNoFieldToReturn)
		return
	}

	from, to, ok := s.exportWindow(w, body)
	if !ok {
		return
	}

	payload, err := s.buildExportCSV(r, exportType, body, catalog, fieldIDs, from, to)
	if err != nil {
		s.internalError(w, "build export", err)
		return
	}

	polls := s.cfg.ExportPollsBeforeDone
	if body.PollsBeforeDone != nil && *body.PollsBeforeDone >= 0 {
		polls = *body.PollsBeforeDone
	}
	params, _ := json.Marshal(body)

	id, err := s.db.CreateExport(r.Context(), exportType, string(params), polls, payload)
	if err != nil {
		s.internalError(w, "create export", err)
		return
	}
	// getchanges reports the new job's id as a number, while the status
	// endpoint reports the same id as a string.
	api.OK(w, map[string]any{"id": id})
}

// exportWindow resolves the requested time range, defaulting to everything.
func (s *Server) exportWindow(w http.ResponseWriter, body exportRequest) (time.Time, time.Time, bool) {
	from := time.Unix(0, 0).UTC()
	to := time.Now().UTC().Add(24 * time.Hour)

	if len(body.TimeRange) > 0 {
		parsed, ok := s.parseRangeBound(body.TimeRange[0], false)
		if !ok {
			api.ErrorText(w, api.CodeInternalError,
				"Unparsable time_range start: "+body.TimeRange[0])
			return time.Time{}, time.Time{}, false
		}
		from = parsed
	}
	if len(body.TimeRange) > 1 {
		parsed, ok := s.parseRangeBound(body.TimeRange[1], true)
		if !ok {
			api.ErrorText(w, api.CodeInternalError,
				"Unparsable time_range end: "+body.TimeRange[1])
			return time.Time{}, time.Time{}, false
		}
		to = parsed
	}
	return from, to, true
}

// buildExportCSV renders the file the job will serve.
//
// Production generates it asynchronously; the mock snapshots it now and only
// pretends to take time, because the behaviour worth exercising is the client's
// polling loop rather than the generation.
func (s *Server) buildExportCSV(r *http.Request, exportType string, body exportRequest, catalog *store.FieldCatalog, fieldIDs []int, from, to time.Time) ([]byte, error) {
	type row struct {
		contactID int64
		timestamp string
	}
	var rows []row

	switch exportType {
	case "changes":
		changes, err := s.db.FieldChanges(r.Context(), from, to, fieldIDs)
		if err != nil {
			return nil, err
		}
		// One row per contact, timestamped with its first change in the window.
		seen := map[int64]bool{}
		for _, c := range changes {
			if seen[c.ContactID] {
				continue
			}
			seen[c.ContactID] = true
			rows = append(rows, row{c.ContactID, c.ChangedAt})
		}

	case "registrations":
		ids, timestamps, err := s.contactsCreatedBetween(r, from, to)
		if err != nil {
			return nil, err
		}
		for i, id := range ids {
			rows = append(rows, row{id, timestamps[i]})
		}

	default: // contactlist
		listID, err := strconv.ParseInt(r.PathValue("listId"), 10, 64)
		if err != nil {
			return nil, err
		}
		ids, memberErr := s.db.ListMembers(r.Context(), listID, 1_000_000, 0)
		if memberErr != nil {
			return nil, memberErr
		}
		now := time.Now().UTC().Format(time.RFC3339)
		for _, id := range ids {
			rows = append(rows, row{id, now})
		}
	}

	ids := make([]int64, 0, len(rows))
	for _, rw := range rows {
		ids = append(ids, rw.contactID)
	}
	contacts, err := store.LoadContacts(r.Context(), s.db.Read, ids, fieldIDs)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if d := []rune(body.Delimiter); len(d) == 1 {
		writer.Comma = d[0]
	}

	withTimestamp := exportType != "contactlist" || body.WithTimestamp == 1

	if body.AddFieldNamesHeader == 1 {
		header := []string{}
		if withTimestamp {
			header = append(header, "Timestamp")
		}
		for _, id := range fieldIDs {
			field, _ := catalog.Get(id)
			header = append(header, field.Name)
		}
		if err := writer.Write(header); err != nil {
			return nil, err
		}
	}

	for _, rw := range rows {
		contact, found := contacts[rw.contactID]
		if !found {
			continue
		}
		record := []string{}
		if withTimestamp {
			// Vienna local time with no offset, exactly as production writes it.
			record = append(record, s.localTime(rw.timestamp))
		}
		for _, id := range fieldIDs {
			record = append(record, contact.Values[id])
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	return buf.Bytes(), writer.Error()
}

func (s *Server) contactsCreatedBetween(r *http.Request, from, to time.Time) ([]int64, []string, error) {
	rows, err := s.db.Read.QueryContext(r.Context(),
		`SELECT id, created_at FROM contacts WHERE created_at >= ? AND created_at <= ? ORDER BY id`,
		from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var ids []int64
	var timestamps []string
	for rows.Next() {
		var id int64
		var created string
		if err := rows.Scan(&id, &created); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		timestamps = append(timestamps, created)
	}
	return ids, timestamps, rows.Err()
}

// handleExportStatus serves GET /api/v2/export/{exportId} and advances the job.
func (s *Server) handleExportStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "exportId")
	if !ok {
		return
	}
	export, err := s.db.PollExport(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNoExport) {
			api.ErrorText(w, api.CodeInternalError, "Unknown export id: "+strconv.FormatInt(id, 10))
			return
		}
		s.internalError(w, "poll export", err)
		return
	}

	api.OK(w, map[string]any{
		// A string here, although getchanges handed out a number.
		"id":        strconv.FormatInt(export.ID, 10),
		"created":   s.localTime(export.CreatedAt),
		"status":    export.Status,
		"type":      export.Type,
		"file_name": export.FileName,
		"ftp_host":  "",
		"ftp_dir":   "",
	})
}

// handleExportData serves GET /api/v2/export/{exportId}/data.
//
// The body is raw CSV rather than an envelope, which is why this is the one
// endpoint whose response a client must not try to JSON-decode.
func (s *Server) handleExportData(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "exportId")
	if !ok {
		return
	}
	payload, err := s.db.ExportPayload(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNoExport):
		api.ErrorText(w, api.CodeInternalError, "Unknown export id: "+strconv.FormatInt(id, 10))
		return
	case errors.Is(err, store.ErrExportNotReady):
		api.ErrorText(w, api.CodeInternalError,
			"Export "+strconv.FormatInt(id, 10)+" is not finished yet")
		return
	case err != nil:
		s.internalError(w, "read export payload", err)
		return
	}

	payload = sliceCSV(payload, r.URL.Query().Get("offset"), r.URL.Query().Get("limit"))
	w.Header().Set("Content-Type", "text/csv;charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(payload)
}

// sliceCSV applies offset and limit to data rows, keeping any header row.
func sliceCSV(payload []byte, rawOffset, rawLimit string) []byte {
	offset, offsetErr := strconv.Atoi(rawOffset)
	limit, limitErr := strconv.Atoi(rawLimit)
	if (offsetErr != nil || offset <= 0) && (limitErr != nil || limit <= 0) {
		return payload
	}

	lines := strings.Split(strings.TrimRight(string(payload), "\n"), "\n")
	if len(lines) == 0 {
		return payload
	}
	if offsetErr != nil || offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	lines = lines[offset:]
	if limitErr == nil && limit > 0 && limit < len(lines) {
		lines = lines[:limit]
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// handleContactLastChange serves POST /api/v2/contact/last_change.
//
// Emarsys has no endpoint that returns a field's history, so this reads the
// change log the mock keeps -- the same table a consent audit trail would use.
func (s *Server) handleContactLastChange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		KeyID     flexibleInt     `json:"keyId"`
		KeyValues flexibleStrings `json:"keyValues"`
		FieldID   flexibleInt     `json:"fieldId"`
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
	if !body.FieldID.Set || !catalog.Has(body.FieldID.Value) {
		api.ErrorText(w, api.CodeInvalidFieldID, "Invalid field id: "+body.FieldID.Raw)
		return
	}

	result := map[string]any{}
	for _, keyValue := range body.KeyValues.Values {
		ids, findErr := store.FindContactIDs(r.Context(), s.db.Read, keyFieldID, keyValue)
		if findErr != nil {
			s.internalError(w, "find contacts", findErr)
			return
		}
		if len(ids) != 1 {
			continue
		}
		change, changeErr := s.db.LastChange(r.Context(), ids[0], body.FieldID.Value)
		if errors.Is(changeErr, store.ErrNoChange) {
			continue
		}
		if changeErr != nil {
			s.internalError(w, "read last change", changeErr)
			return
		}
		result[keyValue] = map[string]any{
			"old_value":     stringOrEmpty(change.OldValue),
			"current_value": stringOrEmpty(change.NewValue),
			"time":          s.localTime(change.ChangedAt),
		}
	}

	api.OK(w, map[string]any{"result": result})
}

func stringOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
