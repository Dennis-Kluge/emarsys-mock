package server

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

//go:embed templates/*.html templates/partials/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// The dashboard is server-rendered. There is no build step, no bundler and no
// CDN: the stylesheet and htmx are embedded in the binary, so the single-file
// deployment story survives contact with the UI.

// dashboardMethods is the method filter offered in the request log and the
// fault editor.
var dashboardMethods = []string{"GET", "POST", "PUT", "DELETE"}

// page is the data every template receives.
type page struct {
	Title     string
	Nav       string
	ReadOnly  bool
	Flash     string
	FlashKind string
}

func (s *Server) newPage(r *http.Request, title, nav string) page {
	return page{
		Title:     title,
		Nav:       nav,
		ReadOnly:  s.cfg.ReadOnly,
		Flash:     r.URL.Query().Get("msg"),
		FlashKind: r.URL.Query().Get("kind"),
	}
}

// parseTemplates builds one template set per page. The funcs are bound here
// because several of them close over the configured export timezone.
func (s *Server) parseTemplates() error {
	funcs := template.FuncMap{
		"local":       s.templateLocalTime,
		"itoa":        strconv.Itoa,
		"deref":       derefString,
		"deref32":     derefInt,
		"deref64":     derefInt64,
		"statusClass": statusClass,
		"replyClass":  replyClass,
		"exportClass": exportClass,
		"optIn":       optInLabel,
		"prettyJSON":  prettyJSON,
	}

	entries, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	s.templates = map[string]*template.Template{}
	for _, entry := range entries {
		name := strings.TrimSuffix(strings.TrimPrefix(entry, "templates/"), ".html")
		if name == "layout" {
			continue
		}
		tmpl, parseErr := template.New("layout.html").Funcs(funcs).
			ParseFS(templateFS, "templates/layout.html", "templates/partials/*.html", entry)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", entry, parseErr)
		}
		s.templates[name] = tmpl
	}

	// The partials are also parsed on their own, so htmx can refresh one part
	// of a page without the layout around it.
	fragments, err := template.New("fragments").Funcs(funcs).
		ParseFS(templateFS, "templates/partials/*.html")
	if err != nil {
		return fmt.Errorf("parse partials: %w", err)
	}
	s.fragments = fragments
	return nil
}

