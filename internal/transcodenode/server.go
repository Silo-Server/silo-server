package transcodenode

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// TranscodeStartRequest is the JSON body for POST /transcode/start.
type TranscodeStartRequest struct {
	SessionID              string  `json:"session_id"`
	PublicSessionID        string  `json:"public_session_id,omitempty"`
	SessionGeneration      string  `json:"session_generation,omitempty"`
	InputPath              string  `json:"input_path"`
	SourceVideoCodec       string  `json:"source_video_codec"`
	VideoBitstreamFilter   string  `json:"video_bitstream_filter,omitempty"`
	SeekSeconds            float64 `json:"seek_seconds"`
	StreamOriginSeconds    float64 `json:"stream_origin_seconds,omitempty"`
	CopySeekAnchorResolved bool    `json:"copy_seek_anchor_resolved,omitempty"`
	StartSegmentNumber     int     `json:"start_segment_number"`
	TargetResolution       string  `json:"target_resolution"`
	TargetCodecVideo       string  `json:"target_codec_video"`
	TargetCodecAudio       string  `json:"target_codec_audio"`
	TargetAudioChannels    int     `json:"target_audio_channels,omitempty"`
	TargetBitrateKbps      int     `json:"target_bitrate_kbps"`
	SegmentDuration        int     `json:"segment_duration"`
	HWAccel                string  `json:"hw_accel"`
	AudioTrackIndex        int     `json:"audio_track_index"`
	SubtitleTrackIndex     int     `json:"subtitle_track_index"`
	SubtitleBurnIn         bool    `json:"subtitle_burn_in"`
	SubtitleCodec          string  `json:"subtitle_codec,omitempty"`
	TotalDuration          float64 `json:"total_duration"`
	RequireReady           bool    `json:"require_ready,omitempty"`
}

// TranscodeStartResponse is the JSON response for POST /transcode/start.
type TranscodeStartResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	HWAccel   string `json:"hw_accel,omitempty"`
}

// HealthResponse is the JSON response for GET /api/v1/health.
type HealthResponse struct {
	Status     string `json:"status"`
	ActiveJobs int32  `json:"active_jobs"`
}

// sessionIdleTTL is how long a job may go without a manifest or segment
// request before the idle reaper closes it. Reaping is safe because a client
// that comes back later re-presents its still-valid stream token and the job
// reconstructs seeked to the requested segment; without the reaper, a job
// whose audience vanished (e.g. a v3 replan retired its transport id and a
// stale in-flight token resurrected the old one) encodes to end-of-file for
// nobody.
const sessionIdleTTL = 10 * time.Minute

// sessionReapInterval is how often the idle reaper sweeps for stale jobs.
const sessionReapInterval = time.Minute

// Server is the HTTP handler for transcode mode.
type Server struct {
	watcher    *nodeconfig.Watcher
	tracker    sessionTracker
	ffmpegSink playback.FFmpegLogSink
	// reloadMu linearizes force-reload teardown against every request that can
	// select or register a transcode process. Lock order is reloadMu, then the
	// per-transport lifecycle lock, then mu. Admission readers never upgrade;
	// force reload holds the write side without holding mu during slow cleanup.
	reloadMu        sync.RWMutex
	sessions        map[string]*playback.TranscodeSession
	identities      map[string]transcodeIdentity
	generationStore playback.SessionGenerationTombstoneStore
	// lastAccess records, per registered session id, when a manifest or segment
	// request last touched the job (registration counts as the first access).
	// Guarded by mu alongside sessions; the idle reaper closes jobs whose entry
	// is older than sessionIdleTTL.
	lastAccess map[string]time.Time
	reaperOnce sync.Once
	mu         sync.RWMutex
	activeJobs atomic.Int32

	// reconstructGroup single-flights node-side session reconstruction per session
	// id so a post-restart wave of concurrent manifest/segment requests for the same
	// lost session spawns exactly one ffmpeg, never racing duplicates into the shared
	// output directory.
	reconstructGroup singleflight.Group
	// reconstructSem bounds how many sessions may be reconstructed (ffmpeg
	// re-spawned) at once after a node restart, pacing the cold-start burst instead
	// of stampeding the host. Lazily sized to NumCPU on first use.
	reconstructSemOnce sync.Once
	reconstructSem     chan struct{}

	// lifecycleMu guards lifecycleLocks, the per-session mutexes that serialize
	// every path which spawns ffmpeg into a session's output dir (fresh start and
	// reconstruct). reconstructGroup only single-flights reconstructs against each
	// other; without this a reconstruct racing a fresh /transcode/start could run
	// two ffmpeg writers against the same dir.
	lifecycleMu    sync.Mutex
	lifecycleLocks map[string]*sessionLifecycleLock

	// recipeStore is the control-plane recipe store consulted when a forwarded
	// token carries no recipe (the jellycompat node hop). Nil disables that path.
	recipeStore recipeStore
}

// sessionTracker is the node-session publication surface used by this server.
// Keeping it narrow permits deterministic ordering tests around force reload.
type sessionTracker interface {
	NodeURL() string
	NodeName() string
	Track(context.Context, nodesessions.SessionInfo)
	Remove(context.Context, string)
	Cleanup(context.Context)
}

type transcodeIdentity struct {
	publicSessionID string
	generation      string
}

var errTranscodeIdentitySuperseded = errors.New("transcode identity superseded")

func transcodeIdentityFromClaims(claims *streamtoken.Claims, transportID string) (transcodeIdentity, error) {
	if claims == nil || claims.SessionID == "" {
		return transcodeIdentity{}, errors.New("token session identity is required")
	}
	if claims.SessionGeneration == "" {
		expected := claims.SessionID
		if claims.TranscodeTransportID != "" {
			expected = claims.TranscodeTransportID
		}
		if expected != transportID {
			return transcodeIdentity{}, errors.New("legacy token transport mismatch")
		}
		return transcodeIdentity{publicSessionID: claims.SessionID}, nil
	}
	generation, err := playback.CanonicalPublicSessionGeneration(claims.SessionGeneration)
	if err != nil {
		return transcodeIdentity{}, err
	}
	expected, err := playback.GenerationBoundTranscodeTransportID(claims.SessionID, generation)
	if err != nil {
		return transcodeIdentity{}, err
	}
	if claims.TranscodeTransportID != expected || transportID != expected {
		return transcodeIdentity{}, errors.New("generation-bound token transport mismatch")
	}
	return transcodeIdentity{publicSessionID: claims.SessionID, generation: generation}, nil
}

