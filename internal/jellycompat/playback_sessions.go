package jellycompat

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// ErrTerminalClaimUnavailable means a staged terminal event still exists but
// another process owns its delivery lease (or already sent its fallback).
var ErrTerminalClaimUnavailable = errors.New("compat terminal event claim unavailable")

// PlaybackSession stores compat-owned playback negotiation state before the
// native Silo playback session starts.
type PlaybackSession struct {
	ID          string
	CompatToken string
	// ClientDeviceID identifies the Jellyfin client installation that created
	// this negotiation. Stock Jellyfin Web can issue a second PlaybackInfo
	// request for the same play before it starts either response; the newer
	// negotiation replaces an older, still-unstarted one from the same device.
	ClientDeviceID string
	ItemID         string
	RouteItemID    string
	// ClientPlaySessionID records the client's own generated PlaySessionId
	// when it differs from ours (Static=true direct play skips PlaybackInfo,
	// so the client never learns the server id). Playback reports carrying
	// that id resolve to this session directly instead of by ambiguous route.
	ClientPlaySessionID        string
	UserID                     string
	InitialSeekSeconds         float64
	MediaSources               []PlaybackMediaSource
	UpstreamSessionID          string
	UpstreamPlayMethod         string
	TranscodeStarted           bool
	ProgressPersistenceKnown   bool
	DisableProgressPersistence bool
	// Terminal hides a play session from stream and progress routing after
	// ActiveEncodings cleanup while retaining the authenticated mapping long
	// enough for a later Stopped report to publish its authoritative position.
	Terminal              bool
	TerminalAuthoritative bool
	TerminalFallbackSent  bool
	TerminalClaimUntil    time.Time
	TerminalEventVersion  int64
	TerminalClaimVersion  int64
	TerminalScrobbleEvent *watchsync.ScrobbleEvent
	// Recipe is the transcode reconstruction descriptor for this session. Jellyfin
	// clients cannot round-trip a native stream token, so jellycompat carries the
	// recipe in its own durable compat store (this struct, persisted as JSONB)
	// rather than in the token. Nil until a transcode actually starts.
	Recipe    *playback.RecipeCard
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time
}

// PlaybackMediaSource stores one negotiated stream source within a compat play session.
type PlaybackMediaSource struct {
	ID                          string
	FileID                      int
	Version                     catalog.FileVersion
	SupportsDirectPlay          bool
	SupportsDirectStream        bool
	SupportsTranscoding         bool
	TranscodeAudio              bool
	DefaultAudioStreamIndex     *int
	SelectedAudioStreamIndex    *int
	DefaultSubtitleStreamIndex  *int
	SelectedSubtitleStreamIndex *int
	ETag                        string
}

