package httpx

import (
	"testing"
	"time"
)

func TestRateLimiterWindowRollover(t *testing.T) {
	rl := NewRateLimiter(3)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	rl.now = func() time.Time { return now }

	for i := range 3 {
		allowed, remaining, _ := rl.Allow("caller")
		if !allowed {
			t.Fatalf("request %d was rejected", i+1)
		}
		if want := 2 - i; remaining != want {
			t.Errorf("remaining after request %d = %d, want %d", i+1, remaining, want)
		}
	}

	allowed, remaining, resetAt := rl.Allow("caller")
	if allowed {
		t.Error("the fourth request should have been rejected")
	}
	if remaining != 0 {
		t.Errorf("remaining = %d, want 0", remaining)
	}
	if !resetAt.After(now) {
		t.Errorf("resetAt = %v, want a time in the future", resetAt)
	}

	// The window is fixed, so the budget comes back whole rather than sliding.
	now = now.Add(time.Minute + time.Second)
	if allowed, _, _ := rl.Allow("caller"); !allowed {
		t.Error("the budget did not reset after the window elapsed")
	}
}

func TestRateLimiterSeparatesCallers(t *testing.T) {
	rl := NewRateLimiter(1)
	if allowed, _, _ := rl.Allow("a"); !allowed {
		t.Fatal("first caller was rejected")
	}
	if allowed, _, _ := rl.Allow("a"); allowed {
		t.Fatal("first caller exceeded its budget without being rejected")
	}
	if allowed, _, _ := rl.Allow("b"); !allowed {
		t.Error("a second caller shared the first caller's budget")
	}
}

func TestRateLimiterSweepsExpiredWindows(t *testing.T) {
	// The map only grows if callers keep changing, which in a mock means a test
	// suite churning credentials. Without the sweep that is an unbounded leak.
	rl := NewRateLimiter(10)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	rl.now = func() time.Time { return now }

	for i := range 1100 {
		rl.Allow("caller-" + string(rune(i)))
	}
	before := len(rl.windows)

	now = now.Add(2 * time.Minute)
	rl.Allow("fresh-caller")

	if len(rl.windows) >= before {
		t.Errorf("windows = %d after the sweep, was %d before", len(rl.windows), before)
	}
}
