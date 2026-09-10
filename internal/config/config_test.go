package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q", cfg.Addr)
	}
	if cfg.DSN != MemoryDSN {
		t.Errorf("DSN = %q, want an in-memory database by default", cfg.DSN)
	}
	if cfg.WSSESkew != 5*time.Minute {
		t.Errorf("WSSESkew = %v", cfg.WSSESkew)
	}
	if cfg.RateLimitPerMinute != 1000 || cfg.MaxBatchContacts != 1000 {
		t.Errorf("limits = %d/%d", cfg.RateLimitPerMinute, cfg.MaxBatchContacts)
	}
	if cfg.MaxContactBodyBytes >= cfg.MaxBodyBytes {
		t.Errorf("contact body limit %d must be below the general limit %d",
			cfg.MaxContactBodyBytes, cfg.MaxBodyBytes)
	}
	if len(cfg.OAuthSigningKey) == 0 {
		t.Error("a signing key must be generated when none is configured")
	}
}

// TestExportTimezoneResolves guards the one deployment trap in this service:
// Emarsys renders export timestamps in Vienna local time, and a scratch or
// distroless image carries no /usr/share/zoneinfo. Only the time/tzdata blank
// import in main keeps this from failing at startup in the container.
func TestExportTimezoneResolves(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ExportLocation.String() != "Europe/Vienna" {
		t.Errorf("ExportLocation = %q, want Europe/Vienna", cfg.ExportLocation)
	}

	// Vienna is UTC+2 in summer; getting UTC back means the zone silently fell
	// back rather than loading.
	summer := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).In(cfg.ExportLocation)
	if _, offset := summer.Zone(); offset != 2*60*60 {
		t.Errorf("summer offset = %ds, want 7200s", offset)
	}
}

func TestLoadRejectsAnUnknownTimezone(t *testing.T) {
	t.Setenv("EXPORT_TIMEZONE", "Mars/Olympus_Mons")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an unknown timezone")
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	t.Setenv("EMARSYS_MOCK_ADDR", ":9999")
	t.Setenv("EMARSYS_MOCK_DB", "/tmp/mock.db")
	t.Setenv("WSSE_SKEW_SECONDS", "60")
	t.Setenv("WSSE_REJECT_NONCE_REUSE", "true")
	t.Setenv("READONLY", "1")
	t.Setenv("RATE_LIMIT_PER_MINUTE", "5")
	t.Setenv("OAUTH_SIGNING_KEY", "fixed-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Addr != ":9999" || cfg.DSN != "/tmp/mock.db" {
		t.Errorf("Addr/DSN = %q/%q", cfg.Addr, cfg.DSN)
	}
	if cfg.WSSESkew != time.Minute || !cfg.WSSERejectReuse || !cfg.ReadOnly {
		t.Errorf("cfg = %+v", cfg)
	}
	// CI lowers the rate limit deliberately so retry and backoff logic can be
	// exercised without sending a thousand requests.
	if cfg.RateLimitPerMinute != 5 {
		t.Errorf("RateLimitPerMinute = %d, want 5", cfg.RateLimitPerMinute)
	}
	if string(cfg.OAuthSigningKey) != "fixed-key" {
		t.Errorf("OAuthSigningKey = %q", cfg.OAuthSigningKey)
	}
}

func TestLoadIgnoresUnparsableValues(t *testing.T) {
	t.Setenv("WSSE_SKEW_SECONDS", "not-a-number")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.WSSESkew != 5*time.Minute {
		t.Errorf("WSSESkew = %v, want the default after an unparsable value", cfg.WSSESkew)
	}
}
