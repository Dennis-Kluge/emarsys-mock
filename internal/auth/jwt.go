package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The mock issues and verifies its own HS256 tokens. That is about forty lines
// of standard library, which is a better trade than a JWT dependency for a
// service whose whole selling point is that it builds and deploys as a single
// static binary.

var (
	ErrMalformedJWT = errors.New("malformed token")
	ErrBadSignature = errors.New("invalid token signature")
	ErrTokenExpired = errors.New("token expired")
)

type Claims struct {
	Subject  string `json:"sub"`
	ClientID string `json:"client_id"`
	Issuer   string `json:"iss"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

var jwtHeader = base64URL([]byte(`{"alg":"HS256","typ":"JWT"}`))

// SignJWT returns a signed HS256 token for the given claims.
func SignJWT(claims Claims, key []byte) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims: %w", err)
	}
	signing := jwtHeader + "." + base64URL(payload)
	return signing + "." + base64URL(sign(signing, key)), nil
}

// ParseJWT verifies the signature and expiry and returns the claims.
func ParseJWT(token string, key []byte, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrMalformedJWT
	}
	signing := parts[0] + "." + parts[1]
	want := base64URL(sign(signing, key))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return Claims{}, ErrBadSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrMalformedJWT
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, ErrMalformedJWT
	}
	if claims.Expires > 0 && now.Unix() >= claims.Expires {
		return Claims{}, ErrTokenExpired
	}
	return claims, nil
}

func sign(signing string, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signing))
	return mac.Sum(nil)
}

func base64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
