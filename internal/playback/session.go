package playback

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Session represents an active playback session.
type Session struct {
	ID                   string
	UserID               int
	ProfileID            string
	MediaFileID          int
	RequestedMediaFileID int
	PlayMethod           PlayMethod
	BasePlayMethod       PlayMethod
	TranscodeAudio       bool   // when true, remux should transcode audio to AAC
	ClientIP             string // resolved client IP for the playback session
	ClientName           string // reported playback client name, when available
	ClientVersion        string // reported playback client version, when available
	ClientUserAgent      string // trimmed request user agent for the playback session

	TranscodeNodeURL string // URL of assigned transcode node (empty = local/integrated)
	AudioTrackIndex  int

	StreamBitrateKbps int    // currently delivered bitrate, when known
	TargetResolution  string // requested output resolution for transcodes
	TargetVideoCodec  string // requested output video codec for transcodes
	TargetAudioCodec  string // requested output audio codec when audio is transcoded
	TargetBitrateKbps int    // requested output bitrate cap for transcodes
	TranscodeHWAccel  string // effective hardware acceleration mode for transcodes

	Position                   float64
	IsPaused                   bool
	HasWebSocket               bool
	HasRealtimeConnection      bool
	DisableProgressPersistence bool
	StartedAt                  time.Time
	UpdatedAt                  time.Time
	LastActivityAt             time.Time
	activeTransportCount       int
}

// SessionStreamState stores the mutable stream-specific details that can
// change after a session is created (audio track, client IP, transcode target,
// and reported bitrate).
type SessionStreamState struct {
	PlayMethod        PlayMethod
	BasePlayMethod    PlayMethod
	AudioTrackIndex   int
	TranscodeAudio    bool
	ClientIP          string
	ClientName        string
	ClientVersion     string
	ClientUserAgent   string
	StreamBitrateKbps int
	TargetResolution  string
	TargetVideoCodec  string
	TargetAudioCodec  string
	TargetBitrateKbps int
	TranscodeHWAccel  string
}

type clientInfoContextKey struct{}

// ClientInfo carries best-effort client metadata from request handling into
// the playback session manager.
type ClientInfo struct {
	Name      string
	Version   string
	UserAgent string
}

// WithClientInfo stores playback client metadata on a context.
func WithClientInfo(ctx context.Context, info ClientInfo) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, clientInfoContextKey{}, info)
}

// ClientInfoFromContext returns playback client metadata stored on a context.
func ClientInfoFromContext(ctx context.Context) ClientInfo {
	if ctx == nil {
		return ClientInfo{}
	}
	info, _ := ctx.Value(clientInfoContextKey{}).(ClientInfo)
	return info
}

// SessionManager tracks active playback sessions and enforces stream limits.
type SessionManager struct {
	sessions      map[string]*Session
	mu            sync.RWMutex
	maxStreams    int
	maxTranscodes int
	limitProvider SessionLimitProvider
	activeGrace   time.Duration
	pausedGrace   time.Duration
	expireHook    func(*Session)
}

// SessionLimits stores per-user admission limits. Zero values mean unlimited.
type SessionLimits struct {
	MaxStreams    int
	MaxTranscodes int
}

// SessionLimitProvider returns the current admission limits for a user.
type SessionLimitProvider func(ctx context.Context, userID int) (SessionLimits, error)

const (
	// DefaultActiveSessionGrace is how long an unpaused session may go without
	// observed playback activity before it stops counting toward limits.
	DefaultActiveSessionGrace = 45 * time.Second

	// DefaultPausedSessionGrace is the longer grace period for paused sessions.
	DefaultPausedSessionGrace = 2 * time.Minute
)

// NewSessionManager creates a SessionManager with the given concurrency limits.
// maxStreams limits total active streams per user.
// maxTranscodes limits concurrent transcode streams per user.
func NewSessionManager(maxStreams, maxTranscodes int) *SessionManager {
	return &SessionManager{
		sessions:      make(map[string]*Session),
		maxStreams:    maxStreams,
		maxTranscodes: maxTranscodes,
		activeGrace:   DefaultActiveSessionGrace,
		pausedGrace:   DefaultPausedSessionGrace,
	}
}

// SetLimitProvider overrides the manager defaults with dynamic per-user
// limits. The constructor limits remain the fallback when no provider is set.
func (m *SessionManager) SetLimitProvider(provider SessionLimitProvider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.limitProvider = provider
}

