// Package server wires configuration, storage and middleware into the two HTTP
// surfaces the service exposes.
//
// The Emarsys-compatible API lives under /api and must match production
// byte-for-byte. The control plane (/_ctl) and dashboard (/admin) are our own
// and have no counterpart in the real product; the deliberately unlikely _ctl
// prefix guarantees they can never collide with an Emarsys path.
package server

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
	"github.com/dennis-kluge/emarsys-mock/internal/auth"
	"github.com/dennis-kluge/emarsys-mock/internal/config"
	"github.com/dennis-kluge/emarsys-mock/internal/httpx"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
	"github.com/dennis-kluge/emarsys-mock/internal/webhook"
)

type Server struct {
	cfg         config.Config
	db          *store.DB
	logger      *slog.Logger
	auth        *auth.Authenticator
	mux         *http.ServeMux
	webhook     *webhook.Sender
	rateLimiter *httpx.RateLimiter
}

func New(cfg config.Config, db *store.DB, logger *slog.Logger) *Server {
	s := &Server{
		cfg:         cfg,
		db:          db,
		logger:      logger,
		mux:         http.NewServeMux(),
		webhook:     webhook.New(cfg.WebhookURL, cfg.WebhookTimeout, logger),
		rateLimiter: httpx.NewRateLimiter(cfg.RateLimitPerMinute),
		auth: auth.New(directory{db}, auth.Options{
			Skew:             cfg.WSSESkew,
			RejectNonceReuse: cfg.WSSERejectReuse,
			SigningKey:       cfg.OAuthSigningKey,
			TokenTTL:         cfg.OAuthTokenTTL,
		}),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	// Token issuance cannot itself require a token.
	s.mux.Handle("POST /oauth2/token", s.auth.TokenHandler())

	// Probes must answer before any credential is configured, so health sits
	// outside the control plane's guard.
	s.mux.HandleFunc("GET /_ctl/health", s.handleCtlHealth)

	authenticated := http.NewServeMux()
	authenticated.HandleFunc("/", s.handleNotImplemented)
	s.registerV2(authenticated)
	s.mux.Handle("/api/", s.auth.Middleware(authenticated))

	s.registerControlPlane()
}

// registerControlPlane wires /_ctl. Everything here is ours; none of it exists
// in the real product.
func (s *Server) registerControlPlane() {
	ctl := http.NewServeMux()
	ctl.HandleFunc("POST /_ctl/reset", s.handleCtlReset)
	ctl.HandleFunc("POST /_ctl/seed", s.handleCtlSeed)
	ctl.HandleFunc("GET /_ctl/requests", s.handleCtlRequests)
	ctl.HandleFunc("GET /_ctl/events/triggers", s.handleCtlTriggers)
	ctl.HandleFunc("GET /_ctl/faults", s.handleCtlFaultList)
	ctl.HandleFunc("POST /_ctl/faults", s.handleCtlFaultCreate)
	ctl.HandleFunc("DELETE /_ctl/faults/{faultId}", s.handleCtlFaultDelete)
	ctl.HandleFunc("POST /_ctl/exports/{exportId}/status", s.handleCtlExportStatus)
	s.mux.Handle("/_ctl/", s.ctlAuth(ctl))
}

// registerV2 wires the Emarsys-compatible v2 surface.
//
// Clients differ on the trailing slash and both forms reach production, so
// every collection-level route is registered twice.
func (s *Server) registerV2(mux *http.ServeMux) {
	both := func(method, path string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+path, h)
		mux.HandleFunc(method+" "+path+"/{$}", h)
	}

	// Fields
	both("GET", "/api/v2/field", s.handleFieldList)
	both("POST", "/api/v2/field", s.handleFieldCreate)
	mux.HandleFunc("GET /api/v2/field/choices", s.handleFieldChoices)
	mux.HandleFunc("DELETE /api/v2/field/{fieldId}", s.handleFieldDelete)
	// /field/translate/{languageId} and /field/{fieldId}/choice are both five
	// segments with a wildcard in a different position, which ServeMux cannot
	// rank. One handler takes both and dispatches on the literal segment.
	mux.HandleFunc("GET /api/v2/field/{first}/{second}", s.handleFieldSubpath)
	mux.HandleFunc("GET /api/v2/field/{fieldId}/choice/translate/{languageId}", s.handleFieldChoice)

	// Contacts
	both("POST", "/api/v2/contact", s.handleContactCreate)
	both("PUT", "/api/v2/contact", s.handleContactUpdate)
	mux.HandleFunc("POST /api/v2/contact/getdata", s.handleContactGetData)
	both("GET", "/api/v2/contact/query", s.handleContactQuery)
	mux.HandleFunc("POST /api/v2/contact/checkids", s.handleContactCheckIDs)
	// getid is not in the official Postman collection; it is served here
	// because integrations written against the older documentation call it.
	mux.HandleFunc("POST /api/v2/contact/getid", s.handleContactCheckIDs)
	mux.HandleFunc("POST /api/v2/contact/delete", s.handleContactDelete)
	mux.HandleFunc("POST /api/v2/contact/last_change", s.handleContactLastChange)

	// Events
	both("GET", "/api/v2/event", s.handleEventList)
	both("POST", "/api/v2/event", s.handleEventCreate)
	mux.HandleFunc("GET /api/v2/event/{eventId}", s.handleEventGet)
	mux.HandleFunc("POST /api/v2/event/{eventId}", s.handleEventRename)
	mux.HandleFunc("POST /api/v2/event/{eventId}/delete", s.handleEventDelete)
	mux.HandleFunc("POST /api/v2/event/{eventId}/trigger", s.handleEventTrigger)
	mux.HandleFunc("GET /api/v2/event/{eventId}/usages", s.handleEventUsages)

	// Contact lists. Note that /delete removes contacts from the list while
	// /deletelist removes the list itself.
	both("GET", "/api/v2/contactlist", s.handleContactListList)
	both("POST", "/api/v2/contactlist", s.handleContactListCreate)
	mux.HandleFunc("GET /api/v2/contactlist/{listId}/{$}", s.handleContactListMembers)
	mux.HandleFunc("GET /api/v2/contactlist/{listId}/count", s.handleContactListCount)
	both("GET", "/api/v2/contactlist/{listId}/contacts", s.handleContactListMembers)
	mux.HandleFunc("GET /api/v2/contactlist/{listId}/contacts/data", s.handleContactListData)
	mux.HandleFunc("POST /api/v2/contactlist/{listId}/add", s.handleContactListAdd)
	mux.HandleFunc("POST /api/v2/contactlist/{listId}/delete", s.handleContactListRemove)
	mux.HandleFunc("POST /api/v2/contactlist/{listId}/replace", s.handleContactListReplace)
	mux.HandleFunc("POST /api/v2/contactlist/{listId}/rename", s.handleContactListRename)
	mux.HandleFunc("POST /api/v2/contactlist/{listId}/deletelist", s.handleContactListDelete)
	mux.HandleFunc("POST /api/v2/contactlist/{listId}/export", s.handleContactListExport)

	// Asynchronous exports
	mux.HandleFunc("POST /api/v2/contact/getchanges", s.handleGetChanges)
	mux.HandleFunc("POST /api/v2/contact/getregistrations", s.handleGetRegistrations)
	mux.HandleFunc("GET /api/v2/export/{exportId}", s.handleExportStatus)
	mux.HandleFunc("GET /api/v2/export/{exportId}/data", s.handleExportData)
}

