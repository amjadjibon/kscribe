package controller

import (
	"sync"
	"time"
)

// RateLimiter is a sliding-window limiter capping diagnosis starts per hour.
// ponytail: per-replica, stdlib only (CON-006) — mutex + timestamp slice;
// a distributed limiter is needed only if replicas > 1.
type RateLimiter struct {
	mu     sync.Mutex
	limit  int // 0 = unlimited
	window time.Duration
	starts []time.Time
	now    func() time.Time // injectable for tests
}

// NewRateLimiter returns a limiter allowing limit starts per hour; 0 disables limiting.
func NewRateLimiter(limit int) *RateLimiter {
	return &RateLimiter{limit: limit, window: time.Hour, now: time.Now}
}

// Allow reports whether another diagnosis may start now, recording it if so.
func (l *RateLimiter) Allow() bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-l.window)
	keep := l.starts[:0]
	for _, t := range l.starts {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	l.starts = keep

	if len(l.starts) >= l.limit {
		return false
	}
	l.starts = append(l.starts, now)
	return true
}
