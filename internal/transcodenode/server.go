package transcodenode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodemetrics"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// TranscodeStartRequest is the JSON body for POST /transcode/start.
type TranscodeStartRequest struct {
	SessionID                  string                 `json:"session_id"`
	InputPath                  string                 `json:"input_path"`
	SourceVideoCodec           string                 `json:"source_video_codec"`
	SourceVideoProfile         string                 `json:"source_video_profile,omitempty"`
	SourceVideoBitDepth        int                    `json:"source_video_bit_depth,omitempty"`
	SourceAudioChannels        int                    `json:"source_audio_channels,omitempty"`
	AudioRecipeVersion         string                 `json:"audio_recipe_version,omitempty"`
	SoftwareVideoDecode        bool                   `json:"software_video_decode,omitempty"`
	ToneMapPolicy              tonemap.Policy         `json:"tone_map_policy,omitempty"`
	ToneMapMode                tonemap.Mode           `json:"tone_map_mode,omitempty"`
	ToneMapSourceKind          tonemap.SourceKind     `json:"tone_map_source_kind,omitempty"`
	ToneMapRecipeVersion       string                 `json:"tone_map_recipe_version,omitempty"`
	ToneMapPreflightRequired   bool                   `json:"tone_map_preflight_required,omitempty"`
	ToneMapSourceRevision      tonemap.SourceRevision `json:"tone_map_source_revision,omitzero"`
	ToneMapDVConfigPresent     bool                   `json:"tone_map_dv_config_present,omitempty"`
	ToneMapDVBLCompatIDPresent bool                   `json:"tone_map_dv_bl_compat_id_present,omitempty"`
	ToneMapDVBLPresent         bool                   `json:"tone_map_dv_bl_present,omitempty"`
	ToneMapDVRPUPresent        bool                   `json:"tone_map_dv_rpu_present,omitempty"`
	VideoBitstreamFilter       string                 `json:"video_bitstream_filter,omitempty"`
	VideoSampleEntry           string                 `json:"video_sample_entry,omitempty"`
	SeekSeconds                float64                `json:"seek_seconds"`
	StreamOriginSeconds        float64                `json:"stream_origin_seconds,omitempty"`
	CopySeekAnchorResolved     bool                   `json:"copy_seek_anchor_resolved,omitempty"`
	StartSegmentNumber         int                    `json:"start_segment_number"`
	TargetResolution           string                 `json:"target_resolution"`
	TargetCodecVideo           string                 `json:"target_codec_video"`
	TargetCodecAudio           string                 `json:"target_codec_audio"`
	TargetAudioChannels        int                    `json:"target_audio_channels,omitempty"`
	TargetAudioBitrateKbps     int                    `json:"target_audio_bitrate_kbps,omitempty"`
	TargetBitrateKbps          int                    `json:"target_bitrate_kbps"`
	SegmentDuration            int                    `json:"segment_duration"`
	HWAccel                    string                 `json:"hw_accel"`
	AudioTrackIndex            int                    `json:"audio_track_index"`
	SubtitleTrackIndex         int                    `json:"subtitle_track_index"`
	SubtitleBurnIn             bool                   `json:"subtitle_burn_in"`
	SubtitleCodec              string                 `json:"subtitle_codec,omitempty"`
	TotalDuration              float64                `json:"total_duration"`
	RequireReady               bool                   `json:"require_ready,omitempty"`
}

// TranscodeStartResponse is the JSON response for POST /transcode/start.
type TranscodeStartResponse struct {
	SessionID   string       `json:"session_id"`
	Status      string       `json:"status"`
	HWAccel     string       `json:"hw_accel,omitempty"`
	ToneMapMode tonemap.Mode `json:"tone_map_mode,omitempty"`
	// AudioRecipeVersion attests the exact byte-affecting audio recipe the node
	// understood. An old node omits it, allowing current callers to stop the job
	// before publishing bytes from a silently ignored SourceAudioChannels field.
	AudioRecipeVersion string `json:"audio_recipe_version,omitempty"`
}

var ErrAudioRecipeAttestationMismatch = errors.New("transcode node audio recipe attestation mismatch")

func validateAudioRecipeRequest(req TranscodeStartRequest) error {
	if req.SourceAudioChannels == 0 && req.AudioRecipeVersion == "" {
		return nil
	}
	if req.AudioRecipeVersion != playback.TransformationAudioToAACRecipeVersionV3 ||
		!playback.IsAudioToAACStereoDownmixV3(req.SourceAudioChannels, req.TargetCodecAudio, req.TargetAudioChannels) ||
		req.TargetAudioChannels != 2 {
		return fmt.Errorf("%w: source_channels=%d target_codec=%q target_channels=%d recipe_version=%q",
			ErrAudioRecipeAttestationMismatch, req.SourceAudioChannels, req.TargetCodecAudio, req.TargetAudioChannels, req.AudioRecipeVersion)
	}
	return nil
}

// ValidateAudioRecipeAttestation checks the start response only when the
// request carries the v2 stereo-downmix recipe. Ordinary legacy starts keep
// accepting the empty responses returned by older nodes.
func ValidateAudioRecipeAttestation(req TranscodeStartRequest, response TranscodeStartResponse) error {
	if err := validateAudioRecipeRequest(req); err != nil {
		return err
	}
	if req.AudioRecipeVersion != "" && response.AudioRecipeVersion != req.AudioRecipeVersion {
		return fmt.Errorf("%w: got %q, want %q", ErrAudioRecipeAttestationMismatch, response.AudioRecipeVersion, req.AudioRecipeVersion)
	}
	return nil
}

// HealthResponse is the JSON response for GET /api/v1/health.
type HealthResponse struct {
	Status     string `json:"status"`
	ActiveJobs int32  `json:"active_jobs"`
	// CapabilitiesHash identifies this node's last computed hardware capability
	// snapshot. It is read from the stored snapshot only — health must stay a
	// cheap liveness answer, so it never triggers a probe — and is empty until
	// the first background snapshot completes.
	CapabilitiesHash string `json:"capabilities_hash,omitempty"`
	// System and GPU are this node's last resource sample. Like the hash above
	// they are read from a snapshot the sampler already published, never
	// measured on the request: health is what the cluster routes on, so it must
	// answer at the same speed whether or not a mount is hung or a GPU query is
	// wedged. Both are omitted on a host that cannot be sampled.
	//
	// This route takes no credential, so the sample is path-free: disk entries
	// carry their role and their fill, never where they are mounted. See
	// nodemetrics.Snapshot.RedactPaths.
	System *nodemetrics.SystemStats `json:"system,omitempty"`
	GPU    []nodemetrics.GPUStats   `json:"gpu,omitempty"`
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

// Session tracking is monitoring-only and must never make a healthy node's
// control-plane response depend on Redis latency.
const sessionTrackingOperationTimeout = 2 * time.Second

// TranscodeStartReadinessTimeout is the node-side RequireReady manifest budget.
const TranscodeStartReadinessTimeout = 8 * time.Second

type sessionTracker interface {
	Track(context.Context, nodesessions.SessionInfo)
	Remove(context.Context, string)
	Cleanup(context.Context)
	NodeURL() string
	NodeName() string
}

// Server is the HTTP handler for transcode mode.
type Server struct {
	watcher      *nodeconfig.Watcher
	tracker      sessionTracker
	ffmpegSink   playback.FFmpegLogSink
	inputPaths   InputPathAuthorizer
	transcodeDir string
	artifactRoot string
	telemetry    *streamtelemetry.Registry
	sessions     map[string]*playback.TranscodeSession
	// lastAccess records, per registered session id, when a manifest or segment
	// request last touched the job (registration counts as the first access).
	// Guarded by mu alongside sessions; the idle reaper closes jobs whose entry
	// is older than sessionIdleTTL.
	lastAccess map[string]time.Time
	reaperOnce sync.Once
	mu         sync.RWMutex
	// reloadMu keeps force-reload teardown atomic with session creation and
	// reconstruction. It is always acquired before lifecycleMu or mu.
	reloadMu   sync.RWMutex
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
	// resolveToneMapRecipeFn is a package-private execution seam for error and
	// lifecycle tests. Production uses resolveToneMapRecipe.
	resolveToneMapRecipeFn func(context.Context, *playback.TranscodeOpts) error

	// lifecycleMu guards lifecycleLocks, the per-session locks that serialize
	// every path which spawns ffmpeg into a session's output dir (fresh start and
	// reconstruct). reconstructGroup only single-flights reconstructs against each
	// other; without this a reconstruct racing a fresh /transcode/start could run
	// two ffmpeg writers against the same dir. Artifact readers use the shared side
	// so concurrent relays remain independent while prepare/delete stay exclusive.
	lifecycleMu    sync.Mutex
	lifecycleLocks map[string]*sessionLifecycleLock

	// recipeStore is the control-plane recipe store consulted when a forwarded
	// token carries no recipe (the jellycompat node hop). Nil disables that path.
	recipeStore recipeStore

	// capabilityHash is the last computed capability snapshot's hash, published
	// by /health without probing. Nil until the first snapshot or capability
	// request completes.
	capabilityHash atomic.Pointer[string]

	// metrics samples host and GPU resources in the background. Nil until
	// StartMetricsSampler runs, which leaves health exactly as it was before.
	metrics *nodemetrics.Sampler

	// gpu keeps a capability re-probe's smoke encode and real transcodes off
	// the encoder at the same time; see gpuGate.
	gpu gpuGate

	// capabilityBuildAdmitted, when set, is called once a capability build has
	// claimed the GPU work slot and is about to wait for the build lock. It is
	// the seam a test uses to observe that ordering rather than sleeping on it;
	// production leaves it nil.
	capabilityBuildAdmitted func()

	// capabilityBuildMu serializes capability assemblies with each other.
	//
	// The gpuGate covers transcodes and downloads, not other capability
	// builders, and the probe caches no longer coalesce a re-probe with work
	// already in flight — bumping the invalidation generation is what makes the
	// re-probe honest. Those two together would otherwise let the scheduled
	// snapshot (or an authenticated /hw-capabilities request) run its smoke
	// matrix beside the operator's, which on session-limited hardware is
	// exactly the collision that publishes a false regression.
	capabilityBuildMu sync.Mutex
}

// storedCapabilityHash returns the last published capability hash, or empty
// when none has been computed yet.
func (s *Server) storedCapabilityHash() string {
	if hash := s.capabilityHash.Load(); hash != nil {
		return *hash
	}
	return ""
}

func (s *Server) storeCapabilityHash(hash string) {
	s.capabilityHash.Store(&hash)
}

func (s *Server) resolveToneMapRecipe(ctx context.Context, opts *playback.TranscodeOpts) error {
	if s.resolveToneMapRecipeFn != nil {
		return s.resolveToneMapRecipeFn(ctx, opts)
	}
	return resolveToneMapRecipe(ctx, opts)
}

// sessionLifecycleLock is a refcounted per-session lock; the refcount lets the
// node drop the map entry once no path holds or waits on it so the map stays
// bounded over the node's lifetime.
type sessionLifecycleLock struct {
	mu   sync.RWMutex
	refs int
}

func (s *Server) retainSessionLifecycleLock(sessionID string) *sessionLifecycleLock {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.lifecycleLocks == nil {
		s.lifecycleLocks = make(map[string]*sessionLifecycleLock)
	}
	lk := s.lifecycleLocks[sessionID]
	if lk == nil {
		lk = &sessionLifecycleLock{}
		s.lifecycleLocks[sessionID] = lk
	}
	lk.refs++
	return lk
}

func (s *Server) releaseSessionLifecycleLock(sessionID string, lk *sessionLifecycleLock) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	lk.refs--
	if lk.refs == 0 {
		delete(s.lifecycleLocks, sessionID)
	}
}