// CompatPlaybackStore persists compat playback negotiation sessions (the
// PlaySessionId → upstream-session mapping plus media sources, route, and seek).
// It is an interface so the backing store is swappable: the in-memory
// PlaybackSessionStore is the default, and a durable (Postgres/Redis)
// implementation lets the mapping survive a server restart so a Jellyfin client
// can resume — a Redis switch then touches only the constructor, nothing else.
type CompatPlaybackStore interface {
	// Put stores or replaces a compat playback session.
	Put(session PlaybackSession)
	// PutNegotiated stores a PlaybackInfo negotiation and atomically replaces
	// older, still-unstarted negotiations for the same client device and item.
	PutNegotiated(session PlaybackSession)
	// Get returns a session when it exists and is not expired.
	Get(id string) (*PlaybackSession, bool)
	// Delete removes a session.
	Delete(id string)
	// HideFromRouting immediately makes a caller-owned session unavailable to
	// stream/progress routing before slower durable terminal staging begins.
	HideFromRouting(id, compatToken string) error
	// StageTerminal hides a session from playback routing and durably records
	// the provider event. An authoritative Stopped event replaces a fallback;
	// a later fallback can never replace an authoritative event.
	StageTerminal(id, compatToken string, event watchsync.ScrobbleEvent, authoritative bool) (*PlaybackSession, error)
	// ClaimTerminal leases one staged event for delivery across server processes.
	ClaimTerminal(id, compatToken string, claimUntil time.Time) (*PlaybackSession, error)
	// ReleaseTerminalClaim releases an exact lease after delivery failure, or
	// records a delivered fallback while retaining the row for a later Stopped.
	ReleaseTerminalClaim(id, compatToken string, claimUntil time.Time, claimVersion int64, fallbackSent bool)
	// CompleteTerminal deletes an authoritatively delivered terminal row only
	// when the caller still owns the exact lease.
	CompleteTerminal(id, compatToken string, claimUntil time.Time, claimVersion int64)
	// ListPendingTerminals returns retryable authoritative events and unsent
	// fallbacks for startup/periodic delivery recovery.
	ListPendingTerminals(ctx context.Context, limit int) ([]PlaybackSession, error)
	// GetFinalizable reads an active or terminal caller-owned session for report
	// validation before an atomic Take.
	GetFinalizable(id, compatToken string) (*PlaybackSession, bool)
	// Update modifies a session in place under the store's lock.
	Update(id string, fn func(*PlaybackSession) error) error
	// FindByRoute resolves a route item / media-source id to a session.
	FindByRoute(compatToken, routeID string) (*PlaybackSession, *PlaybackMediaSource, bool)
	// FindByClientPlaySessionID resolves the client-generated PlaySessionId
	// alias recorded for plays that skipped PlaybackInfo. The alias must
	// identify exactly one live session; ambiguity returns not-found.
	FindByClientPlaySessionID(compatToken, clientPlaySessionID string) (*PlaybackSession, bool)
	// FindFinalizableByClientPlaySessionID is the stop-report variant of alias
	// lookup and includes terminal sessions retained by Deactivate. The report
	// identifiers disambiguate clients that reuse an alias across plays.
	FindFinalizableByClientPlaySessionID(
		compatToken, clientPlaySessionID, routeItemID, mediaSourceID string,
	) (*PlaybackSession, bool)
	// FindFinalizableByRoute resolves exactly one active or terminal session for
	// a caller-owned route. Ambiguous matches return not-found.
	FindFinalizableByRoute(compatToken, routeID string) (*PlaybackSession, *PlaybackMediaSource, bool)
	// FindByUpstreamSessionID resolves the local upstream session that owns a
	// compat play. It is used for process-local failure lifecycle handling.
	FindByUpstreamSessionID(upstreamSessionID string) (*PlaybackSession, bool)
}

// PlaybackSessionStore keeps compat playback sessions in memory. It is the
// default CompatPlaybackStore implementation.
type PlaybackSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]PlaybackSession
	ttl      time.Duration
	now      func() time.Time
	// pendingCursor rotates bounded recovery scans through the full queue so a
	// permanently failing first batch cannot starve later terminal events.
	pendingCursor string
}

// NewPlaybackSessionStore creates a new playback session store.
func NewPlaybackSessionStore(ttl time.Duration, now func() time.Time) *PlaybackSessionStore {
	if now == nil {
		now = time.Now
	}
	if ttl <= 0 {
		// Default the absolute session lifetime to the absolute stream-token TTL
		// (playback.MaxTokenTTL, 24h) so a session never expires while its token
		// is still valid. Absolute from creation, not sliding; mirrors the
		// router default and is config-overridable.
		ttl = playback.MaxTokenTTL
	}
	return &PlaybackSessionStore{
		sessions: make(map[string]PlaybackSession),
		ttl:      ttl,
		now:      now,
	}
}

// Put stores or replaces a compat playback session.
func (s *PlaybackSessionStore) Put(session PlaybackSession) {
	s.putNormalized(session)
}

// PutNegotiated stores a freshly-created PlaybackInfo session. Jellyfin Web
// may negotiate the same play twice and then request both manifests; retaining
// both creates two native sessions, while only the newer one receives progress
// and Stopped reports. Replacing only unstarted sessions keeps real concurrent
// playback intact while preventing the abandoned negotiation from later
// publishing a stale pause.
func (s *PlaybackSessionStore) PutNegotiated(session PlaybackSession) {
	s.putNegotiatedNormalized(session)
}

// putNormalized stores or replaces a compat playback session and returns the
// stored copy with normalized timestamps (CreatedAt/UpdatedAt/ExpiresAt). The
// durable wrapper uses the return value to persist the same timestamps the cache
// just assigned without a second Get (extra lock + copy). Put keeps the
// no-return signature the CompatPlaybackStore interface requires.
func (s *PlaybackSessionStore) putNormalized(session PlaybackSession) PlaybackSession {
	s.mu.Lock()
	defer s.mu.Unlock()

	session = s.normalizeSession(session)
	s.sessions[session.ID] = session
	return session
}

