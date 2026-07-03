package controller

import (
	"testing"
	"time"
)

func TestRateLimiterWindow(t *testing.T) {
	clock := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	l := NewRateLimiter(2)
	l.now = func() time.Time { return clock }

	for i := range 2 {
		if !l.Allow() {
			t.Fatalf("start %d must be allowed", i+1)
		}
	}
	if l.Allow() {
		t.Fatal("N+1th start within the window must be denied")
	}

	// Window expires → allowed again.
	clock = clock.Add(time.Hour + time.Second)
	if !l.Allow() {
		t.Fatal("start after window expiry must be allowed")
	}
}

func TestRateLimiterDisabled(t *testing.T) {
	for _, l := range []*RateLimiter{nil, NewRateLimiter(0)} {
		for range 100 {
			if !l.Allow() {
				t.Fatal("limit 0 / nil limiter must never deny")
			}
		}
	}
}