// lockSessionLifecycle acquires the per-session lifecycle mutex and returns a
// release func. Held across "check existing → spawn → register" and coordinated
// teardown so those paths never run concurrent ffmpeg writers in one session dir.
func (s *Server) lockSessionLifecycle(sessionID string) func() {
	lk := s.retainSessionLifecycleLock(sessionID)
	lk.mu.Lock()
	return func() {
		lk.mu.Unlock()
		s.releaseSessionLifecycleLock(sessionID, lk)
	}
}

// lockSessionLifecycleRead holds the shared side of a lifecycle lock. Artifact
// relays can therefore proceed concurrently, while preparation and deletion
// remain exclusive for the full transfer.
func (s *Server) lockSessionLifecycleRead(sessionID string) func() {
	lk := s.retainSessionLifecycleLock(sessionID)
	lk.mu.RLock()
	return func() {
		lk.mu.RUnlock()
		s.releaseSessionLifecycleLock(sessionID, lk)
	}
}

// restartSessionLocked re-spawns session under the per-session lifecycle lock so
// a segment-recovery restart can never race a fresh start, reconstruct, or
// another restart into the same output directory. It holds the lock only across
// the cancel→respawn transition inside Restart and releases it before the caller
// waits on segments. Under the lock it confirms session is still the live mapped
// session; a concurrent teardown or reconstruct that replaced it yields
// ErrSessionSuperseded rather than re-spawning the stale handle.
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
func NewServer(watcher *nodeconfig.Watcher, tracker *nodesessions.Tracker) *Server {
	var trackerImpl sessionTracker
	if tracker != nil {
		trackerImpl = tracker
	}
	transcodeDir := config.DefaultTranscodeDir
	artifactDir := ""
	if watcher != nil {
		if cfg := watcher.Config(); cfg != nil {
			if strings.TrimSpace(cfg.Playback.TranscodeDir) != "" {
				transcodeDir = cfg.Playback.TranscodeDir
			}
			artifactDir = cfg.Download.ArtifactDir
		}
	}
	artifactRoot := filepath.Join(transcodeDir, downloadprepare.ArtifactDirectoryName)
	if strings.TrimSpace(artifactDir) != "" {
		artifactRoot = config.EffectiveDownloadArtifactDir(artifactDir, transcodeDir)
	}
	s := &Server{
		watcher:      watcher,
		tracker:      trackerImpl,
		transcodeDir: transcodeDir,
		artifactRoot: artifactRoot,
		sessions:     make(map[string]*playback.TranscodeSession),
		lastAccess:   make(map[string]time.Time),
	}
	return s
}

// StartOrphanSweeper runs the age-guarded orphan-transcode sweep immediately and
// then hourly until ctx is canceled. It never blocks (a slow network-filesystem
// delete runs in its own goroutine), so it is safe to call before the node binds
// its listener. This is the node's only filesystem-level reclaimer of dirs left
// behind by a session that was dropped without its output dir being removed — the
// idle reaper only deletes dirs it still tracks in s.sessions, so without this
// periodic pass such orphans would linger until the next process restart. The
// MaxTokenTTL age guard keeps a delete from racing a token-carried reconstruct
// writing into TranscodeDir/<sessionID>: a dir younger than the max token
// lifetime may still be reused, while older dirs are never reconstructable.
func (s *Server) StartOrphanSweeper(ctx context.Context) {
	dir := s.transcodeDir
	playback.StartPeriodicOrphanCleanup(ctx, "transcodenode", dir, func() (int, error) {
		// Spare the live registered jobs by id, not by age alone: now that the
		// sweep runs during live traffic, a long-lived session that re-serves
		// already-written segments stops advancing its dir mtime, so the age
		// guard could misclassify it as orphaned. The live set is authoritative
		// (in-flight reconstructs are covered by their fresh writes + age guard).
		return playback.CleanupOrphanedTranscodeDirs(dir, s.activeSessionIDs(), playback.MaxTokenTTL)
	}, playback.OrphanCleanupInterval)
}

// StartHardwareEncoderWarmup primes the configured hardware encoder behind
// node startup. It is best effort and never delays the health listener. The
// returned channel closes once warmup has settled (including when there was
// nothing to warm), so work that wants a primed encoder — the first capability
// snapshot — can wait for it instead of racing it.
func (s *Server) StartHardwareEncoderWarmup(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if s == nil || s.watcher == nil || ctx == nil {
		close(done)
		return done
	}
	cfg := s.watcher.Config()
	if cfg == nil {
		close(done)
		return done
	}
	playbackCfg := cfg.Playback
	// Claimed here, before the goroutine exists. Warmup is a real smoke encode
	// and the listener opens while it may still be running, so an admin re-probe
	// arriving in a node's first seconds must not see an idle gate — and a claim
	// taken inside the goroutine leaves exactly that window, since the scheduler
	// makes no promise about when it runs. Held rather than requested: warmup is
	// already committed by the time it could be refused.
	s.gpu.holdWork()
	go func() {
		defer close(done)
		defer s.gpu.endWork()
		if err := playback.WarmHardwareEncoder(ctx, playbackCfg.FFmpegPath, playbackCfg.HWAccel, playbackCfg.HWDevice); err != nil {
			slog.DebugContext(ctx, "transcode node hardware encoder warmup failed", "component", "transcodenode", "error", err)
		}
	}()
	return done
}

// activeSessionIDs snapshots the ids of currently registered jobs so the orphan
// sweep spares their output dirs regardless of directory mtime, mirroring the
// central TranscodeManager's live-set snapshot.
func (s *Server) activeSessionIDs() map[string]struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	active := make(map[string]struct{}, len(s.sessions)+1)
	for id := range s.sessions {
		active[id] = struct{}{}
	}
	// Node-local prepared downloads may live under the existing persistent
	// transcode volume. They have their own lifecycle and must never be mistaken
	// for an orphaned HLS session directory. Protect the top-level container for
	// both the default and an explicitly nested artifact directory.
	if s.transcodeDir != "" && s.artifactRoot != "" {
		if rel, err := filepath.Rel(s.transcodeDir, s.artifactRoot); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if first, _, ok := strings.Cut(rel, string(filepath.Separator)); ok {
				active[first] = struct{}{}
			} else {
				active[rel] = struct{}{}
			}
		}
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