// renderFragment writes a single partial, which is what an htmx swap replaces.
func (s *Server) renderFragment(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.fragments.ExecuteTemplate(&buf, name, data); err != nil {
		s.logger.Error("fragment failed", "name", name, "err", err)
		http.Error(w, "could not render "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html;charset=utf-8")
	w.WriteHeader(http.StatusOK)
	buf.WriteTo(w)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := s.templates[name]
	if !ok {
		s.logger.Error("unknown template", "name", name)
		http.Error(w, "unknown view", http.StatusInternalServerError)
		return
	}
	// Render into a buffer first: a template error halfway through would
	// otherwise reach the browser as a half-written page with a 200 on it.
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		s.logger.Error("template failed", "name", name, "err", err)
		http.Error(w, "could not render "+name, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html;charset=utf-8")
	w.WriteHeader(http.StatusOK)
	buf.WriteTo(w)
}

// redirectWith sends the browser somewhere with a flash message attached. A
// query parameter keeps this stateless -- no session, no cookie, no store.
func redirectWith(w http.ResponseWriter, r *http.Request, target, message, kind string) {
	if message != "" {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + "msg=" + url.QueryEscape(message)
		if kind != "" {
			target += "&kind=" + url.QueryEscape(kind)
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// adminWritable refuses a mutation in read-only mode and reports whether the
// caller may continue.
func (s *Server) adminWritable(w http.ResponseWriter, r *http.Request, back string) bool {
	if s.cfg.ReadOnly {
		redirectWith(w, r, back, "This instance is read-only.", "error")
		return false
	}
	return true
}

func (s *Server) registerDashboard() {
	admin := http.NewServeMux()

	admin.Handle("GET /admin/static/", http.StripPrefix("/admin/", http.FileServerFS(staticFS)))
	admin.HandleFunc("GET /admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		// The request log is where anyone debugging an integration ends up, so
		// that is where the dashboard opens.
		http.Redirect(w, r, "/admin/requests", http.StatusSeeOther)
	})

	admin.HandleFunc("GET /admin/requests", s.adminRequests)
	admin.HandleFunc("GET /admin/requests/table", s.adminRequestsTable)
	admin.HandleFunc("GET /admin/contacts", s.adminContacts)
	admin.HandleFunc("GET /admin/contacts/{contactId}", s.adminContactDetail)
	admin.HandleFunc("POST /admin/contacts/{contactId}/field", s.adminContactSetField)

	admin.HandleFunc("GET /admin/fields", s.adminFields)
	admin.HandleFunc("POST /admin/fields", s.adminFieldCreate)
	admin.HandleFunc("POST /admin/fields/{fieldId}/delete", s.adminFieldDelete)
	admin.HandleFunc("POST /admin/fields/{fieldId}/index", s.adminFieldIndex)
	admin.HandleFunc("POST /admin/fields/{fieldId}/choices", s.adminFieldChoice)

	admin.HandleFunc("GET /admin/lists", s.adminLists)
	admin.HandleFunc("POST /admin/lists", s.adminListCreate)
	admin.HandleFunc("POST /admin/lists/{listId}/delete", s.adminListDelete)
	admin.HandleFunc("POST /admin/lists/{listId}/members", s.adminListMembers)
	admin.HandleFunc("POST /admin/segments", s.adminSegmentCreate)
	admin.HandleFunc("POST /admin/segments/{segmentId}/delete", s.adminSegmentDelete)
	admin.HandleFunc("POST /admin/segments/{segmentId}/members", s.adminSegmentMembers)

	admin.HandleFunc("GET /admin/events", s.adminEvents)
	admin.HandleFunc("POST /admin/events", s.adminEventCreate)
	admin.HandleFunc("POST /admin/events/{eventId}/delete", s.adminEventDelete)

	admin.HandleFunc("GET /admin/exports", s.adminExports)
	admin.HandleFunc("POST /admin/exports/{exportId}/status", s.adminExportStatus)
	admin.HandleFunc("GET /admin/exports/{exportId}/download", s.adminExportDownload)

	admin.HandleFunc("GET /admin/faults", s.adminFaults)
	admin.HandleFunc("POST /admin/faults", s.adminFaultCreate)
	admin.HandleFunc("POST /admin/faults/quick", s.adminFaultQuick)
	admin.HandleFunc("POST /admin/faults/{faultId}/delete", s.adminFaultDelete)

	admin.HandleFunc("GET /admin/danger", s.adminDanger)
	admin.HandleFunc("POST /admin/danger/reset", s.adminDangerReset)
	admin.HandleFunc("POST /admin/danger/seed", s.adminDangerSeed)

	s.mux.Handle("/admin/", s.ctlAuth(admin))
	s.mux.Handle("/admin", http.RedirectHandler("/admin/", http.StatusSeeOther))
}

// --- template helpers ------------------------------------------------------

// templateLocalTime renders either a stored timestamp string or a time.Time in
// the configured export timezone.
func (s *Server) templateLocalTime(v any) string {
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return ""
		}
		return t.In(s.cfg.ExportLocation).Format(emarsysTimeLayout)
	case string:
		if t == "" {
			return ""
		}
		return s.localTime(t)
	default:
		return fmt.Sprint(v)
	}
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func statusClass(status int) string {
	switch {
	case status >= 500:
		return "bad"
	case status >= 400:
		return "warn"
	case status >= 200 && status < 300:
		return "ok"
	default:
		return "neutral"
	}
}

// replyClass colours a reply code by what it means for the caller, not by its
// numeric range: 0 is fine, everything else is a problem worth spotting.
func replyClass(code *int) string {
	if code == nil {
		return "neutral"
	}
	if *code == 0 {
		return "ok"
	}
	return "bad"
}

func exportClass(status string) string {
	switch status {
	case store.ExportDone:
		return "ok"
	case store.ExportError:
		return "bad"
	default:
		return "warn"
	}
}

// optInLabel spells out the opt-in codes, which are otherwise two bare digits
// whose meaning nobody remembers.
func optInLabel(value string) template.HTML {
	switch value {
	case "1":
		return `<span class="pill ok">1 opted in</span>`
	case "2":
		return `<span class="pill bad">2 opted out</span>`
	case "":
		return `<span class="muted">—</span>`
	default:
		return template.HTML(`<span class="pill warn">` + template.HTMLEscapeString(value) + `</span>`)
	}
}

func prettyJSON(raw string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
		return raw
	}
	return buf.String()
}
