package jellycompat

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ErrSessionNotFound is returned when a compat session does not exist.
var ErrSessionNotFound = errors.New("compat session not found")

type sessionPersistence interface {
	Upsert(ctx context.Context, session Session) error
	GetByToken(ctx context.Context, token string, now time.Time) (*Session, error)
	IsActive(ctx context.Context, token string, now time.Time) (bool, error)
	DeleteByToken(ctx context.Context, token string) error
	DeleteByUserID(ctx context.Context, userID int) (int, error)
}

// Session stores a compat login plus upstream Silo credentials.
type Session struct {
	Token                 string
	Username              string
	AccountUsername       string
	ProfileID             string
	ProfileName           string
	PseudoUserID          uuid.UUID
	StreamAppUserID       int
	StreamAppSessionID    string
	AuthRevision          int64
	StreamAppAccessToken  string
	StreamAppRefreshToken string
	StreamAppTokenExpiry  time.Time
	CreatedAt             time.Time
	ExpiresAt             time.Time
}

// SessionStore keeps compat sessions in memory.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]Session
	ttl      time.Duration
	now      func() time.Time
	repo     sessionPersistence
}

// NewSessionStore creates a new in-memory session store.
func NewSessionStore(ttl time.Duration, now func() time.Time) *SessionStore {
	return newSessionStore(ttl, now, nil)
}

// NewPersistentSessionStore creates a session store backed by persistent storage.
func NewPersistentSessionStore(ttl time.Duration, now func() time.Time, repo sessionPersistence) *SessionStore {
	return newSessionStore(ttl, now, repo)
}

func newSessionStore(ttl time.Duration, now func() time.Time, repo sessionPersistence) *SessionStore {
	if now == nil {
		now = time.Now
	}
	return &SessionStore{
		sessions: make(map[string]Session),
		ttl:      ttl,
		now:      now,
		repo:     repo,
	}
}

// Put stores or replaces a compat session.
func (s *SessionStore) Put(session Session) error {
	if session.CreatedAt.IsZero() {
		session.CreatedAt = s.now()
	}
	if session.ExpiresAt.IsZero() {
		session.ExpiresAt = session.CreatedAt.Add(s.ttl)
	}

	if s.repo != nil {
		if err := s.repo.Upsert(context.Background(), session); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.Token] = session
	return nil
}

// Get returns a compat session when it exists and is not expired.
// If the session's remaining lifetime is less than half the configured TTL,
// ExpiresAt is extended by the full TTL (sliding window).
func (s *SessionStore) Get(token string) (*Session, bool) {
	s.mu.RLock()
	session, ok := s.sessions[token]
	s.mu.RUnlock()
	if ok {
		if s.repo != nil {
			active, err := s.repo.IsActive(context.Background(), token, s.now())
			if err != nil || !active {
				s.mu.Lock()
				delete(s.sessions, token)
				s.mu.Unlock()
				if err != nil {
					slog.Warn("jellycompat session store validation failed", "token", token, "error", err)
				}
				return nil, false
			}
		}
		if !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(s.now()) {
			s.Delete(token)
			return nil, false
		}
		s.maybeExtendSession(&session, token)
		sessionCopy := session
		return &sessionCopy, true
	}

	if s.repo == nil {
		return nil, false
	}

	persisted, err := s.repo.GetByToken(context.Background(), token, s.now())
	if err != nil {
		if !errors.Is(err, ErrSessionNotFound) {
			slog.Warn("jellycompat session store load failed", "token", token, "error", err)
		}
		return nil, false
	}

	s.maybeExtendSession(persisted, token)

	s.mu.Lock()
	s.sessions[token] = *persisted
	s.mu.Unlock()

	sessionCopy := *persisted
	return &sessionCopy, true
}

// maybeExtendSession extends the session's ExpiresAt if more than half the TTL has elapsed.
func (s *SessionStore) maybeExtendSession(session *Session, token string) {
	if s.ttl <= 0 || session.ExpiresAt.IsZero() {
		return
	}
	remaining := session.ExpiresAt.Sub(s.now())
	if remaining >= s.ttl/2 {
		return
	}
	session.ExpiresAt = s.now().Add(s.ttl)

	s.mu.Lock()
	s.sessions[token] = *session
	s.mu.Unlock()

	if s.repo != nil {
		if err := s.repo.Upsert(context.Background(), *session); err != nil {
			slog.Warn("jellycompat session store extend failed", "token", token, "error", err)
		}
	}
}

// Delete removes a compat session.
func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
	if s.repo != nil {
		if err := s.repo.DeleteByToken(context.Background(), token); err != nil && !errors.Is(err, ErrSessionNotFound) {
			slog.Warn("jellycompat session store delete failed", "token", token, "error", err)
		}
	}
}

// DeleteByUserID removes all compat sessions for a given Silo user ID.
func (s *SessionStore) DeleteByUserID(userID int) {
	s.mu.Lock()
	for token, session := range s.sessions {
		if session.StreamAppUserID == userID {
			delete(s.sessions, token)
		}
	}
	s.mu.Unlock()
	if s.repo != nil {
		if _, err := s.repo.DeleteByUserID(context.Background(), userID); err != nil {
			slog.Warn("jellycompat session store delete by user failed", "user_id", userID, "error", err)
		}
	}
}

// Update modifies a compat session in place.
func (s *SessionStore) Update(token string, fn func(*Session) error) error {
	s.mu.Lock()
	session, ok := s.sessions[token]
	s.mu.Unlock()

	if !ok && s.repo != nil {
		persisted, err := s.repo.GetByToken(context.Background(), token, s.now())
		if err != nil {
			if errors.Is(err, ErrSessionNotFound) {
				return ErrSessionNotFound
			}
			return err
		}
		session = *persisted
		ok = true
	}
	if !ok {
		return ErrSessionNotFound
	}

	if !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(s.now()) {
		s.Delete(token)
		return ErrSessionNotFound
	}
	if err := fn(&session); err != nil {
		return err
	}

	if s.repo != nil {
		if err := s.repo.Upsert(context.Background(), session); err != nil {
			return err
		}
	}

	s.mu.Lock()
	s.sessions[token] = session
	s.mu.Unlock()
	return nil
}