// acquireSessionTouched returns the registered job for sessionID and, when
// found, refreshes its idle clock in the same critical section. Doing both
// under one lock closes the gap where the idle reaper could unregister the
// job between a read-lock lookup and a separate touch, leaving the request
// serving a session whose teardown is already removing its output dir.
func (s *Server) acquireSessionTouched(sessionID string) (*playback.TranscodeSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if ok {
		s.noteSessionAccessLocked(sessionID)
	}
	return session, ok
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
	delete(s.lastAccess, sessionID)
	s.mu.Unlock()

	if err := s.closeSessionOffGPU(session); err != nil {
		slog.Error("close idle transcode session", "component", "transcodenode", "error", err, "session", sessionID, "playback_session_id", sessionID)
	}
	if s.tracker != nil {
		s.tracker.Remove(context.Background(), sessionID)
	}
	slog.Info("transcode node reaped idle session", "component", "transcodenode",
		"session", sessionID, "playback_session_id", sessionID, "idle_ms", time.Since(last).Milliseconds())
}

// closeSessionOffGPU retires one session: it drops the node's job count and
// closes the encoder, with the GPU gate holding the session as work for the
// whole teardown.
//
// The two counters have to overlap here, the same way the gate and activeJobs
// overlap when a session starts. Close waits for ffmpeg to exit, so the encoder
// keeps its GPU session for the length of the call, while activeJobs has to drop
// first for a stop to be reflected immediately. Without the hold, that live
// encoder is counted by nothing and a re-probe admitted in the gap smoke-encodes
// beside it.
func (s *Server) closeSessionOffGPU(session *playback.TranscodeSession) error {
	return s.retireGPUSession(session.Close)
}

// retireGPUSession drops the node's job count and runs one session's teardown
// with the gate holding it as work throughout. The hold discipline is the same
// whatever the teardown is, which is why it is separate from what it closes.
func (s *Server) retireGPUSession(close func() error) error {
	s.gpu.holdWork()
	defer s.gpu.endWork()
	s.activeJobs.Add(-1)
	return close()
}

// recipeStore reads a remote transcode's reconstruction recipe written by central
// at transcode start, keyed by the transport id this node serves the job under.
// It is the reconstruct source for every flow whose request cannot carry a
// complete recipe itself: the jellycompat node-hop token is identity-only by
// design — not because a Jellyfin client can't round-trip it, but because the
// recipe is mutated in place and the client can't be driven to refresh a stale
// token — and a header-authenticated (tokenless) attempt publishes no credential
// at all, so the relayed request carries nothing to rebuild from (see
// internal/noderecipe). On a node restart the node fetches the recipe here
// instead of 404ing. *noderecipe.Store implements it.
type recipeStore interface {
	Get(ctx context.Context, sessionID string) (*playback.RecipeCard, bool)
	// Delete drops a session's recipe so a buffered/retrying request after a node
	// restart cannot reconstruct a brand-new ffmpeg for an already-stopped session.
	// Called only on deliberate teardown; nil-safe and a missing key is a no-op.
	Delete(ctx context.Context, sessionID string) error
}

// SetRecipeStore wires the control-plane recipe store so this node can rebuild a
// jellycompat or header-authenticated transcode after its own restart. Optional;
// without it a request that carries no complete recipe of its own cannot
// reconstruct and 404s as before.
func (s *Server) SetRecipeStore(store recipeStore) {
	s.recipeStore = store
}

// SetInputPathAuthorizer wires the library-root authority used by every node
// endpoint that accepts an FFmpeg input path.
func (s *Server) SetInputPathAuthorizer(authorizer InputPathAuthorizer) {
	s.inputPaths = authorizer
}

// SetStreamTelemetry wires local stream observation. A nil registry is a
// complete no-op.
func (s *Server) SetStreamTelemetry(registry *streamtelemetry.Registry) {
	s.telemetry = registry
}

// Handler returns the chi.Router with all transcode routes.
func (s *Server) Handler() http.Handler {
	declareTranscodeNodeMediaRoutes()
	s.startIdleReaper()
	r := chi.NewRouter()
	r.Get("/api/v1/health", s.handleHealth)
	// Unauthenticated, matching the API listener's own /metrics posture: a
	// scrape target that needs a credential is a scrape target that goes
	// unmonitored, and the exposure is host resource counters, not media.
	r.Method(http.MethodGet, "/metrics", promhttp.Handler())

	r.Group(func(r chi.Router) {
		r.Use(s.requireBearer)
		r.Get("/hw-capabilities", s.handleHWCapabilities)
		r.Post("/chapter-thumbnails/extract", s.handleChapterThumbnailExtract)
		r.Post("/downloads/prepare", s.handleDownloadPrepare)
		r.Head("/downloads/artifacts/{artifact_id}", observeNode(s.telemetry, http.MethodHead, "/downloads/artifacts/{artifact_id}", s.handleDownloadArtifact))
		r.Get("/downloads/artifacts/{artifact_id}", observeNode(s.telemetry, http.MethodGet, "/downloads/artifacts/{artifact_id}", s.handleDownloadArtifact))
		r.Delete("/downloads/artifacts/{artifact_id}", s.handleDeleteDownloadArtifact)
		r.Post("/transcode/start", s.handleStart)
		r.Delete("/transcode/{session_id}", s.handleStop)
		r.Get("/transcode/{session_id}/master.m3u8", observeNode(s.telemetry, http.MethodGet, "/transcode/{session_id}/master.m3u8", s.handleManifest))
		r.Get("/transcode/{session_id}/segment/{name}", observeNode(s.telemetry, http.MethodGet, "/transcode/{session_id}/segment/{name}", s.handleSegment))
		r.Post("/admin/force-reload", s.handleForceReload)
		r.Post("/admin/reload-config", s.handleReloadConfig)
		r.Post("/admin/reprobe-capabilities", s.handleReprobeCapabilities)
		r.Get("/status", s.handleStatus)
	})
	return r
}

// handleDownloadPrepare validates and starts a prepared-download job.
func (s *Server) handleDownloadPrepare(w http.ResponseWriter, r *http.Request) {
	var req downloadprepare.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !downloadprepare.ValidArtifactID(req.ArtifactID) || strings.TrimSpace(req.InputPath) == "" {
		http.Error(w, "a valid artifact_id and input_path are required", http.StatusBadRequest)
		return
	}
	if req.AudioRecipeRequested() && !req.StereoDownmixBoostRequested() {
		http.Error(w, "invalid audio recipe", http.StatusBadRequest)
		return
	}

	cfg := s.watcher.Config()
	if cfg == nil {
		http.Error(w, "node not configured", http.StatusServiceUnavailable)
		return
	}
	if !s.requireApprovedInputPath(w, r, req.InputPath) {
		return
	}
	opts := req.TranscodeOpts(cfg.Playback.FFmpegPath, cfg.Playback.HWAccel, cfg.Playback.HWDevice, s.ffmpegSink)
	artifactRoot := s.artifactRoot
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		http.Error(w, "artifact directory unavailable", http.StatusInternalServerError)
		return
	}
	outputPath := filepath.Join(artifactRoot, req.ArtifactID+".mp4")
	// The unlocked check is only a hint that avoids making invalid recipes wait
	// behind a long-running encode. A candidate reuse is always rechecked under
	// the lifecycle read lock before its result is returned.
	if _, reusable := existingDownloadPrepareResult(outputPath, req); reusable {
		readUnlock := s.lockSessionLifecycleRead("download-artifact-" + req.ArtifactID)
		result, stillReusable := existingDownloadPrepareResult(outputPath, req)
		readUnlock()
		if stillReusable {
			writeDownloadPrepareResult(w, result)
			return
		}
	}
	// Claimed only once this request is committed to producing something: a
	// prepared download encodes on the GPU like any transcode, and the tone-map
	// recipe resolution just below runs ffmpeg probes on it too. Serving an
	// artifact that already exists touches neither, so a re-probe must not
	// refuse it.
	if !s.gpu.beginWork() {
		http.Error(w, "node is re-probing its hardware; retry shortly", http.StatusServiceUnavailable)
		return
	}
	defer s.gpu.endWork()
	if req.ToneMapRequested() {
		if err := s.resolveToneMapRecipe(r.Context(), &opts); err != nil {
			writeToneMapRecipeError(w, err)
			return
		}
	}
	unlock := s.lockSessionLifecycle("download-artifact-" + req.ArtifactID)
	defer unlock()
	if result, ok := existingDownloadPrepareResult(outputPath, req); ok {
		writeDownloadPrepareResult(w, result)
		return
	}
	if err := invalidateDownloadArtifactReceipt(outputPath); err != nil {
		http.Error(w, "failed to invalidate download artifact receipt", http.StatusInternalServerError)
		return
	}

	jobCtx := r.Context()
	s.activeJobs.Add(1)
	defer s.activeJobs.Add(-1)
	if s.tracker != nil {
		finishTracking := s.trackDownloadPrepare(jobCtx, nodesessions.SessionInfo{
			SessionID:  "download-" + req.ArtifactID,
			NodeURL:    s.tracker.NodeURL(),
			NodeName:   s.tracker.NodeName(),
			Type:       "download_prepare",
			CodecVideo: req.TargetCodecVideo,
			CodecAudio: req.TargetCodecAudio,
			Resolution: req.TargetResolution,
			StartedAt:  time.Now().UTC().Format(time.RFC3339),
		})
		defer finishTracking()
	}

	if err := playback.PrepareFile(jobCtx, opts, outputPath); err != nil {
		if jobCtx.Err() == nil {
			slog.ErrorContext(jobCtx, "prepare download artifact", "component", "transcodenode", "artifact_id", req.ArtifactID, "error", err)
		}
		if isToneMapRecipeError(err) {
			writeToneMapRecipeError(w, err)
		} else {
			http.Error(w, "failed to prepare download artifact", http.StatusInternalServerError)
		}
		return
	}
	stat, err := os.Stat(outputPath)
	if err != nil || !stat.Mode().IsRegular() {
		http.Error(w, "prepared download artifact unavailable", http.StatusInternalServerError)
		return
	}
	result, validResult := expectedDownloadPrepareResult(req, stat.Size())
	if !validResult {
		_ = os.Remove(outputPath)
		http.Error(w, "prepared download artifact has invalid attestation", http.StatusInternalServerError)
		return
	}
	if downloadPrepareReceiptRequested(req) {
		if err := writeDownloadArtifactReceipt(outputPath, result); err != nil {
			_ = os.Remove(outputPath)
			http.Error(w, "failed to publish download artifact receipt", http.StatusInternalServerError)
			return
		}
	}
	writeDownloadPrepareResult(w, result)
}