func validateTranscodeStartIdentity(req TranscodeStartRequest) (transcodeIdentity, error) {
	if req.PublicSessionID == "" && req.SessionGeneration == "" {
		return transcodeIdentity{publicSessionID: req.SessionID}, nil
	}
	if req.PublicSessionID == "" || req.SessionGeneration == "" {
		return transcodeIdentity{}, errors.New("public_session_id and session_generation must be supplied together")
	}
	generation, err := playback.CanonicalPublicSessionGeneration(req.SessionGeneration)
	if err != nil {
		return transcodeIdentity{}, err
	}
	expected, err := playback.GenerationBoundTranscodeTransportID(req.PublicSessionID, generation)
	if err != nil {
		return transcodeIdentity{}, err
	}
	if req.SessionID != expected {
		return transcodeIdentity{}, errors.New("session_id does not match public session identity")
	}
	return transcodeIdentity{publicSessionID: req.PublicSessionID, generation: generation}, nil
}

func writeGenerationAdmissionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, playback.ErrSessionGenerationTombstoneUnavailable):
		http.Error(w, "generation authority unavailable", http.StatusServiceUnavailable)
	case errors.Is(err, playback.ErrSessionGenerationEnded):
		http.Error(w, "session generation ended", http.StatusGone)
	default:
		http.Error(w, "invalid session identity", http.StatusBadRequest)
	}
}

func writeReconstructionAdmissionError(w http.ResponseWriter, err error) {
	if errors.Is(err, playback.ErrSessionGenerationEnded) || errors.Is(err, playback.ErrSessionGenerationTombstoneUnavailable) {
		writeGenerationAdmissionError(w, err)
		return
	}
	// A conflicting/superseded in-memory lifecycle is transient from the
	// forwarded client's perspective; retry rather than disguising it as a miss.
	http.Error(w, "session lifecycle changed", http.StatusServiceUnavailable)
}

// sessionLifecycleLock is a refcounted per-session mutex; the refcount lets the
// node drop the map entry once no path holds or waits on it so the map stays
// bounded over the node's lifetime.
type sessionLifecycleLock struct {
	mu   sync.Mutex
	refs int
}

// lockSessionLifecycle acquires the per-session lifecycle mutex and returns a
// release func. Held across "check existing → spawn → register" so a fresh start
// and a reconstruct never run concurrent ffmpeg writers for one session's dir.
func (s *Server) lockSessionLifecycle(sessionID string) func() {
	s.lifecycleMu.Lock()
	if s.lifecycleLocks == nil {
		s.lifecycleLocks = make(map[string]*sessionLifecycleLock)
	}
	lk := s.lifecycleLocks[sessionID]
	if lk == nil {
		lk = &sessionLifecycleLock{}
		s.lifecycleLocks[sessionID] = lk
	}
	lk.refs++
	s.lifecycleMu.Unlock()

	lk.mu.Lock()
	return func() {
		lk.mu.Unlock()
		s.lifecycleMu.Lock()
		lk.refs--
		if lk.refs == 0 {
			delete(s.lifecycleLocks, sessionID)
		}
		s.lifecycleMu.Unlock()
	}
}

// restartSessionLocked re-spawns session under the per-session lifecycle lock so
// a segment-recovery restart can never race a fresh start, reconstruct, or
// another restart into the same output directory. It holds the lock only across
// the cancel→respawn transition inside Restart and releases it before the caller
// waits on segments. Its serving caller already holds reloadMu for reading, in
// the documented reloadMu → lifecycle → mu order. Under the lock it confirms
// session is still the live mapped session; a concurrent teardown or reconstruct
// that replaced it yields ErrSessionSuperseded rather than re-spawning the stale
// handle.
func (s *Server) restartSessionLocked(ctx context.Context, sessionID string, session *playback.TranscodeSession, seekSeconds float64, startSegment int) error {
	unlock := s.lockSessionLifecycle(sessionID)
	defer unlock()
	s.mu.RLock()
	live, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok || live != session {
		return playback.ErrSessionSuperseded
	}
	return session.Restart(ctx, seekSeconds, startSegment)
}

// NewServer creates a new transcode server.
func NewServer(watcher *nodeconfig.Watcher, tracker *nodesessions.Tracker, generationStore playback.SessionGenerationTombstoneStore) *Server {
	s := &Server{
		watcher:         watcher,
		tracker:         tracker,
		sessions:        make(map[string]*playback.TranscodeSession),
		identities:      make(map[string]transcodeIdentity),
		generationStore: generationStore,
		lastAccess:      make(map[string]time.Time),
	}
	return s
}

// StartOrphanSweeper runs the age-guarded orphan-transcode sweep immediately and
// then hourly until ctx is cancelled. It never blocks (a slow network-filesystem
// delete runs in its own goroutine), so it is safe to call before the node binds
// its listener. This is the node's only filesystem-level reclaimer of dirs left
// behind by a session that was dropped without its output dir being removed — the
// idle reaper only deletes dirs it still tracks in s.sessions, so without this
// periodic pass such orphans would linger until the next process restart. The
// MaxTokenTTL age guard keeps a delete from racing a token-carried reconstruct
// writing into TranscodeDir/<sessionID>: a dir younger than the max token
// lifetime may still be reused, while older dirs are never reconstructable.
func (s *Server) StartOrphanSweeper(ctx context.Context) {
	dir := ""
	if cfg := s.watcher.Config(); cfg != nil {
		dir = cfg.Playback.TranscodeDir
	}
	playback.StartPeriodicOrphanCleanup(ctx, "transcodenode", dir, func() (int, error) {
		// Re-read config each run so a hot-reloaded TranscodeDir is honored.
		cfg := s.watcher.Config()
		if cfg == nil {
			return 0, nil
		}
		// Spare the live registered jobs by id, not by age alone: now that the
		// sweep runs during live traffic, a long-lived session that re-serves
		// already-written segments stops advancing its dir mtime, so the age
		// guard could misclassify it as orphaned. The live set is authoritative
		// (in-flight reconstructs are covered by their fresh writes + age guard).
		return playback.CleanupOrphanedTranscodeDirs(cfg.Playback.TranscodeDir, s.activeSessionIDs(), playback.MaxTokenTTL)
	}, playback.OrphanCleanupInterval)
}

// activeSessionIDs snapshots the ids of currently registered jobs so the orphan
// sweep spares their output dirs regardless of directory mtime, mirroring the
// central TranscodeManager's live-set snapshot.
func (s *Server) activeSessionIDs() map[string]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	active := make(map[string]struct{}, len(s.sessions))
	for id := range s.sessions {
		active[id] = struct{}{}
	}
	return active
}

