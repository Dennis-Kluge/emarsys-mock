// Package config reads the service configuration from the environment.
//
// Every value has a default that makes the mock runnable with no configuration
// at all: `emarsys-mock` alone starts a working server on :8080 with an
// in-memory database.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"
)

// MemoryDSN is the DSN value that selects a shared in-memory database.
const MemoryDSN = ":memory:"

type Config struct {
	Addr string
	// DSN is either MemoryDSN or a path to a SQLite file.
	DSN string

	// WSSE
	WSSESkew        time.Duration
	WSSERejectReuse bool

	// OAuth2
	OAuthTokenTTL   time.Duration
	OAuthSigningKey []byte

	// Control plane and dashboard
	CtlToken string
	ReadOnly bool

	// Limits
	RateLimitPerMinute  int
	MaxBatchContacts    int
	MaxBodyBytes        int64
	MaxContactBodyBytes int64
	RequestLogMax       int

	// Behaviour
	ExportPollsBeforeDone int
	ExportLocation        *time.Location
	WebhookURL            string
	WebhookTimeout        time.Duration
}

func Load() (Config, error) {
	c := Config{
		Addr:                  env("EMARSYS_MOCK_ADDR", ":8080"),
		DSN:                   env("EMARSYS_MOCK_DB", MemoryDSN),
		WSSESkew:              time.Duration(envInt("WSSE_SKEW_SECONDS", 300)) * time.Second,
		WSSERejectReuse:       envBool("WSSE_REJECT_NONCE_REUSE", false),
		OAuthTokenTTL:         time.Duration(envInt("OAUTH_TOKEN_TTL_SECONDS", 3600)) * time.Second,
		CtlToken:              env("CTL_TOKEN", ""),
		ReadOnly:              envBool("READONLY", false),
		RateLimitPerMinute:    envInt("RATE_LIMIT_PER_MINUTE", 1000),
		MaxBatchContacts:      envInt("MAX_BATCH_CONTACTS", 1000),
		MaxBodyBytes:          int64(envInt("MAX_BODY_BYTES", 10<<20)),
		MaxContactBodyBytes:   int64(envInt("MAX_CONTACT_BODY_BYTES", 8<<20)),
		RequestLogMax:         envInt("REQUEST_LOG_MAX", 10000),
		ExportPollsBeforeDone: envInt("EXPORT_POLLS_BEFORE_DONE", 2),
		WebhookURL:            env("WEBHOOK_URL", ""),
		WebhookTimeout:        time.Duration(envInt("WEBHOOK_TIMEOUT_SECONDS", 5)) * time.Second,
	}

	// Emarsys renders export timestamps in Vienna local time, not UTC. The
	// tzdata blank import in main keeps this working in a scratch container.
	tzName := env("EXPORT_TIMEZONE", "Europe/Vienna")
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return Config{}, fmt.Errorf("EXPORT_TIMEZONE %q: %w", tzName, err)
	}
	c.ExportLocation = loc

	if key := env("OAUTH_SIGNING_KEY", ""); key != "" {
		c.OAuthSigningKey = []byte(key)
	} else {
		// A random per-process key is the right default: tokens do not need to
		// survive a restart, and nobody can accidentally ship a known secret.
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return Config{}, fmt.Errorf("generate oauth signing key: %w", err)
		}
		c.OAuthSigningKey = []byte(hex.EncodeToString(buf))
	}

	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
