package httpx

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
)

// Recover turns a handler panic into a well-formed Emarsys error envelope. A
// mock that drops the connection on a bug is much harder to debug from the
// client side than one that answers with replyCode 2011.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					logger.Error("panic serving request",
						"path", r.URL.Path,
						"panic", v,
						"stack", string(debug.Stack()))
					api.Error(w, api.CodeInternalError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// LimitBody rejects oversized payloads before a handler sees them. Emarsys caps
// requests at 10 MB overall and 8 MB for contact batches; contactLimit applies
// to paths under /api/v2/contact and /api/v3/contacts.
func LimitBody(general, contact int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limit := general
			if isContactPath(r.URL.Path) && contact > 0 && contact < general {
				limit = contact
			}
			if limit > 0 {
				if r.ContentLength > limit {
					api.ErrorStatus(w, http.StatusRequestEntityTooLarge,
						api.CodeInternalError, "Request entity too large")
					return
				}
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isContactPath(path string) bool {
	return len(path) >= len("/api/v2/contact") &&
		(hasPrefix(path, "/api/v2/contact") || hasPrefix(path, "/api/v3/contacts"))
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// Chain applies middleware so that the first argument is the outermost layer.
func Chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}