func (s *Server) SetFFmpegLogSink(sink playback.FFmpegLogSink) {
	s.ffmpegSink = sink
}

// noteSessionAccessLocked records an access for a registered job. Callers must
// hold s.mu for writing. Lazily allocates so directly-constructed test servers
// work.
func (s *Server) noteSessionAccessLocked(sessionID string) {
	if s.lastAccess == nil {
		s.lastAccess = make(map[string]time.Time)
	}
	s.lastAccess[sessionID] = time.Now()
}

// touchSession refreshes a registered job's idle clock so the reaper spares
// it. Unknown ids are ignored — a reconstruct records its own first access
// when it registers the rebuilt job.
func (s *Server) touchSession(sessionID string) {
	s.mu.Lock()
	if _, ok := s.sessions[sessionID]; ok {
		s.noteSessionAccessLocked(sessionID)
	}
	s.mu.Unlock()
}

// acquireAuthorizedSessionTouched resolves an existing process together with
// the exact public lifecycle identity recorded when it was registered. The
// authority check happens before the process is touched or returned. If a
// concurrent lifecycle replacement changes either value, retry against the
// replacement rather than serving a process authorized under stale identity.
func (s *Server) acquireAuthorizedSessionTouched(ctx context.Context, sessionID string) (*playback.TranscodeSession, bool, error) {
	for {
		s.mu.RLock()
		session, ok := s.sessions[sessionID]
		identity, identityOK := s.identities[sessionID]
		s.mu.RUnlock()
		if !ok {
			return nil, false, nil
		}
		if !identityOK {
			// Processes registered before lifecycle identities were introduced
			// used the transport id as their public legacy session id.
			identity = transcodeIdentity{publicSessionID: sessionID}
		}
		if err := playback.AuthorizeSessionGeneration(ctx, s.generationStore, identity.publicSessionID, identity.generation, time.Now()); err != nil {
			return nil, true, err
		}

		s.mu.Lock()
		current, currentOK := s.sessions[sessionID]
		currentIdentity, currentIdentityOK := s.identities[sessionID]
		if !currentIdentityOK {
			currentIdentity = transcodeIdentity{publicSessionID: sessionID}
		}
		if currentOK && current == session && currentIdentity == identity {
			s.noteSessionAccessLocked(sessionID)
			s.mu.Unlock()
			return session, true, nil
		}
		s.mu.Unlock()
		if !currentOK {
			return nil, false, nil
		}
	}
}

// startIdleReaper launches the background sweep that closes jobs no client has
// touched for sessionIdleTTL. Called once when the node starts serving;
// subsequent calls are no-ops. The goroutine runs for the process lifetime,
// matching the node's own.
func (s *Server) startIdleReaper() {
	s.reaperOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(sessionReapInterval)
			defer ticker.Stop()
			for range ticker.C {
				s.reapIdleSessions(sessionIdleTTL)
			}
		}()
	})
}

// reapIdleSessions closes and unregisters every job whose last manifest or
// segment access is older than ttl. Registration counts as the first access,
// so a job still waiting on its manifest (the RequireReady flow) is never
// reaped mid-wait. Candidates are collected under the map lock, then each is
// re-validated and torn down under its per-session lifecycle lock: Close
// removes the output dir, and without that lock it could race a token
// reconstruct and wipe the segments a fresh ffmpeg is writing. The session's
// recipe is deliberately kept — an idle reap is not a client stop, and a
// still-valid token must be able to reconstruct on the next hit.
func (s *Server) reapIdleSessions(ttl time.Duration) {
	cutoff := time.Now().Add(-ttl)
	type idleJob struct {
		id      string
		session *playback.TranscodeSession
	}
	var candidates []idleJob
	s.mu.Lock()
	for id, session := range s.sessions {
		last, ok := s.lastAccess[id]
		if !ok {
			// Untracked registration (shouldn't happen): start its idle clock
			// now rather than closing a job that may be actively serving.
			s.noteSessionAccessLocked(id)
			continue
		}
		if last.Before(cutoff) {
			candidates = append(candidates, idleJob{id: id, session: session})
		}
	}
	s.mu.Unlock()

	for _, c := range candidates {
		s.reapSession(c.id, c.session, cutoff)
	}
}

// reapSession tears down one idle job under the per-session lifecycle lock so
// its Close can never overlap a start or reconstruct spawning a new ffmpeg
// into the same output dir. Ownership and idleness are re-checked under the
// lock; a job touched or replaced since the sweep scan is spared.
func (s *Server) reapSession(sessionID string, session *playback.TranscodeSession, cutoff time.Time) {
	s.reloadMu.RLock()
	defer s.reloadMu.RUnlock()
	unlock := s.lockSessionLifecycle(sessionID)
	defer unlock()
	s.mu.Lock()
	live, ok := s.sessions[sessionID]
	if !ok || live != session {
		s.mu.Unlock()
		return
	}
	last, tracked := s.lastAccess[sessionID]
	if tracked && !last.Before(cutoff) {
		s.mu.Unlock()
		return
	}
	delete(s.sessions, sessionID)
	delete(s.identities, sessionID)
	delete(s.lastAccess, sessionID)
	s.mu.Unlock()

	s.activeJobs.Add(-1)
	if err := session.Close(); err != nil {
		slog.Error("close idle transcode session", "component", "transcodenode", "error", err, "session", sessionID, "playback_session_id", sessionID)
	}
	if s.tracker != nil {
		s.tracker.Remove(context.Background(), sessionID)
	}
	slog.Info("transcode node reaped idle session", "component", "transcodenode",
		"session", sessionID, "playback_session_id", sessionID, "idle_ms", time.Since(last).Milliseconds())
}

// recipeStore reads a remote transcode's reconstruction recipe written by central
// at transcode start. The jellycompat node-hop token is identity-only by design —
// not because a Jellyfin client can't round-trip it, but because the recipe is
// mutated in place and the client can't be driven to refresh a stale token, so the
// authoritative recipe lives server-side (see internal/noderecipe). On a node
// restart the node fetches it here instead of 404ing. *noderecipe.Store implements it.
type recipeStore interface {
	Get(ctx context.Context, sessionID string) (*playback.RecipeCard, bool)
	// Delete drops a session's recipe so a buffered/retrying request after a node
	// restart cannot reconstruct a brand-new ffmpeg for an already-stopped session.
	// Called only on deliberate teardown; nil-safe and a missing key is a no-op.
	Delete(ctx context.Context, sessionID string) error
}

