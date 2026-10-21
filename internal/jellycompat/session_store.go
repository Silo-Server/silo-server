package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
	// LoginSessionID is the Silo login session the sign-in opened. The
	// compat session ends with it. Empty for a session synthesized for an
	// sa_ API key, which has no login session and is never stored.
	LoginSessionID string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// signedOutDeleteTimeout bounds deleting the stored row of a compat session
// whose login session ended, so the request it refuses is not held up.
const signedOutDeleteTimeout = 2 * time.Second

// loginSessionChecker reports whether a Silo login session is live (not
// revoked, not expired); *auth.Service implements it. An error means it could
// not be checked.
type loginSessionChecker interface {
	LoginSessionActive(ctx context.Context, sessionID string) (bool, error)
}

// SessionStore keeps compat sessions in memory or validates them against a
// persistent repository. Persistent lookups never authorize from memory alone.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]Session
	// evictions counts EvictUser calls; see cache.
	evictions uint64
	ttl       time.Duration
	now       func() time.Time
	repo      sessionPersistence
	// loginSessions, when set, is asked on every lookup whether the
	// session's login session is still live.
	loginSessions loginSessionChecker
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

// WithLoginSessions additionally checks the login session a compat session's
// sign-in opened on every lookup. Its revocation or expiry ends the compat
// session on every replica. It returns s.
func (s *SessionStore) WithLoginSessions(c loginSessionChecker) *SessionStore {
	s.loginSessions = c
	return s
}

