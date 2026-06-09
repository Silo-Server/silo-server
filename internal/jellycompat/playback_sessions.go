package jellycompat

import (
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// PlaybackSession stores compat-owned playback negotiation state before the
// native Silo playback session starts.
type PlaybackSession struct {
	ID                 string
	CompatToken        string
	ItemID             string
	RouteItemID        string
	UserID             string
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
	ID                         string
	FileID                     int
	Version                    catalog.FileVersion
	SupportsDirectPlay         bool
	SupportsDirectStream       bool
	SupportsTranscoding        bool
	TranscodeAudio             bool
	DefaultAudioStreamIndex    *int
	SelectedAudioStreamIndex   *int
	DefaultSubtitleStreamIndex *int
	ETag                       string
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
		if session.RouteItemID == routeID {
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
