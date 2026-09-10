package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
)

// Principal is a set of credentials the mock accepts, in the shape auth needs.
// Keeping it separate from the store row lets the authentication logic be
// tested without a database.
type Principal struct {
	Username     string
	Secret       string
	ClientID     string
	ClientSecret string
	// Permissions are request paths this principal may call. "*" grants
	// everything; a pattern ending in "*" is a prefix match; anything else must
	// match the path exactly. A principal with no permissions gets 403 on every
	// endpoint, which is how the mock reproduces a partially provisioned API
	// user.
	Permissions []string
}

// ErrUnknownPrincipal is returned by a Directory when no credentials match.
var ErrUnknownPrincipal = errors.New("unknown principal")

// Directory resolves credentials. The store implements it.
type Directory interface {
	ByUsername(ctx context.Context, username string) (*Principal, error)
	ByClientID(ctx context.Context, clientID string) (*Principal, error)
	// RememberNonce records a WSSE nonce and reports whether it was unseen.
	RememberNonce(ctx context.Context, nonce string, seenAt time.Time) (bool, error)
}

type Options struct {
	Skew             time.Duration
	RejectNonceReuse bool
	SigningKey       []byte
	TokenTTL         time.Duration
	// Now is injectable so tests can pin the clock. Defaults to time.Now.
	Now func() time.Time
}

type Authenticator struct {
	dir  Directory
	opts Options
}

func New(dir Directory, opts Options) *Authenticator {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Authenticator{dir: dir, opts: opts}
}

type principalCtxKey struct{}

// PrincipalFrom returns the authenticated principal, if any.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalCtxKey{}).(*Principal)
	return p, ok
}

// Middleware authenticates a request via X-WSSE or a bearer token and enforces
// the principal's endpoint permissions.
//
// Failure modes follow production: bad or missing credentials are HTTP 401 with
// replyCode 1, while valid credentials that lack the endpoint permission are
// HTTP 403.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := a.authenticate(r)
		if err != nil {
			api.ErrorStatus(w, http.StatusUnauthorized, api.CodeUnauthorized, unauthorizedText(err))
			return
		}
		if !principal.Allows(r.URL.Path) {
			api.ErrorStatus(w, http.StatusForbidden, api.CodeUnauthorized,
				"The API user has no permission for this endpoint")
			return
		}
		if rec, ok := w.(interface{ RecordAuthUser(string) }); ok {
			rec.RecordAuthUser(principal.Username)
		}
		ctx := context.WithValue(r.Context(), principalCtxKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *Authenticator) authenticate(r *http.Request) (*Principal, error) {
	if header := r.Header.Get("X-WSSE"); header != "" {
		return a.authenticateWSSE(r, header)
	}
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		return a.authenticateBearer(r, strings.TrimPrefix(header, "Bearer "))
	}
	return nil, ErrMalformedHeader
}

func (a *Authenticator) authenticateWSSE(r *http.Request, header string) (*Principal, error) {
	token, err := ParseToken(header)
	if err != nil {
		return nil, err
	}
	principal, err := a.dir.ByUsername(r.Context(), token.Username)
	if err != nil {
		return nil, ErrUnknownPrincipal
	}
	now := a.opts.Now()
	if err := token.Verify(principal.Secret, now, a.opts.Skew); err != nil {
		return nil, err
	}
	if a.opts.RejectNonceReuse {
		fresh, err := a.dir.RememberNonce(r.Context(), token.Nonce, now)
		if err != nil {
			return nil, err
		}
		if !fresh {
			return nil, errors.New("nonce already used")
		}
	}
	return principal, nil
}

func (a *Authenticator) authenticateBearer(r *http.Request, raw string) (*Principal, error) {
	claims, err := ParseJWT(raw, a.opts.SigningKey, a.opts.Now())
	if err != nil {
		return nil, err
	}
	principal, err := a.dir.ByClientID(r.Context(), claims.ClientID)
	if err != nil {
		return nil, ErrUnknownPrincipal
	}
	return principal, nil
}

// Allows reports whether the principal may call the given request path.
func (p *Principal) Allows(path string) bool {
	for _, pattern := range p.Permissions {
		switch {
		case pattern == "*":
			return true
		case strings.HasSuffix(pattern, "*"):
			if strings.HasPrefix(path, strings.TrimSuffix(pattern, "*")) {
				return true
			}
		case pattern == path:
			return true
		}
	}
	return false
}

func unauthorizedText(err error) string {
	switch {
	case errors.Is(err, ErrStaleCreated):
		return "Wrong credentials: the Created timestamp is outside the accepted window"
	case errors.Is(err, ErrTokenExpired):
		return "Wrong credentials: the access token has expired"
	case errors.Is(err, ErrMalformedHeader):
		return "Wrong credentials: missing or malformed authentication header"
	default:
		return "Wrong credentials"
	}
}
