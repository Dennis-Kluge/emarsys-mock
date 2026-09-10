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

	"github.com/dennis-kluge/emarsys-mock/internal/api"
	"github.com/dennis-kluge/emarsys-mock/internal/auth"
	"github.com/dennis-kluge/emarsys-mock/internal/config"
	"github.com/dennis-kluge/emarsys-mock/internal/httpx"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

type Server struct {
	cfg    config.Config
	db     *store.DB
	logger *slog.Logger
	auth   *auth.Authenticator
	mux    *http.ServeMux
}

func New(cfg config.Config, db *store.DB, logger *slog.Logger) *Server {
	s := &Server{
		cfg:    cfg,
		db:     db,
		logger: logger,
		mux:    http.NewServeMux(),
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

	// Probes must answer before any credential is configured.
	s.mux.HandleFunc("GET /_ctl/health", s.handleHealth)

	authenticated := http.NewServeMux()
	authenticated.HandleFunc("/", s.handleNotImplemented)
	s.registerV2(authenticated)
	s.mux.Handle("/api/", s.auth.Middleware(authenticated))
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
}

// Handler returns the fully wrapped handler.
func (s *Server) Handler() http.Handler {
	return httpx.Chain(s.mux,
		httpx.Recover(s.logger),
		httpx.Logging(s.db, httpx.LogOptions{
			Max:          s.cfg.RequestLogMax,
			SkipPrefixes: []string{"/_ctl/health", "/admin/static/"},
		}),
		httpx.LimitBody(s.cfg.MaxBodyBytes, s.cfg.MaxContactBodyBytes),
	)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var fields int
	_ = s.db.Read.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM fields`).Scan(&fields)
	api.OK(w, map[string]any{
		"status":    "ok",
		"read_only": s.cfg.ReadOnly,
		"fields":    fields,
	})
}

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
