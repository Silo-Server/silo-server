package jellycompat

import (
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// PlaybackSession stores compat-owned playback negotiation state before the
// native Silo playback session starts.
type PlaybackSession struct {
	ID          string
	CompatToken string
	ItemID      string
	RouteItemID string
	// ClientPlaySessionID records the client's own generated PlaySessionId
	// when it differs from ours (Static=true direct play skips PlaybackInfo,
	// so the client never learns the server id). Playback reports carrying
	// that id resolve to this session directly instead of by ambiguous route.
	ClientPlaySessionID string
	UserID              string
	InitialSeekSeconds float64
	MediaSources       []PlaybackMediaSource
	UpstreamSessionID  string
	UpstreamPlayMethod string
	TranscodeStarted   bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ExpiresAt          time.Time
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

// PlaybackSessionStore keeps compat playback sessions in memory.
type PlaybackSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]PlaybackSession
	ttl      time.Duration
	now      func() time.Time
}

// NewPlaybackSessionStore creates a new playback session store.
func NewPlaybackSessionStore(ttl time.Duration, now func() time.Time) *PlaybackSessionStore {
	if now == nil {
		now = time.Now
	}
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	return &PlaybackSessionStore{
		sessions: make(map[string]PlaybackSession),
		ttl:      ttl,
		now:      now,
	}
}

// Put stores or replaces a compat playback session.
func (s *PlaybackSessionStore) Put(session PlaybackSession) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if session.CreatedAt.IsZero() {
		session.CreatedAt = s.now()
	}
	session.UpdatedAt = s.now()
	if session.ExpiresAt.IsZero() {
		session.ExpiresAt = session.CreatedAt.Add(s.ttl)
	}
	s.sessions[session.ID] = session
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
	cp := session
	return &cp, true
}

// Delete removes a playback session.
func (s *PlaybackSessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// Update modifies a playback session in place.
func (s *PlaybackSessionStore) Update(id string, fn func(*PlaybackSession) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
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
	if clientPlaySessionID == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := s.now()
	var match *PlaybackSession
	for _, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			continue
		}
		if session.CompatToken != compatToken {
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

// FindByRoute resolves a route item/media-source identifier to a compat playback session.
func (s *PlaybackSessionStore) FindByRoute(compatToken, routeID string) (*PlaybackSession, *PlaybackMediaSource, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := s.now()
	for _, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			continue
		}
		if compatToken != "" && session.CompatToken != compatToken {
			continue
		}
		// UUID-normalized comparison: playback reports echo the item id in
		// whatever casing/dash format the client model uses, which may differ
		// from the raw route param captured at stream time.
		if mediaSourceIDsEqual(session.RouteItemID, routeID) {
			cp := session
			return &cp, nil, true
		}
		for _, source := range session.MediaSources {
			if mediaSourceIDsEqual(source.ID, routeID) {
				cp := session
				sourceCopy := source
				return &cp, &sourceCopy, true
			}
		}
	}

	return nil, nil, false
}