// SetLivenessGracePeriods overrides the grace periods used by admission
// control and stale-session cleanup.
func (m *SessionManager) SetLivenessGracePeriods(active, paused time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if active > 0 {
		m.activeGrace = active
	}
	if paused > 0 {
		m.pausedGrace = paused
	}
}

// SetExpirationHook registers a callback that runs after a session is removed
// by stale cleanup. The hook executes outside the manager lock.
func (m *SessionManager) SetExpirationHook(fn func(*Session)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireHook = fn
}

func normalizeClientMetadataValue(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if maxLen > 0 && len(value) > maxLen {
		value = value[:maxLen]
	}
	return value
}

// StartSession creates a new playback session using the same file as both the
// requested and effective source.
func (m *SessionManager) StartSession(userID int, profileID string, fileID int, method PlayMethod, transcodeAudio bool) (*Session, error) {
	return m.StartSessionWithContext(context.Background(), userID, profileID, fileID, method, transcodeAudio)
}

// StartSessionWithContext creates a new playback session using the same file
// as both the requested and effective source.
func (m *SessionManager) StartSessionWithContext(
	ctx context.Context,
	userID int,
	profileID string,
	fileID int,
	method PlayMethod,
	transcodeAudio bool,
) (*Session, error) {
	return m.StartSessionWithFilesContext(ctx, userID, profileID, fileID, fileID, method, transcodeAudio)
}

// StartSessionWithFiles creates a new playback session after checking
// concurrency limits. requestedFileID is the user's requested version while
// effectiveFileID is the file currently backing playback.
// Returns ErrTooManyStreams if the user has reached the max active stream count.
// Returns ErrTooManyTranscodes if the user has reached the max transcode count
// and the requested method is transcode.
func (m *SessionManager) StartSessionWithFiles(
	userID int,
	profileID string,
	effectiveFileID int,
	requestedFileID int,
	method PlayMethod,
	transcodeAudio bool,
) (*Session, error) {
	return m.StartSessionWithFilesContext(context.Background(), userID, profileID, effectiveFileID, requestedFileID, method, transcodeAudio)
}

// StartSessionWithFilesContext creates a new playback session after checking
// concurrency limits with request-scoped limit lookup.
func (m *SessionManager) StartSessionWithFilesContext(
	ctx context.Context,
	userID int,
	profileID string,
	effectiveFileID int,
	requestedFileID int,
	method PlayMethod,
	transcodeAudio bool,
) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	limits, err := m.limitsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Enforce stream limits.
	if limits.MaxStreams > 0 && m.activeCountLocked(userID) >= limits.MaxStreams {
		return nil, ErrTooManyStreams
	}

	// Enforce transcode limits.
	if method == PlayTranscode && limits.MaxTranscodes > 0 && m.transcodeCountLocked(userID) >= limits.MaxTranscodes {
		return nil, ErrTooManyTranscodes
	}

	now := time.Now()
	clientInfo := ClientInfoFromContext(ctx)
	s := &Session{
		ID:                   uuid.New().String(),
		UserID:               userID,
		ProfileID:            profileID,
		MediaFileID:          effectiveFileID,
		RequestedMediaFileID: requestedFileID,
		PlayMethod:           method,
		BasePlayMethod:       method,
		TranscodeAudio:       transcodeAudio,
		Position:             0,
		IsPaused:             false,
		ClientName:           normalizeClientMetadataValue(clientInfo.Name, 128),
		ClientVersion:        normalizeClientMetadataValue(clientInfo.Version, 64),
		ClientUserAgent:      normalizeClientMetadataValue(clientInfo.UserAgent, 512),
		StartedAt:            now,
		UpdatedAt:            now,
		LastActivityAt:       now,
	}

	m.sessions[s.ID] = s
	return s, nil
}

func (m *SessionManager) limitsForUser(ctx context.Context, userID int) (SessionLimits, error) {
	m.mu.RLock()
	provider := m.limitProvider
	limits := SessionLimits{
		MaxStreams:    m.maxStreams,
		MaxTranscodes: m.maxTranscodes,
	}
	m.mu.RUnlock()

	if provider == nil {
		return limits, nil
	}
	limits, err := provider(ctx, userID)
	if err != nil {
		return SessionLimits{}, fmt.Errorf("load session limits for user %d: %w", userID, err)
	}
	return limits, nil
}

// UpdateProgress updates the playback position and pause state for a session.
func (m *SessionManager) UpdateProgress(sessionID string, position float64, isPaused bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.Position = position
	s.IsPaused = isPaused
	m.touchSessionLocked(s)
	return nil
}

