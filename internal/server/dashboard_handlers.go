package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// --- request log -----------------------------------------------------------

// requestsView is the data behind both the full page and the htmx fragment.
type requestsView struct {
	page
	Entries         []store.RequestLogEntry
	Filter          store.RequestLogFilter
	ReplyCodeFilter string
	Methods         []string
	Live            bool
	// RefreshURL is what the live table polls; ToggleURL flips live mode.
	RefreshURL string
	ToggleURL  string
}

func (s *Server) adminRequests(w http.ResponseWriter, r *http.Request) {
	view, ok := s.buildRequestsView(w, r)
	if !ok {
		return
	}
	s.render(w, "requests", view)
}

// adminRequestsTable serves the table on its own, which is what the live view
// polls. Refreshing only the table keeps the filter inputs from losing focus
// under the cursor every few seconds.
func (s *Server) adminRequestsTable(w http.ResponseWriter, r *http.Request) {
	view, ok := s.buildRequestsView(w, r)
	if !ok {
		return
	}
	s.renderFragment(w, "requests_table", view)
}

func (s *Server) buildRequestsView(w http.ResponseWriter, r *http.Request) (requestsView, bool) {
	params := r.URL.Query()
	filter := store.RequestLogFilter{
		Path:   params.Get("path"),
		Method: strings.ToUpper(params.Get("method")),
		Limit:  200,
	}
	if raw := params.Get("status"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			filter.Status = n
		}
	}

	entries, err := s.db.RequestLogEntries(r.Context(), filter)
	if err != nil {
		s.adminError(w, "read request log", err)
		return requestsView{}, false
	}

	// The reply code is not a column the query filters on, so it is applied
	// here rather than pushed into SQL for one optional field.
	replyFilter := params.Get("reply_code")
	if replyFilter != "" {
		if want, convErr := strconv.Atoi(replyFilter); convErr == nil {
			kept := entries[:0]
			for _, e := range entries {
				if e.ReplyCode != nil && *e.ReplyCode == want {
					kept = append(kept, e)
				}
			}
			entries = kept
		}
	}

	// Newest first: the thing that just happened is what you came to look at.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	live := params.Get("live") == "1"

	// url.Values is a map, so these have to be copies. Sharing one would let
	// the toggle URL's Del strip live=1 back out of the refresh URL, and the
	// first swap would silently turn the tail off again.
	refresh := cloneValues(params)
	refresh.Set("live", "1")
	toggle := cloneValues(params)
	if live {
		toggle.Del("live")
	} else {
		toggle.Set("live", "1")
	}

	return requestsView{
		page:            s.newPage(r, "Requests", "requests"),
		Entries:         entries,
		Filter:          filter,
		ReplyCodeFilter: replyFilter,
		Methods:         dashboardMethods,
		Live:            live,
		RefreshURL:      "/admin/requests/table?" + refresh.Encode(),
		ToggleURL:       "/admin/requests?" + toggle.Encode(),
	}, true
}

// --- contacts --------------------------------------------------------------

// contactListFields are the columns the contact list shows. They are the ones
// anyone scanning for a specific contact actually reads.
var contactListFields = []int{1, 2, 3, 31}

func (s *Server) adminContacts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")

	contacts, err := s.db.SearchContacts(r.Context(), query, contactListFields, 200)
	if err != nil {
		s.adminError(w, "search contacts", err)
		return
	}
	total, err := s.db.CountContacts(r.Context())
	if err != nil {
		s.adminError(w, "count contacts", err)
		return
	}

	s.render(w, "contacts", struct {
		page
		Contacts []store.ContactSummary
		Query    string
		Total    int
	}{s.newPage(r, "Contacts", "contacts"), contacts, query, total})
}

// contactField is one row of the contact detail table.
type contactField struct {
	ID              int
	Name            string
	ApplicationType string
	Indexed         bool
	Value           string
	Choices         []store.Choice
}

func (s *Server) adminContactDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("contactId"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	contact, err := s.db.Contact(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNoContact) {
			http.NotFound(w, r)
			return
		}
		s.adminError(w, "load contact", err)
		return
	}
	catalog, err := s.db.LoadFieldCatalog(r.Context())
	if err != nil {
		s.adminError(w, "load field catalog", err)
		return
	}
	history, err := s.db.ContactHistory(r.Context(), id, 200)
	if err != nil {
		s.adminError(w, "load contact history", err)
		return
	}

	fields := make([]contactField, 0, len(catalog.All()))
	names := map[int]string{}
	for _, f := range catalog.All() {
		names[f.ID] = f.Name
		fields = append(fields, contactField{
			ID:              f.ID,
			Name:            f.Name,
			ApplicationType: f.ApplicationType,
			Indexed:         f.IsIndexed,
			Value:           contact.Values[f.ID],
			Choices:         catalog.Choices(f.ID),
		})
	}

	s.render(w, "contact_detail", struct {
		page
		Contact    store.ContactSummary
		CreatedAt  string
		UpdatedAt  string
		Fields     []contactField
		History    []store.FieldChange
		FieldNames map[int]string
	}{s.newPage(r, "Contact "+strconv.FormatInt(id, 10), "contacts"),
		contact, contact.CreatedAt, contact.UpdatedAt, fields, history, names})
}

