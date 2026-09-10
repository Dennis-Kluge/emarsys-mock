package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// sqlTx is an alias so the handler files do not each need the database/sql
// import purely for a transaction parameter.
type sqlTx = sql.Tx

// --- lists and segments ----------------------------------------------------

type listRow struct {
	ID    int64
	Name  string
	Count int
}

func (s *Server) adminLists(w http.ResponseWriter, r *http.Request) {
	lists, err := s.db.ContactLists(r.Context())
	if err != nil {
		s.adminError(w, "list contact lists", err)
		return
	}
	listRows := make([]listRow, 0, len(lists))
	for _, l := range lists {
		count, countErr := s.db.ListMemberCount(r.Context(), l.ID)
		if countErr != nil {
			s.adminError(w, "count list members", countErr)
			return
		}
		listRows = append(listRows, listRow{l.ID, l.Name, count})
	}

	segments, err := s.db.Segments(r.Context())
	if err != nil {
		s.adminError(w, "list segments", err)
		return
	}
	segmentRows := make([]listRow, 0, len(segments))
	for _, seg := range segments {
		members, memberErr := s.db.SegmentMembers(r.Context(), seg.ID)
		if memberErr != nil {
			s.adminError(w, "read segment members", memberErr)
			return
		}
		segmentRows = append(segmentRows, listRow{seg.ID, seg.Name, len(members)})
	}

	s.render(w, "lists", struct {
		page
		Lists    []listRow
		Segments []listRow
	}{s.newPage(r, "Lists & segments", "lists"), listRows, segmentRows})
}