// UpdateAudioTrack updates the audio track index and optionally the play
// method for a session. Used when switching audio tracks mid-playback.
func (m *SessionManager) UpdateAudioTrack(sessionID string, audioTrackIndex int, method PlayMethod) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.AudioTrackIndex = audioTrackIndex
	s.BasePlayMethod = method
	if s.PlayMethod != PlayTranscode || method == PlayTranscode {
		s.PlayMethod = method
	}
	m.touchSessionLocked(s)
	return nil
}

// UpdateStreamState updates the live stream details for a session. This keeps
// the session manager's authoritative copy in sync with user-driven changes
// like audio track switches and quality changes.
func (m *SessionManager) UpdateStreamState(sessionID string, state SessionStreamState) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	if state.PlayMethod != "" {
		s.PlayMethod = state.PlayMethod
	}
	if state.BasePlayMethod != "" {
		s.BasePlayMethod = state.BasePlayMethod
	}
	s.AudioTrackIndex = state.AudioTrackIndex
	s.TranscodeAudio = state.TranscodeAudio
	s.ClientIP = state.ClientIP
	if value := normalizeClientMetadataValue(state.ClientName, 128); value != "" {
		s.ClientName = value
	}
	if value := normalizeClientMetadataValue(state.ClientVersion, 64); value != "" {
		s.ClientVersion = value
	}
	if value := normalizeClientMetadataValue(state.ClientUserAgent, 512); value != "" {
		s.ClientUserAgent = value
	}
	s.StreamBitrateKbps = state.StreamBitrateKbps
	s.TargetResolution = state.TargetResolution
	s.TargetVideoCodec = state.TargetVideoCodec
	s.TargetAudioCodec = state.TargetAudioCodec
	s.TargetBitrateKbps = state.TargetBitrateKbps
	s.TranscodeHWAccel = state.TranscodeHWAccel
	m.touchSessionLocked(s)
	return nil
}

// SetTranscodeNodeURL assigns a transcode node URL to an existing session.
func (m *SessionManager) SetTranscodeNodeURL(sessionID, url string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.TranscodeNodeURL = url
	m.touchSessionLocked(s)
	return nil
}

// SetEffectiveMediaFileID updates the currently delivered source file while
// preserving the originally requested file selection.
func (m *SessionManager) SetEffectiveMediaFileID(sessionID string, fileID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	if fileID > 0 {
		s.MediaFileID = fileID
	}
	m.touchSessionLocked(s)
	return nil
}

// SetWebSocket marks whether a WebSocket liveness connection is active for a session.
func (m *SessionManager) SetWebSocket(sessionID string, connected bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.HasWebSocket = connected
	m.touchSessionLocked(s)
	return nil
}

// SetRealtimeConnection marks whether a realtime control connection is active for a session.
func (m *SessionManager) SetRealtimeConnection(sessionID string, connected bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.HasRealtimeConnection = connected
	// The admin/session sync layer still exposes a generic websocket flag.
	s.HasWebSocket = connected
	m.touchSessionLocked(s)
	return nil
}

// SetProgressPersistenceDisabled controls whether session progress updates and
// stop events should write resume/history state. This is useful for players
// whose resume timeline is not the same as the session's file-local timeline.
func (m *SessionManager) SetProgressPersistenceDisabled(sessionID string, disabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.DisableProgressPersistence = disabled
	m.touchSessionLocked(s)
	return nil
}

// TouchActivity refreshes the session's activity timestamp without changing
// any other playback state.
func (m *SessionManager) TouchActivity(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	m.touchSessionLocked(s)
	return nil
}

// BeginTransport increments the count of in-flight media transport requests
// for the session and refreshes its activity timestamp.
func (m *SessionManager) BeginTransport(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.activeTransportCount++
	m.touchSessionLocked(s)
	return nil
}

// EndTransport decrements the count of in-flight media transport requests for
// the session and refreshes its activity timestamp.
func (m *SessionManager) EndTransport(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	if s.activeTransportCount > 0 {
		s.activeTransportCount--
	}
	m.touchSessionLocked(s)
	return nil
}

// StopSession removes a session from the manager.
func (m *SessionManager) StopSession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}

	delete(m.sessions, sessionID)
	return nil
}

// GetSession returns the session with the given ID, or ErrSessionNotFound.
func (m *SessionManager) GetSession(sessionID string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	// Return a copy to avoid races.
	cp := *s
	return &cp, nil
}

// GetUserSessions returns all active sessions for a user.
func (m *SessionManager) GetUserSessions(userID int) []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*Session
	for _, s := range m.sessions {
		if s.UserID == userID {
			cp := *s
			result = append(result, &cp)
		}
	}
	return result
}

