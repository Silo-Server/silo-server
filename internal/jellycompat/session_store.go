package jellycompat

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/logredact"
)

// ErrSessionNotFound is returned when a compat session does not exist.
var ErrSessionNotFound = errors.New("compat session not found")

type sessionPersistence interface {
	Upsert(ctx context.Context, session Session) error
	UpdateByToken(ctx context.Context, session Session) error
	GetByToken(ctx context.Context, token string, now time.Time) (*Session, error)
	DeleteByToken(ctx context.Context, token string) error
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
	// evictions counts EvictUser calls; see cache.
	evictions uint64
	ttl       time.Duration
	now       func() time.Time
	repo      sessionPersistence
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
// ExpiresAt is extended by the full TTL (sliding window). A persistent store
// that cannot be read reports the session as missing; callers that must tell
// the two apart use Lookup.
func (s *SessionStore) Get(token string) (*Session, bool) {
	session, err := s.Lookup(context.Background(), token)
	if err != nil {
		if !errors.Is(err, ErrSessionNotFound) {
			slog.Warn("jellycompat session store load failed", "token_prefix", safeTokenPrefix(token), "error", logredact.SanitizeText(err.Error()))
		}
		return nil, false
	}
	return session, true
}

// Lookup is Get that reports why no session came back: ErrSessionNotFound
// for a token with no live session (or one whose stored tokens cannot be
// decrypted), any other error for a persistent store that could not be read,
// which judges nothing about the token.
func (s *SessionStore) Lookup(ctx context.Context, token string) (*Session, error) {
	s.mu.RLock()
	session, ok := s.sessions[token]
	evictions := s.evictions
	s.mu.RUnlock()
	if ok {
		if !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(s.now()) {
			s.Delete(token)
			return nil, ErrSessionNotFound
		}
		if err := s.maybeExtendSession(&session, token, evictions); err != nil {
			return nil, err
		}
		sessionCopy := session
		return &sessionCopy, nil
	}

	// A token Postgres cannot take as text (invalid UTF-8 or a NUL byte)
	// matches no session; querying with it would fail like an outage.
	if s.repo == nil || !auth.ValidTextKey(token) {
		return nil, ErrSessionNotFound
	}

	persisted, err := s.repo.GetByToken(ctx, token, s.now())
	if errors.Is(err, errSessionUnreadable) {
		slog.WarnContext(ctx, "jellycompat session cannot be decrypted; treating it as signed out", "token_prefix", safeTokenPrefix(token), "error", logredact.SanitizeText(err.Error()))
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}

	if err := s.maybeExtendSession(persisted, token, evictions); err != nil {
		return nil, err
	}
	s.cache(token, *persisted, evictions)

	sessionCopy := *persisted
	return &sessionCopy, nil
}

// maybeExtendSession extends the session's ExpiresAt if more than half the TTL
// has elapsed. The stored row is updated, never re-created: when it is gone,
// the session was signed out, possibly by an account-wide revocation this
// replica has not evicted yet, so the copy is dropped and ErrSessionNotFound
// returned. Any other store error keeps the session and is only logged.
func (s *SessionStore) maybeExtendSession(session *Session, token string, evictions uint64) error {
	if s.ttl <= 0 || session.ExpiresAt.IsZero() {
		return nil
	}
	remaining := session.ExpiresAt.Sub(s.now())
	if remaining >= s.ttl/2 {
		return nil
	}
	session.ExpiresAt = s.now().Add(s.ttl)

	if s.repo != nil {
		err := s.repo.UpdateByToken(context.Background(), *session)
		if errors.Is(err, ErrSessionNotFound) {
			s.uncache(token)
			return ErrSessionNotFound
		}
		if err != nil {
			slog.Warn("jellycompat session store extend failed", "token_prefix", safeTokenPrefix(token), "error", logredact.SanitizeText(err.Error()))
		}
	}
	s.cache(token, *session, evictions)
	return nil
}

// cache stores a session read when EvictUser had run evictions times. If it
// has run since, the copy may predate the revocation that evicted the
// account, so it is left out and the next request reads the stored row.
func (s *SessionStore) cache(token string, session Session, evictions uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.evictions == evictions {
		s.sessions[token] = session
	}
}

func (s *SessionStore) uncache(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// Delete removes a compat session.
func (s *SessionStore) Delete(token string) {
	s.uncache(token)
	if s.repo != nil {
		if err := s.repo.DeleteByToken(context.Background(), token); err != nil && !errors.Is(err, ErrSessionNotFound) {
			slog.Warn("jellycompat session store delete failed", "token_prefix", safeTokenPrefix(token), "error", logredact.SanitizeText(err.Error()))
		}
	}
}

// EvictUser drops a Silo account's compat sessions from this store's memory.
// Signing an account out deletes its stored sessions in the revoking
// transaction (auth.RevokeSignInsInTransaction); each replica then calls
// EvictUser so the copies it already loaded stop working too.
func (s *SessionStore) EvictUser(userID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictions++
	for token, session := range s.sessions {
		if session.StreamAppUserID == userID {
			delete(s.sessions, token)
		}
	}
}

// Update modifies a compat session in place. It returns ErrSessionNotFound
// when the stored row is gone, even if this replica still caches a copy.
func (s *SessionStore) Update(token string, fn func(*Session) error) error {
	s.mu.RLock()
	session, ok := s.sessions[token]
	evictions := s.evictions
	s.mu.RUnlock()

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
		if err := s.repo.UpdateByToken(context.Background(), session); err != nil {
			if errors.Is(err, ErrSessionNotFound) {
				s.uncache(token)
			}
			return err
		}
	}

	s.cache(token, session, evictions)
	return nil
}
