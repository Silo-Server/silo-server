package playback

import (
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrIdempotencyConflict        = errors.New("idempotency key conflicts with its existing binding")
	ErrIdempotencyCapacity        = errors.New("idempotency store capacity exhausted")
	ErrIdempotencyRetryable error = idempotencyRetryableError{}
)

type idempotencyRetryableError struct{}

func (idempotencyRetryableError) Error() string   { return "idempotency attempt aborted; retry is safe" }
func (idempotencyRetryableError) Retryable() bool { return true }

type TerminationStatus string

const (
	TerminationStatusTerminated        TerminationStatus = "terminated"
	TerminationStatusAlreadyTerminated TerminationStatus = "already_terminated"
)

type TerminationBinding struct {
	ServerID   string
	SessionID  string
	Generation string
	SnapshotID string
	ReasonCode string
	Reason     string
	Title      string
	Message    string
	DeadlineMS int
}

type TerminationResult struct {
	Status    TerminationStatus
	CommandID string
}

type idempotencyEntry struct {
	binding  TerminationBinding
	result   TerminationResult
	err      error
	expires  time.Time
	done     chan struct{}
	complete bool
}

// IdempotencyStore is a bounded, concurrency-safe successful-result cache.
// Identical in-flight calls wait for the first attempt. Failed attempts are
// removed so a retry can perform the operation.
type IdempotencyStore struct {
	mu      sync.Mutex
	entries map[string]*idempotencyEntry
	max     int
	ttl     time.Duration
	now     func() time.Time
}

func NewIdempotencyStore(maxEntries int, ttl time.Duration) *IdempotencyStore {
	if maxEntries <= 0 {
		maxEntries = 1
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &IdempotencyStore{entries: make(map[string]*idempotencyEntry), max: maxEntries, ttl: ttl, now: time.Now}
}

func (s *IdempotencyStore) SetClock(now func() time.Time) {
	if s == nil || now == nil {
		return
	}
	s.mu.Lock()
	s.now = now
	s.mu.Unlock()
}

func (s *IdempotencyStore) Do(key string, binding TerminationBinding, fn func() (TerminationResult, error)) (TerminationResult, bool, error) {
	key = strings.TrimSpace(key)
	for {
		s.mu.Lock()
		now := s.now().UTC()
		s.pruneLocked(now)
		if existing, ok := s.entries[key]; ok {
			if existing.binding != binding {
				s.mu.Unlock()
				return TerminationResult{}, false, ErrIdempotencyConflict
			}
			if existing.complete {
				result := existing.result
				s.mu.Unlock()
				return result, true, nil
			}
			done := existing.done
			s.mu.Unlock()
			<-done
			s.mu.Lock()
			result := existing.result
			err := existing.err
			complete := existing.complete
			s.mu.Unlock()
			if err != nil {
				return TerminationResult{}, false, err
			}
			if !complete {
				return TerminationResult{}, false, ErrIdempotencyRetryable
			}
			return result, true, nil
		}
		if !s.makeRoomLocked() {
			s.mu.Unlock()
			return TerminationResult{}, false, ErrIdempotencyCapacity
		}
		entry := &idempotencyEntry{binding: binding, done: make(chan struct{})}
		s.entries[key] = entry
		s.mu.Unlock()

		result, err := s.invoke(key, entry, fn)

		s.mu.Lock()
		if current := s.entries[key]; current == entry {
			if err != nil {
				entry.err = err
				delete(s.entries, key)
			} else {
				entry.result = result
				entry.expires = s.now().UTC().Add(s.ttl)
				entry.complete = true
			}
			close(entry.done)
		}
		s.mu.Unlock()
		return result, false, err
	}
}

func (s *IdempotencyStore) invoke(key string, entry *idempotencyEntry, fn func() (TerminationResult, error)) (result TerminationResult, err error) {
	returned := false
	defer func() {
		if returned {
			return
		}
		panicValue := recover()
		s.mu.Lock()
		if current := s.entries[key]; current == entry {
			entry.err = ErrIdempotencyRetryable
			delete(s.entries, key)
			close(entry.done)
		}
		s.mu.Unlock()
		panic(panicValue)
	}()
	if key == "" || fn == nil {
		result, err = TerminationResult{}, ErrIdempotencyConflict
	} else {
		result, err = fn()
	}
	returned = true
	return result, err
}

func (s *IdempotencyStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now().UTC())
	return len(s.entries)
}

func (s *IdempotencyStore) pruneLocked(now time.Time) {
	for key, entry := range s.entries {
		if entry.complete && !now.Before(entry.expires) {
			delete(s.entries, key)
		}
	}
}

func (s *IdempotencyStore) makeRoomLocked() bool {
	return len(s.entries) < s.max
}
