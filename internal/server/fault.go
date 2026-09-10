package server

import (
	"bytes"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"

	"github.com/dennis-kluge/emarsys-mock/internal/api"
	"github.com/dennis-kluge/emarsys-mock/internal/store"
)

// faultMiddleware short-circuits requests that match an enabled fault rule.
//
// Rules apply only to the Emarsys surface. A rule that could break /_ctl would
// make the mock unrecoverable: the endpoint needed to delete the rule would be
// the one failing.
func (s *Server) faultMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		rules, err := s.db.FaultRules(r.Context(), true)
		if err != nil {
			s.logger.Error("could not read fault rules", "err", err)
			next.ServeHTTP(w, r)
			return
		}
		if len(rules) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		body := peekBody(r, len(rules) > 0)

		for _, rule := range rules {
			if !ruleMatches(rule, r, body) {
				continue
			}
			// A rule below 1.0 fires only some of the time, which is how a
			// flaky upstream is simulated.
			if rule.Probability < 1 && rand.Float64() >= rule.Probability {
				continue
			}
			if err := s.db.ConsumeFaultHit(r.Context(), rule.ID); err != nil {
				s.logger.Error("could not consume fault hit", "err", err)
			}
			s.logger.Info("fault rule fired",
				"rule", rule.ID, "method", r.Method, "path", r.URL.Path, "status", rule.HTTPStatus)

			// Without this an injected 429 is missing the header a client's
			// backoff reads, so the path the injection exists to exercise is
			// the one path it does not reach.
			if rule.RetryAfter != nil {
				w.Header().Set("Retry-After", strconv.Itoa(*rule.RetryAfter))
			}
			api.ErrorStatus(w, rule.HTTPStatus, api.ReplyCode(rule.ReplyCode), rule.ReplyText)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ruleMatches reports whether a rule applies to this request. An empty matcher
// matches everything, so a rule with no matchers at all breaks every API call —
// which is occasionally exactly what a test wants.
func ruleMatches(rule store.FaultRule, r *http.Request, body []byte) bool {
	if rule.MatchMethod != "" && !strings.EqualFold(rule.MatchMethod, r.Method) {
		return false
	}
	if rule.MatchPathPattern != "" && !matchPattern(rule.MatchPathPattern, r.URL.Path) {
		return false
	}
	if rule.MatchBodyContains != "" && !bytes.Contains(body, []byte(rule.MatchBodyContains)) {
		return false
	}
	return true
}

// matchPattern implements the glob the fault rules use: "*" stands for any
// sequence of characters, including "/". Anything without a "*" must match the
// path exactly.
func matchPattern(pattern, path string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == path
	}

	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	rest := path[len(parts[0]):]

	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(rest, parts[i])
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(parts[i]):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

// peekBody reads the request body for matching and puts it back for the
// handler, the same trick the request logger uses.
func peekBody(r *http.Request, needed bool) []byte {
	if !needed || r.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(nil))
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return raw
}