// SetRecipeStore wires the control-plane recipe store so this node can rebuild a
// jellycompat transcode after its own restart. Optional; without it a recipe-less
// (jellycompat) token cannot reconstruct and the request 404s as before.
func (s *Server) SetRecipeStore(store recipeStore) {
	s.recipeStore = store
}

// Handler returns the chi.Router with all transcode routes.
func (s *Server) Handler() http.Handler {
	s.startIdleReaper()
	r := chi.NewRouter()
	r.Get("/api/v1/health", s.handleHealth)

	r.Group(func(r chi.Router) {
		r.Use(s.requireBearer)
		r.Get("/hw-capabilities", s.handleHWCapabilities)
		r.Post("/chapter-thumbnails/extract", s.handleChapterThumbnailExtract)
		r.Post("/transcode/start", s.handleStart)
		r.Delete("/transcode/{session_id}", s.handleStop)
		r.Get("/transcode/{session_id}/master.m3u8", s.handleManifest)
		r.Get("/transcode/{session_id}/segment/{name}", s.handleSegment)
		r.Post("/admin/force-reload", s.handleForceReload)
		r.Get("/status", s.handleStatus)
	})
	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HealthResponse{
		Status:     "ok",
		ActiveJobs: s.activeJobs.Load(),
	})
}