func expectedDownloadPrepareResult(req downloadprepare.Request, fileSize int64) (downloadprepare.Result, bool) {
	result := downloadprepare.Result{ArtifactID: req.ArtifactID, FileSize: fileSize}
	if !downloadPrepareReceiptRequested(req) {
		return result, true
	}
	if req.ToneMapRequested() {
		if !req.ValidToneMapAttestation() {
			return downloadprepare.Result{}, false
		}
		result.ToneMapRecipeVersion = req.ToneMapRecipeVersion
		result.ToneMapMode = req.ToneMapMode
		result.ToneMapSourceRevisionFingerprint = req.ToneMapSourceRevision.Fingerprint()
	}
	if req.AudioRecipeRequested() && !req.StereoDownmixBoostRequested() {
		return downloadprepare.Result{}, false
	}
	result.ExecutionFingerprint = req.ExecutionFingerprint()
	return result, result.ExecutionFingerprint != ""
}

func downloadPrepareReceiptRequested(req downloadprepare.Request) bool {
	return req.ExecutionAttestationRequested()
}

func existingDownloadPrepareResult(outputPath string, req downloadprepare.Request) (downloadprepare.Result, bool) {
	stat, err := os.Stat(outputPath)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() <= 0 {
		return downloadprepare.Result{}, false
	}
	want, valid := expectedDownloadPrepareResult(req, stat.Size())
	if !valid {
		return downloadprepare.Result{}, false
	}
	if !downloadPrepareReceiptRequested(req) {
		return want, true
	}
	receipt, err := readDownloadArtifactReceipt(outputPath)
	if err != nil || receipt != want {
		return downloadprepare.Result{}, false
	}
	return receipt, true
}

// toneMapRecipeRequested reports whether any transported field claims that the
// request carries a tone-map recipe, including partial recipes that must fail.
func toneMapRecipeRequested(opts playback.TranscodeOpts) bool {
	return downloadprepare.NewRequest("", opts).ToneMapRequested()
}

// resolveToneMapRecipe validates the frozen selection against this node's live
// executor and replaces environment-specific filter and backend fields.
func resolveToneMapRecipe(ctx context.Context, opts *playback.TranscodeOpts) error {
	if opts == nil {
		return errors.New("missing tone-map recipe")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	resolveCtx, cancel := context.WithTimeout(ctx, playback.CapabilityEndpointTimeout(opts.HWAccel, opts.HWDevice))
	defer cancel()
	if err := resolveCtx.Err(); err != nil {
		return err
	}
	resolved, err := playback.ResolveToneMapExecutor(resolveCtx, *opts)
	if contextErr := resolveCtx.Err(); contextErr != nil {
		return fmt.Errorf("%w: %w", playback.ErrToneMapExecutorUnavailable, contextErr)
	}
	if err != nil {
		return err
	}
	*opts = resolved
	return nil
}

func writeToneMapRecipeError(w http.ResponseWriter, err error) {
	if errors.Is(err, playback.ErrToneMapExecutorUnavailable) {
		w.Header().Set(ToneMapExecutionErrorHeader, ToneMapExecutorUnavailableCode)
		http.Error(w, "tone-map executor unavailable", http.StatusServiceUnavailable)
		return
	}
	if errors.Is(err, playback.ErrToneMapSourceValidationUnavailable) {
		w.Header().Set(ToneMapExecutionErrorHeader, ToneMapSourceValidationUnavailableCode)
		http.Error(w, "tone-map source validation unavailable", http.StatusServiceUnavailable)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		http.Error(w, "tone-map capability probe unavailable", http.StatusServiceUnavailable)
		return
	}
	if errors.Is(err, tonemap.ErrSourceRevisionChanged) {
		w.Header().Set(ToneMapExecutionErrorHeader, ToneMapSourceRevisionChangedCode)
	} else if errors.Is(err, tonemap.ErrSourcePreflightRejected) {
		w.Header().Set(ToneMapExecutionErrorHeader, ToneMapSourcePreflightRejectedCode)
	}
	http.Error(w, "unsupported or stale tone-map recipe", http.StatusUnprocessableEntity)
}

func isToneMapRecipeError(err error) bool {
	return errors.Is(err, playback.ErrToneMapExecutorUnavailable) ||
		errors.Is(err, playback.ErrToneMapSourceValidationUnavailable) ||
		errors.Is(err, tonemap.ErrSourceRevisionChanged) ||
		errors.Is(err, tonemap.ErrSourcePreflightRejected)
}

func writeDownloadPrepareResult(w http.ResponseWriter, result downloadprepare.Result) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Server) sessionOutputDir(sessionID string) string {
	return filepath.Join(s.transcodeDir, sessionID)
}

func (s *Server) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	artifactID := chi.URLParam(r, "artifact_id")
	if !downloadprepare.ValidArtifactID(artifactID) {
		http.NotFound(w, r)
		return
	}
	cfg := s.watcher.Config()
	if cfg == nil {
		http.Error(w, "node not configured", http.StatusServiceUnavailable)
		return
	}
	// Share the lock with other readers, but serialize with preparation and
	// deletion. A recovery HEAD must not report a definitive 404 while
	// PrepareFile is still publishing this artifact.
	unlock := s.lockSessionLifecycleRead("download-artifact-" + artifactID)
	defer unlock()
	path := filepath.Join(s.artifactRoot, artifactID+".mp4")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "artifact unavailable", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		http.Error(w, "artifact unavailable", http.StatusInternalServerError)
		return
	}
	streamtelemetry.Attach(r.Context(), streamtelemetry.Attachment{})
	w.Header().Set("Content-Disposition", `attachment; filename="`+artifactID+`.mp4"`)
	w.Header().Set("Content-Type", playback.MimeFromExtension(path))
	w.Header().Set("ETag", `"`+artifactID+`-`+strconv.FormatInt(stat.Size(), 10)+`"`)
	if receipt, err := readDownloadArtifactReceipt(path); err == nil && receipt.ArtifactID == artifactID && receipt.FileSize == stat.Size() {
		downloadprepare.SetResultHeaders(w.Header(), receipt)
	}
	http.ServeContent(w, r, stat.Name(), stat.ModTime(), f)
}

