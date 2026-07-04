package controller

import (
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	defaultDedupTTL  = time.Hour
	dedupSweepThresh = 1024 // sweep when map exceeds this; increase if event rate is very high
)

// Deduper is a TTL-based in-memory deduplication guard.
// per-replica scope — shared state needs a distributed cache (e.g. Redis) if replicas > 1 (CON-006).
type Deduper struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
	now  func() time.Time // injectable for tests
}

// NewDeduper returns a Deduper with the given TTL (defaults to 1 hour if <= 0).
func NewDeduper(ttl time.Duration) *Deduper {
	if ttl <= 0 {
		ttl = defaultDedupTTL
	}
	return &Deduper{ttl: ttl, seen: make(map[string]time.Time), now: time.Now}
}

// ShouldProcess returns true and marks the key if it hasn't been seen within the TTL.
// Returns false for duplicates within the window.
// Sweeps expired entries when the map exceeds dedupSweepThresh to bound memory use (MED-001).
func (d *Deduper) ShouldProcess(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if exp, ok := d.seen[key]; ok && now.Before(exp) {
		return false
	}
	// Sweep expired entries before inserting when map is large.
	if len(d.seen) >= dedupSweepThresh {
		for k, exp := range d.seen {
			if now.After(exp) {
				delete(d.seen, k)
			}
		}
	}
	d.seen[key] = now.Add(d.ttl)
	return true
}

// EventKey returns the dedup key for a core v1 Event.
// Uses the event UID when present; falls back to a composite of namespace/kind/name/reason.
func EventKey(ev *corev1.Event) string {
	if ev.UID != "" {
		return string(ev.UID)
	}
	return fmt.Sprintf("%s/%s/%s/%s", ev.Namespace, ev.InvolvedObject.Kind, ev.InvolvedObject.Name, ev.Reason)
}