func (s *PlaybackSessionStore) putNegotiatedNormalized(session PlaybackSession) (PlaybackSession, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session = s.normalizeSession(session)
	removed := make([]string, 0, 1)
	if session.CompatToken != "" && session.ClientDeviceID != "" && session.RouteItemID != "" {
		for id, existing := range s.sessions {
			if id == session.ID || existing.Terminal || existing.UpstreamSessionID != "" {
				continue
			}
			if existing.CompatToken == session.CompatToken &&
				existing.ClientDeviceID == session.ClientDeviceID &&
				mediaSourceIDsEqual(existing.RouteItemID, session.RouteItemID) {
				delete(s.sessions, id)
				removed = append(removed, id)
			}
		}
	}
	s.sessions[session.ID] = session
	return session, removed
}

func (s *PlaybackSessionStore) normalizeSession(session PlaybackSession) PlaybackSession {
	now := s.now()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	session.UpdatedAt = now
	if session.ExpiresAt.IsZero() {
		session.ExpiresAt = session.CreatedAt.Add(s.ttl)
	}
	return session
}

// Get returns a playback session when it exists and is not expired.
func (s *PlaybackSessionStore) Get(id string) (*PlaybackSession, bool) {
	s.mu.RLock()
	session, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if !session.ExpiresAt.After(s.now()) {
		s.Delete(id)
		return nil, false
	}
	if session.Terminal {
		return nil, false
	}
	cp := session
	return &cp, true
}

// Delete removes a playback session.
func (s *PlaybackSessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func (s *PlaybackSessionStore) compatTokenForID(id string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[id].CompatToken
}

// HideFromRouting marks a session terminal without requiring its provider event
// to have been staged yet. Final-report lookups remain available for retries.
func (s *PlaybackSessionStore) HideFromRouting(id, compatToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok || session.CompatToken != compatToken || !session.ExpiresAt.After(s.now()) {
		return ErrSessionNotFound
	}
	session.Terminal = true
	session.UpdatedAt = s.now()
	s.sessions[id] = session
	return nil
}

func (s *PlaybackSessionStore) terminalIDsByCompatToken(compatToken string) map[string]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]struct{})
	now := s.now()
	for id, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			delete(s.sessions, id)
			continue
		}
		if session.CompatToken == compatToken && session.Terminal {
			result[id] = struct{}{}
		}
	}
	return result
}

func (s *PlaybackSessionStore) replaceByCompatToken(
	compatToken string,
	replacements []PlaybackSession,
	preserveIDs map[string]struct{},
) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	affected := make(map[string]struct{}, len(replacements))
	for id, session := range s.sessions {
		_, preserve := preserveIDs[id]
		if session.CompatToken == compatToken && !preserve {
			delete(s.sessions, id)
			affected[id] = struct{}{}
		}
	}
	for _, session := range replacements {
		if _, preserve := preserveIDs[session.ID]; preserve {
			continue
		}
		if session.CreatedAt.IsZero() {
			session.CreatedAt = s.now()
		}
		if session.ExpiresAt.IsZero() {
			session.ExpiresAt = session.CreatedAt.Add(s.ttl)
		}
		s.sessions[session.ID] = session
		affected[session.ID] = struct{}{}
	}
	result := make([]string, 0, len(affected))
	for id := range affected {
		result = append(result, id)
	}
	return result
}

// StageTerminal hides a playback session and records the event that must reach
// watch providers. Authoritative final reports replace provisional cleanup
// events; provisional events never overwrite an authoritative report.
func (s *PlaybackSessionStore) StageTerminal(
	id string,
	compatToken string,
	event watchsync.ScrobbleEvent,
	authoritative bool,
) (*PlaybackSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok || session.CompatToken != compatToken {
		return nil, ErrSessionNotFound
	}
	if !session.ExpiresAt.After(s.now()) {
		delete(s.sessions, id)
		return nil, ErrSessionNotFound
	}
	if !session.TerminalAuthoritative || authoritative {
		eventCopy := event
		session.TerminalScrobbleEvent = &eventCopy
		session.TerminalAuthoritative = authoritative
		session.TerminalEventVersion++
	}
	session.Terminal = true
	session.UpdatedAt = s.now()
	s.sessions[id] = session
	return &session, nil
}

