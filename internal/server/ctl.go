package server

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// The control plane is ours, not Emarsys'. It answers plain JSON rather than a
// replyCode envelope, so there is never any doubt about which surface a
// response came from.

type ctlError struct {
	Error string `json:"error"`
}

func writeCtl(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, `{"error":"could not encode response"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(status)
	w.Write(body)
}

func ctlFail(w http.ResponseWriter, status int, message string) {
	writeCtl(w, status, ctlError{Error: message})
}

// ctlAuth guards the control plane and the dashboard.
//
// With CTL_TOKEN set, a caller needs it. Without one the surface is reachable
// only from the loopback interface, so an unconfigured mock exposed on a shared
// network cannot be reset or reprogrammed by whoever finds it.
func (s *Server) ctlAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.CtlToken != "" {
			if !s.hasCtlToken(r) {
				ctlFail(w, http.StatusUnauthorized, "control plane token required")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !isLoopback(r) {
			ctlFail(w, http.StatusForbidden,
				"control plane is restricted to localhost; set CTL_TOKEN to open it up")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hasCtlToken(r *http.Request) bool {
	candidates := []string{
		r.Header.Get("X-Ctl-Token"),
		strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
		r.URL.Query().Get("token"),
	}
	if c, err := r.Cookie("ctl_token"); err == nil {
		candidates = append(candidates, c.Value)
	}
	for _, candidate := range candidates {
		if candidate != "" &&
			subtle.ConstantTimeCompare([]byte(candidate), []byte(s.cfg.CtlToken)) == 1 {
			return true
		}
	}
	return false
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// requireWritable blocks every mutating control-plane call in read-only mode,
// so a shared instance can be handed out without anyone being able to reset it
// under a colleague's running test.
func (s *Server) requireWritable(w http.ResponseWriter) bool {
	if s.cfg.ReadOnly {
		ctlFail(w, http.StatusForbidden, "instance is running in read-only mode")
		return false
	}
	return true
}

// handleCtlHealth serves GET /_ctl/health.
func (s *Server) handleCtlHealth(w http.ResponseWriter, r *http.Request) {
	var fields, contacts int
	_ = s.db.Read.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM fields`).Scan(&fields)
	_ = s.db.Read.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM contacts`).Scan(&contacts)

	writeCtl(w, http.StatusOK, map[string]any{
		"status":                   "ok",
		"read_only":                s.cfg.ReadOnly,
		"fields":                   fields,
		"contacts":                 contacts,
		"export_timezone":          s.cfg.ExportLocation.String(),
		"rate_limit_per_minute":    s.cfg.RateLimitPerMinute,
		"export_polls_before_done": s.cfg.ExportPollsBeforeDone,
		"webhook_configured":       s.webhook.Enabled(),
	})
}

// handleCtlReset serves POST /_ctl/reset.
func (s *Server) handleCtlReset(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w) {
		return
	}
	if err := s.db.Reset(); err != nil {
		s.logger.Error("reset failed", "err", err)
		ctlFail(w, http.StatusInternalServerError, "reset failed: "+err.Error())
		return
	}
	writeCtl(w, http.StatusOK, map[string]any{"status": "reset"})
}

// seedRequest is the fixture format the control plane accepts.
//
// The fields block deliberately matches the shape of a GET /v2/field response,
// so the real catalogue of a production account can be dumped and loaded here
// unchanged. Guessing a customer's custom fields would be worse than importing
// them.
type seedRequest struct {
	Reset  bool `json:"reset"`
	Fields []struct {
		ID              int    `json:"id"`
		Name            string `json:"name"`
		ApplicationType string `json:"application_type"`
		StringID        string `json:"string_id"`
		Indexed         bool   `json:"indexed"`
		Choices         []struct {
			ID     int    `json:"id"`
			Choice string `json:"choice"`
		} `json:"choices"`
	} `json:"fields"`
	Events []struct {
		Name string `json:"name"`
	} `json:"events"`
	ContactLists []struct {
		Name string `json:"name"`
	} `json:"contact_lists"`
	Contacts []map[string]json.RawMessage `json:"contacts"`
	KeyID    flexibleInt                  `json:"key_id"`
}

// handleCtlSeed serves POST /_ctl/seed.
func (s *Server) handleCtlSeed(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w) {
		return
	}

	var body seedRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		ctlFail(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if body.Reset {
		if err := s.db.Reset(); err != nil {
			ctlFail(w, http.StatusInternalServerError, "reset failed: "+err.Error())
			return
		}
	}

	summary := map[string]int{}

	for _, f := range body.Fields {
		if f.ID == 0 || f.Name == "" {
			continue
		}
		if err := s.db.UpsertField(r.Context(), store.FieldDef{
			ID:              f.ID,
			Name:            f.Name,
			StringID:        f.StringID,
			ApplicationType: f.ApplicationType,
			IsIndexed:       f.Indexed,
		}); err != nil {
			ctlFail(w, http.StatusInternalServerError, "seed field: "+err.Error())
			return
		}
		summary["fields"]++
		for _, c := range f.Choices {
			if err := s.db.AddChoice(r.Context(), f.ID, c.ID, c.Choice, c.ID); err != nil {
				ctlFail(w, http.StatusInternalServerError, "seed choice: "+err.Error())
				return
			}
		}
	}

	for _, e := range body.Events {
		if _, err := s.db.CreateEvent(r.Context(), e.Name); err == nil {
			summary["events"]++
		}
	}
	for _, l := range body.ContactLists {
		if _, err := s.db.CreateContactList(r.Context(), l.Name); err == nil {
			summary["contact_lists"]++
		}
	}

	if len(body.Contacts) > 0 {
		created, err := s.seedContacts(r, body)
		if err != nil {
			ctlFail(w, http.StatusBadRequest, err.Error())
			return
		}
		summary["contacts"] = created
	}

	writeCtl(w, http.StatusOK, map[string]any{"status": "seeded", "created": summary})
}

func (s *Server) seedContacts(r *http.Request, body seedRequest) (int, error) {
	catalog, err := s.db.LoadFieldCatalog(r.Context())
	if err != nil {
		return 0, err
	}
	keyFieldID := 3
	if body.KeyID.Set && body.KeyID.IsNumeric() {
		keyFieldID = body.KeyID.Value
	}

	created := 0
	err = s.db.WithTx(r.Context(), func(tx *sql.Tx) error {
		for _, raw := range body.Contacts {
			row, parseErr := parseRow(raw, catalog)
			if parseErr != nil {
				return parseErr
			}
			keyValue, present := row.keyValueOf(keyFieldID)
			if !present {
				return errors.New("seed contact without a value for key field " + strconv.Itoa(keyFieldID))
			}
			existing, findErr := store.FindContactIDs(r.Context(), tx, keyFieldID, keyValue)
			if findErr != nil {
				return findErr
			}
			if len(existing) > 0 {
				if applyErr := store.ApplyContactValues(r.Context(), tx, existing[0], row.values, "seed"); applyErr != nil {
					return applyErr
				}
				continue
			}
			if _, createErr := store.CreateContact(r.Context(), tx, row.values, "seed"); createErr != nil {
				return createErr
			}
			created++
		}
		return nil
	})
	return created, err
}

// handleCtlRequests serves GET /_ctl/requests, which is how a test asserts what
// an integration actually called.
func (s *Server) handleCtlRequests(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	filter := store.RequestLogFilter{
		Path:   params.Get("path"),
		Method: strings.ToUpper(params.Get("method")),
	}
	if raw := params.Get("since"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			filter.SinceID = n
		}
	}
	if raw := params.Get("status"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			filter.Status = n
		}
	}
	if raw := params.Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			filter.Limit = n
		}
	}

	entries, err := s.db.RequestLogEntries(r.Context(), filter)
	if err != nil {
		ctlFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeCtl(w, http.StatusOK, map[string]any{"requests": entries, "count": len(entries)})
}

// handleCtlTriggers serves GET /_ctl/events/triggers.
func (s *Server) handleCtlTriggers(w http.ResponseWriter, r *http.Request) {
	var eventID int64
	if raw := r.URL.Query().Get("event_id"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			eventID = n
		}
	}
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}

	triggers, err := s.db.Triggers(r.Context(), eventID, limit)
	if err != nil {
		ctlFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeCtl(w, http.StatusOK, map[string]any{"triggers": triggers, "count": len(triggers)})
}

// handleCtlFaultList serves GET /_ctl/faults.
func (s *Server) handleCtlFaultList(w http.ResponseWriter, r *http.Request) {
	rules, err := s.db.FaultRules(r.Context(), r.URL.Query().Get("enabled") == "1")
	if err != nil {
		ctlFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeCtl(w, http.StatusOK, map[string]any{"faults": rules})
}

// handleCtlFaultCreate serves POST /_ctl/faults.
func (s *Server) handleCtlFaultCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w) {
		return
	}
	var rule store.FaultRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		ctlFail(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	created, err := s.db.CreateFaultRule(r.Context(), rule)
	if err != nil {
		ctlFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeCtl(w, http.StatusOK, created)
}

// handleCtlFaultDelete serves DELETE /_ctl/faults/{faultId}.
func (s *Server) handleCtlFaultDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("faultId"), 10, 64)
	if err != nil {
		ctlFail(w, http.StatusBadRequest, "invalid fault id")
		return
	}
	if err := s.db.DeleteFaultRule(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNoFaultRule) {
			ctlFail(w, http.StatusNotFound, "no such fault rule")
			return
		}
		ctlFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeCtl(w, http.StatusOK, map[string]any{"status": "deleted", "id": id})
}

// handleCtlExportStatus serves POST /_ctl/exports/{exportId}/status, which
// forces a job into a state so a test does not have to poll it to completion.
func (s *Server) handleCtlExportStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("exportId"), 10, 64)
	if err != nil {
		ctlFail(w, http.StatusBadRequest, "invalid export id")
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		ctlFail(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	switch body.Status {
	case store.ExportScheduled, store.ExportInProgress, store.ExportDone, store.ExportError:
	default:
		ctlFail(w, http.StatusBadRequest,
			`status must be one of "scheduled", "in progress", "done", "error"`)
		return
	}
	if err := s.db.SetExportStatus(r.Context(), id, body.Status); err != nil {
		if errors.Is(err, store.ErrNoExport) {
			ctlFail(w, http.StatusNotFound, "no such export")
			return
		}
		ctlFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeCtl(w, http.StatusOK, map[string]any{"status": body.Status, "id": id})
}
