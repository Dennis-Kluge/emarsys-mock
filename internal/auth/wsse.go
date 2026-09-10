// Package auth implements the two credential schemes the Suite API accepts:
// WSSE for the v2 endpoints and OAuth2 client-credentials for v3.
package auth

import (
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Token is a parsed X-WSSE header.
type Token struct {
	Username       string
	PasswordDigest string
	Nonce          string
	Created        string
}

var (
	ErrMalformedHeader = errors.New("malformed X-WSSE header")
	ErrDigestMismatch  = errors.New("password digest mismatch")
	ErrStaleCreated    = errors.New("created timestamp outside the accepted window")
)

// wssePair matches one Key="value" pair. Emarsys clients differ in spacing and
// ordering, so the header is parsed pair-wise rather than with one fixed regex.
var wssePair = regexp.MustCompile(`(\w+)\s*=\s*"([^"]*)"`)

// ParseToken reads an X-WSSE header value.
func ParseToken(header string) (Token, error) {
	if header == "" {
		return Token{}, ErrMalformedHeader
	}
	rest := strings.TrimSpace(header)
	if after, ok := cutPrefixFold(rest, "UsernameToken"); ok {
		rest = strings.TrimSpace(after)
	}

	var t Token
	for _, m := range wssePair.FindAllStringSubmatch(rest, -1) {
		switch strings.ToLower(m[1]) {
		case "username":
			t.Username = m[2]
		case "passworddigest":
			t.PasswordDigest = m[2]
		case "nonce":
			t.Nonce = m[2]
		case "created":
			t.Created = m[2]
		}
	}
	if t.Username == "" || t.PasswordDigest == "" || t.Nonce == "" || t.Created == "" {
		return Token{}, ErrMalformedHeader
	}
	return t, nil
}

// Digest computes the WSSE PasswordDigest the way Emarsys does it.
//
// The unusual part, and the single most common reason a hand-written client
// fails against the real API, is that the SHA-1 is base64-encoded as its
// *hexadecimal string* rather than as its raw bytes:
//
//	base64( hex( sha1( nonce + created + secret ) ) )
func Digest(nonce, created, secret string) string {
	sum := sha1.Sum([]byte(nonce + created + secret))
	hexed := hex.EncodeToString(sum[:])
	return base64.StdEncoding.EncodeToString([]byte(hexed))
}

// Verify checks the digest and the freshness of the Created timestamp.
// skew is the maximum absolute distance from now that is still accepted.
func (t Token) Verify(secret string, now time.Time, skew time.Duration) error {
	created, err := parseCreated(t.Created)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStaleCreated, err)
	}
	if skew > 0 {
		delta := now.Sub(created)
		if delta < 0 {
			delta = -delta
		}
		if delta > skew {
			return ErrStaleCreated
		}
	}
	want := Digest(t.Nonce, t.Created, secret)
	if subtle.ConstantTimeCompare([]byte(want), []byte(t.PasswordDigest)) != 1 {
		return ErrDigestMismatch
	}
	return nil
}

// createdLayouts covers the formats seen in the wild. Emarsys documents
// ISO 8601 in UTC, but SAP-generated clients emit an explicit offset and some
// emit sub-second precision.
var createdLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05-07:00",
	"2006-01-02T15:04:05",
}

func parseCreated(v string) (time.Time, error) {
	for _, layout := range createdLayouts {
		if ts, err := time.Parse(layout, v); err == nil {
			return ts.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised Created format %q", v)
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// BuildHeader produces a valid X-WSSE header value. The mock uses it in tests,
// and integrations can use it to sign requests against the mock without
// reimplementing the digest.
func BuildHeader(username, secret string, now time.Time, nonce string) string {
	created := now.UTC().Format("2006-01-02T15:04:05Z")
	return fmt.Sprintf(
		`UsernameToken Username="%s", PasswordDigest="%s", Nonce="%s", Created="%s"`,
		username, Digest(nonce, created, secret), nonce, created)
}
