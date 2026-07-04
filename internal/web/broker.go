package web

import "sync"

// Event is an SSE payload — a pre-rendered HTML fragment.
type Event struct {
	HTML string
}

// Broker fans out SSE events to per-incident subscribers.
// in-memory, per-replica. replicas:1 in MVP so no cross-replica delivery needed.
type Broker struct {
	mu   sync.Mutex
	subs map[string]map[chan Event]struct{}
}

// NewBroker returns an initialised Broker.
func NewBroker() *Broker {
	return &Broker{subs: make(map[string]map[chan Event]struct{})}
}

// Subscribe returns a receive-only channel for incidentID and a cancel func.
// The cancel func must be called when the subscriber is done.
func (b *Broker) Subscribe(incidentID string) (<-chan Event, func()) {
	ch := make(chan Event, 8)
	b.mu.Lock()
	if b.subs[incidentID] == nil {
		b.subs[incidentID] = make(map[chan Event]struct{})
	}
	b.subs[incidentID][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() { b.unsubscribe(incidentID, ch) }
}

func (b *Broker) unsubscribe(incidentID string, ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs[incidentID], ch)
	if len(b.subs[incidentID]) == 0 {
		delete(b.subs, incidentID)
	}
}

// Publish sends an event to every subscriber of incidentID.
// Drops the event for any subscriber whose buffer is full rather than blocking.
// non-blocking drop; add back-pressure/queue if throughput matters.
func (b *Broker) Publish(incidentID string, e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[incidentID] {
		select {
		case ch <- e:
		default:
		}
	}
}
