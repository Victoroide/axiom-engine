package idempotency

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type entry struct {
	Fingerprint string
	Token       string
	Response    *Response
	Expires     time.Time
}

// Memory bounds response retention within one process.
type Memory struct {
	mu      sync.Mutex
	entries map[string]entry
	order   []string
	ttl     time.Duration
	max     int
	now     func() time.Time
}

// NewMemory constructs a bounded, expiring response store.
func NewMemory(ttl time.Duration, maxEntries int) *Memory {
	return &Memory{entries: make(map[string]entry), ttl: ttl, max: max(1, maxEntries), now: time.Now}
}

// Begin reserves a key or returns the matching reservation's state.
func (m *Memory) Begin(_ context.Context, key, fingerprint string) (Reservation, State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.prune(now)
	if e, ok := m.entries[key]; ok {
		if e.Fingerprint != fingerprint {
			return Reservation{}, Mismatch, nil
		}
		if e.Response == nil {
			return Reservation{}, InProgress, nil
		}
		response := *e.Response
		response.Body = append([]byte(nil), response.Body...)
		return Reservation{Response: response}, Replay, nil
	}
	for len(m.entries) >= m.max {
		delete(m.entries, m.order[0])
		m.order = m.order[1:]
	}
	token := uuid.NewString()
	m.entries[key] = entry{Fingerprint: fingerprint, Token: token}
	m.order = append(m.order, key)
	return Reservation{Token: token}, Acquired, nil
}

func (m *Memory) prune(now time.Time) {
	kept := m.order[:0]
	for _, key := range m.order {
		e, ok := m.entries[key]
		if !ok || (!e.Expires.IsZero() && !now.Before(e.Expires)) {
			delete(m.entries, key)
			continue
		}
		kept = append(kept, key)
	}
	m.order = kept
}

// Complete publishes a response only while the caller owns the reservation.
func (m *Memory) Complete(_ context.Context, key, token string, response Response) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[key]; ok && e.Token == token {
		response.Body = append([]byte(nil), response.Body...)
		response.ContentType = strings.Clone(response.ContentType)
		e.Response = &response
		e.Expires = m.now().Add(m.ttl)
		m.entries[key] = e
	}
	return nil
}

// Abort releases a failed request without disturbing a newer reservation.
func (m *Memory) Abort(_ context.Context, key, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[key]; ok && e.Token == token {
		delete(m.entries, key)
	}
	return nil
}