func (s *Server) handleDeleteDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	artifactID := chi.URLParam(r, "artifact_id")
	if !downloadprepare.ValidArtifactID(artifactID) {
		http.NotFound(w, r)
		return
	}
	cfg := s.watcher.Config()
	if cfg == nil {
		http.Error(w, "node not configured", http.StatusServiceUnavailable)
		return
	}
	unlock := s.lockSessionLifecycle("download-artifact-" + artifactID)
	defer unlock()
	path := filepath.Join(s.artifactRoot, artifactID+".mp4")
	if err := invalidateDownloadArtifactReceipt(path); err != nil {
		http.Error(w, "failed to remove artifact receipt", http.StatusInternalServerError)
		return
	}
	for _, candidate := range []string{path, path + ".part"} {
		if err := os.Remove(candidate); err != nil && !os.IsNotExist(err) {
			http.Error(w, "failed to remove artifact", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// trackDownloadPrepare runs the monitoring lifecycle off the request path.
// One goroutine owns both operations so Remove can never overtake Track, even
// when the encode completes before Redis responds. finish only signals that
// the job ended; it never waits for either bounded Redis operation.
func (s *Server) trackDownloadPrepare(ctx context.Context, info nodesessions.SessionInfo) func() {
	finished := make(chan struct{})
	var finishOnce sync.Once
	baseCtx := context.WithoutCancel(ctx)
	go func() {
		trackCtx, cancelTrack := context.WithTimeout(baseCtx, sessionTrackingOperationTimeout)
		s.tracker.Track(trackCtx, info)
		cancelTrack()

		<-finished
		removeCtx, cancelRemove := context.WithTimeout(baseCtx, sessionTrackingOperationTimeout)
		s.tracker.Remove(removeCtx, info.SessionID)
		cancelRemove()
	}()
	return func() {
		finishOnce.Do(func() { close(finished) })
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.metrics.Snapshot().RedactPaths()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HealthResponse{
		Status:           "ok",
		ActiveJobs:       s.activeJobs.Load(),
		CapabilitiesHash: s.storedCapabilityHash(),
		System:           snapshot.System,
		GPU:              snapshot.GPU,
	})
}

// StartMetricsSampler begins background resource sampling until ctx is
// canceled, and publishes the readings on /health, /status and /metrics.
//
// The scratch dir is the only mount a transcode node samples: it is the volume
// that silently kills transcodes when it fills, and it is the one path the node
// is guaranteed to be able to see. Media roots are the API host's to report,
// because path visibility varies per node and a node cannot tell "root I cannot
// see" from "root that does not exist".
func (s *Server) StartMetricsSampler(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	s.metrics = nodemetrics.NewSampler(nodemetrics.Options{
		// Fixed for this process: a node writes every session under the
		// directory it resolved at startup, so a later config edit does not move
		// the volume this one is filling.
		ScratchDir:       func() string { return s.transcodeDir },
		DeviceSessions:   playback.HWDeviceLoadSnapshot,
		DeviceIdentities: playback.SamplerDeviceIdentities,
	})
	s.metrics.Start(ctx)
}

// buildCapabilitySnapshot runs the node's full capability detection: hardware
// walk, tone-map probe, and transformation registry, hashed into one identity.
// It is the single assembly used by both the capability endpoint and the
// background snapshot, so the hash a health response advertises always
// describes the payload the endpoint would serve.
//
// The probes behind it are individually cached, so repeating this is cheap once
// the first pass has run.
// ErrCapabilityBuildBusy reports that a capability snapshot was not attempted
// because a re-probe holds the encoder. The previously published hash stands.
var ErrCapabilityBuildBusy = errors.New("capability build refused while the node is re-probing")

func (s *Server) buildCapabilitySnapshot(ctx context.Context) (playback.HWAccelInfo, error) {
	// A snapshot runs ffmpeg on the GPU whenever the probe caches are cold, so
	// it registers as GPU work. That is what stops a manual re-probe from
	// claiming an apparently idle encoder and running its own smoke matrix
	// beside this one.
	//
	// It deliberately does *not* refuse while transcodes are running. A node
	// under sustained load would then never refresh its inventory, and its
	// advertised hash would go stale indefinitely — a worse failure than the
	// cold-start contention this would avoid, which a positive probe result
	// caches away after the first success and which the next snapshot corrects.
	if !s.gpu.beginWork() {
		return playback.HWAccelInfo{}, ErrCapabilityBuildBusy
	}
	defer s.gpu.endWork()
	if s.capabilityBuildAdmitted != nil {
		s.capabilityBuildAdmitted()
	}
	s.capabilityBuildMu.Lock()
	defer s.capabilityBuildMu.Unlock()
	return s.buildCapabilitySnapshotLocked(ctx)
}

// buildCapabilitySnapshotLocked is buildCapabilitySnapshot's body. Callers must
// hold capabilityBuildMu; the re-probe takes it itself so its cache
// invalidation and its rebuild are one step no other builder can interleave
// with.
func (s *Server) buildCapabilitySnapshotLocked(ctx context.Context) (playback.HWAccelInfo, error) {
	ffmpegPath := ""
	configuredHWAccel := playback.HWAccelNone
	hwDevice := ""
	if cfg := s.watcher.Config(); cfg != nil {
		ffmpegPath = cfg.Playback.FFmpegPath
		configuredHWAccel = cfg.Playback.HWAccel
		hwDevice = cfg.Playback.HWDevice
	}
	resolveCtx, cancel := context.WithTimeout(ctx, toneMapCapabilityResolveTimeout(configuredHWAccel, hwDevice))
	defer cancel()
	// One detection walk answers both questions: Resolved honors the configured
	// backend's pass-through contract, and DetectedBackends explains it.
	//
	// A walk that ran out of budget is refused rather than published: it marks
	// unprobed backends Verified=false, which is byte-identical to a real
	// hardware failure, so hashing it would make the API persist a capability
	// regression for hardware that is fine.
	info, err := playback.DetectHWAccelWithFFmpegContextResult(resolveCtx, configuredHWAccel, ffmpegPath, hwDevice)
	if err != nil {
		return playback.HWAccelInfo{}, err
	}
	info.ProbeRequestTimeoutMillis = playback.CapabilityRequestTimeout(configuredHWAccel, hwDevice).Milliseconds()
	capabilities, err := tonemap.Probe(resolveCtx, playback.ResolveFFmpegPath(ffmpegPath), info.Resolved, hwDevice)
	if err != nil {
		return playback.HWAccelInfo{}, err
	}
	info.ToneMapCapabilities = capabilities
	registry, err := playback.ProbeTransformationRegistryWithToneMapV3Result(resolveCtx, ffmpegPath, info.ToneMapCapabilities)
	if err != nil {
		return playback.HWAccelInfo{}, err
	}
	info.Transformations = registry.Advertised()
	info.CapabilityHash = playback.ComputeCapabilityHash(info)
	return info, nil
}

// handleHWCapabilities reports live smoke-tested node capabilities.
func (s *Server) handleHWCapabilities(w http.ResponseWriter, r *http.Request) {
	info, err := s.buildCapabilitySnapshot(r.Context())
	if err != nil {
		http.Error(w, "capability probe unavailable", http.StatusServiceUnavailable)
		return
	}
	// A served report is as authoritative as a scheduled snapshot, so health
	// starts advertising this hash immediately rather than at the next tick.
	s.storeCapabilityHash(info.CapabilityHash)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// capabilitySnapshotInterval is how often the node recomputes its capability
// snapshot. The probes behind it are cached, so this mostly re-reads sysfs and
// re-hashes; it exists to notice hardware or ffmpeg changing underneath a
// long-running node without waiting for a restart.
const capabilitySnapshotInterval = 15 * time.Minute

// StartCapabilitySnapshots keeps the capability hash published by /health
// current, in the background, until ctx is canceled. ready gates the first
// snapshot so it observes a primed encoder rather than racing warmup; a nil
// channel means snapshot immediately.
func (s *Server) StartCapabilitySnapshots(ctx context.Context, ready <-chan struct{}) {
	if s == nil || ctx == nil {
		return
	}
	go func() {
		if ready != nil {
			select {
			case <-ctx.Done():
				return
			case <-ready:
			}
		}
		s.refreshCapabilitySnapshot(ctx)
		ticker := time.NewTicker(capabilitySnapshotInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refreshCapabilitySnapshot(ctx)
			}
		}
	}()
}

func (s *Server) refreshCapabilitySnapshot(ctx context.Context) {
	info, err := s.buildCapabilitySnapshot(ctx)
	if err != nil {
		// Keep the previous hash: a failed probe is not evidence the hardware
		// changed, and clearing it would look like a downgrade to the API.
		slog.DebugContext(ctx, "transcode node capability snapshot failed", "component", "transcodenode", "error", err)
		return
	}
	if previous := s.storedCapabilityHash(); previous != "" && previous != info.CapabilityHash {
		slog.InfoContext(ctx, "transcode node capabilities changed", "component", "transcodenode",
			"previous_hash", previous, "hash", info.CapabilityHash, "resolved", info.Resolved)
	}
	s.storeCapabilityHash(info.CapabilityHash)
}

func toneMapCapabilityResolveTimeout(hardwareBackend, hardwareDevice string) time.Duration {
	return playback.CapabilityEndpointTimeout(hardwareBackend, hardwareDevice)
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
	if !s.requireApprovedInputPath(w, r, req.InputPath) {
		return
	}

	cfg := s.watcher.Config()
	if cfg == nil {
		writeChapterThumbnailError(w, http.StatusServiceUnavailable, "node_unavailable", "node not configured")
		return
	}
	// A QSV or VAAPI extraction reserves a render device and runs ffmpeg on it,
	// so it takes the same exclusion a transcode does. Without this the route
	// leaves the gate looking idle — it never touches activeJobs either — and a
	// re-probe could smoke-encode beside a live extraction.
	if !s.gpu.beginWork() {
		writeChapterThumbnailError(w, http.StatusServiceUnavailable, "node_unavailable",
			"node is re-probing its hardware; retry shortly")
		return
	}
	defer s.gpu.endWork()
	frame, reason, err := chapterthumbs.ExtractFrame(r.Context(), chapterthumbs.FrameExtractOptions{
		InputPath:            req.InputPath,
		SeekSeconds:          req.SeekSeconds,
		FFmpegPath:           cfg.Playback.FFmpegPath,
		HWAccel:              cfg.Playback.HWAccel,
		HWDevice:             cfg.Playback.HWDevice,
		ToneMap:              req.ToneMap,
		AllowSoftwareToneMap: req.AllowSoftwareToneMap,
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
		if cfg == nil {
			http.Error(w, "node not configured", http.StatusServiceUnavailable)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != cfg.Auth.JWTSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleStart validates and starts a remote HLS transcode.
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
	if err := validateAudioRecipeRequest(req); err != nil {
		http.Error(w, "invalid audio recipe", http.StatusBadRequest)
		return
	}
	if !s.requireApprovedInputPath(w, r, req.InputPath) {
		return
	}
	cfg := s.watcher.Config()
	if cfg == nil {
		http.Error(w, "node not configured", http.StatusServiceUnavailable)
		return
	}
	// Held from here until the handler returns, by which point activeJobs
	// covers the session. Without the overlap a re-probe could start in the gap
	// between admitting this transcode and it becoming visible as an active
	// job, and its smoke encode would then race a live encoder session.
	if !s.gpu.beginWork() {
		http.Error(w, "node is re-probing its hardware; retry shortly", http.StatusServiceUnavailable)
		return
	}
	defer s.gpu.endWork()
	outputDir := s.sessionOutputDir(req.SessionID)

	opts := playback.TranscodeOpts{
		InputPath:                  req.InputPath,
		OutputDir:                  outputDir,
		SessionID:                  req.SessionID,
		SourceVideoCodec:           req.SourceVideoCodec,
		SourceVideoProfile:         req.SourceVideoProfile,
		SourceVideoBitDepth:        req.SourceVideoBitDepth,
		SourceAudioChannels:        req.SourceAudioChannels,
		SoftwareVideoDecode:        req.SoftwareVideoDecode,
		ToneMapPolicy:              req.ToneMapPolicy,
		ToneMapMode:                req.ToneMapMode,
		ToneMapSourceKind:          req.ToneMapSourceKind,
		ToneMapRecipeVersion:       req.ToneMapRecipeVersion,
		ToneMapPreflightRequired:   req.ToneMapPreflightRequired,
		ToneMapSourceRevision:      req.ToneMapSourceRevision,
		ToneMapDVConfigPresent:     req.ToneMapDVConfigPresent,
		ToneMapDVBLCompatIDPresent: req.ToneMapDVBLCompatIDPresent,
		ToneMapDVBLPresent:         req.ToneMapDVBLPresent,
		ToneMapDVRPUPresent:        req.ToneMapDVRPUPresent,
		VideoBitstreamFilter:       req.VideoBitstreamFilter,
		VideoSampleEntry:           req.VideoSampleEntry,
		SeekSeconds:                req.SeekSeconds,
		StreamOriginSeconds:        req.StreamOriginSeconds,
		CopySeekAnchorResolved:     req.CopySeekAnchorResolved,
		StartSegmentNumber:         req.StartSegmentNumber,
		TargetResolution:           req.TargetResolution,
		TargetCodecVideo:           req.TargetCodecVideo,
		TargetCodecAudio:           req.TargetCodecAudio,
		TargetAudioChannels:        req.TargetAudioChannels,
		TargetAudioBitrateKbps:     req.TargetAudioBitrateKbps,
		TargetBitrateKbps:          req.TargetBitrateKbps,
		SegmentDuration:            req.SegmentDuration,
		FFmpegPath:                 cfg.Playback.FFmpegPath,
		HWAccel:                    req.HWAccel,
		// This node's configured device (or device list — StartTranscode
		// resolves it to one GPU), matching what reconstruction uses so fresh
		// and reconstructed sessions balance identically.
		HWDevice:           cfg.Playback.HWDevice,
		AudioTrackIndex:    req.AudioTrackIndex,
		SubtitleTrackIndex: req.SubtitleTrackIndex,
		SubtitleBurnIn:     req.SubtitleBurnIn,
		SubtitleCodec:      req.SubtitleCodec,
		TotalDuration:      req.TotalDuration,
		FastStart:          true,
		NodeType:           "transcode",
		ExecutionMode:      "transcode_node",
		FFmpegLogSink:      s.ffmpegSink,
	}

	if opts.HWAccel == "" && cfg.Playback.HWAccel != "" {
		opts.HWAccel = cfg.Playback.HWAccel
	}
	if toneMapRecipeRequested(opts) {
		if err := s.resolveToneMapRecipe(r.Context(), &opts); err != nil {
			writeToneMapRecipeError(w, err)
			return
		}
	}
	s.reloadMu.RLock()
	defer s.reloadMu.RUnlock()
	if s.watcher.Config() != cfg {
		http.Error(w, "node configuration changed", http.StatusServiceUnavailable)
		return
	}

	// Hold the per-session lifecycle lock across teardown → spawn → register so a
	// concurrent reconstruct cannot run a second ffmpeg writer against this
	// session's output dir while we replace it.
	unlock := s.lockSessionLifecycle(req.SessionID)

	// Defensively close any existing session for this ID so that a quality
	// switch doesn't orphan the old ffmpeg process or leave stale segments.
	s.mu.Lock()
	if old, ok := s.sessions[req.SessionID]; ok {
		delete(s.sessions, req.SessionID)
		delete(s.lastAccess, req.SessionID)
		s.mu.Unlock()
		_ = s.closeSessionOffGPU(old)
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

	session, err := playback.StartTranscode(r.Context(), opts)
	if err != nil {
		unlock()
		slog.ErrorContext(r.Context(), "start transcode", "component", "transcodenode", "error", err, "session", req.SessionID, "playback_session_id", req.SessionID)
		if isToneMapRecipeError(err) {
			writeToneMapRecipeError(w, err)
		} else {
			http.Error(w, "failed to start transcode", http.StatusInternalServerError)
		}
		return
	}
	if req.RequireReady {
		if _, err := session.WaitForManifest(TranscodeStartReadinessTimeout); err != nil {
			wasRunning := session.IsRunning()
			_ = session.Close()
			// Mirror the API server's local-transport retry: an early death
			// under VideoToolbox retries once in software (there is no
			// alternate render device to move to), so a hardware encoder
			// session this Mac cannot create does not fail clustered
			// playback while CPU encoding was available.
			retryAccel := playback.StartupRetryHWAccel(opts)
			if wasRunning || retryAccel == opts.HWAccel {
				unlock()
				slog.ErrorContext(r.Context(), "transcode failed readiness check", "component", "transcodenode", "error", err, "session", req.SessionID, "playback_session_id", req.SessionID)
				http.Error(w, "transcode did not become ready", http.StatusInternalServerError)
				return
			}
			slog.WarnContext(r.Context(), "transcode crashed during startup; retrying with software encoding",
				"component", "transcodenode", "error", err, "session", req.SessionID, "playback_session_id", req.SessionID)
			retryOpts := opts
			retryOpts.HWAccel = retryAccel
			session, err = playback.StartTranscode(context.WithoutCancel(r.Context()), retryOpts)
			if err != nil {
				unlock()
				slog.ErrorContext(r.Context(), "start transcode retry", "component", "transcodenode", "error", err, "session", req.SessionID, "playback_session_id", req.SessionID)
				http.Error(w, "failed to start transcode", http.StatusInternalServerError)
				return
			}
			if _, retryErr := session.WaitForManifest(TranscodeStartReadinessTimeout); retryErr != nil {
				_ = session.Close()
				unlock()
				slog.ErrorContext(r.Context(), "transcode failed readiness check", "component", "transcodenode", "error", retryErr, "session", req.SessionID, "playback_session_id", req.SessionID)
				http.Error(w, "transcode did not become ready", http.StatusInternalServerError)
				return
			}
		}
	}

	s.mu.Lock()
	s.sessions[req.SessionID] = session
	s.noteSessionAccessLocked(req.SessionID)
	s.mu.Unlock()
	unlock()
	s.activeJobs.Add(1)

	// Track session in Redis off the request path — the API server (and
	// behind it the playback client) is blocked on this 202, and the
	// tracking write is monitoring-only.
	effectiveHWAccel := session.Opts().HWAccel
	trackCtx := context.WithoutCancel(r.Context())
	go s.tracker.Track(trackCtx, nodesessions.SessionInfo{
		SessionID:   req.SessionID,
		NodeURL:     s.tracker.NodeURL(),
		NodeName:    s.tracker.NodeName(),
		Type:        "transcode",
		CodecVideo:  req.TargetCodecVideo,
		CodecAudio:  req.TargetCodecAudio,
		Resolution:  req.TargetResolution,
		HWAccel:     effectiveHWAccel,
		ToneMapMode: string(session.Opts().ToneMapMode),
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
	})

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(TranscodeStartResponse{
		SessionID:          req.SessionID,
		Status:             "started",
		HWAccel:            effectiveHWAccel,
		ToneMapMode:        session.Opts().ToneMapMode,
		AudioRecipeVersion: req.AudioRecipeVersion,
	})
}

func (s *Server) requireApprovedInputPath(w http.ResponseWriter, r *http.Request, path string) bool {
	if s.inputPaths == nil {
		http.Error(w, "input path authority unavailable", http.StatusServiceUnavailable)
		return false
	}
	allowed, err := s.inputPaths.Allowed(r.Context(), path)
	if err != nil {
		slog.ErrorContext(r.Context(), "authorize transcode input path", "component", "transcodenode", "error", err)
		http.Error(w, "input path authority unavailable", http.StatusServiceUnavailable)
		return false
	}
	if !allowed {
		http.Error(w, "input_path must be an approved absolute media file", http.StatusBadRequest)
		return false
	}
	return true
}

// reconstructFromToken rebuilds a transcode session this node lost to its own
// restart, from whichever recipe source the request has.
//
// A legacy attempt forwards the client's verified stream token in the
// X-Silo-Stream-Token header, and a native token carries the full byte-affecting
// recipe (the former Postgres "recipe card"), so the node can re-spawn ffmpeg
// seeked to the requested segment rather than 404ing — mirroring the integrated
// server's token-carried reconstruct.
//
// Two flows reach this path with no usable token at all and rebuild from the
// control-plane recipe store instead: jellycompat, whose node-hop token is
// identity-only by design (see internal/noderecipe), and a header-authenticated
// (tokenless) attempt, where no client-visible URL carries a credential and the
// relayed request therefore has no token to forward. The token was never this
// route's authorization — the static bearer already authenticated the caller —
// so its absence only removes a recipe source, never a permission.
//
// Returns nil when no source yields a complete transcode recipe for the session
// id in the URL, which the caller renders as a genuine not-found.
//
// requestedSegment is the segment the client is fetching, or negative on the
// manifest path. Reconstruction is single-flighted per session id so concurrent
// manifest and segment requests for the same lost session share one ffmpeg.
func (s *Server) reconstructFromToken(r *http.Request, sessionID string, requestedSegment int) (*playback.TranscodeSession, error) {
	var card playback.RecipeCard
	tokenComplete := false
	if tokenStr := r.Header.Get("X-Silo-Stream-Token"); tokenStr != "" {
		cfg := s.watcher.Config()
		if cfg == nil {
			return nil, nil
		}
		claims, err := streamtoken.Verify(tokenStr, cfg.Auth.JWTSecret)
		if err != nil {
			slog.WarnContext(r.Context(), "transcode node reconstruct: invalid stream token", "component", "transcodenode", "error", err,
				"session", sessionID, "playback_session_id", sessionID)
			return nil, nil
		}
		card = playback.RecipeCardFromClaims(claims)
		// A presented token's recipe must be a transcode card for the session id in
		// the URL: a mismatch is a forged or stale request, and direct/remux cards
		// carry no encode parameters to rebuild. An empty PlayMethod is a transcode
		// card (back-compat).
		if !recipeServesTransport(card, sessionID) {
			return nil, nil
		}
		tokenComplete = recipeIsComplete(card)
	}
	// Without a complete token recipe the store is the only remaining source; with
	// no store wired there is nothing to rebuild from, so 404.
	if !tokenComplete && s.recipeStore == nil {
		return nil, nil
	}

	v, err, _ := s.reconstructGroup.Do(sessionID, func() (interface{}, error) {
		// A concurrent reconstruct (or a fresh start) may already have registered the
		// session; serve it rather than spawning a duplicate ffmpeg.
		s.mu.RLock()
		existing, ok := s.sessions[sessionID]
		s.mu.RUnlock()
		if ok {
			return existing, nil
		}
		resolved := card
		if !tokenComplete {
			// No complete token recipe (jellycompat's identity-only token, or a
			// header-authenticated attempt with no token at all): fetch the recipe
			// central wrote to the control-plane store at transcode start. A miss,
			// a recipe for another transport, or an incomplete one is a genuine
			// not-found (404), never a spawn from a bad recipe.
			fetched, ok := s.recipeStore.Get(r.Context(), sessionID)
			if !ok || fetched == nil || !recipeServesTransport(*fetched, sessionID) || !recipeIsComplete(*fetched) {
				return (*playback.TranscodeSession)(nil), nil
			}
			resolved = *fetched
		}
		return s.spawnReconstruct(r, sessionID, requestedSegment, resolved)
	})
	if session, _ := v.(*playback.TranscodeSession); session != nil {
		return session, nil
	}
	return nil, err
}

// recipeServesTransport reports whether a recipe card describes the transcode
// this node serves under transportID — the id in the node-facing URL.
//
// The two writers key that id differently and both shapes are accepted. A native
// v3 remote transcode runs under a plan-scoped transport id (so a prepared
// successor can coexist with its predecessor), recorded on the card as
// TranscodeTransportID; jellycompat runs under the upstream playback session id
// and leaves the field empty, so SessionID is the transport id there. A card
// that matches neither is a forged, stale, or misrouted request. An empty
// PlayMethod counts as a transcode card (back-compat).
func recipeServesTransport(card playback.RecipeCard, transportID string) bool {
	expected := card.SessionID
	if card.TranscodeTransportID != "" {
		expected = card.TranscodeTransportID
	}
	return expected == transportID && (card.PlayMethod == "" || card.PlayMethod == playback.PlayTranscode)
}

// recipeIsComplete reports whether a recipe carries the encode parameters
// ffmpeg needs. An identity-only card (the jellycompat node-hop token) is not
// complete and has to be resolved against the control-plane store.
func recipeIsComplete(card playback.RecipeCard) bool {
	return card.SegmentDuration > 0 && card.TargetCodecVideo != ""
}

// spawnReconstruct re-spawns ffmpeg for a lost session from its recipe card and
// registers it in the live map. It is only ever called inside the per-session
// single-flight in reconstructFromToken, so it is the sole writer racing to
// register sessionID. Returns nil if the spawn fails or the slot wait is canceled.
func (s *Server) spawnReconstruct(r *http.Request, sessionID string, requestedSegment int, card playback.RecipeCard) (*playback.TranscodeSession, error) {
	if s.inputPaths == nil {
		slog.ErrorContext(r.Context(), "transcode node reconstruct input authority unavailable", "component", "transcodenode", "session", sessionID)
		return nil, nil
	}
	allowed, err := s.inputPaths.Allowed(r.Context(), card.InputPath)
	if err != nil || !allowed {
		slog.WarnContext(r.Context(), "transcode node reconstruct input rejected", "component", "transcodenode", "session", sessionID, "error", err)
		return nil, nil
	}
	s.mu.RLock()
	existing, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if ok {
		return existing, nil
	}
	cfg := s.watcher.Config()
	if cfg == nil {
		return nil, nil
	}
	// A reconstruct spawns ffmpeg on the GPU exactly as a fresh start does, so
	// it takes the same exclusion against a running capability re-probe. Held
	// until this call returns, by which point activeJobs covers the session.
	if !s.gpu.beginWork() {
		slog.InfoContext(r.Context(), "transcode node reconstruct deferred while re-probing hardware",
			"component", "transcodenode", "session", sessionID)
		return nil, nil
	}
	defer s.gpu.endWork()
	outputDir := s.sessionOutputDir(sessionID)
	opts := card.TranscodeOpts(outputDir, cfg.Playback.FFmpegPath, s.ffmpegSink)
	opts.SessionID = sessionID
	// Recipe cards preserve the original launch tuning, but reconstruction must
	// retain the normal manifest cushion rather than the fresh-start fast path.
	opts.FastStart = false
	// Re-resolve environment-specific encode knobs from this node's live config; the
	// token deliberately omits HWAccel/HWDevice so an operator change applies on
	// rebuild. Run as a transcode node, not integrated (card.TranscodeOpts defaults).
	opts.HWAccel = cfg.Playback.HWAccel
	opts.HWDevice = cfg.Playback.HWDevice
	opts.NodeType = "transcode"
	opts.ExecutionMode = "transcode_node"
	if toneMapRecipeRequested(opts) {
		err := s.resolveToneMapRecipe(context.WithoutCancel(r.Context()), &opts)
		if err != nil {
			slog.ErrorContext(r.Context(), "transcode node reconstruct tone-map recipe unavailable", "component", "transcodenode", "error", err,
				"session", sessionID, "playback_session_id", sessionID)
			return nil, err
		}
	}

	s.reloadMu.RLock()
	defer s.reloadMu.RUnlock()
	if s.watcher.Config() != cfg {
		return nil, nil
	}

	// Serialize against a concurrent fresh /transcode/start for this session so the
	// two never run ffmpeg writers against the same dir. Re-check under the lock and
	// yield to any live session rather than spawning a duplicate.
	unlock := s.lockSessionLifecycle(sessionID)
	defer unlock()
	s.mu.RLock()
	existing, ok = s.sessions[sessionID]
	s.mu.RUnlock()
	if ok {
		return existing, nil
	}

	// Pace the cold-start burst only after this session owns its lifecycle lock.
	// A waiter behind an in-flight start must not consume capacity that unrelated
	// sessions need to reconstruct.
	release, ok := s.acquireReconstructSlot(r.Context())
	if !ok {
		return nil, r.Context().Err()
	}
	defer release()

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

	session, err := playback.StartTranscode(r.Context(), opts)
	if err != nil {
		slog.ErrorContext(r.Context(), "transcode node reconstruct start failed", "component", "transcodenode", "error", err,
			"session", sessionID, "playback_session_id", sessionID)
		return nil, err
	}

	// Readiness is normally the caller's concern, but a VideoToolbox session
	// can die at encoder init (e.g. a session the hardware cannot create at
	// these dimensions), and registering the dead session would serve this
	// media as permanently missing. Mirror handleStart's software retry for
	// the accel StartupRetryHWAccel would change; other accels keep the
	// existing register-immediately behavior.
	if retryAccel := playback.StartupRetryHWAccel(opts); retryAccel != opts.HWAccel {
		if _, waitErr := session.WaitForManifest(playback.ManifestStartupTimeout); waitErr != nil {
			if session.IsRunning() {
				slog.WarnContext(r.Context(), "reconstructed transcode slow to produce a manifest", "component", "transcodenode",
					"error", waitErr, "session", sessionID, "playback_session_id", sessionID)
			} else {
				// Keep the shared output directory: the retry writes into it.
				_ = session.CloseProcess()
				slog.WarnContext(r.Context(), "reconstructed transcode crashed during startup; retrying with software encoding",
					"component", "transcodenode", "error", waitErr, "session", sessionID, "playback_session_id", sessionID)
				retryOpts := opts
				retryOpts.HWAccel = retryAccel
				session, err = playback.StartTranscode(context.WithoutCancel(r.Context()), retryOpts)
				if err != nil {
					slog.ErrorContext(r.Context(), "transcode node reconstruct retry failed", "component", "transcodenode", "error", err,
						"session", sessionID, "playback_session_id", sessionID)
					return nil, err
				}
				if _, retryErr := session.WaitForManifest(playback.ManifestStartupTimeout); retryErr != nil {
					_ = session.Close()
					slog.ErrorContext(r.Context(), "reconstructed software retry failed readiness check", "component", "transcodenode", "error", retryErr,
						"session", sessionID, "playback_session_id", sessionID)
					return nil, retryErr
				}
			}
		}
	}

	// Yield to a winner registered by another path; close only the duplicate ffmpeg,
	// never the shared output directory the winner is actively serving.
	s.mu.Lock()
	if existing, ok := s.sessions[sessionID]; ok {
		s.mu.Unlock()
		_ = session.CloseProcess()
		return existing, nil
	}
	s.sessions[sessionID] = session
	s.noteSessionAccessLocked(sessionID)
	s.mu.Unlock()
	s.activeJobs.Add(1)

	trackCtx := context.WithoutCancel(r.Context())
	go s.tracker.Track(trackCtx, nodesessions.SessionInfo{
		SessionID:   sessionID,
		NodeURL:     s.tracker.NodeURL(),
		NodeName:    s.tracker.NodeName(),
		Type:        "transcode",
		CodecVideo:  card.TargetCodecVideo,
		CodecAudio:  card.TargetCodecAudio,
		Resolution:  card.TargetResolution,
		HWAccel:     session.Opts().HWAccel,
		ToneMapMode: string(session.Opts().ToneMapMode),
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		AuthUserID:  card.UserID,
		ProfileID:   card.ProfileID,
		MediaFileID: card.MediaFileID,
	})

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
	delete(s.sessions, sessionID)
	delete(s.lastAccess, sessionID)
	s.mu.Unlock()

	if err := s.closeSessionOffGPU(session); err != nil {
		slog.ErrorContext(r.Context(), "close transcode session", "component", "transcodenode", "error", err, "session", sessionID, "playback_session_id", sessionID)
	}

	if err := os.RemoveAll(s.sessionOutputDir(sessionID)); err != nil {
		slog.WarnContext(r.Context(), "remove transcode session directory", "component", "transcodenode", "session", sessionID, "error", err)
	}

	// Drop the recipe so a buffered/retrying request after a node restart cannot
	// reconstruct a new ffmpeg for this now-stopped session. Best-effort: a stop
	// must still succeed even if the recipe store is briefly unavailable.
	if s.recipeStore != nil {
		if err := s.recipeStore.Delete(r.Context(), sessionID); err != nil {
			slog.WarnContext(r.Context(), "delete transcode recipe on stop", "component", "transcodenode", "error", err, "session", sessionID, "playback_session_id", sessionID)
		}
	}

	s.tracker.Remove(r.Context(), sessionID)

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")

	// Lookup and liveness refresh happen atomically so the idle reaper can
	// never unregister the job between them and tear down a session this
	// request is about to serve from.
	session, ok := s.acquireSessionTouched(sessionID)
	if !ok {
		// Lost the in-memory session (this node restarted): rebuild it from the
		// stream token the proxy forwarded. The manifest path carries no segment
		// context, so reconstruct at the recipe's original start position.
		var reconstructErr error
		session, reconstructErr = s.reconstructFromToken(r, sessionID, -1)
		if session == nil {
			if reconstructErr != nil && isToneMapRecipeError(reconstructErr) {
				writeToneMapRecipeError(w, reconstructErr)
				return
			}
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		// A reconstruct that yielded to a concurrently registered winner has
		// not recorded this hit; count it so the reaper sees the liveness.
		s.touchSession(sessionID)
	}
	s.attachTelemetrySession(r, sessionID)

	var manifest []byte
	var err error
	if r.URL.Query().Get(playback.SourceTimelineQueryParam) == "1" {
		manifest, err = session.BuildSourceAlignedPlaybackManifest("segment/", r.URL.RawQuery)
	} else {
		manifest, err = session.BuildPlaybackManifest("segment/", r.URL.RawQuery)
	}
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
	sessionID := chi.URLParam(r, "session_id")
	name := chi.URLParam(r, "name")

	// Lookup and liveness refresh happen atomically so the idle reaper can
	// never unregister the job between them and tear down a session this
	// request is about to serve from.
	session, ok := s.acquireSessionTouched(sessionID)
	if !ok {
		// Lost the in-memory session (this node restarted): rebuild it from the
		// forwarded stream token, seeked to the segment the client is requesting so
		// playback resumes near its position instead of restarting from the start.
		requestedSegment := -1
		if n, parseErr := playback.ParseSegmentNumber(name); parseErr == nil {
			requestedSegment = n
		}
		var reconstructErr error
		session, reconstructErr = s.reconstructFromToken(r, sessionID, requestedSegment)
		if session == nil {
			if reconstructErr != nil && isToneMapRecipeError(reconstructErr) {
				writeToneMapRecipeError(w, reconstructErr)
				return
			}
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		// A reconstruct that yielded to a concurrently registered winner has
		// not recorded this hit; count it so the reaper sees the liveness.
		s.touchSession(sessionID)
	}
	s.attachTelemetrySession(r, sessionID)

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
						r.Context(),
						sessionID,
						session,
						seekSeconds,
						segNum,
					); restartErr == nil {
						segPath, err = session.WaitForSegment(name, 30*time.Second)
					} else {
						err = restartErr
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
		if isToneMapRecipeError(err) {
			writeToneMapRecipeError(w, err)
			return
		}
		http.Error(w, "segment not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	http.ServeFile(w, r, segPath)
}

// handleReloadConfig re-reads this node's configuration and nothing else.
//
// It exists because /admin/force-reload is destructive: it tears down every
// live playback session so a configuration change cannot leave a running ffmpeg
// on stale settings. That is the right answer when an operator asks for it
// explicitly, and the wrong one for the control plane's own housekeeping — the
// API nudges a node after its acceleration overrides change, and a policy edit
// that says it applies to new transcodes must not interrupt the ones already
// playing. Sessions keep the settings they started with, which is exactly what
// the override documentation promises.
func (s *Server) handleReloadConfig(w http.ResponseWriter, r *http.Request) {
	// The same lock the destructive route takes, so a start cannot be admitted
	// against a config this reload is in the middle of replacing.
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if err := s.watcher.ForceReload(r.Context()); err != nil {
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.InfoContext(r.Context(), "transcode node configuration reloaded", "component", "transcodenode")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleForceReload(w http.ResponseWriter, r *http.Request) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if err := s.watcher.ForceReload(r.Context()); err != nil {
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	type forceReloadVictim struct {
		id      string
		session *playback.TranscodeSession
	}
	s.mu.RLock()
	victims := make([]forceReloadVictim, 0, len(s.sessions))
	for id, session := range s.sessions {
		victims = append(victims, forceReloadVictim{id: id, session: session})
	}
	s.mu.RUnlock()

	for _, victim := range victims {
		unlock := s.lockSessionLifecycle(victim.id)

		s.mu.Lock()
		if current, ok := s.sessions[victim.id]; !ok || current != victim.session {
			s.mu.Unlock()
			unlock()
			continue
		}
		delete(s.sessions, victim.id)
		delete(s.lastAccess, victim.id)
		s.mu.Unlock()

		_ = s.closeSessionOffGPU(victim.session)
		if err := os.RemoveAll(s.sessionOutputDir(victim.id)); err != nil {
			slog.WarnContext(r.Context(), "remove transcode session directory during reload", "component", "transcodenode", "session", victim.id, "error", err)
		}

		// A force-reload tears this session down for good, so drop its recipe too:
		// otherwise a buffered/retrying request could reconstruct a session this
		// reload deliberately killed. Keep the lifecycle lock through deletion so a
		// concurrent same-ID start cannot have its newly written recipe removed.
		if s.recipeStore != nil {
			id := victim.id
			if err := s.recipeStore.Delete(r.Context(), id); err != nil {
				slog.WarnContext(r.Context(), "delete transcode recipe on force reload", "component", "transcodenode", "error", err, "session", id, "playback_session_id", id)
			}
		}

		// Drop only this victim from the tracker. A blanket Cleanup here would
		// also wipe unrelated tracker-only work, such as an active download
		// preparation, even though force reload does not stop that job.
		s.tracker.Remove(r.Context(), victim.id)
		unlock()
	}

	slog.InfoContext(r.Context(), "transcode force reload completed", slog.String("component", "transcodenode"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	sessionIDs := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		sessionIDs = append(sessionIDs, id)
	}
	s.mu.RUnlock()

	snapshot := s.metrics.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	type statusResponse struct {
		Status     string                   `json:"status"`
		ActiveJobs int32                    `json:"active_jobs"`
		Sessions   []string                 `json:"sessions"`
		System     *nodemetrics.SystemStats `json:"system,omitempty"`
		GPU        []nodemetrics.GPUStats   `json:"gpu,omitempty"`
	}
	json.NewEncoder(w).Encode(statusResponse{
		Status:     "ok",
		ActiveJobs: s.activeJobs.Load(),
		Sessions:   sessionIDs,
		System:     snapshot.System,
		GPU:        snapshot.GPU,
	})
}