// ClaimTerminal leases one staged event. Expired leases may be reclaimed after
// a process dies; a delivered fallback is skipped unless Stopped subsequently
// staged an authoritative replacement.
func (s *PlaybackSessionStore) ClaimTerminal(id, compatToken string, claimUntil time.Time) (*PlaybackSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok || session.CompatToken != compatToken || !session.Terminal || session.TerminalScrobbleEvent == nil {
		return nil, ErrSessionNotFound
	}
	if !session.ExpiresAt.After(s.now()) {
		delete(s.sessions, id)
		return nil, ErrSessionNotFound
	}
	if session.TerminalClaimUntil.After(s.now()) || (session.TerminalFallbackSent && !session.TerminalAuthoritative) {
		return nil, ErrTerminalClaimUnavailable
	}
	session.TerminalClaimUntil = claimUntil
	session.TerminalClaimVersion = session.TerminalEventVersion
	session.UpdatedAt = s.now()
	s.sessions[id] = session
	return &session, nil
}

// ReleaseTerminalClaim releases an exact delivery lease. A stale caller cannot
// clear a successor's lease after its own lease expires.
func (s *PlaybackSessionStore) ReleaseTerminalClaim(
	id string,
	compatToken string,
	claimUntil time.Time,
	claimVersion int64,
	fallbackSent bool,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok || session.CompatToken != compatToken || !session.TerminalClaimUntil.Equal(claimUntil) ||
		session.TerminalClaimVersion != claimVersion {
		return
	}
	session.TerminalClaimUntil = time.Time{}
	session.TerminalClaimVersion = 0
	if fallbackSent {
		session.TerminalFallbackSent = true
	}
	session.UpdatedAt = s.now()
	s.sessions[id] = session
}

// CompleteTerminal removes an authoritatively delivered event while protecting
// a newer lease from a stale completion.
func (s *PlaybackSessionStore) CompleteTerminal(id, compatToken string, claimUntil time.Time, claimVersion int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok || session.CompatToken != compatToken || !session.TerminalAuthoritative ||
		!session.TerminalClaimUntil.Equal(claimUntil) || session.TerminalClaimVersion != claimVersion ||
		session.TerminalEventVersion != claimVersion {
		return
	}
	delete(s.sessions, id)
}

// ListPendingTerminals returns staged events that still need delivery. A
// successfully sent fallback remains retained for a possible authoritative
// replacement but is not itself pending.
func (s *PlaybackSessionStore) ListPendingTerminals(_ context.Context, limit int) ([]PlaybackSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	now := s.now()
	eligible := make([]PlaybackSession, 0, len(s.sessions))
	for id, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			delete(s.sessions, id)
			continue
		}
		if !session.Terminal || session.TerminalScrobbleEvent == nil ||
			(session.TerminalFallbackSent && !session.TerminalAuthoritative) {
			continue
		}
		eligible = append(eligible, session)
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	start := sort.Search(len(eligible), func(i int) bool { return eligible[i].ID > s.pendingCursor })
	if start == len(eligible) && s.pendingCursor != "" {
		start = 0
		s.pendingCursor = ""
	}
	end := min(start+limit, len(eligible))
	result := append([]PlaybackSession(nil), eligible[start:end]...)
	if len(result) > 0 {
		s.pendingCursor = result[len(result)-1].ID
	} else {
		s.pendingCursor = ""
	}
	return result, nil
}

func (s *PlaybackSessionStore) deleteExpired() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	removed := make(map[string]string)
	for id, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			removed[id] = session.CompatToken
			delete(s.sessions, id)
		}
	}
	return removed
}

// GetFinalizable returns an active or terminal caller-owned session so a stop
// report can validate its media fields before atomically consuming it.
func (s *PlaybackSessionStore) GetFinalizable(id, compatToken string) (*PlaybackSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok || session.CompatToken != compatToken {
		return nil, false
	}
	if !session.ExpiresAt.After(s.now()) {
		delete(s.sessions, id)
		return nil, false
	}
	copy := session
	return &copy, true
}

// Update modifies a playback session in place.
func (s *PlaybackSessionStore) Update(id string, fn func(*PlaybackSession) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok || session.Terminal {
		return ErrSessionNotFound
	}
	if !session.ExpiresAt.After(s.now()) {
		delete(s.sessions, id)
		return ErrSessionNotFound
	}
	if err := fn(&session); err != nil {
		return err
	}
	session.UpdatedAt = s.now()
	s.sessions[id] = session
	return nil
}

