package playback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	TranscodeAudio       bool // when true, remux should transcode audio to AAC
	RemuxDVMode          RemuxDVMode
	ClientIP             string // resolved client IP for the playback session
	ClientName           string // reported playback client name, when available
	ClientVersion        string // reported playback client version, when available
	ClientUserAgent      string // trimmed request user agent for the playback session

	TranscodeNodeURL     string // URL of assigned transcode node (empty = local/integrated)
	TranscodeTransportID string // remote node process identity; empty means session ID
	AudioTrackIndex      int

	StreamBitrateKbps int    // currently delivered bitrate, when known
	TargetResolution  string // requested output resolution for transcodes
	TargetVideoCodec  string // requested output video codec for transcodes
	TargetAudioCodec  string // requested output audio codec when audio is transcoded
	TargetBitrateKbps int    // requested output bitrate cap for transcodes
	TranscodeHWAccel  string // effective hardware acceleration mode for transcodes

	// Byte-affecting transcode recipe fields the offloaded restart path needs to
	// rebuild the exact same stream after an audio switch. Local transcodes read
	// these from the live ts.Opts(); offloaded transcodes own no local runtime, so
	// the session is the only place to recover them (see HandleChangeAudioTrack).
	SubtitleTrackIndex int // -1 = no subtitles
	SubtitleBurnIn     bool
	SegmentDuration    int // HLS segment length in seconds (cadence)

	Position                   float64
	IsPaused                   bool
	HasWebSocket               bool
	HasRealtimeConnection      bool
	DisableProgressPersistence bool
	StartedAt                  time.Time
	UpdatedAt                  time.Time
	LastActivityAt             time.Time
	activeTransportCount       int
	replacementPlayMethod      PlayMethod
	streamRevision             uint64
}

// SessionStreamState stores the mutable stream-specific details that can
// change after a session is created (audio track, client IP, transcode target,
// and reported bitrate).
type SessionStreamState struct {
	PlayMethod           PlayMethod
	BasePlayMethod       PlayMethod
	AudioTrackIndex      int
	TranscodeAudio       bool
	RemuxDVMode          RemuxDVMode
	ClientIP             string
	ClientName           string
	ClientVersion        string
	ClientUserAgent      string
	StreamBitrateKbps    int
	TargetResolution     string
	TargetVideoCodec     string
	TargetAudioCodec     string
	TargetBitrateKbps    int
	TranscodeHWAccel     string
	TranscodeNodeURL     string
	TranscodeTransportID string
	TranscodeRouteSet    bool

	// Byte-affecting transcode recipe fields preserved so an offloaded restart
	// (e.g. audio switch) can rebuild the exact same stream. SubtitleTrackIndex
	// defaults to 0 on a zero-value state; callers that manage subtitles must set
	// it explicitly (-1 for none) — burn-in is additionally gated by
	// SubtitleBurnIn so a zero index never burns track 0 by accident.
	SubtitleTrackIndex int
	SubtitleBurnIn     bool
	SegmentDuration    int
}

// SessionReplacement is the complete mutable session state associated with a
// protocol-v3 replacement plan. Position is optional because ordinary failure
// recovery must preserve the player's latest progress while seek recovery
// intentionally moves the authoritative timeline.
type SessionReplacement struct {
	EffectiveMediaFileID int
	StreamState          SessionStreamState
	PositionSeconds      *float64
	IsPaused             bool
	PreservePaused       bool
}

// SessionReplacementRollback is an opaque compare-and-swap token returned by
// ApplyReplacement. It can restore the previous session state only while no
// newer stream or progress mutation has superseded the replacement.
type SessionReplacementRollback struct {
	sessionID                    string
	appliedRevision              uint64
	previousEffectiveMediaFileID int
	previousStreamState          SessionStreamState
	previousPosition             float64
	previousPaused               bool
	restoreProgress              bool
	previousReplacementMethod    PlayMethod
}

// ErrSessionReplacementSuperseded means a replacement rollback would overwrite
// a newer session mutation. Callers should terminate the session rather than
// expose state that disagrees with the durable playback plan.
var ErrSessionReplacementSuperseded = errors.New("session replacement was superseded")

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
	sessions         map[string]*Session
	mu               sync.RWMutex
	maxStreams       int
	maxTranscodes    int
	limitProvider    SessionLimitProvider
	admissionDecider AdmissionDecider
	activeGrace      time.Duration
	pausedGrace      time.Duration
	expireHook       func(*Session)
}