// GetSessionsByMediaFileID returns active sessions associated with the given file.
func (m *SessionManager) GetSessionsByMediaFileID(fileID int) []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if fileID <= 0 {
		return nil
	}

	var result []*Session
	for _, s := range m.sessions {
		if s.MediaFileID != fileID && s.RequestedMediaFileID != fileID {
			continue
		}
		cp := *s
		result = append(result, &cp)
	}
	return result
}

// ActiveCount returns the number of active sessions for a user.
func (m *SessionManager) ActiveCount(userID int) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeCountLocked(userID)
}

// TranscodeCount returns the number of active transcode sessions for a user.
func (m *SessionManager) TranscodeCount(userID int) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.transcodeCountLocked(userID)
}

// activeCountLocked counts active sessions for a user. Caller must hold the lock.
func (m *SessionManager) activeCountLocked(userID int) int {
	now := time.Now()
	count := 0
	for _, s := range m.sessions {
		if s.UserID == userID && m.countsTowardLimitsLocked(s, now) {
			count++
		}
	}
	return count
}

// transcodeCountLocked counts transcode sessions for a user. Caller must hold the lock.
func (m *SessionManager) transcodeCountLocked(userID int) int {
	now := time.Now()
	count := 0
	for _, s := range m.sessions {
		if s.UserID == userID && s.PlayMethod == PlayTranscode && m.countsTowardLimitsLocked(s, now) {
			count++
		}
	}
	return count
}

// AllSessions returns a snapshot of all active sessions. Each session is
// copied to avoid data races with concurrent updates.
func (m *SessionManager) AllSessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		cp := *s
		result = append(result, &cp)
	}
	return result
}

// CleanExpired removes sessions whose last playback activity exceeds maxIdle.
// Paused sessions receive a 3x grace period for backwards compatibility.
func (m *SessionManager) CleanExpired(maxIdle time.Duration) []*Session {
	return m.CleanInactive(maxIdle, maxIdle*3)
}

// CleanStale removes sessions that have exceeded the manager's configured
// liveness grace windows.
func (m *SessionManager) CleanStale() []*Session {
	m.mu.RLock()
	active := m.activeGrace
	paused := m.pausedGrace
	m.mu.RUnlock()
	return m.CleanInactive(active, paused)
}

// CleanInactive removes sessions whose last playback activity exceeds the
// provided grace period. Sessions with an active media transport request are
// preserved even if they have not emitted a recent heartbeat yet.
func (m *SessionManager) CleanInactive(activeIdle, pausedIdle time.Duration) []*Session {
	m.mu.Lock()

	now := time.Now()
	var expired []*Session
	for id, s := range m.sessions {
		if s.activeTransportCount > 0 {
			continue
		}
		if m.sessionIsInactiveLocked(s, now, activeIdle, pausedIdle) {
			cp := *s
			expired = append(expired, &cp)
			delete(m.sessions, id)
		}
	}
	hook := m.expireHook
	m.mu.Unlock()

	if hook != nil {
		for _, s := range expired {
			hook(s)
		}
	}
	return expired
}

func (m *SessionManager) touchSessionLocked(s *Session) {
	now := time.Now()
	s.LastActivityAt = now
	s.UpdatedAt = now
}

func (m *SessionManager) countsTowardLimitsLocked(s *Session, now time.Time) bool {
	if s == nil {
		return false
	}
	if s.activeTransportCount > 0 {
		return true
	}
	return !m.sessionIsInactiveLocked(s, now, m.activeGrace, m.pausedGrace)
}

func (m *SessionManager) sessionIsInactiveLocked(s *Session, now time.Time, activeIdle, pausedIdle time.Duration) bool {
	if s == nil {
		return true
	}

	lastActivity := s.LastActivityAt
	if lastActivity.IsZero() {
		lastActivity = s.UpdatedAt
	}
	if lastActivity.IsZero() {
		lastActivity = s.StartedAt
	}
	if lastActivity.IsZero() {
		return false
	}

	grace := activeIdle
	if s.IsPaused {
		grace = pausedIdle
	}
	if grace <= 0 {
		return !lastActivity.After(now)
	}

	return !lastActivity.Add(grace).After(now)
}

// String returns a human-readable summary of a session.
func (s *Session) String() string {
	return fmt.Sprintf("Session{id=%s user=%d file=%d method=%s pos=%.1f paused=%v}",
		s.ID, s.UserID, s.MediaFileID, s.PlayMethod, s.Position, s.IsPaused)
}
