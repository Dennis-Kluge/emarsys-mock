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
	s.mux.Handle("/api/", s.auth.Middleware(authenticated))
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

// handleNotImplemented answers any authenticated /api path that has no handler
// yet. Returning a well-formed envelope rather than Go's default 404 page means
// a client that hits an endpoint we have not built can tell that apart from a
// transport failure.
func (s *Server) handleNotImplemented(w http.ResponseWriter, r *http.Request) {
	api.ErrorStatus(w, http.StatusNotFound, api.CodeInternalError,
		"Endpoint not implemented by emarsys-mock: "+r.Method+" "+r.URL.Path)
}