// SessionLimits stores per-user admission limits. Zero values mean unlimited.
type SessionLimits struct {
	MaxStreams               int
	MaxTranscodes            int
	TranscodingDisabled      bool
	AudioTranscodingDisabled bool
}

// SessionLimitProvider returns the current admission limits for a user.
type SessionLimitProvider func(ctx context.Context, userID int) (SessionLimits, error)

// AdmissionRequest is the fact set passed to an optional policy admission
// decider. Counts are computed by SessionManager from live in-memory sessions.
type AdmissionRequest struct {
	UserID                  int
	Limits                  SessionLimits
	CurrentActiveStreams    int
	CurrentActiveTranscodes int
	RequestedMethod         PlayMethod
	RequiresVideoTranscode  bool
	RequiresAudioTranscode  bool
}

// AdmissionDecision is the result of an optional policy admission decision.
// Reason is free text for logs; ReasonCode is the typed contract mapped to
// sentinel errors (values mirror the vendor policy reason_code output).
type AdmissionDecision struct {
	Allowed    bool
	Reason     string
	ReasonCode string
}

// Admission reason codes recognized by admissionDenyError. They mirror the
// policy package's ReasonCode* constants; playback cannot import policy
// (policy's adapters import playback), so the shared values are pinned by
// tests on both sides.
const (
	AdmissionReasonMaxStreamsExceeded       = "max_streams_exceeded"
	AdmissionReasonMaxTranscodesExceeded    = "max_transcodes_exceeded"
	AdmissionReasonTranscodingDisabled      = "transcoding_disabled"
	AdmissionReasonAudioTranscodingDisabled = "audio_transcoding_disabled"
)

// AdmissionDecider can replace SessionManager's inline limit comparison while
// keeping session counting in Go.
type AdmissionDecider func(ctx context.Context, req AdmissionRequest) (AdmissionDecision, error)

