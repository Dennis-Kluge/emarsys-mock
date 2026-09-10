package auth

import (
	"errors"
	"testing"
	"time"
)

func TestJWTRoundTrip(t *testing.T) {
	key := []byte("test-signing-key")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	token, err := SignJWT(Claims{
		Subject:  "mock-api-user",
		ClientID: "mock-client",
		Issuer:   "emarsys-mock",
		IssuedAt: now.Unix(),
		Expires:  now.Add(time.Hour).Unix(),
	}, key)
	if err != nil {
		t.Fatalf("SignJWT() error = %v", err)
	}

	claims, err := ParseJWT(token, key, now)
	if err != nil {
		t.Fatalf("ParseJWT() error = %v", err)
	}
	if claims.ClientID != "mock-client" || claims.Subject != "mock-api-user" {
		t.Errorf("ParseJWT() = %+v", claims)
	}

	t.Run("rejects an expired token", func(t *testing.T) {
		if _, err := ParseJWT(token, key, now.Add(2*time.Hour)); !errors.Is(err, ErrTokenExpired) {
			t.Fatalf("ParseJWT() = %v, want ErrTokenExpired", err)
		}
	})

	t.Run("rejects a foreign key", func(t *testing.T) {
		if _, err := ParseJWT(token, []byte("other-key"), now); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("ParseJWT() = %v, want ErrBadSignature", err)
		}
	})

	t.Run("rejects a tampered payload", func(t *testing.T) {
		tampered := token[:len(token)-4] + "AAAA"
		if _, err := ParseJWT(tampered, key, now); err == nil {
			t.Fatal("ParseJWT() accepted a tampered token")
		}
	})

	t.Run("rejects a malformed token", func(t *testing.T) {
		if _, err := ParseJWT("not.a.jwt.at.all", key, now); !errors.Is(err, ErrMalformedJWT) {
			t.Fatalf("ParseJWT() = %v, want ErrMalformedJWT", err)
		}
	})
}
