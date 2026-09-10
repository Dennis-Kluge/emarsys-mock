package auth

import (
	"errors"
	"testing"
	"time"
)

// TestDigest pins the digest algorithm against vectors computed independently
// (Python: base64(sha1(nonce+created+secret).hexdigest())).
//
// The double encoding is the whole point of this test. A SHA-1 that is
// base64-encoded from its raw bytes rather than from its hex string produces
// "dipsKkVLwfq04VntCabUEKKVQZ8=" for the first case, which is what a
// reasonable-looking but wrong implementation returns.
func TestDigest(t *testing.T) {
	cases := []struct {
		name, nonce, created, secret, want string
	}{
		{
			name:    "documented example",
			nonce:   "d36e316282959a9ed4c89851497a717f",
			created: "2010-12-27T13:14:15Z",
			secret:  "secret",
			want:    "NzYyYTZjMmE0NTRiYzFmYWI0ZTE1OWVkMDlhNmQ0MTBhMjk1NDE5Zg==",
		},
		{
			name:    "zero nonce",
			nonce:   "00000000000000000000000000000000",
			created: "2020-01-01T00:00:00Z",
			secret:  "mock-secret",
			want:    "NTdhMWU4YWVlMWE3ZDE4MjQ1NmMwOTZkZmU1MDc3YjI0MzQzZmJkNg==",
		},
		{
			name:    "seeded credentials",
			nonce:   "a1b2c3d4e5f60718293a4b5c6d7e8f90",
			created: "2026-09-10T12:00:00Z",
			secret:  "mock-secret",
			want:    "NzJiNjUzMGU4OWIwYTlhZTM4YTA5YTk4NmRkOWNlMzg3Mjc4ZWM4MA==",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Digest(tc.nonce, tc.created, tc.secret); got != tc.want {
				t.Errorf("Digest() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDigestIsNotRawBytesBase64(t *testing.T) {
	const rawBytesVariant = "dipsKkVLwfq04VntCabUEKKVQZ8="
	if got := Digest("d36e316282959a9ed4c89851497a717f", "2010-12-27T13:14:15Z", "secret"); got == rawBytesVariant {
		t.Fatal("Digest() base64-encodes the raw SHA-1 bytes; Emarsys encodes the hex string")
	}
}

func TestParseToken(t *testing.T) {
	const (
		user   = "mock-api-user"
		digest = "NzYyYTZjMmE0NTRiYzFmYWI0ZTE1OWVkMDlhNmQ0MTBhMjk1NDE5Zg=="
		nonce  = "d36e316282959a9ed4c89851497a717f"
		crt    = "2010-12-27T13:14:15Z"
	)

	cases := []struct {
		name    string
		header  string
		wantErr bool
	}{
		{
			name:   "canonical",
			header: `UsernameToken Username="` + user + `", PasswordDigest="` + digest + `", Nonce="` + nonce + `", Created="` + crt + `"`,
		},
		{
			name:   "no UsernameToken prefix",
			header: `Username="` + user + `", PasswordDigest="` + digest + `", Nonce="` + nonce + `", Created="` + crt + `"`,
		},
		{
			name:   "reordered and tightly spaced",
			header: `UsernameToken Created="` + crt + `",Nonce="` + nonce + `",Username="` + user + `",PasswordDigest="` + digest + `"`,
		},
		{name: "empty", header: "", wantErr: true},
		{name: "missing nonce", header: `UsernameToken Username="u", PasswordDigest="d", Created="` + crt + `"`, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, err := ParseToken(tc.header)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseToken(%q) = %+v, want error", tc.header, token)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseToken() error = %v", err)
			}
			if token.Username != user || token.PasswordDigest != digest ||
				token.Nonce != nonce || token.Created != crt {
				t.Errorf("ParseToken() = %+v", token)
			}
		})
	}
}

func TestTokenVerify(t *testing.T) {
	const secret = "mock-secret"
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	valid := func(created string) Token {
		return Token{
			Username:       "mock-api-user",
			Nonce:          "a1b2c3d4e5f60718293a4b5c6d7e8f90",
			Created:        created,
			PasswordDigest: Digest("a1b2c3d4e5f60718293a4b5c6d7e8f90", created, secret),
		}
	}

	t.Run("accepts a fresh token", func(t *testing.T) {
		if err := valid("2026-09-10T12:00:00Z").Verify(secret, now, 5*time.Minute); err != nil {
			t.Fatalf("Verify() = %v, want nil", err)
		}
	})

	t.Run("accepts an offset timestamp", func(t *testing.T) {
		// SAP-generated clients emit an explicit offset rather than Z.
		if err := valid("2026-09-10T14:00:00+02:00").Verify(secret, now, 5*time.Minute); err != nil {
			t.Fatalf("Verify() = %v, want nil", err)
		}
	})

	t.Run("rejects a stale timestamp", func(t *testing.T) {
		err := valid("2026-09-10T11:00:00Z").Verify(secret, now, 5*time.Minute)
		if !errors.Is(err, ErrStaleCreated) {
			t.Fatalf("Verify() = %v, want ErrStaleCreated", err)
		}
	})

	t.Run("accepts any age when skew is disabled", func(t *testing.T) {
		if err := valid("2010-12-27T13:14:15Z").Verify(secret, now, 0); err != nil {
			t.Fatalf("Verify() = %v, want nil", err)
		}
	})

	t.Run("rejects a wrong secret", func(t *testing.T) {
		err := valid("2026-09-10T12:00:00Z").Verify("other-secret", now, 5*time.Minute)
		if !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("Verify() = %v, want ErrDigestMismatch", err)
		}
	})

	t.Run("rejects an unparsable timestamp", func(t *testing.T) {
		tok := valid("2026-09-10T12:00:00Z")
		tok.Created = "yesterday"
		if err := tok.Verify(secret, now, 5*time.Minute); !errors.Is(err, ErrStaleCreated) {
			t.Fatalf("Verify() = %v, want ErrStaleCreated", err)
		}
	})
}
