package auth

import (
	"net/http"
	"time"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
)

// TokenHandler serves POST /oauth2/token with the client-credentials grant.
//
// Credentials arrive as HTTP Basic auth, matching the Emarsys OIDC flow. The
// issued token is a self-contained HS256 JWT, so verifying it needs no state.
func (a *Authenticator) TokenHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			api.ErrorStatus(w, http.StatusMethodNotAllowed, api.CodeUnauthorized, "Method not allowed")
			return
		}
		clientID, clientSecret, ok := r.BasicAuth()
		if !ok {
			// Some clients put the credentials in the form body instead.
			_ = r.ParseForm()
			clientID = r.PostFormValue("client_id")
			clientSecret = r.PostFormValue("client_secret")
		}
		if clientID == "" || clientSecret == "" {
			writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client credentials missing")
			return
		}

		_ = r.ParseForm()
		if grant := r.PostFormValue("grant_type"); grant != "" && grant != "client_credentials" {
			writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
				"only client_credentials is supported")
			return
		}

		principal, err := a.dir.ByClientID(r.Context(), clientID)
		if err != nil || !subtleEqual(principal.ClientSecret, clientSecret) {
			writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
			return
		}

		now := a.opts.Now()
		ttl := a.opts.TokenTTL
		if ttl <= 0 {
			ttl = time.Hour
		}
		token, err := SignJWT(Claims{
			Subject:  principal.Username,
			ClientID: principal.ClientID,
			Issuer:   "emarsys-mock",
			IssuedAt: now.Unix(),
			Expires:  now.Add(ttl).Unix(),
		}, a.opts.SigningKey)
		if err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not issue token")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": token,
			"token_type":   "Bearer",
			"expires_in":   int(ttl.Seconds()),
		})
	}
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]any{
		"error":             code,
		"error_description": description,
	})
}