func (s *Server) adminContactSetField(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("contactId")
	back := "/admin/contacts/" + id
	if !s.adminWritable(w, r, back) {
		return
	}
	contactID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirectWith(w, r, back, "Could not read the form.", "error")
		return
	}

	fieldID, err := strconv.Atoi(r.PostFormValue("field_id"))
	if err != nil {
		redirectWith(w, r, back, "Invalid field id.", "error")
		return
	}
	value := r.PostFormValue("value")

	catalog, err := s.db.LoadFieldCatalog(r.Context())
	if err != nil {
		s.adminError(w, "load field catalog", err)
		return
	}
	// The dashboard validates exactly as the API does. An edit here that the
	// API would have rejected would make the mock lie about its own data.
	if err := validateValue(fieldID, value, catalog); err != nil {
		redirectWith(w, r, back, err.Error(), "error")
		return
	}

	err = s.db.WithTx(r.Context(), func(tx *sqlTx) error {
		return store.ApplyContactValues(r.Context(), tx, contactID,
			map[int]string{fieldID: value}, "dashboard")
	})
	if err != nil {
		s.adminError(w, "update contact", err)
		return
	}

	field, _ := catalog.Get(fieldID)
	message := "Saved " + field.Name + "."
	if value == "" {
		message = "Cleared " + field.Name + "."
	}
	redirectWith(w, r, back, message, "")
}

// --- fields ----------------------------------------------------------------

// adminFieldRow adds a field's choices to its definition for the catalogue view.
type adminFieldRow struct {
	store.FieldDef
	Choices []store.Choice
}

func (s *Server) adminFields(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.db.LoadFieldCatalog(r.Context())
	if err != nil {
		s.adminError(w, "load field catalog", err)
		return
	}

	rows := make([]adminFieldRow, 0, len(catalog.All()))
	for _, f := range catalog.All() {
		rows = append(rows, adminFieldRow{FieldDef: f, Choices: catalog.Choices(f.ID)})
	}

	types := make([]string, 0, len(applicationTypes))
	for name := range applicationTypes {
		types = append(types, name)
	}
	sortStrings(types)

	s.render(w, "fields", struct {
		page
		Fields           []adminFieldRow
		ApplicationTypes []string
	}{s.newPage(r, "Fields", "fields"), rows, types})
}

func (s *Server) adminFieldCreate(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/fields") {
		return
	}
	r.ParseForm()

	name := strings.TrimSpace(r.PostFormValue("name"))
	appType := r.PostFormValue("application_type")
	if name == "" || !applicationTypes[appType] {
		redirectWith(w, r, "/admin/fields", "A name and a known application type are required.", "error")
		return
	}

	id, err := s.db.CreateField(r.Context(), name, appType,
		strings.TrimSpace(r.PostFormValue("string_id")), r.PostFormValue("indexed") == "1")
	if err != nil {
		redirectWith(w, r, "/admin/fields", err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/fields",
		"Created field "+strconv.Itoa(id)+" — that is the id integrations will send.", "")
}

func (s *Server) adminFieldDelete(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/fields") {
		return
	}
	id, err := strconv.Atoi(r.PathValue("fieldId"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.db.DeleteField(r.Context(), id); err != nil {
		redirectWith(w, r, "/admin/fields", err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/fields", "Deleted field "+strconv.Itoa(id)+".", "")
}

func (s *Server) adminFieldIndex(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/fields") {
		return
	}
	id, err := strconv.Atoi(r.PathValue("fieldId"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()
	indexed := r.PostFormValue("indexed") == "1"

	if err := s.db.SetFieldIndexed(r.Context(), id, indexed); err != nil {
		redirectWith(w, r, "/admin/fields", err.Error(), "error")
		return
	}
	state := "no longer queryable"
	if indexed {
		state = "queryable via contact/query"
	}
	redirectWith(w, r, "/admin/fields", "Field "+strconv.Itoa(id)+" is "+state+".", "")
}

func (s *Server) adminFieldChoice(w http.ResponseWriter, r *http.Request) {
	if !s.adminWritable(w, r, "/admin/fields") {
		return
	}
	fieldID, err := strconv.Atoi(r.PathValue("fieldId"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()

	choiceID, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("choice_id")))
	if err != nil {
		redirectWith(w, r, "/admin/fields", "Choice id must be a number.", "error")
		return
	}
	label := strings.TrimSpace(r.PostFormValue("label"))
	if label == "" {
		redirectWith(w, r, "/admin/fields", "Choice label must not be empty.", "error")
		return
	}
	if err := s.db.AddChoice(r.Context(), fieldID, choiceID, label, choiceID); err != nil {
		redirectWith(w, r, "/admin/fields", err.Error(), "error")
		return
	}
	redirectWith(w, r, "/admin/fields", "Added choice "+strconv.Itoa(choiceID)+".", "")
}

func cloneValues(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func (s *Server) adminError(w http.ResponseWriter, what string, err error) {
	s.logger.Error("dashboard failed", "what", what, "err", err)
	http.Error(w, "could not "+what+": "+err.Error(), http.StatusInternalServerError)
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// seedExample is prefilled into the danger zone so the fixture format is
// discoverable rather than something to look up.
const seedExample = `{
  "fields": [
    {"id": 45, "name": "Loyalty tier", "application_type": "singlechoice",
     "choices": [{"id": 1, "choice": "Silver"}, {"id": 2, "choice": "Gold"}]}
  ],
  "events": [{"name": "tier_upgraded"}],
  "contact_lists": [{"name": "Gold members"}],
  "contacts": [{"3": "jane@example.com", "1": "Jane", "31": "1"}]
}`