const (
	// DefaultActiveSessionGrace is how long an unpaused session may go without
	// observed playback activity before it stops counting toward limits.
	DefaultActiveSessionGrace = 45 * time.Second

	// DefaultPausedSessionGrace is the longer grace period for paused
	// sessions. It must comfortably cover an intentional pause (dinner
	// break, phone call): reaping a paused session kills its transcode
	// and there is currently no revival path, so a too-short grace makes
	// pressing Play after a long pause freeze the client (issue #243).
	// Keep in sync with pausedSessionGrace in internal/worker/cleanup.go.
	DefaultPausedSessionGrace = 30 * time.Minute
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

// SetAdmissionDecider installs an optional policy admission hook. A nil decider
// keeps the legacy inline comparison.
func (m *SessionManager) SetAdmissionDecider(decider AdmissionDecider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.admissionDecider = decider
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
// Returns ErrTranscodingDisabled when the user may not start a video or audio transcode.
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

	for {
		m.mu.Lock()
		decider := m.admissionDecider
		if decider == nil {
			if err := m.inlineAdmissionErrorLocked(userID, method, transcodeAudio, limits); err != nil {
				m.mu.Unlock()
				return nil, err
			}
			s := newSession(ctx, userID, profileID, effectiveFileID, requestedFileID, method, transcodeAudio)
			m.sessions[s.ID] = s
			m.mu.Unlock()
			return s, nil
		}
		activeStreams := m.activeCountLocked(userID)
		activeTranscodes := m.transcodeCountLocked(userID)
		m.mu.Unlock()

		decision, err := decider(ctx, AdmissionRequest{
			UserID:                  userID,
			Limits:                  limits,
			CurrentActiveStreams:    activeStreams,
			CurrentActiveTranscodes: activeTranscodes,
			RequestedMethod:         method,
			RequiresVideoTranscode:  method == PlayTranscode,
			RequiresAudioTranscode:  transcodeAudio,
		})
		if err != nil {
			// Fail closed, but make an engine outage distinguishable from a
			// genuine concurrency-limit denial in the logs.
			slog.WarnContext(ctx, "playback admission decider error; denying session", "component", "playback",
				"user_id", userID, "method", method, "error", err)
			return nil, admissionDenyError("")
		}
		if !decision.Allowed {
			return nil, admissionDenyError(decision.ReasonCode)
		}

		m.mu.Lock()
		if activeStreams != m.activeCountLocked(userID) || activeTranscodes != m.transcodeCountLocked(userID) {
			m.mu.Unlock()
			continue
		}
		s := newSession(ctx, userID, profileID, effectiveFileID, requestedFileID, method, transcodeAudio)
		m.sessions[s.ID] = s
		m.mu.Unlock()
		return s, nil
	}
}

func (m *SessionManager) inlineAdmissionErrorLocked(userID int, method PlayMethod, transcodeAudio bool, limits SessionLimits) error {
	if err := transcodingDisabledError(method == PlayTranscode, transcodeAudio, limits); err != nil {
		return err
	}

	if limits.MaxStreams > 0 && m.activeCountLocked(userID) >= limits.MaxStreams {
		return ErrTooManyStreams
	}

	if method == PlayTranscode && limits.MaxTranscodes > 0 && m.transcodeCountLocked(userID) >= limits.MaxTranscodes {
		return ErrTooManyTranscodes
	}
	return nil
}

func newSession(
	ctx context.Context,
	userID int,
	profileID string,
	effectiveFileID int,
	requestedFileID int,
	method PlayMethod,
	transcodeAudio bool,
) *Session {
	now := time.Now()
	clientInfo := ClientInfoFromContext(ctx)
	return &Session{
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
}

// admissionDenyError maps a typed reason code to a sentinel error. Anything
// unrecognized — custom-override denials, engine failures — is a generic
// policy denial, not a concurrency-limit error.
func admissionDenyError(reasonCode string) error {
	switch reasonCode {
	case AdmissionReasonMaxStreamsExceeded:
		return ErrTooManyStreams
	case AdmissionReasonMaxTranscodesExceeded:
		return ErrTooManyTranscodes
	case AdmissionReasonTranscodingDisabled:
		return ErrTranscodingDisabled
	case AdmissionReasonAudioTranscodingDisabled:
		return ErrAudioTranscodingDisabled
	default:
		return ErrPlaybackNotAllowed
	}
}

// RegisterReconstructed re-inserts a session under an existing ID after the
// in-memory state was lost (e.g. a server restart). Unlike StartSession* it
// does NOT mint a new UUID and does NOT run admission/limit accounting: the
// session already existed and was admitted before the restart, so counting it
// again would be wrong. If a live session with the same ID already exists
// (a concurrent reconstruct won the race), the existing one is returned and
// the caller's copy is discarded.
//
// The caller is responsible for having re-bound s.UserID to the live
// authenticated request before calling this — RegisterReconstructed performs
// no authorization itself.
func (m *SessionManager) RegisterReconstructed(s *Session) *Session {
	if s == nil || s.ID == "" {
		return s
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.sessions[s.ID]; ok {
		return existing
	}
	now := time.Now()
	if s.StartedAt.IsZero() {
		s.StartedAt = now
	}
	s.UpdatedAt = now
	s.LastActivityAt = now
	m.sessions[s.ID] = s
	return s
}

// RegisterReconstructedWithLimits is RegisterReconstructed plus the same per-user
// admission caps StartSession enforces. Token-carried reconstruct replays a
// signed recipe to rebuild a session lost to a restart; without a cap check a
// client could replay one token repeatedly (or after legitimately reaching its
// limit) and reconstruct past the per-user concurrent stream/transcode caps,
// since RegisterReconstructed skips admission accounting.
//
// Legitimately reconstructing a user's own surviving sessions still succeeds:
// the cap counts the user's *currently-live* sessions, and the one being rebuilt
// is not yet in the map, so the first MaxStreams reconstructs admit. Only the
// over-cap replay or disabled transcode is refused. If an
// identical session id is already live (a concurrent reconstruct won), it is
// returned without re-counting. Caps are looked up via the same limit provider
// as StartSession.
func (m *SessionManager) RegisterReconstructedWithLimits(ctx context.Context, s *Session) (*Session, error) {
	if s == nil || s.ID == "" {
		return s, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limits, err := m.limitsForUser(ctx, s.UserID)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.sessions[s.ID]; ok {
		return existing, nil
	}

	// The session being reconstructed is not yet in the map, so the live counts
	// reflect the user's *other* sessions; admitting one more must stay within cap.
	if err := transcodingDisabledError(s.PlayMethod == PlayTranscode, s.TranscodeAudio, limits); err != nil {
		return nil, err
	}
	if limits.MaxStreams > 0 && m.activeCountLocked(s.UserID) >= limits.MaxStreams {
		return nil, ErrTooManyStreams
	}
	if s.PlayMethod == PlayTranscode && limits.MaxTranscodes > 0 &&
		m.transcodeCountLocked(s.UserID) >= limits.MaxTranscodes {
		return nil, ErrTooManyTranscodes
	}

	now := time.Now()
	if s.StartedAt.IsZero() {
		s.StartedAt = now
	}
	s.UpdatedAt = now
	s.LastActivityAt = now
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
		// Tag provider failures with ErrLimitProviderUnavailable so the
		// reconstruct admission path can distinguish a transient limit-lookup
		// failure (which it may fail open on) from a genuine over-cap rejection.
		return SessionLimits{}, fmt.Errorf("load session limits for user %d: %w",
			userID, errors.Join(ErrLimitProviderUnavailable, err))
	}
	return limits, nil
}

// CheckTranscodingAllowed verifies account-level restrictions before an
// existing session switches to video or audio transcoding.
func (m *SessionManager) CheckTranscodingAllowed(ctx context.Context, userID int, requiresVideoTranscode bool) error {
	limits, err := m.limitsForUser(ctx, userID)
	if err != nil {
		return err
	}
	return transcodingDisabledError(requiresVideoTranscode, !requiresVideoTranscode, limits)
}

// CheckReplacementAllowed applies current user limits and admission policy to
// an in-place protocol-v3 recipe replacement. The existing session is excluded
// from the counts because the replacement inherits its stream slot; a direct
// to transcode change still has to acquire an available transcode slot.
func (m *SessionManager) CheckReplacementAllowed(ctx context.Context, sessionID string, method PlayMethod, transcodeAudio bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Bounded CAS: persistent count churn means the user is actively starting
	// and stopping sessions; failing closed after a few rounds beats spinning
	// with a limit-provider DB call per iteration.
	const maxAdmissionRetries = 8
	for attempt := 0; attempt < maxAdmissionRetries; attempt++ {
		m.mu.Lock()
		current, ok := m.sessions[sessionID]
		if !ok {
			m.mu.Unlock()
			return ErrSessionNotFound
		}
		userID := current.UserID
		currentMethod := current.PlayMethod
		// Exclude the replaced session from both counts instead of decrementing
		// the totals: a failed session idle past the liveness grace is already
		// absent from the count, and a blind decrement would free a slot that
		// belongs to another live session.
		otherStreams := m.activeCountExcludingLocked(userID, sessionID)
		otherTranscodes := m.transcodeCountExcludingLocked(userID, sessionID)
		decider := m.admissionDecider
		m.mu.Unlock()

		limits, err := m.limitsForUser(ctx, userID)
		if err != nil {
			return err
		}
		if err := transcodingDisabledError(method == PlayTranscode, transcodeAudio, limits); err != nil {
			return err
		}
		if decider == nil {
			m.mu.Lock()
			stillCurrent, stillExists := m.sessions[sessionID]
			countsStable := stillExists && stillCurrent.PlayMethod == currentMethod && otherStreams == m.activeCountExcludingLocked(userID, sessionID) && otherTranscodes == m.transcodeCountExcludingLocked(userID, sessionID)
			if !countsStable {
				m.mu.Unlock()
				continue
			}
			if limits.MaxTranscodes > 0 && method == PlayTranscode && otherTranscodes >= limits.MaxTranscodes {
				m.mu.Unlock()
				return ErrTooManyTranscodes
			}
			stillCurrent.replacementPlayMethod = method
			m.mu.Unlock()
			return nil
		}
		decision, err := decider(ctx, AdmissionRequest{UserID: userID, Limits: limits, CurrentActiveStreams: otherStreams, CurrentActiveTranscodes: otherTranscodes, RequestedMethod: method, RequiresVideoTranscode: method == PlayTranscode, RequiresAudioTranscode: transcodeAudio})
		if err != nil {
			// Fail closed, but make an engine outage distinguishable from a
			// genuine concurrency-limit denial in the logs.
			slog.WarnContext(ctx, "playback replacement admission decider error; denying replacement", "component", "playback",
				"user_id", userID, "session", sessionID, "method", method, "error", err)
			return ErrPlaybackNotAllowed
		}
		if !decision.Allowed {
			return admissionDenyError(decision.ReasonCode)
		}
		m.mu.Lock()
		stillCurrent, stillExists := m.sessions[sessionID]
		countsStable := stillExists && stillCurrent.PlayMethod == currentMethod && otherStreams == m.activeCountExcludingLocked(userID, sessionID) && otherTranscodes == m.transcodeCountExcludingLocked(userID, sessionID)
		if countsStable {
			stillCurrent.replacementPlayMethod = method
			m.mu.Unlock()
			return nil
		}
		m.mu.Unlock()
	}
	return ErrPlaybackNotAllowed
}

// CancelReplacementReservation releases a protocol-v3 capacity reservation
// after a replacement fails before UpdateStreamState commits its new method.
func (m *SessionManager) CancelReplacementReservation(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[sessionID]; session != nil {
		session.replacementPlayMethod = ""
	}
}

func transcodingDisabledError(requiresVideoTranscode, requiresAudioTranscode bool, limits SessionLimits) error {
	if requiresVideoTranscode && limits.TranscodingDisabled {
		return ErrTranscodingDisabled
	}
	if requiresAudioTranscode && limits.TranscodingDisabled && limits.AudioTranscodingDisabled {
		return ErrAudioTranscodingDisabled
	}
	return nil
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
	s.streamRevision++
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
	s.streamRevision++
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

	applySessionStreamStateLocked(s, state)
	s.streamRevision++
	m.touchSessionLocked(s)
	return nil
}

func applySessionStreamStateLocked(s *Session, state SessionStreamState) {
	if state.PlayMethod != "" {
		s.PlayMethod = state.PlayMethod
	}
	if state.BasePlayMethod != "" {
		s.BasePlayMethod = state.BasePlayMethod
	}
	s.AudioTrackIndex = state.AudioTrackIndex
	s.TranscodeAudio = state.TranscodeAudio
	if state.TranscodeRouteSet {
		// A full v3 route description owns the DV mode outright: a replan from
		// a DV strip remux to an SDR source must clear the stale mode or every
		// later remux request fails the profile check. Legacy partial updates
		// never carry a mode and must not clobber one.
		s.RemuxDVMode = state.RemuxDVMode
	} else if state.RemuxDVMode != "" {
		s.RemuxDVMode = state.RemuxDVMode
	}
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
	if state.TranscodeRouteSet {
		s.TranscodeNodeURL = state.TranscodeNodeURL
		s.TranscodeTransportID = state.TranscodeTransportID
	}
	s.SubtitleTrackIndex = state.SubtitleTrackIndex
	s.SubtitleBurnIn = state.SubtitleBurnIn
	s.SegmentDuration = state.SegmentDuration
	if state.TranscodeRouteSet {
		// Only the replacement commit consumes the v3 capacity reservation;
		// unrelated legacy stream updates arriving mid-replan must not release
		// the slot and let a concurrent admission race past the transcode cap.
		s.replacementPlayMethod = ""
	}
}

func snapshotSessionStreamStateLocked(s *Session) SessionStreamState {
	return SessionStreamState{
		PlayMethod:           s.PlayMethod,
		BasePlayMethod:       s.BasePlayMethod,
		AudioTrackIndex:      s.AudioTrackIndex,
		TranscodeAudio:       s.TranscodeAudio,
		RemuxDVMode:          s.RemuxDVMode,
		ClientIP:             s.ClientIP,
		ClientName:           s.ClientName,
		ClientVersion:        s.ClientVersion,
		ClientUserAgent:      s.ClientUserAgent,
		StreamBitrateKbps:    s.StreamBitrateKbps,
		TargetResolution:     s.TargetResolution,
		TargetVideoCodec:     s.TargetVideoCodec,
		TargetAudioCodec:     s.TargetAudioCodec,
		TargetBitrateKbps:    s.TargetBitrateKbps,
		TranscodeHWAccel:     s.TranscodeHWAccel,
		TranscodeNodeURL:     s.TranscodeNodeURL,
		TranscodeTransportID: s.TranscodeTransportID,
		TranscodeRouteSet:    true,
		SubtitleTrackIndex:   s.SubtitleTrackIndex,
		SubtitleBurnIn:       s.SubtitleBurnIn,
		SegmentDuration:      s.SegmentDuration,
	}
}

func restoreSessionStreamStateLocked(s *Session, state SessionStreamState) {
	s.PlayMethod = state.PlayMethod
	s.BasePlayMethod = state.BasePlayMethod
	s.AudioTrackIndex = state.AudioTrackIndex
	s.TranscodeAudio = state.TranscodeAudio
	s.RemuxDVMode = state.RemuxDVMode
	s.ClientIP = state.ClientIP
	s.ClientName = state.ClientName
	s.ClientVersion = state.ClientVersion
	s.ClientUserAgent = state.ClientUserAgent
	s.StreamBitrateKbps = state.StreamBitrateKbps
	s.TargetResolution = state.TargetResolution
	s.TargetVideoCodec = state.TargetVideoCodec
	s.TargetAudioCodec = state.TargetAudioCodec
	s.TargetBitrateKbps = state.TargetBitrateKbps
	s.TranscodeHWAccel = state.TranscodeHWAccel
	s.TranscodeNodeURL = state.TranscodeNodeURL
	s.TranscodeTransportID = state.TranscodeTransportID
	s.SubtitleTrackIndex = state.SubtitleTrackIndex
	s.SubtitleBurnIn = state.SubtitleBurnIn
	s.SegmentDuration = state.SegmentDuration
}

// ApplyReplacement atomically updates every live-session field owned by a
// protocol-v3 plan and returns a CAS rollback token for a later persistence
// failure.
func (m *SessionManager) ApplyReplacement(sessionID string, replacement SessionReplacement) (SessionReplacementRollback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return SessionReplacementRollback{}, ErrSessionNotFound
	}
	if replacement.EffectiveMediaFileID <= 0 {
		return SessionReplacementRollback{}, errors.New("replacement effective media file id is invalid")
	}

	rollback := SessionReplacementRollback{
		sessionID:                    sessionID,
		previousEffectiveMediaFileID: s.MediaFileID,
		previousStreamState:          snapshotSessionStreamStateLocked(s),
		previousReplacementMethod:    s.replacementPlayMethod,
	}
	if replacement.PositionSeconds != nil {
		rollback.previousPosition = s.Position
		rollback.previousPaused = s.IsPaused
		rollback.restoreProgress = true
	}

	s.MediaFileID = replacement.EffectiveMediaFileID
	applySessionStreamStateLocked(s, replacement.StreamState)
	if replacement.PositionSeconds != nil {
		s.Position = *replacement.PositionSeconds
		if !replacement.PreservePaused {
			s.IsPaused = replacement.IsPaused
		}
	}
	s.streamRevision++
	rollback.appliedRevision = s.streamRevision
	m.touchSessionLocked(s)
	return rollback, nil
}

// RollbackReplacement restores the state captured by ApplyReplacement when no
// newer session mutation has superseded it.
func (m *SessionManager) RollbackReplacement(sessionID string, rollback SessionReplacementRollback) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if rollback.sessionID != sessionID || rollback.appliedRevision == 0 || s.streamRevision != rollback.appliedRevision {
		return ErrSessionReplacementSuperseded
	}
	s.MediaFileID = rollback.previousEffectiveMediaFileID
	restoreSessionStreamStateLocked(s, rollback.previousStreamState)
	if rollback.restoreProgress {
		s.Position = rollback.previousPosition
		s.IsPaused = rollback.previousPaused
	}
	s.replacementPlayMethod = rollback.previousReplacementMethod
	s.streamRevision++
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
	s.streamRevision++
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
	s.streamRevision++
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
	return m.activeCountExcludingLocked(userID, "")
}

// activeCountExcludingLocked counts a user's limit-relevant sessions while
// ignoring one session entirely. Replacement admission uses this instead of
// subtracting one from the total: a failed session awaiting replan is often
// idle past the liveness grace and already absent from the count, so a blind
// decrement would free another session's slot.
func (m *SessionManager) activeCountExcludingLocked(userID int, excludeSessionID string) int {
	now := time.Now()
	count := 0
	for _, s := range m.sessions {
		if excludeSessionID != "" && s.ID == excludeSessionID {
			continue
		}
		if s.UserID == userID && m.countsTowardLimitsLocked(s, now) {
			count++
		}
	}
	return count
}

// transcodeCountLocked counts transcode sessions for a user. Caller must hold the lock.
func (m *SessionManager) transcodeCountLocked(userID int) int {
	return m.transcodeCountExcludingLocked(userID, "")
}

func (m *SessionManager) transcodeCountExcludingLocked(userID int, excludeSessionID string) int {
	now := time.Now()
	count := 0
	for _, s := range m.sessions {
		if excludeSessionID != "" && s.ID == excludeSessionID {
			continue
		}
		if s.UserID == userID && (s.PlayMethod == PlayTranscode || s.replacementPlayMethod == PlayTranscode) && m.countsTowardLimitsLocked(s, now) {
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