func (s *Server) handleHWCapabilities(w http.ResponseWriter, r *http.Request) {
	ffmpegPath := ""
	if cfg := s.watcher.Config(); cfg != nil {
		ffmpegPath = cfg.Playback.FFmpegPath
	}
	info := playback.DetectHWAccelWithFFmpeg(ffmpegPath)
	info.Transformations = playback.ProbeTransformationRegistryV3(r.Context(), ffmpegPath).Advertised()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (s *Server) handleChapterThumbnailExtract(w http.ResponseWriter, r *http.Request) {
	var req chapterthumbs.RemoteExtractRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeChapterThumbnailError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if strings.TrimSpace(req.InputPath) == "" {
		writeChapterThumbnailError(w, http.StatusBadRequest, "invalid_request", "input_path is required")
		return
	}

	cfg := s.watcher.Config()
	frame, reason, err := chapterthumbs.ExtractFrame(r.Context(), chapterthumbs.FrameExtractOptions{
		InputPath:   req.InputPath,
		SeekSeconds: req.SeekSeconds,
		FFmpegPath:  cfg.Playback.FFmpegPath,
		HWAccel:     cfg.Playback.HWAccel,
		HWDevice:    cfg.Playback.HWDevice,
		ToneMap:     req.ToneMap,
	})
	if err != nil {
		writeChapterThumbnailError(w, http.StatusUnprocessableEntity, reason, err.Error())
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(frame)
}

func writeChapterThumbnailError(w http.ResponseWriter, status int, reason string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(chapterthumbs.RemoteExtractErrorResponse{
		Reason: reason,
		Error:  message,
	})
}

// requireBearer is middleware that checks for Authorization: Bearer {secret}.
func (s *Server) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.watcher.Config()
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != cfg.Auth.JWTSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	var req TranscodeStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.SessionID == "" || req.InputPath == "" {
		http.Error(w, "session_id and input_path are required", http.StatusBadRequest)
		return
	}
	identity, err := validateTranscodeStartIdentity(req)
	if err != nil {
		writeGenerationAdmissionError(w, err)
		return
	}

	// Keep force reload outside the complete check/spawn/register transaction.
	// This is acquired before the per-transport lifecycle lock by contract.
	s.reloadMu.RLock()
	defer s.reloadMu.RUnlock()

	cfg := s.watcher.Config()
	outputDir := filepath.Join(cfg.Playback.TranscodeDir, req.SessionID)

	opts := playback.TranscodeOpts{
		InputPath:              req.InputPath,
		OutputDir:              outputDir,
		SessionID:              req.SessionID,
		SourceVideoCodec:       req.SourceVideoCodec,
		VideoBitstreamFilter:   req.VideoBitstreamFilter,
		SeekSeconds:            req.SeekSeconds,
		StreamOriginSeconds:    req.StreamOriginSeconds,
		CopySeekAnchorResolved: req.CopySeekAnchorResolved,
		StartSegmentNumber:     req.StartSegmentNumber,
		TargetResolution:       req.TargetResolution,
		TargetCodecVideo:       req.TargetCodecVideo,
		TargetCodecAudio:       req.TargetCodecAudio,
		TargetAudioChannels:    req.TargetAudioChannels,
		TargetBitrateKbps:      req.TargetBitrateKbps,
		SegmentDuration:        req.SegmentDuration,
		FFmpegPath:             cfg.Playback.FFmpegPath,
		HWAccel:                req.HWAccel,
		HWDevice:               "",
		AudioTrackIndex:        req.AudioTrackIndex,
		SubtitleTrackIndex:     req.SubtitleTrackIndex,
		SubtitleBurnIn:         req.SubtitleBurnIn,
		SubtitleCodec:          req.SubtitleCodec,
		TotalDuration:          req.TotalDuration,
		FastStart:              true,
		NodeType:               "transcode",
		ExecutionMode:          "transcode_node",
		FFmpegLogSink:          s.ffmpegSink,
	}

	if opts.HWAccel == "" && cfg.Playback.HWAccel != "" {
		opts.HWAccel = cfg.Playback.HWAccel
	}

	// Hold the per-session lifecycle lock across teardown → spawn → register so a
	// concurrent reconstruct cannot run a second ffmpeg writer against this
	// session's output dir while we replace it.
	unlock := s.lockSessionLifecycle(req.SessionID)
	defer unlock()
	if err := playback.AuthorizeSessionGeneration(r.Context(), s.generationStore, identity.publicSessionID, identity.generation, time.Now()); err != nil {
		writeGenerationAdmissionError(w, err)
		return
	}

	s.mu.RLock()
	_, exists := s.sessions[req.SessionID]
	existingIdentity := s.identities[req.SessionID]
	s.mu.RUnlock()
	if exists && existingIdentity != identity {
		http.Error(w, "transport identity conflict", http.StatusConflict)
		return
	}

	// Defensively close any existing session for this ID so that a quality
	// switch doesn't orphan the old ffmpeg process or leave stale segments.
	s.mu.Lock()
	if old, ok := s.sessions[req.SessionID]; ok {
		delete(s.sessions, req.SessionID)
		delete(s.identities, req.SessionID)
		delete(s.lastAccess, req.SessionID)
		s.mu.Unlock()
		s.activeJobs.Add(-1)
		// Retire the old publication while the reload admission lease and
		// per-transport lifecycle lock still serialize this exact replacement.
		// s.mu is released before tracker I/O. A failed successor therefore
		// leaves no refreshable ghost; a successful one publishes once below.
		if s.tracker != nil {
			s.tracker.Remove(r.Context(), req.SessionID)
		}
		_ = old.Close()
		// Move the old segment directory aside and delete it in the
		// background: removing a long session's segments can take seconds
		// on slow disks, and the playback start that triggered this switch
		// is blocked waiting for our 202.
		staleDir := outputDir + ".stale-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := os.Rename(outputDir, staleDir); err == nil {
			go func() { _ = os.RemoveAll(staleDir) }()
		} else {
			os.RemoveAll(outputDir)
		}
	} else {
		s.mu.Unlock()
	}

	session, err := playback.StartTranscode(context.WithoutCancel(r.Context()), opts)
	if err != nil {
		slog.ErrorContext(r.Context(), "start transcode", "component", "transcodenode", "error", err, "session", req.SessionID, "playback_session_id", req.SessionID)
		http.Error(w, "failed to start transcode", http.StatusInternalServerError)
		return
	}
	if req.RequireReady {
		if _, err := session.WaitForManifest(8 * time.Second); err != nil {
			_ = session.Close()
			slog.ErrorContext(r.Context(), "transcode failed readiness check", "component", "transcodenode", "error", err, "session", req.SessionID, "playback_session_id", req.SessionID)
			http.Error(w, "transcode did not become ready", http.StatusInternalServerError)
			return
		}
	}

	s.mu.Lock()
	s.sessions[req.SessionID] = session
	s.identities[req.SessionID] = identity
	s.noteSessionAccessLocked(req.SessionID)
	s.mu.Unlock()
	s.activeJobs.Add(1)

	// Complete tracker publication before releasing the reload admission lease.
	// s.mu is intentionally not held during tracker I/O, and the request context
	// bounds cancellation. Force reload can therefore clean only after this
	// publication finishes, never before a detached publisher runs.
	effectiveHWAccel := session.Opts().HWAccel
	if s.tracker != nil {
		s.tracker.Track(r.Context(), nodesessions.SessionInfo{
			SessionID:  req.SessionID,
			NodeURL:    s.tracker.NodeURL(),
			NodeName:   s.tracker.NodeName(),
			Type:       "transcode",
			CodecVideo: req.TargetCodecVideo,
			CodecAudio: req.TargetCodecAudio,
			Resolution: req.TargetResolution,
			HWAccel:    effectiveHWAccel,
			StartedAt:  time.Now().UTC().Format(time.RFC3339),
		})
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(TranscodeStartResponse{
		SessionID: req.SessionID,
		Status:    "started",
		HWAccel:   effectiveHWAccel,
	})
}

// reconstructFromToken rebuilds a transcode session this node lost to its own
// restart. The proxy forwards the client's verified stream token in the
// X-Silo-Stream-Token header; the token carries the full byte-affecting recipe
// (the former Postgres "recipe card"), so the node can re-spawn ffmpeg seeked to
// the requested segment rather than 404ing — mirroring the integrated server's
// token-carried reconstruct. Returns nil when the request carries no usable
// transcode token, which the caller renders as a genuine not-found.
//
// requestedSegment is the segment the client is fetching, or negative on the
// manifest path. Reconstruction is single-flighted per session id so concurrent
// manifest and segment requests for the same lost session share one ffmpeg.
func (s *Server) reconstructFromToken(r *http.Request, sessionID string, requestedSegment int) *playback.TranscodeSession {
	s.reloadMu.RLock()
	defer s.reloadMu.RUnlock()
	session, _ := s.reconstructFromTokenAdmitted(r, sessionID, requestedSegment)
	return session
}

// reconstructFromTokenAdmitted performs reconstruction while its caller holds
// reloadMu for reading. Route handlers use this form because they retain the
// same admission lease through serving; the wrapper above covers direct users.
func (s *Server) reconstructFromTokenAdmitted(r *http.Request, sessionID string, requestedSegment int) (*playback.TranscodeSession, error) {
	tokenStr := r.Header.Get("X-Silo-Stream-Token")
	if tokenStr == "" {
		return nil, nil
	}
	cfg := s.watcher.Config()
	claims, err := streamtoken.Verify(tokenStr, cfg.Auth.JWTSecret)
	if err != nil {
		slog.WarnContext(r.Context(), "transcode node reconstruct: invalid stream token", "component", "transcodenode", "error", err,
			"session", sessionID, "playback_session_id", sessionID)
		return nil, nil
	}
	card := playback.RecipeCardFromClaims(claims)
	// The token's recipe must be a transcode card for the session id in the URL: a
	// mismatch is a forged or stale request, and direct/remux cards carry no encode
	// parameters to rebuild. An empty PlayMethod is a transcode card (back-compat).
	identity, err := transcodeIdentityFromClaims(claims, sessionID)
	if err != nil || (card.PlayMethod != "" && card.PlayMethod != playback.PlayTranscode) {
		return nil, nil
	}
	if err := playback.AuthorizeSessionGeneration(r.Context(), s.generationStore, identity.publicSessionID, identity.generation, time.Now()); err != nil {
		return nil, err
	}
	// A native token carries the full byte-affecting recipe. The jellycompat node
	// hop signs an identity-only token by design (see internal/noderecipe for why),
	// so its card decodes with no encode parameters. For the jellycompat case the
	// recipe is fetched from the control-plane recipe store below; without that
	// store there is nothing to rebuild from, so 404.
	tokenComplete := card.SegmentDuration > 0 && card.TargetCodecVideo != ""
	if !tokenComplete && s.recipeStore == nil {
		return nil, nil
	}

	v, err, _ := s.reconstructGroup.Do(sessionID, func() (interface{}, error) {
		// A concurrent reconstruct (or a fresh start) may already have registered the
		// session; serve it rather than spawning a duplicate ffmpeg.
		s.mu.RLock()
		existing, ok := s.sessions[sessionID]
		existingIdentity := s.identities[sessionID]
		s.mu.RUnlock()
		if ok {
			if existingIdentity != identity {
				return (*playback.TranscodeSession)(nil), errTranscodeIdentitySuperseded
			}
			return existing, nil
		}
		resolved := card
		if !tokenComplete {
			// Recipe-less (jellycompat) token: fetch the recipe central wrote to the
			// control-plane store at transcode start. A miss / incomplete recipe is a
			// genuine not-found (404), never a spawn from a bad recipe.
			fetched, ok := s.recipeStore.Get(r.Context(), identity.publicSessionID)
			if !ok || fetched == nil || fetched.SessionID != identity.publicSessionID ||
				fetched.SessionGeneration != identity.generation || fetched.TranscodeTransportID != sessionID ||
				fetched.SegmentDuration <= 0 || fetched.TargetCodecVideo == "" {
				return (*playback.TranscodeSession)(nil), nil
			}
			resolved = *fetched
		}
		return s.spawnReconstruct(r, sessionID, requestedSegment, resolved)
	})
	if err != nil {
		return nil, err
	}
	if session, _ := v.(*playback.TranscodeSession); session != nil {
		return session, nil
	}
	return nil, nil
}

// spawnReconstruct re-spawns ffmpeg for a lost session from its recipe card and
// registers it in the live map. It is only ever called inside the per-session
// single-flight in reconstructFromToken, so it is the sole writer racing to
// register sessionID. Spawn failures remain genuine misses; authority,
// cancellation, and supersession errors are preserved for route status mapping.
func (s *Server) spawnReconstruct(r *http.Request, sessionID string, requestedSegment int, card playback.RecipeCard) (*playback.TranscodeSession, error) {
	// Pace the cold-start burst so a node restart that loses many sessions does not
	// launch every ffmpeg at once. A client that disconnects while waiting releases
	// its slot rather than queueing dead work.
	release, ok := s.acquireReconstructSlot(r.Context())
	if !ok {
		return nil, r.Context().Err()
	}
	defer release()

	// Serialize against a concurrent fresh /transcode/start for this session so the
	// two never run ffmpeg writers against the same dir. Re-check under the lock and
	// yield to any live session rather than spawning a duplicate.
	unlock := s.lockSessionLifecycle(sessionID)
	defer unlock()
	s.mu.RLock()
	existing, ok := s.sessions[sessionID]
	existingIdentity := s.identities[sessionID]
	s.mu.RUnlock()
	cardClaims := card.ToClaims()
	if ok {
		identity, identityErr := transcodeIdentityFromClaims(&cardClaims, sessionID)
		if identityErr == nil && existingIdentity == identity {
			return existing, nil
		}
		return nil, errTranscodeIdentitySuperseded
	}
	identity, err := transcodeIdentityFromClaims(&cardClaims, sessionID)
	if err != nil {
		return nil, nil
	}
	if err := playback.AuthorizeSessionGeneration(r.Context(), s.generationStore, identity.publicSessionID, identity.generation, time.Now()); err != nil {
		return nil, err
	}

	cfg := s.watcher.Config()
	outputDir := filepath.Join(cfg.Playback.TranscodeDir, sessionID)
	opts := card.TranscodeOpts(outputDir, cfg.Playback.FFmpegPath, s.ffmpegSink)
	opts.SessionID = sessionID
	// Re-resolve environment-specific encode knobs from this node's live config; the
	// token deliberately omits HWAccel/HWDevice so an operator change applies on
	// rebuild. Run as a transcode node, not integrated (card.TranscodeOpts defaults).
	opts.HWAccel = cfg.Playback.HWAccel
	opts.HWDevice = cfg.Playback.HWDevice
	opts.NodeType = "transcode"
	opts.ExecutionMode = "transcode_node"

	// Resume near the segment the client is actually requesting. The card records
	// the original start; if the client has played past it, spawning at the old
	// position forces a wait-then-seek stall. A negative requestedSegment (manifest
	// path) carries no segment context, so the card position stands.
	//
	// The fast seg×dur mapping is only valid for ENCODED transcodes, whose forced
	// keyframes make every segment exactly SegmentDuration long. Copy-mode segments
	// have variable durations, so seg×dur points at the wrong source time and causes
	// multi-second A/V desync after a restart. For copy-mode cards leave the card's
	// original start untouched and let the segment-recovery machinery seek forward
	// once the manifest is rebuilt. This mirrors doReconstructTranscode in
	// internal/playback/transcode_manager.go so both reconstruct paths stay consistent.
	if requestedSegment > card.StartSegmentNumber && card.SegmentDuration > 0 &&
		!strings.EqualFold(card.TargetCodecVideo, "copy") {
		opts.StartSegmentNumber = requestedSegment
		opts.SeekSeconds = float64(requestedSegment * card.SegmentDuration)
	}

	session, err := playback.StartTranscode(context.WithoutCancel(r.Context()), opts)
	if err != nil {
		slog.ErrorContext(r.Context(), "transcode node reconstruct start failed", "component", "transcodenode", "error", err,
			"session", sessionID, "playback_session_id", sessionID)
		return nil, nil
	}

	// Yield to a winner registered by another path; close only the duplicate ffmpeg,
	// never the shared output directory the winner is actively serving.
	s.mu.Lock()
	if existing, ok := s.sessions[sessionID]; ok {
		existingIdentity := s.identities[sessionID]
		s.mu.Unlock()
		_ = session.CloseProcess()
		if existingIdentity == identity {
			return existing, nil
		}
		return nil, errTranscodeIdentitySuperseded
	}
	s.sessions[sessionID] = session
	s.identities[sessionID] = identity
	s.noteSessionAccessLocked(sessionID)
	s.mu.Unlock()
	s.activeJobs.Add(1)

	// As on fresh start, publish synchronously under the caller's existing reload
	// admission lease, with no session-map lock held and no child lock transfer.
	if s.tracker != nil {
		s.tracker.Track(r.Context(), nodesessions.SessionInfo{
			SessionID:   sessionID,
			NodeURL:     s.tracker.NodeURL(),
			NodeName:    s.tracker.NodeName(),
			Type:        "transcode",
			CodecVideo:  card.TargetCodecVideo,
			CodecAudio:  card.TargetCodecAudio,
			Resolution:  card.TargetResolution,
			HWAccel:     session.Opts().HWAccel,
			StartedAt:   time.Now().UTC().Format(time.RFC3339),
			AuthUserID:  card.UserID,
			ProfileID:   card.ProfileID,
			MediaFileID: card.MediaFileID,
		})
	}

	slog.InfoContext(r.Context(), "transcode node session reconstructed from token", "component", "transcodenode",
		"session", sessionID, "playback_session_id", sessionID,
		"requested_segment", requestedSegment, "start_segment_number", opts.StartSegmentNumber)
	return session, nil
}

// acquireReconstructSlot blocks until a reconstruct slot is free or the request
// context is canceled, returning a release func and true on success. The semaphore
// is lazily sized to NumCPU so a node restart paces its ffmpeg cold starts.
func (s *Server) acquireReconstructSlot(ctx context.Context) (func(), bool) {
	s.reconstructSemOnce.Do(func() {
		n := runtime.NumCPU()
		if n < 1 {
			n = 4
		}
		s.reconstructSem = make(chan struct{}, n)
	})
	select {
	case s.reconstructSem <- struct{}{}:
		return func() { <-s.reconstructSem }, true
	case <-ctx.Done():
		return nil, false
	}
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	s.reloadMu.RLock()
	defer s.reloadMu.RUnlock()

	// Serialize against in-flight starts and reconstructs. A RequireReady
	// start registers its job only after the readiness wait; a stop racing
	// that wait (the API rolling back a timed-out start) would otherwise miss
	// the map and 404, orphaning the just-spawned ffmpeg until the idle
	// reaper finds it. Blocking here until the start registers turns that
	// miss into a normal teardown.
	unlock := s.lockSessionLifecycle(sessionID)
	defer unlock()

	s.mu.Lock()
	session, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	identity := s.identities[sessionID]
	delete(s.sessions, sessionID)
	delete(s.identities, sessionID)
	delete(s.lastAccess, sessionID)
	s.mu.Unlock()
	s.activeJobs.Add(-1)

	if err := session.Close(); err != nil {
		slog.ErrorContext(r.Context(), "close transcode session", "component", "transcodenode", "error", err, "session", sessionID, "playback_session_id", sessionID)
	}

	cfg := s.watcher.Config()
	outputDir := filepath.Join(cfg.Playback.TranscodeDir, sessionID)
	os.RemoveAll(outputDir)

	// Drop the recipe so a buffered/retrying request after a node restart cannot
	// reconstruct a new ffmpeg for this now-stopped session. Best-effort: a stop
	// must still succeed even if the recipe store is briefly unavailable.
	if s.recipeStore != nil {
		recipeSessionID := identity.publicSessionID
		if recipeSessionID == "" {
			recipeSessionID = sessionID
		}
		if err := s.recipeStore.Delete(r.Context(), recipeSessionID); err != nil {
			slog.WarnContext(r.Context(), "delete transcode recipe on stop", "component", "transcodenode", "error", err, "session", sessionID, "playback_session_id", sessionID)
		}
	}

	s.tracker.Remove(r.Context(), sessionID)

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	s.reloadMu.RLock()
	defer s.reloadMu.RUnlock()

	sessionID := chi.URLParam(r, "session_id")

	if r.Header.Get("X-Silo-Stream-Token") != "" {
		session, reconstructErr := s.reconstructFromTokenAdmitted(r, sessionID, -1)
		if reconstructErr != nil {
			writeReconstructionAdmissionError(w, reconstructErr)
			return
		}
		if session == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		s.touchSession(sessionID)
		s.serveManifest(w, r, sessionID, session)
		return
	}

	// Lookup and liveness refresh happen atomically so the idle reaper can
	// never unregister the job between them and tear down a session this
	// request is about to serve from.
	session, ok, err := s.acquireAuthorizedSessionTouched(r.Context(), sessionID)
	if err != nil {
		writeGenerationAdmissionError(w, err)
		return
	}
	if !ok {
		// Lost the in-memory session (this node restarted): rebuild it from the
		// stream token the proxy forwarded. The manifest path carries no segment
		// context, so reconstruct at the recipe's original start position.
		session, err = s.reconstructFromTokenAdmitted(r, sessionID, -1)
		if err != nil {
			writeReconstructionAdmissionError(w, err)
			return
		}
		if session == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		// A reconstruct that yielded to a concurrently registered winner has
		// not recorded this hit; count it so the reaper sees the liveness.
		s.touchSession(sessionID)
	}

	s.serveManifest(w, r, sessionID, session)
}

func (s *Server) serveManifest(w http.ResponseWriter, r *http.Request, sessionID string, session *playback.TranscodeSession) {
	manifest, err := session.BuildPlaybackManifest("segment/", r.URL.RawQuery)
	if err != nil {
		slog.ErrorContext(r.Context(), "get manifest", "component", "transcodenode", "error", err, "session", sessionID, "playback_session_id", sessionID)
		http.Error(w, "manifest not ready", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Write(manifest)
}

func (s *Server) handleSegment(w http.ResponseWriter, r *http.Request) {
	s.reloadMu.RLock()
	defer s.reloadMu.RUnlock()

	sessionID := chi.URLParam(r, "session_id")
	name := chi.URLParam(r, "name")

	requestedSegment := -1
	if n, parseErr := playback.ParseSegmentNumber(name); parseErr == nil {
		requestedSegment = n
	}
	if r.Header.Get("X-Silo-Stream-Token") != "" {
		session, reconstructErr := s.reconstructFromTokenAdmitted(r, sessionID, requestedSegment)
		if reconstructErr != nil {
			writeReconstructionAdmissionError(w, reconstructErr)
			return
		}
		if session == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		s.touchSession(sessionID)
		s.serveSegment(w, r, sessionID, name, session)
		return
	}

	// Lookup and liveness refresh happen atomically so the idle reaper can
	// never unregister the job between them and tear down a session this
	// request is about to serve from.
	session, ok, err := s.acquireAuthorizedSessionTouched(r.Context(), sessionID)
	if err != nil {
		writeGenerationAdmissionError(w, err)
		return
	}
	if !ok {
		// Lost the in-memory session (this node restarted): rebuild it from the
		// forwarded stream token, seeked to the segment the client is requesting so
		// playback resumes near its position instead of restarting from the start.
		session, err = s.reconstructFromTokenAdmitted(r, sessionID, requestedSegment)
		if err != nil {
			writeReconstructionAdmissionError(w, err)
			return
		}
		if session == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		// A reconstruct that yielded to a concurrently registered winner has
		// not recorded this hit; count it so the reaper sees the liveness.
		s.touchSession(sessionID)
	}

	s.serveSegment(w, r, sessionID, name, session)
}

func (s *Server) serveSegment(w http.ResponseWriter, r *http.Request, sessionID, name string, session *playback.TranscodeSession) {
	segPath, err := session.GetSegment(name)
	if err != nil && err == playback.ErrSegmentNotFound {
		segNum, parseErr := playback.ParseSegmentNumber(name)
		if parseErr == nil {
			now := time.Now()
			decision := session.SegmentRecoveryDecision(segNum, now)
			lastProducedAgeMS := int64(-1)
			if !decision.Progress.LastProducedAt.IsZero() {
				lastProducedAgeMS = now.Sub(decision.Progress.LastProducedAt).Milliseconds()
			}
			slog.InfoContext(r.Context(), "transcode segment missing", "component", "transcodenode",
				"segment", name,
				"requested_segment", segNum,
				"produced_head", decision.Progress.ProducedHead,
				"last_requested_segment", decision.Progress.LastRequestedSegment,
				"start_segment_number", decision.Progress.StartSegmentNumber,
				"last_produced_age_ms", lastProducedAgeMS,
				"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
				"reason", decision.Reason,
				"session", sessionID,
				"playback_session_id", sessionID,
			)
			if decision.Wait {
				slog.InfoContext(r.Context(), "transcode segment wait", "component", "transcodenode",
					"segment", name,
					"requested_segment", segNum,
					"produced_head", decision.Progress.ProducedHead,
					"last_requested_segment", decision.Progress.LastRequestedSegment,
					"start_segment_number", decision.Progress.StartSegmentNumber,
					"last_produced_age_ms", lastProducedAgeMS,
					"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
					"reason", decision.Reason,
					"session", sessionID,
					"playback_session_id", sessionID,
				)
				segPath, err = session.WaitForSegment(name, decision.WaitTimeout)
				if err != nil && err == playback.ErrSegmentNotFound {
					slog.InfoContext(r.Context(), "transcode segment wait timeout", "component", "transcodenode",
						"segment", name,
						"requested_segment", segNum,
						"produced_head", decision.Progress.ProducedHead,
						"last_requested_segment", decision.Progress.LastRequestedSegment,
						"start_segment_number", decision.Progress.StartSegmentNumber,
						"last_produced_age_ms", lastProducedAgeMS,
						"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
						"reason", decision.Reason,
						"session", sessionID,
						"playback_session_id", sessionID,
					)
				}
			}

			if err != nil && err == playback.ErrSegmentNotFound && decision.RestartOnTimeout {
				seekSeconds, ok, seekErr := session.RestartSeekTarget(segNum)
				if seekErr != nil && !errors.Is(seekErr, playback.ErrManifestNotReady) {
					slog.ErrorContext(r.Context(), "resolve transcode node seek target", "component", "transcodenode", "error", seekErr, "segment", name, "session", sessionID, "playback_session_id", sessionID)
				}

				if ok {
					slog.InfoContext(r.Context(), "transcode node seek restart", "component", "transcodenode",
						"segment", name,
						"requested_segment", segNum,
						"produced_head", decision.Progress.ProducedHead,
						"last_requested_segment", decision.Progress.LastRequestedSegment,
						"start_segment_number", decision.Progress.StartSegmentNumber,
						"last_produced_age_ms", lastProducedAgeMS,
						"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
						"reason", decision.Reason,
						"seek_seconds", seekSeconds,
						"session", sessionID,
						"playback_session_id", sessionID,
					)

					if restartErr := s.restartSessionLocked(
						context.WithoutCancel(r.Context()),
						sessionID,
						session,
						seekSeconds,
						segNum,
					); restartErr == nil {
						segPath, err = session.WaitForSegment(name, 30*time.Second)
					}
				}
				if !ok && session.IsCopyVideo() {
					err = playback.ErrSegmentNotFound
				}
			}
		} else if session.IsRunning() {
			// Non-numbered segment (e.g., init.mp4 for fMP4 HLS).
			// Wait briefly — the init segment is written almost immediately.
			segPath, err = session.WaitForSegment(name, 10*time.Second)
		}
	}
	if err != nil {
		http.Error(w, "segment not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	http.ServeFile(w, r, segPath)
}

func (s *Server) handleForceReload(w http.ResponseWriter, r *http.Request) {
	if err := s.watcher.ForceReload(r.Context()); err != nil {
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	cfg := s.watcher.Config()
	s.closeSessionsForForceReload(r.Context(), cfg.Playback.TranscodeDir)

	slog.InfoContext(r.Context(), "transcode force reload completed", slog.String("component", "transcodenode"))
	w.WriteHeader(http.StatusNoContent)
}

// closeSessionsForForceReload deliberately tears down every process and its
// reconstruction recipe. Recipe keys are captured from the recorded public
// identities before those mappings are cleared; legacy jobs without a mapping
// retain the historical transport-id-as-public-id fallback.
func (s *Server) closeSessionsForForceReload(ctx context.Context, transcodeDir string) {
	type stoppedSession struct {
		transportID string
		session     *playback.TranscodeSession
	}

	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	s.mu.Lock()
	stopped := make([]stoppedSession, 0, len(s.sessions))
	recipeKeys := make(map[string]struct{}, len(s.sessions))
	for id, session := range s.sessions {
		recipeKey := id
		if identity, ok := s.identities[id]; ok && identity.publicSessionID != "" {
			recipeKey = identity.publicSessionID
		}
		recipeKeys[recipeKey] = struct{}{}
		stopped = append(stopped, stoppedSession{transportID: id, session: session})
	}
	s.sessions = make(map[string]*playback.TranscodeSession)
	s.identities = make(map[string]transcodeIdentity)
	s.lastAccess = make(map[string]time.Time)
	s.activeJobs.Store(0)
	s.mu.Unlock()

	for _, stopped := range stopped {
		if err := stopped.session.Close(); err != nil {
			slog.WarnContext(ctx, "close transcode on force reload", "component", "transcodenode", "error", err, "session", stopped.transportID, "playback_session_id", stopped.transportID)
		}
		_ = os.RemoveAll(filepath.Join(transcodeDir, stopped.transportID))
	}

	// A force-reload tears every session down for good, so drop their recipes too:
	// otherwise a buffered/retrying request could reconstruct a session this reload
	// deliberately killed. Best-effort, done outside the map lock.
	if s.recipeStore != nil {
		for recipeKey := range recipeKeys {
			if err := s.recipeStore.Delete(ctx, recipeKey); err != nil {
				slog.WarnContext(ctx, "delete transcode recipe on force reload", "component", "transcodenode", "error", err, "session", recipeKey, "playback_session_id", recipeKey)
			}
		}
	}
	if s.tracker != nil {
		s.tracker.Cleanup(ctx)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	sessionIDs := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		sessionIDs = append(sessionIDs, id)
	}
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	type statusResponse struct {
		Status     string   `json:"status"`
		ActiveJobs int32    `json:"active_jobs"`
		Sessions   []string `json:"sessions"`
	}
	json.NewEncoder(w).Encode(statusResponse{
		Status:     "ok",
		ActiveJobs: s.activeJobs.Load(),
		Sessions:   sessionIDs,
	})
}