// Put stores or replaces a compat session.
func (s *SessionStore) Put(session Session) error {
	session = s.withLifetime(session)
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

// PutTx stores a compat session in tx, so it exists exactly when tx
// commits. A persistent store writes it in tx and loads it on its first
// lookup; a memory-only store, which has no database, keeps it at once. It
// returns the session as stored.
func (s *SessionStore) PutTx(ctx context.Context, tx pgx.Tx, session Session) (Session, error) {
	session = s.withLifetime(session)
	if s.repo != nil {
		txRepo, ok := s.repo.(txSessionPersistence)
		if !ok {
			return session, errors.New("compat session store cannot write in a transaction")
		}
		return session, txRepo.UpsertTx(ctx, tx, session)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.Token] = session
	return session, nil
}

// maxPseudoUserOwnerCandidates bounds the sessions one owner lookup checks.
// Each candidate whose login session ended is deleted as it is checked, so a
// later lookup reaches the rest.
const maxPseudoUserOwnerCandidates = 8

// PseudoUserOwner returns the account and profile of a live compat session
// signed in as the pseudo-user id, or ErrSessionNotFound. Every candidate goes
// through Recheck, so a session whose login session was signed out or expired
// names no owner. A persistent store finds candidates in the database, which
// sees every replica's sessions; a memory-only store searches its own.
func (s *SessionStore) PseudoUserOwner(ctx context.Context, id uuid.UUID) (pseudoUserOwner, error) {
	tokens, err := s.pseudoUserTokens(ctx, id)
	if err != nil {
		return pseudoUserOwner{}, err
	}
	for _, token := range tokens {
		session, err := s.Recheck(ctx, token)
		if errors.Is(err, ErrSessionNotFound) {
			continue
		}
		if err != nil {
			return pseudoUserOwner{}, err
		}
		return pseudoUserOwner{UserID: session.StreamAppUserID, ProfileID: session.ProfileID}, nil
	}
	return pseudoUserOwner{}, ErrSessionNotFound
}

func (s *SessionStore) pseudoUserTokens(ctx context.Context, id uuid.UUID) ([]string, error) {
	if lookup, ok := s.repo.(pseudoUserTokenLookup); ok {
		return lookup.PseudoUserTokens(ctx, id, s.now(), maxPseudoUserOwnerCandidates)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var tokens []string
	for token, session := range s.sessions {
		if len(tokens) == maxPseudoUserOwnerCandidates {
			break
		}
		if session.PseudoUserID == id {
			tokens = append(tokens, token)
		}
	}
	return tokens, nil
}

// txSessionPersistence is a sessionPersistence that can write inside a
// caller's transaction (SessionRepository).
type txSessionPersistence interface {
	UpsertTx(ctx context.Context, tx pgx.Tx, session Session) error
}

func (s *SessionStore) withLifetime(session Session) Session {
	if session.CreatedAt.IsZero() {
		session.CreatedAt = s.now()
	}
	if session.ExpiresAt.IsZero() {
		session.ExpiresAt = session.CreatedAt.Add(s.ttl)
	}
	return session
}

// Get returns a compat session when it exists and is not expired.
// If the session's remaining lifetime is less than half the configured TTL,
// ExpiresAt is extended by the full TTL (sliding window). A persistent store
// that cannot be read reports the session as missing; callers that must tell
// the two apart use Lookup.
func (s *SessionStore) Get(token string) (*Session, bool) {
	return s.GetContext(context.Background(), token)
}

// GetContext is Get bounded by ctx.
func (s *SessionStore) GetContext(ctx context.Context, token string) (*Session, bool) {
	session, err := s.Lookup(ctx, token)
	if err != nil {
		if !errors.Is(err, ErrSessionNotFound) {
			slog.WarnContext(ctx, "jellycompat session store load failed", "token_prefix", safeTokenPrefix(token), "error", logredact.SanitizeText(err.Error()))
		}
		return nil, false
	}
	return session, true
}

// Lookup is Get that reports why no session came back: ErrSessionNotFound
// for a token with no live session (or one whose stored row cannot be read,
// or whose login session was signed out or expired), any other error for a
// store that could not be read or a login session that could not be checked,
// which judges nothing about the token.
// Persistent lookups always read the stored row, so a completed durable token
// deletion is observed on every replica, regardless of cached copies or events.
func (s *SessionStore) Lookup(ctx context.Context, token string) (*Session, error) {
	s.mu.RLock()
	session, ok := s.sessions[token]
	evictions := s.evictions
	s.mu.RUnlock()
	if ok && s.repo == nil {
		if !session.ExpiresAt.IsZero() && !session.ExpiresAt.After(s.now()) {
			s.Delete(token)
			return nil, ErrSessionNotFound
		}
		if err := s.checkLoginSession(ctx, &session); err != nil {
			return nil, err
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

	persisted, err := s.loadStored(ctx, token)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			s.uncache(token)
		}
		return nil, err
	}

	if err := s.maybeExtendSession(persisted, token, evictions); err != nil {
		return nil, err
	}
	s.cache(token, *persisted, evictions)

	sessionCopy := *persisted
	return &sessionCopy, nil
}

// Recheck reads a session for a long-lived connection that must notice a
// sign-out made on any replica. A persistent store reads the stored row past
// this replica's memory, checks its login session, and neither caches nor
// extends it; a memory-only store answers as Lookup. Errors are Lookup's.
func (s *SessionStore) Recheck(ctx context.Context, token string) (*Session, error) {
	if s.repo == nil {
		return s.Lookup(ctx, token)
	}
	if !auth.ValidTextKey(token) {
		return nil, ErrSessionNotFound
	}
	return s.loadStored(ctx, token)
}

// loadStored reads token's stored session and checks its login session. A
// row that cannot be read, such as one whose tokens cannot be decrypted,
// counts as signed out.
func (s *SessionStore) loadStored(ctx context.Context, token string) (*Session, error) {
	session, err := s.repo.GetByToken(ctx, token, s.now())
	if errors.Is(err, errSessionUnreadable) {
		slog.WarnContext(ctx, "jellycompat session cannot be read; treating it as signed out", "token_prefix", safeTokenPrefix(token), "error", logredact.SanitizeText(err.Error()))
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := s.checkLoginSession(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

// checkLoginSession ends a compat session whose login session was signed out
// or expired: it deletes the session and returns ErrSessionNotFound. When the
// login session cannot be checked it returns that error and keeps the
// session, so the client retries instead of signing in again. A session with
// no login session is not checked.
func (s *SessionStore) checkLoginSession(ctx context.Context, session *Session) error {
	if s.loginSessions == nil || session.LoginSessionID == "" {
		return nil
	}
	active, err := s.loginSessions.LoginSessionActive(ctx, session.LoginSessionID)
	if err != nil {
		return fmt.Errorf("check login session: %w", err)
	}
	if !active {
		// A row the bounded delete leaves behind is refused again on its
		// next lookup.
		deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), signedOutDeleteTimeout)
		defer cancel()
		s.delete(deleteCtx, session.Token)
		return ErrSessionNotFound
	}
	return nil
}

// maybeExtendSession extends the session's ExpiresAt if more than half the TTL
// has elapsed. The stored row is updated, never re-created: when it is gone,
// the session was signed out, possibly after the lookup read it, so the copy
// is dropped and ErrSessionNotFound returned. Any other store error keeps
// the session and is only logged.
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
// Persistent Lookup always reads that row even when a copy is cached here.
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
	s.delete(context.Background(), token)
}

func (s *SessionStore) delete(ctx context.Context, token string) {
	s.uncache(token)
	if s.repo != nil {
		if err := s.repo.DeleteByToken(ctx, token); err != nil && !errors.Is(err, ErrSessionNotFound) {
			slog.WarnContext(ctx, "jellycompat session store delete failed", "token_prefix", safeTokenPrefix(token), "error", logredact.SanitizeText(err.Error()))
		}
	}
}

// EvictUser drops a Silo account's compat sessions from this store's memory.
// Signing an account out deletes its stored sessions in the revoking
// transaction (auth.RevokeSignInsInTransaction); each replica then calls
// EvictUser to discard its copies. Persistent lookups independently validate
// the stored row even if the eviction event is missed.
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