// FindByClientPlaySessionID resolves the client-generated PlaySessionId alias
// recorded for plays that skipped PlaybackInfo (Static=true direct play). The
// alias must identify exactly one live session: a client that reuses one
// PlaySessionId across plays makes the alias ambiguous, and the caller should
// fall back to route matching instead of binding an arbitrary session.
func (s *PlaybackSessionStore) FindByClientPlaySessionID(compatToken, clientPlaySessionID string) (*PlaybackSession, bool) {
	return s.findByClientPlaySessionID(compatToken, clientPlaySessionID, "", "", false)
}

// FindFinalizableByClientPlaySessionID includes terminal sessions retained for
// an authoritative final report. Route identifiers narrow reused client aliases
// to the play described by that report before the uniqueness check runs.
func (s *PlaybackSessionStore) FindFinalizableByClientPlaySessionID(
	compatToken, clientPlaySessionID, routeItemID, mediaSourceID string,
) (*PlaybackSession, bool) {
	return s.findByClientPlaySessionID(
		compatToken, clientPlaySessionID, routeItemID, mediaSourceID, true,
	)
}

func (s *PlaybackSessionStore) findByClientPlaySessionID(
	compatToken string,
	clientPlaySessionID string,
	routeItemID string,
	mediaSourceID string,
	includeTerminal bool,
) (*PlaybackSession, bool) {
	if clientPlaySessionID == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := s.now()
	var match *PlaybackSession
	for _, session := range s.sessions {
		if !session.ExpiresAt.After(now) || (!includeTerminal && session.Terminal) {
			continue
		}
		if session.CompatToken != compatToken {
			continue
		}
		if routeItemID != "" && !mediaSourceIDsEqual(session.RouteItemID, routeItemID) {
			continue
		}
		if mediaSourceID != "" && findMediaSource(&session, mediaSourceID) == nil {
			continue
		}
		if session.ClientPlaySessionID == clientPlaySessionID {
			if match != nil {
				return nil, false
			}
			cp := session
			match = &cp
		}
	}
	return match, match != nil
}

// FindByUpstreamSessionID resolves the unique compat play attached to a local
// upstream session.
func (s *PlaybackSessionStore) FindByUpstreamSessionID(upstreamSessionID string) (*PlaybackSession, bool) {
	if upstreamSessionID == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := s.now()
	for _, session := range s.sessions {
		if session.ExpiresAt.After(now) && !session.Terminal && session.UpstreamSessionID == upstreamSessionID {
			copy := session
			return &copy, true
		}
	}
	return nil, false
}

// FindByRoute resolves a route item/media-source identifier to a compat playback session.
func (s *PlaybackSessionStore) FindByRoute(compatToken, routeID string) (*PlaybackSession, *PlaybackMediaSource, bool) {
	return s.findByRoute(compatToken, routeID, false, false)
}

// FindFinalizableByRoute includes terminal sessions retained for a stopped
// report, but only returns a unique token-scoped match.
func (s *PlaybackSessionStore) FindFinalizableByRoute(
	compatToken, routeID string,
) (*PlaybackSession, *PlaybackMediaSource, bool) {
	return s.findByRoute(compatToken, routeID, true, true)
}

func (s *PlaybackSessionStore) findByRoute(
	compatToken, routeID string,
	includeTerminal, requireUnique bool,
) (*PlaybackSession, *PlaybackMediaSource, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := s.now()
	var matchedSession *PlaybackSession
	var matchedSource *PlaybackMediaSource
	for _, session := range s.sessions {
		if !session.ExpiresAt.After(now) || (!includeTerminal && session.Terminal) {
			continue
		}
		if compatToken != "" && session.CompatToken != compatToken {
			continue
		}
		// UUID-normalized comparison: playback reports echo the item id in
		// whatever casing/dash format the client model uses, which may differ
		// from the raw route param captured at stream time.
		if mediaSourceIDsEqual(session.RouteItemID, routeID) {
			if requireUnique && matchedSession != nil {
				return nil, nil, false
			}
			cp := session
			matchedSession = &cp
			matchedSource = nil
			if !requireUnique {
				return matchedSession, nil, true
			}
			continue
		}
		for _, source := range session.MediaSources {
			if mediaSourceIDsEqual(source.ID, routeID) {
				if requireUnique && matchedSession != nil {
					return nil, nil, false
				}
				cp := session
				sourceCopy := source
				matchedSession = &cp
				matchedSource = &sourceCopy
				if !requireUnique {
					return matchedSession, matchedSource, true
				}
				break
			}
		}
	}

	return matchedSession, matchedSource, matchedSession != nil
}