func (s *Server) adminListCreate(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/lists") {
		return
	}
	r.ParseForm()
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		redirectWith(w, r, "/admin/lists", "A list needs a name.", "error")
		return
	}
	id, err := s.db.CreateContactList(r.Context(), name)
	if err != nil {
		redirectWith(w, r, "/admin/lists", err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/lists", "Created list "+strconv.FormatInt(id, 10)+".", "")
}

func (s *Server) adminListDelete(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/lists") {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("listId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.db.DeleteContactList(r.Context(), id); err != nil {
		redirectWith(w, r, "/admin/lists", err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/lists", "Deleted the list.", "")
}

func (s *Server) adminListMembers(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/lists") {
		return
	}
	listID, err := strconv.ParseInt(r.PathValue("listId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()

	contactID, ok := s.resolveContactRef(r, r.PostFormValue("contact"))
	if !ok {
		redirectWith(w, r, "/admin/lists", "No contact matches that id or e-mail.", "error")
		return
	}

	add := r.PostFormValue("action") != "remove"
	err = s.db.WithTx(r.Context(), func(tx *sql.Tx) error {
		if add {
			_, addErr := store.AddToListCounting(r.Context(), tx, listID, []int64{contactID})
			return addErr
		}
		_, removeErr := store.RemoveFromList(r.Context(), tx, listID, []int64{contactID})
		return removeErr
	})
	if err != nil {
		redirectWith(w, r, "/admin/lists", err.Error(), "error")
		return
	}

	verb := "Added"
	if !add {
		verb = "Removed"
	}
	redirectWith(w, r, "/admin/lists",
		verb+" contact "+strconv.FormatInt(contactID, 10)+".", "")
}

func (s *Server) adminSegmentCreate(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/lists") {
		return
	}
	r.ParseForm()
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		redirectWith(w, r, "/admin/lists", "A segment needs a name.", "error")
		return
	}
	id, err := s.db.CreateSegment(r.Context(), name, "{}")
	if err != nil {
		redirectWith(w, r, "/admin/lists", err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/lists",
		"Created segment "+strconv.FormatInt(id, 10)+". Membership is whatever you set here; the mock evaluates no criteria.", "")
}

func (s *Server) adminSegmentDelete(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/lists") {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("segmentId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.db.DeleteSegment(r.Context(), id); err != nil {
		redirectWith(w, r, "/admin/lists", err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/lists", "Deleted the segment.", "")
}

func (s *Server) adminSegmentMembers(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/lists") {
		return
	}
	segmentID, err := strconv.ParseInt(r.PathValue("segmentId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()

	contactID, ok := s.resolveContactRef(r, r.PostFormValue("contact"))
	if !ok {
		redirectWith(w, r, "/admin/lists", "No contact matches that id or e-mail.", "error")
		return
	}
	add := r.PostFormValue("action") != "remove"
	if err := s.db.SetSegmentMember(r.Context(), segmentID, contactID, add); err != nil {
		redirectWith(w, r, "/admin/lists", err.Error(), "error")
		return
	}

	verb := "Added"
	if !add {
		verb = "Removed"
	}
	redirectWith(w, r, "/admin/lists",
		verb+" contact "+strconv.FormatInt(contactID, 10)+".", "")
}

// resolveContactRef accepts either a contact id or a value of any field, which
// in practice means an e-mail address.
func (s *Server) resolveContactRef(r *http.Request, ref string) (int64, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, false
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		if _, err := s.db.Contact(r.Context(), id); err == nil {
			return id, true
		}
	}
	ids, err := store.FindContactIDs(r.Context(), s.db.Read, 3, ref)
	if err != nil || len(ids) == 0 {
		return 0, false
	}
	return ids[0], true
}

// --- events ----------------------------------------------------------------

func (s *Server) adminEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.db.Events(r.Context())
	if err != nil {
		s.adminError(w, "list events", err)
		return
	}
	triggers, err := s.db.Triggers(r.Context(), 0, 100)
	if err != nil {
		s.adminError(w, "list triggers", err)
		return
	}

	s.render(w, "events", struct {
		page
		Events   []store.Event
		Triggers []store.EventTrigger
	}{s.newPage(r, "Events", "events"), events, triggers})
}

func (s *Server) adminEventCreate(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/events") {
		return
	}
	r.ParseForm()
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		redirectWith(w, r, "/admin/events", "An event needs a name.", "error")
		return
	}
	event, err := s.db.CreateEvent(r.Context(), name)
	if err != nil {
		redirectWith(w, r, "/admin/events", "Could not create the event: "+err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/events",
		"Created event "+strconv.FormatInt(event.ID, 10)+" — trigger it at /api/v2/event/"+
			strconv.FormatInt(event.ID, 10)+"/trigger.", "")
}

func (s *Server) adminEventDelete(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/events") {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("eventId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.db.DeleteEvent(r.Context(), id); err != nil {
		redirectWith(w, r, "/admin/events", err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/events", "Deleted the event and its triggers.", "")
}

// --- exports ---------------------------------------------------------------

func (s *Server) adminExports(w http.ResponseWriter, r *http.Request) {
	exports, err := s.db.Exports(r.Context(), 100)
	if err != nil {
		s.adminError(w, "list exports", err)
		return
	}
	s.render(w, "exports", struct {
		page
		Exports []store.Export
	}{s.newPage(r, "Exports", "exports"), exports})
}

func (s *Server) adminExportStatus(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/exports") {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("exportId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()
	status := r.PostFormValue("status")
	if status == "" {
		status = store.ExportDone
	}
	if err := s.db.SetExportStatus(r.Context(), id, status); err != nil {
		redirectWith(w, r, "/admin/exports", err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/exports",
		"Export "+strconv.FormatInt(id, 10)+" is now "+status+".", "")
}

func (s *Server) adminExportDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("exportId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	payload, err := s.db.ExportPayload(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrExportNotReady) {
			redirectWith(w, r, "/admin/exports", "That export is not finished yet.", "error")
			return
		}
		if errors.Is(err, store.ErrNoExport) {
			http.NotFound(w, r)
			return
		}
		s.adminError(w, "read export payload", err)
		return
	}

	export, _ := s.db.ExportByID(r.Context(), id)
	name := export.FileName
	if name == "" {
		name = "export_" + strconv.FormatInt(id, 10) + ".csv"
	}
	w.Header().Set("Content-Type", "text/csv;charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.WriteHeader(http.StatusOK)
	w.Write(payload)
}

// --- faults ----------------------------------------------------------------

func (s *Server) adminFaults(w http.ResponseWriter, r *http.Request) {
	faults, err := s.db.FaultRules(r.Context(), false)
	if err != nil {
		s.adminError(w, "list fault rules", err)
		return
	}
	s.render(w, "faults", struct {
		page
		Faults  []store.FaultRule
		Methods []string
	}{s.newPage(r, "Faults", "faults"), faults, dashboardMethods})
}

func (s *Server) adminFaultCreate(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/faults") {
		return
	}
	r.ParseForm()

	rule := store.FaultRule{
		MatchMethod:       r.PostFormValue("match_method"),
		MatchPathPattern:  strings.TrimSpace(r.PostFormValue("match_path_pattern")),
		MatchBodyContains: r.PostFormValue("match_body_contains"),
		ReplyText:         r.PostFormValue("reply_text"),
	}
	if n, err := strconv.Atoi(r.PostFormValue("http_status")); err == nil {
		rule.HTTPStatus = n
	}
	if n, err := strconv.Atoi(r.PostFormValue("reply_code")); err == nil {
		rule.ReplyCode = n
	}
	if f, err := strconv.ParseFloat(r.PostFormValue("probability"), 64); err == nil {
		rule.Probability = f
	}
	if raw := strings.TrimSpace(r.PostFormValue("remaining_hits")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			rule.RemainingHits = &n
		}
	}

	created, err := s.db.CreateFaultRule(r.Context(), rule)
	if err != nil {
		redirectWith(w, r, "/admin/faults", err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/faults",
		"Rule "+strconv.FormatInt(created.ID, 10)+" is live.", "")
}

func (s *Server) adminFaultQuick(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/faults") {
		return
	}
	once := 1
	if _, err := s.db.CreateFaultRule(r.Context(), store.FaultRule{
		MatchPathPattern: "/api/*",
		HTTPStatus:       http.StatusTooManyRequests,
		ReplyCode:        2011,
		ReplyText:        "Rate limit exceeded",
		RemainingHits:    &once,
	}); err != nil {
		redirectWith(w, r, "/admin/faults", err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/faults", "The next API request will get a 429.", "")
}

func (s *Server) adminFaultDelete(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/faults") {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("faultId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.db.DeleteFaultRule(r.Context(), id); err != nil {
		redirectWith(w, r, "/admin/faults", err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/faults", "Deleted the rule.", "")
}

// --- danger zone -----------------------------------------------------------

type dashboardStats struct {
	Contacts, Fields, Events, Triggers, Requests int
}

func (s *Server) adminDanger(w http.ResponseWriter, r *http.Request) {
	stats := dashboardStats{}
	counts := map[string]*int{
		"contacts":       &stats.Contacts,
		"fields":         &stats.Fields,
		"events":         &stats.Events,
		"event_triggers": &stats.Triggers,
		"request_log":    &stats.Requests,
	}
	for table, target := range counts {
		if err := s.db.Read.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM `+table).Scan(target); err != nil {
			s.adminError(w, "count "+table, err)
			return
		}
	}

	s.render(w, "danger", struct {
		page
		Stats       dashboardStats
		Timezone    string
		SeedExample string
	}{s.newPage(r, "Danger zone", "danger"), stats, s.cfg.ExportLocation.String(), seedExample})
}

func (s *Server) adminDangerReset(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/danger") {
		return
	}
	if err := s.db.Reset(); err != nil {
		redirectWith(w, r, "/admin/danger", "Reset failed: "+err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/danger", "Database reset to the seeded state.", "")
}

func (s *Server) adminDangerSeed(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/danger") {
		return
	}
	r.ParseForm()

	payload := strings.TrimSpace(r.PostFormValue("payload"))
	if payload == "" {
		redirectWith(w, r, "/admin/danger", "Nothing to load.", "error")
		return
	}

	var body seedRequest
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		redirectWith(w, r, "/admin/danger", "That is not valid JSON: "+err.Error(), "error")
		return
	}
	body.Reset = r.PostFormValue("reset") == "1"

	// The form is a thin wrapper around the control-plane path, so a fixture
	// behaves identically whichever way it arrives.
	summary, err := s.applySeed(r, body)
	if err != nil {
		redirectWith(w, r, "/admin/danger", "Seed failed: "+err.Error(), "error")
		return
	}

	parts := make([]string, 0, len(summary))
	for _, key := range []string{"fields", "events", "contact_lists", "contacts"} {
		if n := summary[key]; n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+strings.ReplaceAll(key, "_", " "))
		}
	}
	message := "Fixture loaded."
	if len(parts) > 0 {
		message = "Loaded " + strings.Join(parts, ", ") + "."
	}
	redirectWith(w, r, "/admin/danger", message, "")
}