// Handler returns the fully wrapped handler.
//
// The order matters. Logging sits near the outside so a rate-limited or
// fault-injected request still shows up in the log, and both of those sit
// outside the router so they never depend on a route existing.
func (s *Server) Handler() http.Handler {
	return httpx.Chain(s.mux,
		httpx.Recover(s.logger),
		httpx.Logging(s.db, httpx.LogOptions{
			Max: s.cfg.RequestLogMax,
			// Probe and asset traffic would otherwise crowd out the calls
			// anyone actually wants to look at.
			SkipPrefixes: []string{"/_ctl/health", "/_ctl/requests", "/admin/static/"},
		}),
		httpx.LimitBody(s.cfg.MaxBodyBytes, s.cfg.MaxContactBodyBytes),
		s.rateLimiter.Middleware(rateLimitKey, isEmarsysPath),
		s.faultMiddleware,
	)
}

// rateLimitKey identifies the caller a limit applies to.
//
// Emarsys limits per API key, so the WSSE username or the bearer token is the
// right bucket. The remote address is only a fallback for unauthenticated
// calls, which are rejected a moment later anyway.
func rateLimitKey(r *http.Request) string {
	if header := r.Header.Get("X-WSSE"); header != "" {
		if token, err := auth.ParseToken(header); err == nil {
			return "wsse:" + token.Username
		}
	}
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		return "bearer:" + header[len("Bearer "):]
	}
	return "addr:" + r.RemoteAddr
}

// isEmarsysPath keeps the limit off our own surfaces: a rate-limited control
// plane would make the mock unrecoverable exactly when a test needs to reset it.
func isEmarsysPath(path string) bool {
	return strings.HasPrefix(path, "/api/")
}

// Shutdown waits for in-flight webhook deliveries, so a trigger accepted just
// before a SIGTERM is not silently dropped.
func (s *Server) Shutdown() { s.webhook.Wait() }

// internalError logs the cause and answers with the generic Emarsys internal
// error, so a bug in the mock never reaches a client as a broken connection.
func (s *Server) internalError(w http.ResponseWriter, what string, err error) {
	s.logger.Error("request failed", "what", what, "err", err)
	api.Error(w, api.CodeInternalError)
}

// handleNotImplemented answers any authenticated /api path that has no handler
// yet. Returning a well-formed envelope rather than Go's default 404 page means
// a client that hits an endpoint we have not built can tell that apart from a
// transport failure.
func (s *Server) handleNotImplemented(w http.ResponseWriter, r *http.Request) {
	api.ErrorStatus(w, http.StatusNotFound, api.CodeInternalError,
		"Endpoint not implemented by emarsys-mock: "+r.Method+" "+r.URL.Path)
}
