package transcodenode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/buildinfo"
	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/downloadstorage"
	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodemetrics"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/telemetry"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
)

// TranscodeStartRequest is the JSON body for POST /transcode/start.
type TranscodeStartRequest struct {
	SessionID string `json:"session_id"`
	// StartID is an opaque id the core picks for this one start. A release
	// that names it ends the job only while this start's job still runs, so a
	// late release cannot end a newer start under the same transport id.
	StartID string `json:"start_id,omitempty"`
	// PlaybackSessionID, UserID, ProfileID and MediaFileID are the playback
	// session the transcode serves. The node files its session record and
	// stream telemetry under them; it reads no stream token to learn them.
	PlaybackSessionID string `json:"playback_session_id,omitempty"`
	UserID            int    `json:"user_id,omitempty"`
	ProfileID         string `json:"profile_id,omitempty"`
	MediaFileID       int    `json:"media_file_id,omitempty"`
	// InputPath is the catalog path of the source. The node does not use it:
	// it opens SourceGrant and runs FFmpeg on the path the core approves.
	InputPath string `json:"input_path"`
	// SourceGrant is the core's source grant for the input, naming this node
	// (internal/nodesource). The node exchanges it at the core's sources/open
	// route and keeps the source open for the session's life.
	SourceGrant                string                 `json:"source_grant,omitempty" required:"true"`
	SourceVideoCodec           string                 `json:"source_video_codec"`
	SourceVideoProfile         string                 `json:"source_video_profile,omitempty"`
	SourceVideoBitDepth        int                    `json:"source_video_bit_depth,omitempty"`
	SourceAudioChannels        int                    `json:"source_audio_channels,omitempty"`
	AudioRecipeVersion         string                 `json:"audio_recipe_version,omitempty"`
	CopyFMP4RecipeVersion      string                 `json:"copy_fmp4_recipe_version,omitempty"`
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
	VideoRange                 string                 `json:"video_range,omitempty"`
	CopyVideoMPEGTS            bool                   `json:"copy_video_mpegts,omitempty"`
	KeyframePlaylist           bool                   `json:"keyframe_playlist,omitempty"`
	// KeyframePlanResume asks a resume to serve the keyframe plan an
	// earlier start of its session served (see playback.TranscodeOpts).
	KeyframePlanResume     bool    `json:"keyframe_plan_resume,omitempty"`
	SeekSeconds            float64 `json:"seek_seconds"`
	StreamOriginSeconds    float64 `json:"stream_origin_seconds,omitempty"`
	CopySeekAnchorResolved bool    `json:"copy_seek_anchor_resolved,omitempty"`
	StartSegmentNumber     int     `json:"start_segment_number"`
	TargetResolution       string  `json:"target_resolution"`
	TargetCodecVideo       string  `json:"target_codec_video"`
	TargetCodecAudio       string  `json:"target_codec_audio"`
	CopyAudioCodec         string  `json:"copy_audio_codec,omitempty"`
	TargetAudioChannels    int     `json:"target_audio_channels,omitempty"`
	TargetAudioBitrateKbps int     `json:"target_audio_bitrate_kbps,omitempty"`
	TargetBitrateKbps      int     `json:"target_bitrate_kbps"`
	SegmentDuration        int     `json:"segment_duration"`
	HWAccel                string  `json:"hw_accel"`
	AudioTrackIndex        int     `json:"audio_track_index"`
	RequireAudioTrack      bool    `json:"require_audio_track,omitempty"`
	SubtitleTrackIndex     int     `json:"subtitle_track_index"`
	SubtitleBurnIn         bool    `json:"subtitle_burn_in"`
	SubtitleCodec          string  `json:"subtitle_codec,omitempty"`
	// SubtitleRendition asks the executor to emit the selected text track as
	// the playlist's WebVTT subtitle rendition. Only nodes advertising
	// playback.HLSSubtitleRenditionTransportFeatureV3 for the video target
	// receive it.
	SubtitleRendition bool    `json:"subtitle_rendition,omitempty"`
	TotalDuration     float64 `json:"total_duration"`
	RequireReady      bool    `json:"require_ready,omitempty"`
	// SubtitleRenditionTracks asks for every listed embedded text track as an
	// on-demand rendition. Only nodes advertising
	// playback.TransportFeatureHLSOnDemandSubtitleRenditionsV1 receive it.
	SubtitleRenditionTracks []playback.SubtitleRenditionTrack `json:"subtitle_rendition_tracks,omitempty"`
	// SubtitleRenditionSource is the source version the tracks were planned
	// from; segments are read from it only.
	SubtitleRenditionSource playback.SubtitleRenditionSource `json:"subtitle_rendition_source,omitzero"`
	// AutoFallbackReady asks the node to wait for the first manifest only when
	// its own hw_accel=auto fallback is enabled for this start, so an early
	// hardware failure can move to a safer path. The node decides from its
	// live hardware; any other start is not waited on. RequireReady still
	// forces a wait.
	AutoFallbackReady bool `json:"auto_fallback_ready,omitempty"`
	ThrottleSeconds   int  `json:"throttle_seconds,omitempty"`
}

// TranscodeStartResponse is the JSON response for POST /transcode/start.
type TranscodeStartResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	HWAccel   string `json:"hw_accel,omitempty"`
	// EncoderHWAccel may differ from HWAccel when the GPU tone-maps frames
	// that libx265 encodes on CPU. Older nodes omit it.
	EncoderHWAccel string       `json:"encoder_hw_accel,omitempty"`
	ToneMapMode    tonemap.Mode `json:"tone_map_mode,omitempty"`
	// AudioRecipeVersion attests the exact byte-affecting audio recipe the node
	// understood. An old node omits it, allowing current callers to stop the job
	// before publishing bytes from a silently ignored SourceAudioChannels field.
	AudioRecipeVersion string `json:"audio_recipe_version,omitempty"`
	// RequireAudioTrack confirms that the executor made the selected audio mandatory.
	RequireAudioTrack bool `json:"require_audio_track,omitempty"`
	// CopyFMP4RecipeVersion attests the copy-video HLS timestamp and bitstream
	// recipe. Old nodes omit it and are rejected before their bytes are exposed.
	CopyFMP4RecipeVersion string `json:"copy_fmp4_recipe_version,omitempty"`
	// ThrottleSeconds attests that the executor understood and armed the
	// control plane's resolved forward-buffer policy.
	ThrottleSeconds int `json:"throttle_seconds,omitempty"`
	// SoftwareVideoDecode reports that the executed recipe decodes video on
	// the CPU, as the hw_accel=auto fallback may choose after a hardware
	// decoder failed to start. Older nodes omit it, so callers OR it with the
	// requested value.
	SoftwareVideoDecode bool `json:"software_video_decode,omitempty"`
	// KeyframePlaylist attests that the executor understood a requested
	// keyframe playlist. Older nodes omit it and are rejected before they
	// serve FFmpeg's growing playlist under a recipe that records the planned
	// one.
	KeyframePlaylist bool `json:"keyframe_playlist,omitempty"`
	// KeyframePlanned reports that the session serves the keyframe plan,
	// which keyframe_playlist only confirms was understood: a copy the node
	// can't plan keeps FFmpeg's playlist. Older nodes omit it, which reads as
	// FFmpeg's playlist.
	KeyframePlanned bool `json:"keyframe_planned,omitempty"`
	// KeyframePlanResume reports that a resume serves a keyframe plan, which
	// its recipe must record so a rebuild serves it again. Older nodes omit
	// it; they never plan a resume.
	KeyframePlanResume bool `json:"keyframe_plan_resume,omitempty"`
	// SubtitleRendition attests that the executor emits the requested subtitle
	// rendition. Older nodes omit it and are rejected before the plan
	// promises a rendition their playlist does not carry; nodes that predate
	// remux renditions omit it for a copy remux.
	SubtitleRendition bool `json:"subtitle_rendition,omitempty"`
	// SubtitleRenditionTracks attests how many on-demand renditions the
	// executor serves. Older nodes omit it and are rejected before the plan
	// lists renditions their playlist does not carry.
	SubtitleRenditionTracks int `json:"subtitle_rendition_tracks,omitempty"`
	// CopyVideoMPEGTS attests that the executor packages the requested copy
	// remux as MPEG-TS. Older nodes omit it and are rejected before an fMP4
	// stream is published under a plan that promises MPEG-TS.
	CopyVideoMPEGTS bool `json:"copy_video_mpegts,omitempty"`
	// CopyAudioCodec attests the copied audio codec the executor applies.
	// Older nodes omit it and are rejected before they copy AAC into fMP4
	// segments without aac_adtstoasc.
	CopyAudioCodec string `json:"copy_audio_codec,omitempty"`
}

var ErrAudioRecipeAttestationMismatch = errors.New("transcode node audio recipe attestation mismatch")
var ErrCopyFMP4RecipeAttestationMismatch = errors.New("transcode node copy-fmp4 recipe attestation mismatch")
var ErrThrottleAttestationMismatch = errors.New("transcode node throttle attestation mismatch")

// ErrKeyframePlaylistAttestationMismatch reports an executor that ignored a
// requested keyframe playlist.
var ErrKeyframePlaylistAttestationMismatch = errors.New("transcode node keyframe playlist attestation mismatch")

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

// ValidateAudioRecipeAttestation requires confirmation of mandatory audio and
// the v2 stereo-downmix recipe. Optional legacy starts accept empty responses.
func ValidateAudioRecipeAttestation(req TranscodeStartRequest, response TranscodeStartResponse) error {
	if req.RequireAudioTrack && !response.RequireAudioTrack {
		return fmt.Errorf("%w: required audio track was not confirmed", ErrAudioRecipeAttestationMismatch)
	}
	if err := validateAudioRecipeRequest(req); err != nil {
		return err
	}
	if req.AudioRecipeVersion != "" && response.AudioRecipeVersion != req.AudioRecipeVersion {
		return fmt.Errorf("%w: got %q, want %q", ErrAudioRecipeAttestationMismatch, response.AudioRecipeVersion, req.AudioRecipeVersion)
	}
	return nil
}

func validateCopyFMP4RecipeRequest(req TranscodeStartRequest) error {
	isCopy := strings.EqualFold(strings.TrimSpace(req.TargetCodecVideo), "copy")
	if !isCopy && req.CopyFMP4RecipeVersion == "" {
		return nil
	}
	if !isCopy || req.CopyFMP4RecipeVersion != playback.CopyFMP4RecipeVersion {
		return fmt.Errorf("%w: target_codec=%q recipe_version=%q",
			ErrCopyFMP4RecipeAttestationMismatch, req.TargetCodecVideo, req.CopyFMP4RecipeVersion)
	}
	return nil
}

// ValidateCopyFMP4RecipeAttestation requires current copy-video starts to be
// acknowledged by the executor. Encoded starts remain compatible with older
// nodes and their empty responses.
func ValidateCopyFMP4RecipeAttestation(req TranscodeStartRequest, response TranscodeStartResponse) error {
	if err := validateCopyFMP4RecipeRequest(req); err != nil {
		return err
	}
	if req.CopyFMP4RecipeVersion != "" && response.CopyFMP4RecipeVersion != req.CopyFMP4RecipeVersion {
		return fmt.Errorf("%w: got %q, want %q",
			ErrCopyFMP4RecipeAttestationMismatch, response.CopyFMP4RecipeVersion, req.CopyFMP4RecipeVersion)
	}
	return nil
}

// ValidateKeyframePlaylistAttestation rejects an executor that silently
// ignored a requested keyframe playlist. Requests without one stay compatible
// with older nodes, which omit the field. A resume that must serve its earlier
// keyframe plan also needs the node to confirm it planned: an older node
// echoes keyframe_playlist, ignores keyframe_plan_resume, and serves FFmpeg's
// playlist, which numbers and cuts segments differently.
func ValidateKeyframePlaylistAttestation(req TranscodeStartRequest, response TranscodeStartResponse) error {
	if (req.KeyframePlaylist && !response.KeyframePlaylist) ||
		(req.KeyframePlanResume && !response.KeyframePlanResume) {
		return ErrKeyframePlaylistAttestationMismatch
	}
	return nil
}

// ErrCopyVideoMPEGTSAttestationMismatch reports an executor that ignored
// requested MPEG-TS copy packaging.
var ErrCopyVideoMPEGTSAttestationMismatch = errors.New("transcode node mpeg-ts copy attestation mismatch")

// ValidateCopyVideoMPEGTSAttestation rejects an executor that silently
// packaged a requested MPEG-TS copy remux as fMP4. Requests without it stay
// compatible with older nodes, which omit the field.
func ValidateCopyVideoMPEGTSAttestation(req TranscodeStartRequest, response TranscodeStartResponse) error {
	if req.CopyVideoMPEGTS && !response.CopyVideoMPEGTS {
		return ErrCopyVideoMPEGTSAttestationMismatch
	}
	return nil
}

// ErrCopyAudioCodecAttestationMismatch reports an executor that ignored the
// copied audio codec of a run that needs aac_adtstoasc.
var ErrCopyAudioCodecAttestationMismatch = errors.New("transcode node copied audio codec attestation mismatch")

// ValidateCopyAudioCodecAttestation rejects an executor that did not confirm
// the copied codec of a run that copies AAC into fMP4 segments. Other requests
// stay compatible with older nodes, which omit the field.
func ValidateCopyAudioCodecAttestation(req TranscodeStartRequest, response TranscodeStartResponse) error {
	opts := playback.TranscodeOpts{
		SourceVideoCodec: req.SourceVideoCodec,
		TargetCodecVideo: req.TargetCodecVideo,
		TargetCodecAudio: req.TargetCodecAudio,
		CopyAudioCodec:   req.CopyAudioCodec,
		CopyVideoMPEGTS:  req.CopyVideoMPEGTS,
	}
	if playback.HLSCopiesAACIntoFMP4(opts) && response.CopyAudioCodec != req.CopyAudioCodec {
		return fmt.Errorf("%w: got %q, want %q", ErrCopyAudioCodecAttestationMismatch, response.CopyAudioCodec, req.CopyAudioCodec)
	}
	return nil
}

// ErrSubtitleRenditionAttestationMismatch reports an executor that ignored a
// requested subtitle rendition.
var ErrSubtitleRenditionAttestationMismatch = errors.New("transcode node subtitle rendition attestation mismatch")

// ValidateSubtitleRenditionAttestation rejects an executor that silently
// ignored a requested subtitle rendition. Requests without one stay compatible
// with older nodes, which omit the field.
func ValidateSubtitleRenditionAttestation(req TranscodeStartRequest, response TranscodeStartResponse) error {
	if req.SubtitleRendition && !response.SubtitleRendition || response.SubtitleRenditionTracks != len(req.SubtitleRenditionTracks) {
		return ErrSubtitleRenditionAttestationMismatch
	}
	return nil
}

// ValidateThrottleAttestation rejects an executor that silently ignored an
// enabled throttle policy. Disabled requests remain compatible with older
// nodes whose response omits the field.
func ValidateThrottleAttestation(req TranscodeStartRequest, response TranscodeStartResponse) error {
	if req.ThrottleSeconds > 0 && response.ThrottleSeconds != req.ThrottleSeconds {
		return fmt.Errorf("%w: got %d, want %d", ErrThrottleAttestationMismatch, response.ThrottleSeconds, req.ThrottleSeconds)
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
	System      *nodemetrics.SystemStats         `json:"system,omitempty"`
	GPU         []nodemetrics.GPUStats           `json:"gpu,omitempty"`
	Attribution *nodemetrics.ResourceAttribution `json:"attribution,omitempty"`
	SampledAt   time.Time                        `json:"sampled_at,omitzero"`
	// Build identifies the binary this node runs, so the API can show whether
	// the fleet is on the same revision as the server. Diagnostic only, like
	// `server_version` on the API's own system route; nothing routes on it.
	Build buildinfo.Info `json:"build"`
	// Artifacts is the last measurement of this node's prepared-download
	// directory, path removed for the same reason as the disk entries. Absent
	// until the first measurement finishes.
	Artifacts *downloadstorage.Usage `json:"artifacts,omitempty"`
	// NodeControlVersion is the node control protocol this node speaks
	// (nodepool.NodeControlVersion). The core places no work on a transcode
	// node whose health answer lacks it.
	NodeControlVersion int `json:"node_control_version"`
	// KeyRejected reports that the core refused this node's key at its last
	// configuration read: it was rotated or the node deleted. The core treats
	// the node as unhealthy until a read succeeds.
	KeyRejected bool `json:"key_rejected,omitempty"`
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

// TranscodeStartReadinessTimeout is the node-side RequireReady manifest budget
// for one FFmpeg attempt.
const TranscodeStartReadinessTimeout = playback.ManifestStartupTimeout

// TranscodeStartReadyMaxDuration bounds how long a RequireReady start can wait
// on manifests across attempts: under hw_accel=auto each early exit moves to a
// safer path with its own TranscodeStartReadinessTimeout. Callers size their
// start deadline to it so a fallback that succeeds on the node is not canceled.
const TranscodeStartReadyMaxDuration = time.Duration(playback.MaxAutoTranscodeStartupAttempts) * TranscodeStartReadinessTimeout

// progressiveRemuxShutdownTimeout bounds a destructive reload when a canceled
// FFmpeg process does not exit. A timed-out reload must fail rather than report
// completion while the old-config process is still live.
const progressiveRemuxShutdownTimeout = 10 * time.Second

type progressiveRemuxRequest struct {
	id                uint64
	playbackSessionID string
	cancel            context.CancelFunc
	done              <-chan struct{}
}

// Server is the HTTP handler for transcode mode.
type Server struct {
	// watcher is the node's configuration and registered identity, read
	// from the core.
	watcher RuntimeConfig
	// nodeKey is this node's own key, the only key requireBearer accepts.
	// Empty refuses every signed route.
	nodeKey   string
	nodeRowID func() (int, bool)
	tracker   SessionTracker
	// core answers lost sessions, progressive remuxes and recipe
	// revocation; see SetCore.
	core Core
	// opener opens every job's input with its grant; see SetSourceOpener.
	opener SourceOpener
	// keyRejected reports whether the core refuses this node's key; nil
	// reports false.
	keyRejected     func() bool
	ffmpegSink      playback.FFmpegLogSink
	prepareProgress prepareProgressRegistry
	transcodeDir    string
	artifactRoot    string
	// artifactProber measures artifactRoot in the background for health,
	// status, and the artifact listing.
	artifactProber *downloadstorage.Prober
	// artifactLister lists artifactRoot for the API, one read at a time.
	artifactLister            downloadstorage.DirGuard
	telemetry                 *streamtelemetry.Registry
	sessions                  map[string]*playback.TranscodeSession
	progressiveRemuxes        map[string]progressiveRemuxRequest
	stoppedProgressiveRemuxes map[string]time.Time
	// stoppedTranscodes records, per HLS transport id, until when a stream
	// token may still ask this process to rebuild a transport the API stopped.
	// Reconstruction refuses those ids, so a segment request still in flight
	// when the stop lands, or a stale token for a retired transport, cannot
	// restart an encode nobody is watching. Only a fresh start clears an entry;
	// the idle reaper and a release (fence=0) record none, because a client may
	// return to a reaped job and a released id lives on.
	// Guarded by mu.
	stoppedTranscodes map[string]time.Time
	// lastStopFencePrune is when the stop-fence maps were last swept; pruning
	// runs at most once per stopFencePruneInterval. Guarded by mu.
	lastStopFencePrune       time.Time
	progressiveRemuxSequence atomic.Uint64
	// lastAccess records, per registered session id, when a manifest or segment
	// request last touched the job (registration counts as the first access).
	// Guarded by mu alongside sessions; the idle reaper closes jobs whose entry
	// is older than sessionIdleTTL.
	lastAccess map[string]time.Time
	// identities records, per registered session id, the playback session
	// it serves, and sessionSources the source it reads, which it owns.
	// Guarded by mu alongside sessions.
	identities     map[string]sessionIdentity
	sessionSources map[string]nodesource.Source
	reaperOnce     sync.Once
	// mediaSamples admits media sampling runs up to the configured per-node
	// capacity, whichever API servers send them.
	mediaSamplesOnce sync.Once
	mediaSamples     *mediasample.Limiter
	// mediaSampleWait overrides mediasample.MaxRemoteAdmissionWait in tests.
	mediaSampleWait time.Duration
	mu              sync.RWMutex
	// reloadMu keeps force-reload teardown atomic with session creation and
	// reconstruction. It is always acquired before lifecycleMu or mu.
	reloadMu   sync.RWMutex
	activeJobs atomic.Int32
	// jobCap is the node's own job cap, 0 for none; admission guards
	// admitted, the new work it let in that activeJobs does not count yet,
	// remuxSeen, when each progressive remux transport was last served, and
	// continuing, the overlap allowances granted per playback session and
	// not yet registered. See jobcap.go.
	jobCap          atomic.Int32
	admission       sync.Mutex
	admitted        int
	remuxSeen       map[string]time.Time
	continuing      map[string]int
	remuxSeenPruned time.Time
	// declaredHWAccel and artifactBudget are what the node's operator
	// declared; see SetDeclaredHWAccel and SetArtifactBudget.
	declaredHWAccel string
	artifactBudget  atomic.Int64
	// inFlight counts requests for work activeJobs does not count; see
	// trackInFlight.
	inFlight atomic.Int32
	// shutdownDrain is set by SIGTERM: from then on the node takes no new work
	// while its sessions finish. See draining.
	shutdownDrain atomic.Bool
	// drainAnnouncer tells the API servers the node started or stopped
	// draining; see SetDrainAnnouncer.
	drainAnnouncer atomic.Pointer[func()]
	// drainWatch is set while announceWhenDrained runs.
	drainWatch atomic.Bool
	// drainedReported latches a drained health answer; drainFence orders it
	// against work admission. See healthStatus.
	drainFence      sync.Mutex
	drainedReported atomic.Bool
	// trickplay admits up to playback.trickplay_node_capacity runs at once.
	trickplay trickplayWork
	// shuttingDown is guarded by reloadMu. Once set, no fresh or reconstructed
	// session may register after the shutdown drain has taken its snapshot.
	shuttingDown bool
	// startThrottler is a test seam; production falls back to session.StartThrottler.
	startThrottler func(*playback.TranscodeSession, int)

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
	// autoTranscodePipelineFn is a package-private seam for the hw_accel=auto
	// fallback pipeline. Production uses playback.NewAutoTranscodePipeline.
	autoTranscodePipelineFn func(context.Context, playback.TranscodeOpts) *playback.AutoTranscodePipeline

	// lifecycleMu guards lifecycleLocks, the per-session locks that serialize
	// every path which spawns ffmpeg into a session's output dir (fresh start and
	// reconstruct). reconstructGroup only single-flights reconstructs against each
	// other; without this a reconstruct racing a fresh /transcode/start could run
	// two ffmpeg writers against the same dir. Artifact readers use the shared side
	// so concurrent relays remain independent while prepare/delete stay exclusive.
	lifecycleMu    sync.Mutex
	lifecycleLocks map[string]*sessionLifecycleLock

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

func (s *Server) autoTranscodePipeline(ctx context.Context, opts playback.TranscodeOpts) *playback.AutoTranscodePipeline {
	if s.autoTranscodePipelineFn != nil {
		return s.autoTranscodePipelineFn(ctx, opts)
	}
	return playback.NewAutoTranscodePipeline(ctx, opts)
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

// restartSegmentLocked resolves and applies missing-segment recovery while the
// node's lifecycle lock keeps the current manifest and live session stable.
func (s *Server) restartSegmentLocked(
	ctx context.Context,
	sessionID string,
	session *playback.TranscodeSession,
	segNum int,
	seenGeneration time.Time,
) (playback.SegmentRecoveryTarget, bool, error) {
	unlock := s.lockSessionLifecycle(sessionID)
	defer unlock()
	s.mu.RLock()
	live, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok || live != session {
		return playback.SegmentRecoveryTarget{}, false, playback.ErrSessionSuperseded
	}
	return session.RestartSegmentSince(ctx, segNum, seenGeneration)
}

// NewServer creates a new transcode server. watcher must have loaded its
// configuration: the transcode and artifact directories are read once, here,
// and a change to either applies at the next restart.
func NewServer(watcher RuntimeConfig, tracker SessionTracker) *Server {
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
		watcher:                   watcher,
		tracker:                   tracker,
		transcodeDir:              transcodeDir,
		artifactRoot:              artifactRoot,
		artifactProber:            downloadstorage.NewProber(0),
		sessions:                  make(map[string]*playback.TranscodeSession),
		progressiveRemuxes:        make(map[string]progressiveRemuxRequest),
		stoppedProgressiveRemuxes: make(map[string]time.Time),
		stoppedTranscodes:         make(map[string]time.Time),
		lastAccess:                make(map[string]time.Time),
	}
	if watcher != nil {
		s.nodeRowID = watcher.NodeRowID
	}
	return s
}

func (s *Server) armSessionThrottler(session *playback.TranscodeSession, seconds int) {
	if session == nil || seconds <= 0 {
		return
	}
	start := s.startThrottler
	if start == nil {
		start = func(session *playback.TranscodeSession, seconds int) { session.StartThrottler(seconds) }
	}
	session.SetRestartHook(func(context.Context) { start(session, seconds) })
	start(session, seconds)
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
	// for an orphaned HLS session directory.
	if name := s.artifactContainerName(); name != "" {
		active[name] = struct{}{}
	}
	return active
}

// artifactContainerName returns the top-level directory under the transcode
// root that holds prepared downloads, for both the default and an explicitly
// nested artifact directory, or "" when the artifact root lies elsewhere.
func (s *Server) artifactContainerName() string {
	if s.transcodeDir == "" || s.artifactRoot == "" {
		return ""
	}
	rel, err := filepath.Rel(s.transcodeDir, s.artifactRoot)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	first, _, _ := strings.Cut(rel, string(filepath.Separator))
	return first
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
// recipe is deliberately kept: an idle reap is not a client stop, and the core
// still resolves the session's recipe when its client comes back.
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
	s.forgetSessionLocked(sessionID)
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

// SetStreamTelemetry wires local stream observation. A nil registry is a
// complete no-op.
func (s *Server) SetStreamTelemetry(registry *streamtelemetry.Registry) {
	s.telemetry = registry
}

// SetNodeKey installs this node's own key (SILO_NODE_KEY), which verifies
// every request the core and proxies sign to it. It must be called during
// construction, before the server handles requests.
func (s *Server) SetNodeKey(key string) {
	s.nodeKey = key
}

// writeStreamDenied answers a denied session: 410 with no media bytes. The
// session is over; a client that retries keeps hitting this wall until its
// tokens expire.
func writeStreamDenied(w http.ResponseWriter) {
	http.Error(w, "playback session ended", http.StatusGone)
}

// sealedHandler is what Handler hands out: the finished router behind an
// unexported field and a ServeHTTP method, nothing else, so no assertion or
// type switch recovers a registration surface from it, and the route
// inventory refuses the reflect calls that could (MethodByName, Method,
// NumMethod, NewAt, UnsafePointer, UnsafeAddr, Pointer) and any import of
// unsafe in this package: short of unsafe, nothing gets the router back (see
// docs/architecture/api-contract.md). Do not embed http.Handler here:
// embedding exports the field and promotes its methods.
type sealedHandler struct {
	h http.Handler
}

func (h sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.h.ServeHTTP(w, r) }

// Handler returns the transcode node listener as a sealed http.Handler. The
// route inventory generator requires exactly this shape: seal the unexported
// constructor and nothing else. A test that needs to walk the tree calls
// router directly.
func (s *Server) Handler() http.Handler {
	return sealedHandler{h: s.router()}
}

// router is the transcode node's registration surface. The route inventory
// generator walks this method; every registration must be reachable from it.
func (s *Server) router() chi.Router {
	declareTranscodeNodeMediaRoutes()
	s.startIdleReaper()
	r := chi.NewRouter()
	r.Get("/api/v1/health", s.handleHealth)
	// Unauthenticated, matching the main server's dedicated metrics listener: a
	// scrape target that needs a credential is a scrape target that goes
	// unmonitored, and the exposure is host resource counters, not media.
	r.Method(http.MethodGet, "/metrics", promhttp.Handler())

	r.Group(func(r chi.Router) {
		r.Use(s.requireBearer)
		r.Get("/hw-capabilities", s.handleHWCapabilities)
		r.Post("/chapter-thumbnails/extract", s.handleChapterThumbnailExtract)
		r.Post("/trickplay/extract", s.handleTrickplayExtract)
		r.Post("/media-samples/run", s.handleMediaSample) // mediasample.RemotePath
		r.Post("/downloads/prepare", s.handleDownloadPrepare)
		r.Get("/downloads/prepare/{artifact_id}/progress", s.handleDownloadPrepareProgress)
		r.Head("/downloads/artifacts/{artifact_id}", observeNode(s.telemetry, http.MethodHead, "/downloads/artifacts/{artifact_id}", s.handleDownloadArtifact))
		r.Get("/downloads/artifacts/{artifact_id}", observeNode(s.telemetry, http.MethodGet, "/downloads/artifacts/{artifact_id}", s.handleDownloadArtifact))
		r.Delete("/downloads/artifacts/{artifact_id}", s.handleDeleteDownloadArtifact)
		r.Get("/downloads/artifacts", s.handleListDownloadArtifacts)
		r.Post("/transcode/start", s.handleStart)
		r.Delete("/transcode/{session_id}", s.handleStop)
		r.Head("/remux/{session_id}", observeNode(s.telemetry, http.MethodHead, "/remux/{session_id}", s.handleRemux))
		r.Get("/remux/{session_id}", observeNode(s.telemetry, http.MethodGet, "/remux/{session_id}", s.handleRemux))
		r.Get("/transcode/{session_id}/master.m3u8", observeNode(s.telemetry, http.MethodGet, "/transcode/{session_id}/master.m3u8", s.handleManifest))
		r.Get("/transcode/{session_id}/segment/{name}", observeNode(s.telemetry, http.MethodGet, "/transcode/{session_id}/segment/{name}", s.handleSegment))
		r.Post("/transcode/{session_id}/segment/{name}/downloaded", s.handleSegmentDownloaded)
		r.Get("/transcode/{session_id}/output-bound", s.handleOutputBound)
		r.Post("/admin/force-reload", s.handleForceReload)
		r.Post("/admin/reload-config", s.handleReloadConfig)
		r.Post("/admin/reprobe-capabilities", s.handleReprobeCapabilities)
		r.Get("/status", s.handleStatus)
	})
	return r
}

// handleDownloadPrepare validates and starts a prepared-download job.
func (s *Server) handleDownloadPrepare(w http.ResponseWriter, r *http.Request) {
	defer s.trackInFlight()()
	var req downloadprepare.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !downloadprepare.ValidArtifactID(req.ArtifactID) {
		http.Error(w, "a valid artifact_id is required", http.StatusBadRequest)
		return
	}
	if req.AudioRecipeRequested() && !req.StereoDownmixBoostRequested() {
		http.Error(w, "invalid audio recipe", http.StatusBadRequest)
		return
	}
	if req.PreparedTracksRequested() && !req.ValidPreparedTracks() {
		http.Error(w, "invalid track recipe", http.StatusBadRequest)
		return
	}
	if s.draining() {
		http.Error(w, errNodeDraining, http.StatusServiceUnavailable)
		return
	}
	slot, refused := s.admitPreparation(w, req)
	if refused {
		return
	}
	defer slot.release()

	cfg := s.watcher.Config()
	if cfg == nil {
		http.Error(w, "node not configured", http.StatusServiceUnavailable)
		return
	}
	// The source stays open for the whole preparation; a source the core
	// withdraws ends the encode.
	source, ok := s.openJobSource(w, r, req.SourceGrant)
	if !ok {
		return
	}
	defer func() { _ = source.Close() }()
	opts := req.TranscodeOpts(cfg.Playback.FFmpegPath, cfg.Playback.HWAccel, cfg.Playback.HWDevice, s.ffmpegSink)
	opts.InputPath = source.Input()
	opts.ToneMapSourceRevision = toneMapRevisionOnNode(opts.ToneMapSourceRevision, source)
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
	if slot == nil {
		// Admission was skipped because this artifact looked prepared or
		// in preparation, but what is there does not answer this request
		// (another recipe), so the node has to encode after all. Admitted
		// before the receipt goes, so a refusal leaves the artifact that is
		// there usable for the recipe it answers.
		admitted, refused := s.admitNewPreparation(w)
		if refused {
			return
		}
		slot = admitted
		defer slot.release()
	}
	if err := invalidateDownloadArtifactReceipt(outputPath); err != nil {
		http.Error(w, "failed to invalidate download artifact receipt", http.StatusInternalServerError)
		return
	}

	jobCtx, cancelJob := sourceContext(r.Context(), source)
	defer cancelJob()
	s.countJob(slot)
	defer s.activeJobs.Add(-1)
	if s.tracker != nil {
		trackingID := "download-" + req.ArtifactID
		s.tracker.Track(jobCtx, nodesessions.SessionInfo{
			SessionID:  trackingID,
			Type:       "download_prepare",
			CodecVideo: req.TargetCodecVideo,
			CodecAudio: req.TargetCodecAudio,
			Resolution: req.TargetResolution,
			StartedAt:  time.Now().UTC().Format(time.RFC3339),
		})
		defer s.tracker.Remove(context.WithoutCancel(jobCtx), trackingID)
	}

	progress := s.prepareProgress.begin(req.ArtifactID, req.TotalDuration)
	defer s.prepareProgress.end(req.ArtifactID, progress)
	opts.PrepareProgressSink = progress
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
	if req.PreparedTracksRequested() && !req.ValidPreparedTracks() {
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

// sessionIDPattern keeps a session id to one plain directory name under the
// transcode root. sessionOutputDir joins it unchecked, stop and the reapers
// RemoveAll that path, and FFmpeg expands % in the segment filename, so "." (the
// root), ".." (its parent), separators and % are all refused.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// validSessionID also refuses the directory holding prepared downloads when it
// sits under the same root, as it does by default. The comparison ignores case
// because a macOS node's volume usually does.
func (s *Server) validSessionID(sessionID string) bool {
	if !sessionIDPattern.MatchString(sessionID) {
		return false
	}
	reserved := s.artifactContainerName()
	return reserved == "" || !strings.EqualFold(sessionID, reserved)
}

func (s *Server) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	defer s.trackInFlight()()
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

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.metrics.Snapshot().RedactPaths()
	var artifacts *downloadstorage.Usage
	if usage, ok := s.artifactUsage(); ok {
		redacted := usage.RedactPath()
		artifacts = &redacted
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HealthResponse{
		Status:             s.healthStatus(),
		ActiveJobs:         s.activeJobs.Load(),
		CapabilitiesHash:   s.storedCapabilityHash(),
		System:             snapshot.System,
		GPU:                snapshot.GPU,
		Attribution:        snapshot.Attribution,
		SampledAt:          snapshot.SampledAt,
		Build:              buildinfo.Current(),
		Artifacts:          artifacts,
		NodeControlVersion: nodepool.NodeControlVersion,
		KeyRejected:        s.keyRejected != nil && s.keyRejected(),
	})
}

// artifactUsage answers from the background measurement of the artifact
// directory and starts a new one when the last is due. It never measures on
// the caller's goroutine: health must answer at the same speed whether or not
// the artifact volume is a hung network mount.
func (s *Server) artifactUsage() (downloadstorage.Usage, bool) {
	if s == nil || s.artifactProber == nil || s.artifactRoot == "" {
		return downloadstorage.Usage{}, false
	}
	return s.artifactProber.Current(s.artifactRoot, s.transcodeDir)
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
	// Progressive remux tokens bind execution to the stable stream_nodes row.
	// Do not let central commit work to this node until it knows that
	// identity; otherwise every committed GET would fail closed at serve time
	// with no different route for a replan to choose. Remuxes and theme
	// conversions resolve through the core and open the source it grants, so
	// a node wired without either takes neither.
	if s.nodeRowID != nil && s.core != nil && s.opener != nil {
		if nodeID, ok := s.nodeRowID(); ok && nodeID > 0 {
			info.TransportFeatures = []string{playback.TransportFeatureProgressiveRemuxExecutionV1,
				playback.TransportFeatureThemeAudioExecutionV1}
		}
	}
	info.TransportFeatures = append(info.TransportFeatures, playback.TransportFeaturePreparedTracksV1,
		playback.TransportFeatureTrickplayExtractV1, playback.TransportFeatureTrickplayExtractV2,
		playback.TransportFeatureHLSSubtitleRenditionV1, playback.TransportFeatureHLSRemuxSubtitleRenditionV1,
		playback.TransportFeatureHLSOnDemandSubtitleRenditionsV1)
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
	defer s.trackInFlight()()
	var req chapterthumbs.RemoteExtractRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeChapterThumbnailError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if s.draining() {
		writeChapterThumbnailError(w, http.StatusServiceUnavailable, chapterNodeUnavailable, errNodeDraining)
		return
	}
	source, err := s.openSource(r.Context(), req.SourceGrant)
	if err != nil {
		status, message := sourceFailure(r.Context(), err)
		writeChapterThumbnailError(w, status, chapterSourceReason(status), message)
		return
	}
	defer func() { _ = source.Close() }()
	ctx, cancel := sourceContext(r.Context(), source)
	defer cancel()

	cfg := s.watcher.Config()
	if cfg == nil {
		writeChapterThumbnailError(w, http.StatusServiceUnavailable, chapterNodeUnavailable, "node not configured")
		return
	}
	// A QSV or VAAPI extraction reserves a render device and runs ffmpeg on it,
	// so it takes the same exclusion a transcode does. Without this the route
	// leaves the gate looking idle — it never touches activeJobs either — and a
	// re-probe could smoke-encode beside a live extraction.
	if !s.gpu.beginWork() {
		writeChapterThumbnailError(w, http.StatusServiceUnavailable, chapterNodeUnavailable,
			"node is re-probing its hardware; retry shortly")
		return
	}
	defer s.gpu.endWork()
	frame, reason, err := chapterthumbs.ExtractFrame(ctx, chapterthumbs.FrameExtractOptions{
		InputPath:            source.Input(),
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

// chapterNodeUnavailable is the reason of a chapter extraction this node
// cannot run now. It is the reason the core's chapter client treats as the
// node's failure (chapterthumbs.IsInfrastructureRemoteFailure), so under
// prefer_transcode_nodes the core extracts the frame itself instead of
// recording the chapter as failed. Trickplay and media sampling use their own
// "node_unavailable" reason, which their core clients expect.
const chapterNodeUnavailable = chapterthumbs.RemoteNodeUnavailableReason

// chapterSourceReason is the reason a chapter extraction's source failure
// carries, by its status: a mount mismatch (409) or a core this node could not
// ask (503) is this node unavailable, which the core answers by extracting
// locally; a missing or refused grant (400) is an invalid request, which it
// records against the chapter.
func chapterSourceReason(status int) string {
	if status == http.StatusServiceUnavailable || status == http.StatusConflict {
		return chapterNodeUnavailable
	}
	return "invalid_request"
}

func writeChapterThumbnailError(w http.ResponseWriter, status int, reason string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(chapterthumbs.RemoteExtractErrorResponse{
		Reason: reason,
		// FFmpeg's log can name a relayed input, a capability on this host.
		Error: fspath.RedactRelayed(message),
	})
}

// requireBearer is middleware that admits only API servers and proxies: it
// checks the request's node signature (see nodeauth) against this node's own
// key. There is no legacy bearer and no shared secret: only a holder of this
// node's key can sign for it. The name predates signing and is kept because
// the route inventory classifies by it.
func (s *Server) requireBearer(next http.Handler) http.Handler {
	next = telemetry.TrustedHTTPHandler("worker", next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.nodeKey == "" || nodeauth.VerifySigned(r, s.nodeKey) != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleStart validates and starts a remote HLS transcode.
func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	defer s.trackInFlight()()
	var req TranscodeStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.SessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	if !s.validSessionID(req.SessionID) {
		http.Error(w, "invalid session_id", http.StatusBadRequest)
		return
	}
	if req.ThrottleSeconds < 0 {
		http.Error(w, "throttle_seconds must not be negative", http.StatusBadRequest)
		return
	}
	if err := validateAudioRecipeRequest(req); err != nil {
		http.Error(w, "invalid audio recipe", http.StatusBadRequest)
		return
	}
	if err := validateCopyFMP4RecipeRequest(req); err != nil {
		http.Error(w, "invalid copy-fmp4 recipe", http.StatusBadRequest)
		return
	}
	// A start that replaces a session this node runs (an audio switch, a
	// Jellyfin retry), or a new transport for a playback session it serves
	// (a quality switch or replan), continues a job the node holds, so the
	// cap never refuses it. Admitted before the source opens, so a refusal
	// costs the core nothing.
	slot, releaseAdmission, admitted := s.admitStart(req, time.Now())
	if !admitted {
		writeAtCapacity(w)
		return
	}
	defer releaseAdmission()
	// The session owns the source once it registers, across FFmpeg restarts
	// and seeks; until then every return closes it.
	source, ok := s.openJobSource(w, r, req.SourceGrant)
	if !ok {
		return
	}
	sourceOwned := false
	defer func() {
		if !sourceOwned {
			_ = source.Close()
		}
	}()
	// A draining node starts no new transcode, but it still replaces one it
	// runs: a Jellyfin audio-track switch re-posts the same session id here,
	// and refusing it would end the stream it is meant to keep alive. A start
	// admitted just before the drain began is counted by inFlight and then
	// activeJobs, so the drain waits for it like any other session.
	if s.draining() && !s.hostsSession(req.SessionID) {
		http.Error(w, errNodeDraining, http.StatusServiceUnavailable)
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
		InputPath:                  source.Input(),
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
		ToneMapSourceRevision:      toneMapRevisionOnNode(req.ToneMapSourceRevision, source),
		ToneMapDVConfigPresent:     req.ToneMapDVConfigPresent,
		ToneMapDVBLCompatIDPresent: req.ToneMapDVBLCompatIDPresent,
		ToneMapDVBLPresent:         req.ToneMapDVBLPresent,
		ToneMapDVRPUPresent:        req.ToneMapDVRPUPresent,
		VideoBitstreamFilter:       req.VideoBitstreamFilter,
		VideoSampleEntry:           req.VideoSampleEntry,
		VideoRange:                 req.VideoRange,
		CopyVideoMPEGTS:            req.CopyVideoMPEGTS,
		KeyframePlaylist:           req.KeyframePlaylist,
		KeyframePlanResume:         req.KeyframePlanResume,
		SeekSeconds:                req.SeekSeconds,
		StreamOriginSeconds:        req.StreamOriginSeconds,
		CopySeekAnchorResolved:     req.CopySeekAnchorResolved,
		StartSegmentNumber:         req.StartSegmentNumber,
		TargetResolution:           req.TargetResolution,
		TargetCodecVideo:           req.TargetCodecVideo,
		TargetCodecAudio:           req.TargetCodecAudio,
		CopyAudioCodec:             req.CopyAudioCodec,
		TargetAudioChannels:        req.TargetAudioChannels,
		TargetAudioBitrateKbps:     req.TargetAudioBitrateKbps,
		TargetBitrateKbps:          req.TargetBitrateKbps,
		SegmentDuration:            req.SegmentDuration,
		SegmentRetentionSeconds:    cfg.Playback.SegmentRetentionSeconds,
		FFmpegPath:                 cfg.Playback.FFmpegPath,
		HWAccel:                    s.jobHWAccel(req.HWAccel, req.ToneMapMode == tonemap.ModeHardware),
		// This node's configured device (or device list — StartTranscode
		// resolves it to one GPU), matching what reconstruction uses so fresh
		// and reconstructed sessions balance identically.
		HWDevice:           cfg.Playback.HWDevice,
		AudioTrackIndex:    req.AudioTrackIndex,
		RequireAudioTrack:  req.RequireAudioTrack,
		SubtitleTrackIndex: req.SubtitleTrackIndex,
		SubtitleBurnIn:     req.SubtitleBurnIn,
		SubtitleCodec:      req.SubtitleCodec,
		SubtitleRendition:  req.SubtitleRendition,
		TotalDuration:      req.TotalDuration,
		FastStart:          true,
		NodeType:           "transcode",
		ExecutionMode:      "transcode_node",
		FFmpegLogSink:      s.ffmpegSink,
	}
	opts.SubtitleRenditionTracks, opts.SubtitleRenditionSource = req.SubtitleRenditionTracks, renditionSourceOnNode(req.SubtitleRenditionSource, source)

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
	if s.shuttingDown {
		http.Error(w, "node is shutting down", http.StatusServiceUnavailable)
		return
	}
	if s.watcher.Config() != cfg {
		http.Error(w, "node configuration changed", http.StatusServiceUnavailable)
		return
	}

	// Hold the per-session lifecycle lock across teardown → spawn → register so a
	// concurrent reconstruct cannot run a second ffmpeg writer against this
	// session's output dir while we replace it.
	unlock := s.lockSessionLifecycle(req.SessionID)

	// Start the replacement before touching an existing session. A transient
	s.mu.Lock()
	_, hasExisting := s.sessions[req.SessionID]
	s.mu.Unlock()
	replacementDir := ""
	published := false
	defer func() {
		if replacementDir != "" && !published {
			_ = os.RemoveAll(replacementDir)
		}
	}()
	if hasExisting {
		if tempDir, tempErr := os.MkdirTemp(s.transcodeDir, req.SessionID+"-replacement-"); tempErr == nil {
			opts.OutputDir = tempDir
			replacementDir = tempDir
		} else {
			unlock()
			http.Error(w, "failed to prepare transcode replacement", http.StatusInternalServerError)
			return
		}
	}
	// spawn or validation failure must leave a healthy live session intact.
	var session *playback.TranscodeSession
	var err error
	pipeline := s.autoTranscodePipeline(r.Context(), opts)
	if req.RequireReady || req.AutoFallbackReady && pipeline.Enabled() {
		// Every attempt writes into opts.OutputDir, the replacement directory
		// when a live session exists, and a failed attempt is closed before
		// the next, so the live session is untouched until publication. Under
		// hw_accel=auto an early exit moves to the next safer path; otherwise
		// the legacy retry mirrors the API server's local transport: an early
		// death under VideoToolbox retries once in software (there is no
		// alternate render device to move to), so a hardware encoder session
		// this Mac cannot create does not fail clustered playback while CPU
		// encoding was available.
		session, err = playback.StartReadyTranscode(r.Context(), pipeline, playback.TranscodeStartup{
			Timeout:     TranscodeStartReadinessTimeout,
			LegacyRetry: playback.TranscodeStartupRetryAccelChange,
		})
	} else {
		session, err = playback.StartTranscode(r.Context(), opts)
	}
	if errors.Is(err, playback.ErrRelayedSubtitleBurnIn) {
		unlock()
		// This node reads the source through the core and cannot burn this
		// track in; the caller starts it on another node or itself.
		slog.WarnContext(r.Context(), "transcode start refused: subtitle burn-in needs the media file", "component", "transcodenode",
			"error", err, "session", req.SessionID, "playback_session_id", req.SessionID)
		http.Error(w, relayedBurnInRefusal, http.StatusConflict)
		return
	}
	var startupErr *playback.TranscodeStartupError
	if errors.As(err, &startupErr) {
		unlock()
		if r.Context().Err() != nil {
			// The API server abandoned the start; it stops the remote transcode itself.
			slog.InfoContext(r.Context(), "transcode start abandoned by caller", "component", "transcodenode", "error", err, "session", req.SessionID, "playback_session_id", req.SessionID)
			return
		}
		slog.ErrorContext(r.Context(), "transcode failed readiness check", "component", "transcodenode", "error", err, "session", req.SessionID, "playback_session_id", req.SessionID)
		http.Error(w, "transcode did not become ready", http.StatusInternalServerError)
		return
	}
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

	// The replacement has successfully spawned, so retire the old session and
	// publish the new one under the same ID.
	s.mu.Lock()
	if old, ok := s.sessions[req.SessionID]; ok {
		s.forgetSessionLocked(req.SessionID)
		s.mu.Unlock()
		_ = s.closeSessionOffGPU(old)
		staleDir := outputDir + ".stale-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := os.Rename(outputDir, staleDir); err == nil {
			go func() { _ = os.RemoveAll(staleDir) }()
		} else {
			_ = os.RemoveAll(outputDir)
		}
	} else {
		s.mu.Unlock()
	}

	effectiveHWAccel := session.Opts().HWAccel
	encoderHWAccel := session.Opts().EffectiveEncoderHWAccel()
	s.mu.Lock()
	s.registerSessionLocked(req.SessionID, session, identityOfStart(req), source)
	sourceOwned = true
	// A start is the API choosing this transport again (a Jellyfin retry or
	// audio switch reuses the id after stopping it), so it lifts the stop fence.
	delete(s.stoppedTranscodes, req.SessionID)
	published = true
	s.mu.Unlock()
	// Recorded under the lifecycle lock, so a stop that follows at once
	// cannot remove the record before it is written.
	if s.tracker != nil {
		startedAt := time.Now().UTC()
		s.tracker.Track(r.Context(), nodesessions.SessionInfo{
			SessionID:         req.SessionID,
			Type:              "transcode",
			CodecVideo:        req.TargetCodecVideo,
			CodecAudio:        req.TargetCodecAudio,
			Resolution:        req.TargetResolution,
			HWAccel:           encoderHWAccel,
			ToneMapMode:       string(session.Opts().ToneMapMode),
			StartedAt:         startedAt.Format(time.RFC3339),
			StartedAtUnixNano: startedAt.UnixNano(),
			AuthUserID:        req.UserID,
			ProfileID:         req.ProfileID,
			MediaFileID:       req.MediaFileID,
		})
	}
	unlock()
	s.countJob(slot)
	s.armSessionThrottler(session, req.ThrottleSeconds)

	w.WriteHeader(http.StatusAccepted)
	response := TranscodeStartResponse{
		SessionID:             req.SessionID,
		Status:                "started",
		HWAccel:               effectiveHWAccel,
		EncoderHWAccel:        encoderHWAccel,
		ToneMapMode:           session.Opts().ToneMapMode,
		AudioRecipeVersion:    req.AudioRecipeVersion,
		RequireAudioTrack:     session.Opts().RequireAudioTrack,
		CopyFMP4RecipeVersion: req.CopyFMP4RecipeVersion,
		ThrottleSeconds:       req.ThrottleSeconds,
		SoftwareVideoDecode:   session.Opts().SoftwareVideoDecode,
		KeyframePlaylist:      session.Opts().KeyframePlaylist,
		KeyframePlanned:       session.Opts().KeyframePlanned,
		KeyframePlanResume:    session.Opts().KeyframePlanResume,
		SubtitleRendition:     session.HasSubtitleRendition(),
		CopyVideoMPEGTS:       session.Opts().CopyVideoMPEGTS,
		CopyAudioCodec:        session.Opts().CopyAudioCodec,
	}
	response.SubtitleRenditionTracks = session.SubtitleRenditionTrackCount()
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.WarnContext(r.Context(), "write transcode start response", "component", "transcodenode", "session", req.SessionID, "error", err)
	}
}

// reconstructLost rebuilds a transcode session this node lost, to its own
// restart or an idle reap, from the recipe the core resolves for the
// transport id in the URL (purpose hls). The relay's stream token, when it
// forwarded one, goes to the core unread; the core verifies it, checks the
// deny markers and binds the recipe to this node.
//
// A transport this process stopped is refused before the core is asked. A
// session the core denies is errReconstructDenied, any other refusal is a
// genuine not-found (nil, nil), and a core the node cannot ask is
// errReconstructUnavailable.
//
// requestedSegment is the segment the client is fetching, or negative on the
// manifest path. Reconstruction, the resolve included, is single-flighted
// per session id so concurrent manifest and segment requests for the same
// lost session share one resolve and one ffmpeg.
func (s *Server) reconstructLost(r *http.Request, sessionID string, requestedSegment int) (*playback.TranscodeSession, error) {
	// Cheap early refusal; spawnReconstruct re-checks under the lifecycle lock.
	if s.transcodeStopped(sessionID) {
		return nil, errTranscodeStopped
	}
	v, err, _ := s.reconstructGroup.Do(sessionID, func() (any, error) {
		// A concurrent reconstruct (or a fresh start) may already have registered the
		// session; serve it rather than spawning a duplicate ffmpeg.
		s.mu.RLock()
		existing, ok := s.sessions[sessionID]
		s.mu.RUnlock()
		if ok {
			return existing, nil
		}
		resolution, err := s.resolve(r, sessionID, nodecontrol.ResolvePurposeHLS)
		if err != nil {
			switch reason := nodecontrol.Reason(err); {
			case reason == nodecontrol.ResolveDenied:
				return (*playback.TranscodeSession)(nil), errReconstructDenied
			case reason != "":
				// Every refusal carries its reason; the rest are failures to ask.
				return (*playback.TranscodeSession)(nil), nil
			}
			slog.WarnContext(r.Context(), "transcode node reconstruct: recipe unavailable", "component", "transcodenode", "error", err,
				"session", sessionID, "playback_session_id", sessionID)
			return (*playback.TranscodeSession)(nil), fmt.Errorf("%w: %w", errReconstructUnavailable, err)
		}
		return s.spawnReconstruct(r, sessionID, requestedSegment, resolution)
	})
	if session, _ := v.(*playback.TranscodeSession); session != nil {
		return session, nil
	}
	return nil, err
}

// spawnReconstruct re-spawns ffmpeg for a lost session from the core's
// resolution and registers it in the live map. It is only ever called inside
// the per-session single-flight in reconstructLost, so it is the sole writer
// racing to register sessionID. Returns nil if the spawn fails or the slot
// wait is canceled.
func (s *Server) spawnReconstruct(r *http.Request, sessionID string, requestedSegment int, resolution *nodecontrol.Resolution) (*playback.TranscodeSession, error) {
	defer s.trackInFlight()()
	card := resolution.Recipe
	if !s.validSessionID(sessionID) {
		slog.WarnContext(r.Context(), "transcode node reconstruct session id rejected", "component", "transcodenode", "session", sessionID)
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
	source, err := s.openSource(r.Context(), resolution.SourceGrant)
	if err != nil {
		return nil, reconstructSourceError(r.Context(), sessionID, err)
	}
	// The session owns the source once it registers, across FFmpeg restarts
	// and seeks; until then every return closes it.
	sourceOwned := false
	defer func() {
		if !sourceOwned {
			_ = source.Close()
		}
	}()
	opts := s.reconstructOpts(sessionID, card, cfg, source)
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
	// A draining node that has reported itself drained rebuilds no lost
	// session: whoever read that may stop it. The client's next plan puts the
	// session on another node.
	if s.shuttingDown || s.reportedDrained() {
		return nil, nil
	}
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
	// A stop records its fence under this lock, so a request that found no job
	// because the stop was tearing it down sees the fence here.
	if s.transcodeStopped(sessionID) {
		return nil, errTranscodeStopped
	}

	// Pace the cold-start burst only after this session owns its lifecycle lock.
	// A waiter behind an in-flight start must not consume capacity that unrelated
	// sessions need to reconstruct.
	release, ok := s.acquireReconstructSlot(r.Context())
	if !ok {
		return nil, r.Context().Err()
	}
	defer release()
	// Past admission the rebuild serves every request waiting on it, so its
	// probes and its start must not end with the request that triggered it.
	rebuildCtx := context.WithoutCancel(r.Context())

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

	session, err := s.startReconstructProcess(rebuildCtx, r, sessionID, opts)
	if err != nil {
		return nil, err
	}

	// Yield to a winner registered by another path; close only the duplicate ffmpeg,
	// never the shared output directory the winner is actively serving.
	s.mu.Lock()
	if existing, ok := s.sessions[sessionID]; ok {
		s.mu.Unlock()
		_ = session.CloseProcess()
		return existing, nil
	}
	s.registerSessionLocked(sessionID, session, identityOfResolution(resolution), source)
	sourceOwned = true
	s.mu.Unlock()
	s.activeJobs.Add(1)
	s.armSessionThrottler(session, card.ThrottleSeconds)

	s.trackRebuilt(r.Context(), sessionID, session, resolution)

	slog.InfoContext(r.Context(), "transcode node session reconstructed", "component", "transcodenode",
		"session", sessionID, "playback_session_id", sessionID,
		"requested_segment", requestedSegment, "start_segment_number", opts.StartSegmentNumber)
	return session, nil
}

// reconstructSourceError is a rebuild's failure to open the source the core
// granted: a refused grant or a mount that does not show the source is a
// session this node cannot rebuild (nil, answered as not found), anything
// else a core it could not ask.
func reconstructSourceError(ctx context.Context, sessionID string, err error) error {
	slog.WarnContext(ctx, "transcode node reconstruct input unavailable", "component", "transcodenode", "session", sessionID, "error", err)
	if sourceRefused(err) || errors.Is(err, nodesource.ErrMountMismatch) {
		return nil
	}
	return fmt.Errorf("%w: %w", errReconstructUnavailable, err)
}

// trackRebuilt records a rebuilt session under the playback identity the core
// resolved, from the session's own start when the core named one.
func (s *Server) trackRebuilt(ctx context.Context, sessionID string, session *playback.TranscodeSession, resolution *nodecontrol.Resolution) {
	if s.tracker == nil {
		return
	}
	card := resolution.Recipe
	startedAt, startedAtSource := resolvedStart(resolution)
	s.tracker.Track(ctx, nodesessions.SessionInfo{
		SessionID:         sessionID,
		Type:              "transcode",
		CodecVideo:        card.TargetCodecVideo,
		CodecAudio:        card.TargetCodecAudio,
		Resolution:        card.TargetResolution,
		HWAccel:           session.Opts().EffectiveEncoderHWAccel(),
		ToneMapMode:       string(session.Opts().ToneMapMode),
		StartedAt:         startedAt.Format(time.RFC3339),
		StartedAtUnixNano: startedAt.UnixNano(),
		StartedAtSource:   startedAtSource,
		AuthUserID:        resolution.UserID,
		ProfileID:         resolution.ProfileID,
		MediaFileID:       resolution.MediaFileID,
	})
}

// reconstructOpts is the FFmpeg options of a rebuild of card under sessionID,
// reading source.
func (s *Server) reconstructOpts(sessionID string, card playback.RecipeCard, cfg *config.Config, source nodesource.Source) playback.TranscodeOpts {
	opts := card.TranscodeOpts(s.sessionOutputDir(sessionID), cfg.Playback.FFmpegPath, s.ffmpegSink)
	opts.InputPath = source.Input()
	opts.SubtitleRenditionSource = renditionSourceOnNode(opts.SubtitleRenditionSource, source)
	opts.ToneMapSourceRevision = toneMapRevisionOnNode(opts.ToneMapSourceRevision, source)
	opts.SessionID = sessionID
	opts.Reconstructed = true
	// Recipe cards preserve the original launch tuning, but reconstruction must
	// retain the normal manifest cushion rather than the fresh-start fast path.
	opts.FastStart = false
	// Re-resolve environment-specific encode knobs from this node's live config; the
	// recipe deliberately omits HWAccel/HWDevice so an operator change applies on
	// rebuild. Run as a transcode node, not integrated (card.TranscodeOpts defaults).
	opts.HWAccel = cfg.Playback.HWAccel
	opts.HWDevice = cfg.Playback.HWDevice
	opts.SegmentRetentionSeconds = cfg.Playback.SegmentRetentionSeconds
	opts.NodeType = "transcode"
	opts.ExecutionMode = "transcode_node"
	return opts
}

// startReconstructProcess starts a rebuild's FFmpeg. Under hw_accel=auto the
// reconstruct walks the same safer paths as a fresh start in this output
// directory, keeping a slow process rather than duplicating it. Under an
// explicit backend it starts once, and when the GPU decoder rejects the
// source (a card minted where the GPU decodes it) it moves to CPU decoding.
// An accel StartupRetryHWAccel would change (VideoToolbox, or QSV/VAAPI
// without a render device) is not waited on by the pipeline: it keeps the
// software-encode retry below, which has its own wait.
func (s *Server) startReconstructProcess(rebuildCtx context.Context, r *http.Request, sessionID string, opts playback.TranscodeOpts) (*playback.TranscodeSession, error) {
	pipeline := s.autoTranscodePipeline(rebuildCtx, opts)
	retryAccel := opts.HWAccel
	if !pipeline.Enabled() {
		retryAccel = playback.StartupRetryHWAccel(opts)
	}
	var (
		session *playback.TranscodeSession
		err     error
	)
	if retryAccel != opts.HWAccel {
		session, err = playback.StartTranscode(rebuildCtx, opts)
	} else {
		session, err = playback.StartReconstructTranscode(rebuildCtx, pipeline, playback.TranscodeStartup{Timeout: playback.ManifestStartupTimeout})
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "transcode node reconstruct start failed", "component", "transcodenode", "error", err,
			"session", sessionID, "playback_session_id", sessionID)
		return nil, err
	}

	// Readiness is normally the caller's concern, but a VideoToolbox session
	// can die at encoder init (e.g. a session the hardware cannot create at
	// these dimensions), and registering the dead session would serve this
	// media as permanently missing. Outside the automatic pipeline, mirror
	// handleStart's software retry for the accel StartupRetryHWAccel would
	// change. The rebuild reuses the lost session's output directory, so wait
	// for a manifest this process wrote: the previous one would report a
	// process that died at startup as ready and skip the retry (#4099).
	if retryAccel != opts.HWAccel {
		if _, waitErr := session.WaitForGenerationManifest(playback.ManifestStartupTimeout); waitErr != nil {
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
				session, err = playback.StartTranscode(rebuildCtx, retryOpts)
				if err != nil {
					slog.ErrorContext(r.Context(), "transcode node reconstruct retry failed", "component", "transcodenode", "error", err,
						"session", sessionID, "playback_session_id", sessionID)
					return nil, err
				}
				if _, retryErr := session.WaitForGenerationManifest(playback.ManifestStartupTimeout); retryErr != nil {
					_ = session.Close()
					slog.ErrorContext(r.Context(), "reconstructed software retry failed readiness check", "component", "transcodenode", "error", retryErr,
						"session", sessionID, "playback_session_id", sessionID)
					return nil, retryErr
				}
			}
		}
	}
	return session, nil
}

// Shutdown prevents new sessions from registering, then closes every session
// owned by this node and its source. Reconstruction recipes are deliberately
// retained: after the process restarts, a still-authorized client may rebuild
// its session.
func (s *Server) Shutdown(ctx context.Context) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if s.shuttingDown {
		return nil
	}
	s.shuttingDown = true

	type victim struct {
		id      string
		session *playback.TranscodeSession
	}
	s.mu.Lock()
	victims := make([]victim, 0, len(s.sessions))
	for id, session := range s.sessions {
		victims = append(victims, victim{id: id, session: session})
	}
	for _, victim := range victims {
		s.forgetSessionLocked(victim.id)
	}
	progressive := maps.Clone(s.progressiveRemuxes)
	s.mu.Unlock()

	for _, request := range progressive {
		request.cancel()
	}
	var errs []error
	for _, victim := range victims {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
		}
		if err := s.closeSessionOffGPU(victim.session); err != nil {
			errs = append(errs, fmt.Errorf("close session %s: %w", victim.id, err))
		}
		if s.tracker != nil {
			s.tracker.Remove(ctx, victim.id)
		}
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, progressiveRemuxShutdownTimeout)
	defer cancelWait()
	for id, request := range progressive {
		if err := waitForProgressiveRemuxShutdown(waitCtx, id, request); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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

// handleRemux executes a progressive remux on demand. The node signature
// authenticates the proxy; the core resolves every GET and HEAD (purpose
// remux): it verifies the relay's stream token, which binds the media recipe
// and the exact transcode-execution and proxy-egress route, checks the deny
// markers and the stored authority a stop withdraws, and grants the source.
func (s *Server) handleRemux(w http.ResponseWriter, r *http.Request) {
	defer s.trackInFlight()()
	cfg := s.watcher.Config()
	if cfg == nil {
		http.Error(w, "node not configured", http.StatusServiceUnavailable)
		return
	}
	transportID := chi.URLParam(r, "session_id")
	// A transport this process stopped is refused before the core is asked.
	if s.progressiveRemuxStopped(transportID) {
		http.Error(w, "session stopped", http.StatusGone)
		return
	}
	// So are the local refusals admission below would make anyway, so a
	// request this node cannot serve costs the core no resolve or source
	// open. Admission checks them again under its locks.
	if status, message, refused := s.remuxRefusedLocally(r.Method, transportID); refused {
		http.Error(w, message, status)
		return
	}
	resolution, err := s.resolve(r, transportID, nodecontrol.ResolvePurposeRemux)
	if err != nil {
		writeRemuxRefusal(w, r, transportID, err)
		return
	}
	claims := resolution.Recipe.ToClaims()
	theme := claims.PlayMethod == streamtoken.PlayMethodThemeAAC
	playbackSessionID := resolution.PlaybackSessionID
	if playbackSessionID == "" {
		playbackSessionID = transportID
	}
	// A GET for a playback session this node serves, or served within the
	// idle lifetime, is a viewer seeking or coming back: it continues a job
	// the node holds. Any other is new work and takes its slot here, before
	// the source opens, so a refusal costs the core no open, and before the
	// remux is registered, so a request the cap turns away is never what
	// another request for the same playback session continues.
	// A HEAD starts and registers nothing, so it holds neither a slot nor
	// the allowance a GET for the same playback session may need.
	var (
		slot      *jobSlot
		overlap   *continuation
		continues bool
	)
	if r.Method != http.MethodHead {
		overlap, continues = s.claimContinuation(playbackSessionID, time.Now())
		defer overlap.release()
		if !continues {
			admitted, ok := s.admitJob()
			if !ok {
				writeAtCapacity(w)
				return
			}
			slot = admitted
			defer slot.release()
		}
	}
	// A GET opens its source once, before admission, and holds it for the
	// request; a source the core withdraws ends it. HEAD reads no media.
	var source nodesource.Source
	if r.Method != http.MethodHead {
		opened, ok := s.openJobSource(w, r, resolution.SourceGrant)
		if !ok {
			return
		}
		source = opened
		defer func() { _ = source.Close() }()
	}
	seekSeconds := 0.0
	if rawSeek := r.URL.Query().Get("seek"); rawSeek != "" {
		parsed, parseErr := strconv.ParseFloat(rawSeek, 64)
		if parseErr != nil || parsed < 0 {
			http.Error(w, "invalid seek", http.StatusBadRequest)
			return
		}
		seekSeconds = parsed
	}

	// Admit against one stable configuration generation. Force reload takes the
	// exclusive side, then cancels every request registered below before it
	// returns; a request that waited through a reload must retry with a fresh
	// route instead of starting FFmpeg from the stale cfg pointer above.
	s.reloadMu.RLock()
	if s.shuttingDown {
		s.reloadMu.RUnlock()
		http.Error(w, "node is shutting down", http.StatusServiceUnavailable)
		return
	}
	// A draining node still serves its viewers' reconnects and seeks, which
	// each arrive as a fresh GET, until it has reported itself drained.
	if r.Method != http.MethodHead && s.reportedDrained() {
		s.reloadMu.RUnlock()
		http.Error(w, errNodeDraining, http.StatusServiceUnavailable)
		return
	}
	if s.watcher.Config() != cfg {
		s.reloadMu.RUnlock()
		http.Error(w, "node configuration changed", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodHead {
		s.reloadMu.RUnlock()
		contentType := playback.RemuxContentType(claims.AudioOnly)
		if contentType == "" {
			contentType = "video/mp4"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store, max-age=0")
		w.WriteHeader(http.StatusOK)
		return
	}

	sourceCtx, cancelSource := sourceContext(r.Context(), source)
	defer cancelSource()
	ctx, cancel := context.WithCancel(sourceCtx)
	requestID := s.progressiveRemuxSequence.Add(1)
	abort := make(chan struct{})
	stop := sync.OnceFunc(func() {
		cancel()
		close(abort)
	})
	done := make(chan struct{})
	unlock := s.lockSessionLifecycle(transportID)
	s.mu.Lock()
	now := time.Now()
	s.pruneStopFencesLocked(now)
	if s.stoppedProgressiveRemuxes[transportID].After(now) {
		s.mu.Unlock()
		unlock()
		s.reloadMu.RUnlock()
		cancel()
		http.Error(w, "session stopped", http.StatusGone)
		return
	}
	if _, active := s.progressiveRemuxes[transportID]; active {
		s.mu.Unlock()
		unlock()
		s.reloadMu.RUnlock()
		cancel()
		http.Error(w, "session already active", http.StatusConflict)
		return
	}
	s.progressiveRemuxes[transportID] = progressiveRemuxRequest{
		id: requestID, playbackSessionID: playbackSessionID, cancel: stop, done: done,
	}
	s.mu.Unlock()
	unlock()
	s.reloadMu.RUnlock()
	defer func() {
		cancel()
		s.mu.Lock()
		if current, ok := s.progressiveRemuxes[transportID]; ok && current.id == requestID {
			delete(s.progressiveRemuxes, transportID)
		}
		s.mu.Unlock()
		close(done)
	}()

	// The remux is registered above, so it counts as running from here: its
	// slot when it was new work, the overlap allowance when it continued.
	s.countJob(slot)
	overlap.release()
	defer s.activeJobs.Add(-1)
	s.noteRemux(playbackSessionID, time.Now())
	defer func() { s.noteRemux(playbackSessionID, time.Now()) }()
	if s.tracker != nil {
		startedAt, startedAtSource := resolvedStart(resolution)
		trackerType := "remux"
		if theme {
			trackerType = "theme_audio"
		}
		s.tracker.Track(ctx, nodesessions.SessionInfo{
			SessionID: playbackSessionID, Type: trackerType,
			CodecAudio: claims.TargetCodecAudio, StartedAt: startedAt.Format(time.RFC3339), StartedAtUnixNano: startedAt.UnixNano(),
			StartedAtSource: startedAtSource, AuthUserID: resolution.UserID, ProfileID: resolution.ProfileID, MediaFileID: resolution.MediaFileID,
		})
		defer s.tracker.Remove(context.WithoutCancel(ctx), playbackSessionID)
	}

	request := r.WithContext(ctx)
	if theme {
		// A theme conversion is a transfer attributed to its viewer, not a
		// playback session.
		streamtelemetry.Attach(ctx, streamtelemetry.Attachment{Subject: streamtelemetry.UserSubject(resolution.UserID), ProfileID: resolution.ProfileID})
	} else if streamtelemetry.Observing(ctx) {
		streamtelemetry.Attach(ctx, streamtelemetry.Attachment{SessionID: playbackSessionID})
	}
	if err := playback.ServeRemuxWithOptions(w, request, source.Input(), "mp4", seekSeconds, claims.TranscodeAudio, claims.AudioTrackIndex, claims.DVProfile, playback.RemuxServeOptions{
		DVMode: playback.RemuxDVMode(claims.RemuxDVMode), FFmpegPath: cfg.Playback.FFmpegPath,
		DropResumeLeadingPictures: claims.RemuxResumeLeadingPictureDrop,
		StreamOriginSeconds:       playback.RemuxStreamOrigin(r.URL.Query()),
		ContentType:               playback.RemuxContentType(claims.AudioOnly), AudioOnly: claims.AudioOnly,
		SourceAudioChannels: claims.SourceAudioChannels, TargetAudioChannels: claims.TargetAudioChannels,
		TargetAudioBitrateKbps: claims.TargetAudioBitrateKbps, CopyAudioCodec: claims.CopyAudioCodec,
		Abort: abort,
	}); err != nil && !errors.Is(err, context.Canceled) {
		slog.WarnContext(ctx, "progressive remux ended with an error", "component", "transcodenode", "error", err,
			"session", transportID, "playback_session_id", playbackSessionID)
	}
}

// remuxRefusedLocally reports the refusal handleRemux's admission would give
// a request for transportID from this node's own state: a node shutting down,
// a GET on a node that has reported itself drained, or a GET for a transport
// that already has a request running. It reads that state without holding it,
// so admission's check stays the authoritative one.
func (s *Server) remuxRefusedLocally(method, transportID string) (status int, message string, refused bool) {
	s.reloadMu.RLock()
	shuttingDown := s.shuttingDown
	s.reloadMu.RUnlock()
	if shuttingDown {
		return http.StatusServiceUnavailable, "node is shutting down", true
	}
	if method == http.MethodHead {
		return 0, "", false
	}
	if s.reportedDrained() {
		return http.StatusServiceUnavailable, errNodeDraining, true
	}
	s.mu.RLock()
	_, active := s.progressiveRemuxes[transportID]
	s.mu.RUnlock()
	if active {
		return http.StatusConflict, "session already active", true
	}
	return 0, "", false
}

// stopFencePruneInterval bounds how often a stop or remux registration sweeps
// the fence maps under mu. Every fence lookup compares its expiry with the
// current time, so an expired entry left until the next sweep is inert.
const stopFencePruneInterval = time.Minute

func (s *Server) pruneStopFencesLocked(now time.Time) {
	if now.Sub(s.lastStopFencePrune) < stopFencePruneInterval {
		return
	}
	s.lastStopFencePrune = now
	for id, expiresAt := range s.stoppedProgressiveRemuxes {
		if !expiresAt.After(now) {
			delete(s.stoppedProgressiveRemuxes, id)
		}
	}
	for id, expiresAt := range s.stoppedTranscodes {
		if !expiresAt.After(now) {
			delete(s.stoppedTranscodes, id)
		}
	}
}

// errTranscodeStopped is reconstructLost refusing a transport the API
// stopped on this node; callers answer it like a denied session.
var errTranscodeStopped = errors.New("transcode transport stopped")

// transcodeStopped reports whether the API stopped transportID on this node
// within the last stream-token lifetime and no start has reused it since.
func (s *Server) transcodeStopped(transportID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stoppedTranscodes[transportID].After(time.Now())
}

// progressiveRemuxStopped reports whether the API stopped the progressive
// remux transportID on this node within the last stream-token lifetime.
func (s *Server) progressiveRemuxStopped(transportID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stoppedProgressiveRemuxes[transportID].After(time.Now())
}

// releaseSupersededLocked reports whether the HLS job under transportID is
// not the one a release (a stop with fence=0) was sent for, so the release
// must leave it running. A release that names a start ends only that start's
// job. One that names none, a retire of the node a transport left, ends the
// job it found on arrival or a rebuild published while it waited: that
// rebuild resolved before the move off this node committed, since the core
// refuses one whose route names another node. It leaves a job a start
// published while it waited. A stop that ends the session for good is fenced
// and ends any job. The caller holds mu.
func (s *Server) releaseSupersededLocked(transportID, startID string, arrived *playback.TranscodeSession) bool {
	session, ok := s.sessions[transportID]
	if !ok {
		return false
	}
	identity := s.identities[transportID]
	if startID != "" {
		return identity.startID != startID
	}
	return session != arrived && !identity.rebuilt
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	fenced := r.URL.Query().Get(playback.TranscodeStopFenceQueryParam) != "0"
	startID := r.URL.Query().Get(playback.TranscodeStopStartIDQueryParam)
	// A release is sent in the background and can arrive after a newer start
	// of the same transport id. Note the job it found on arrival: a start
	// published while it waits for the lifecycle lock is newer than it.
	var arrived *playback.TranscodeSession
	if !fenced && startID == "" {
		s.mu.RLock()
		arrived = s.sessions[sessionID]
		s.mu.RUnlock()
	}

	// Serialize against in-flight starts and reconstructs. A RequireReady
	// start registers its job only after the readiness wait; a stop racing
	// that wait (the API rolling back a timed-out start) would otherwise miss
	// the map and 404, orphaning the just-spawned ffmpeg until the idle
	// reaper finds it. Blocking here until the start registers turns that
	// miss into a normal teardown. A release does the same only for the start
	// it names: one without a start id leaves the job such a start publishes,
	// though not a rebuild (releaseSupersededLocked).
	unlock := s.lockSessionLifecycle(sessionID)
	defer unlock()

	// The core deletes the transport's stored recipe before it calls here;
	// the node keeps only its in-memory fences.
	s.mu.Lock()
	now := time.Now()
	s.pruneStopFencesLocked(now)
	progressive, progressiveFound := s.progressiveRemuxes[sessionID]
	if !fenced && !progressiveFound && s.releaseSupersededLocked(sessionID, startID, arrived) {
		s.mu.Unlock()
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	// An HLS job is tracked under the transport id it started with, which for a
	// native v3 transcode is not its card's SessionID. Only a progressive remux
	// still running here is tracked under the viewer's playback session id; one
	// that has ended already removed its own record.
	trackerSessionID := sessionID
	if progressiveFound && progressive.playbackSessionID != "" {
		trackerSessionID = progressive.playbackSessionID
	}
	// A stop may reach a progressive remux between its GETs, when none runs
	// here, and the node cannot tell its transport from an HLS one: every
	// fenced stop fences both.
	if progressiveFound || fenced {
		if s.stoppedProgressiveRemuxes == nil {
			s.stoppedProgressiveRemuxes = make(map[string]time.Time)
		}
		s.stoppedProgressiveRemuxes[sessionID] = now.Add(playback.MaxTokenTTL)
	}
	// Fence every stopped transport, found or not: a segment request already in
	// flight, or one carrying a retired transport's token, would otherwise rebuild
	// the job from its stream token after this stop returns. A release (the API
	// moving the transport to another node) keeps the id rebuildable here.
	if fenced {
		if s.stoppedTranscodes == nil {
			s.stoppedTranscodes = make(map[string]time.Time)
		}
		s.stoppedTranscodes[sessionID] = now.Add(playback.MaxTokenTTL)
	}
	session, ok := s.sessions[sessionID]
	found := ok || progressiveFound
	if ok {
		s.forgetSessionLocked(sessionID)
	}
	s.mu.Unlock()
	if progressiveFound {
		progressive.cancel()
	}

	if ok {
		if err := s.closeSessionOffGPU(session); err != nil {
			slog.ErrorContext(r.Context(), "close transcode session", "component", "transcodenode", "error", err, "session", sessionID, "playback_session_id", sessionID)
		}
	}

	if ok {
		if err := os.RemoveAll(s.sessionOutputDir(sessionID)); err != nil {
			slog.WarnContext(r.Context(), "remove transcode session directory", "component", "transcodenode", "session", sessionID, "error", err)
		}
	}

	if s.tracker != nil {
		s.tracker.Remove(r.Context(), trackerSessionID)
	}
	var shutdownErr error
	if progressiveFound {
		waitCtx, cancelWait := context.WithTimeout(r.Context(), progressiveRemuxShutdownTimeout)
		shutdownErr = waitForProgressiveRemuxShutdown(waitCtx, sessionID, progressive)
		cancelWait()
	}
	if shutdownErr != nil {
		http.Error(w, "progressive remux shutdown unavailable", http.StatusServiceUnavailable)
		return
	}
	if !found {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")

	// Lookup and liveness refresh happen atomically so the idle reaper can
	// never unregister the job between them and tear down a session this
	// request is about to serve from. A live session is served without asking
	// the core: every relay checks the deny marker before it forwards.
	session, ok := s.acquireSessionTouched(sessionID)
	if !ok {
		// Lost the in-memory session (this node restarted): rebuild it from the
		// recipe the core resolves. The manifest path carries no segment
		// context, so reconstruct at the recipe's original start position.
		var reconstructErr error
		session, reconstructErr = s.reconstructLost(r, sessionID, -1)
		if session == nil {
			writeReconstructFailure(w, reconstructErr)
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
	if errors.Is(err, playback.ErrManifestQueryTooLong) {
		http.Error(w, "playlist request query too long", http.StatusBadRequest)
		return
	}
	if errors.Is(err, playback.ErrSubtitleRenditionNotFound) {
		http.Error(w, "subtitle rendition not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "get manifest", "component", "transcodenode", "error", err, "session", sessionID, "playback_session_id", sessionID)
		http.Error(w, "manifest not ready", http.StatusServiceUnavailable)
		return
	}
	playback.WarnIfPlaylistOversized(r.Context(), "transcodenode", sessionID, manifest)

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store, no-transform, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	// Same reason as the API's manifest handler. The API relays a playlist
	// requested without a stream token as-is, headers included, so the length
	// has to start here.
	w.Header().Set("Content-Length", strconv.Itoa(len(manifest)))
	w.Write(manifest)
}

func (s *Server) handleSegment(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	name := chi.URLParam(r, "name")

	// Lookup and liveness refresh happen atomically so the idle reaper can
	// never unregister the job between them and tear down a session this
	// request is about to serve from. A live session is served without asking
	// the core: every relay checks the deny marker before it forwards.
	session, ok := s.acquireSessionTouched(sessionID)
	if !ok {
		// Lost the in-memory session (this node restarted): rebuild it from the
		// recipe the core resolves, seeked to the segment the client is requesting
		// so playback resumes near its position instead of restarting from the start.
		requestedSegment := -1
		if n, parseErr := playback.ParseSegmentNumber(name); parseErr == nil {
			requestedSegment = n
		} else if n, ok := playback.ParseSubtitleSegmentNumber(name); ok {
			requestedSegment = n
		}
		var reconstructErr error
		session, reconstructErr = s.reconstructLost(r, sessionID, requestedSegment)
		if session == nil {
			writeReconstructFailure(w, reconstructErr)
			return
		}
		// A reconstruct that yielded to a concurrently registered winner has
		// not recorded this hit; count it so the reaper sees the liveness.
		s.touchSession(sessionID)
	}
	s.attachTelemetrySession(r, sessionID)

	if _, ok := playback.ParseSubtitleSegmentNumber(name); ok {
		// Subtitle rendition segments never wait out or restart the video
		// pipeline: they are answered from the cues already on disk, or read
		// through the source's index.
		session.ServeSubtitleSegment(w, r, name)
		return
	}
	segmentLease, err := playback.OpenSegmentWithRecovery(r.Context(), session, name, playback.SegmentRecoveryOptions{
		SessionID: sessionID,
		Component: "transcodenode",
		Restart: func(ctx context.Context, segNum int, seenGeneration time.Time) (playback.SegmentRecoveryTarget, bool, error) {
			return s.restartSegmentLocked(ctx, sessionID, session, segNum, seenGeneration)
		},
	})
	if err != nil {
		if r.Context().Err() != nil {
			// net/http also ends the context of a client that only
			// half-closed and is still reading. Answer it as a timed-out
			// wait would: writing nothing reaches it as an empty 200.
			err = playback.ErrSegmentNotFound
		}
		if isToneMapRecipeError(err) {
			writeToneMapRecipeError(w, err)
			return
		}
		http.Error(w, "segment not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	proxied := r.Header.Get(transcodeproxy.RequestHeader) == "1"
	if proxied {
		w.Header().Set(transcodeproxy.GenerationHeader, segmentLease.GenerationToken)
	}
	defer func() { _ = segmentLease.Close() }()
	sw := httpstream.NewRollingDeadlineWriter(w)
	http.ServeContent(sw, r, segmentLease.Info.Name(), segmentLease.Info.ModTime(), segmentLease.File)
	if !proxied && r.Method == http.MethodGet &&
		sw.CompletedFullResponse(segmentLease.Info.Size()) {
		if segmentNumber, parseErr := playback.ParseSegmentNumber(name); parseErr == nil {
			session.ReportSegmentDownloadedForGeneration(segmentNumber, segmentLease.Generation)
		}
	}
}

// handleSegmentDownloaded records completion only after the central API has
// delivered the full segment to its downstream playback client. The generation
// returned with the original segment response prevents a delayed acknowledgement
// from advancing a replacement FFmpeg timeline.
func (s *Server) handleSegmentDownloaded(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	name := chi.URLParam(r, "name")
	segmentNumber, err := playback.ParseSegmentNumber(name)
	if err != nil {
		http.Error(w, "invalid segment", http.StatusBadRequest)
		return
	}
	generationToken := r.Header.Get(transcodeproxy.GenerationHeader)
	if generationToken == "" {
		http.Error(w, "invalid segment generation", http.StatusBadRequest)
		return
	}

	session, ok := s.acquireSessionTouched(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	session.ReportSegmentDownloadedForGenerationToken(segmentNumber, generationToken)
	w.WriteHeader(http.StatusNoContent)
}

// outputBoundResponse is the node's answer to whether a transport's client is
// waiting on the encoder.
type outputBoundResponse struct {
	OutputBound bool `json:"output_bound"`
}

// handleOutputBound tells the core whether the client of a transport this node
// runs is waiting on its encoder: a restart is replacing FFmpeg, or the client
// has fetched all produced segments but at most the newest
// (playback.TranscodeSession.OutputBoundsClient). A stall the client reports
// then was paced by this node's encoder, not the link, and the core learns no
// bandwidth limit from it. A progressive remux has no segment lead to compare
// and answers false. A transport the node does not run is 404, and the read
// neither reconstructs one nor counts as client activity.
func (s *Server) handleOutputBound(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	s.mu.RLock()
	session, transcoding := s.sessions[sessionID]
	_, remuxing := s.progressiveRemuxes[sessionID]
	s.mu.RUnlock()
	if !transcoding && !remuxing {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(outputBoundResponse{OutputBound: transcoding && session.OutputBoundsClient()}); err != nil {
		slog.WarnContext(r.Context(), "write output-bound response", "component", "transcodenode", "session", sessionID, "error", err)
	}
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
	if err := s.teardownForForceReload(r.Context()); err != nil {
		http.Error(w, "force reload teardown failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.InfoContext(r.Context(), "transcode force reload completed", slog.String("component", "transcodenode"))
	w.WriteHeader(http.StatusNoContent)
}

// teardownForForceReload asks the core to withdraw every recipe bound to this
// node, then ends every session and progressive remux it runs, so no buffered
// request rebuilds one this reload killed from a stored recipe.
// The revocation runs here, under the reload gate, rather than on the core
// before it calls: a revocation followed by a failed call would end the
// sessions of a node that never reloaded. A failed revocation fails the
// reload, which the operator retries; the sessions are torn down regardless.
func (s *Server) teardownForForceReload(ctx context.Context) error {
	var authorityErr error
	if s.core != nil {
		if err := s.core.RevokeRecipes(ctx); err != nil {
			authorityErr = fmt.Errorf("revoke this node's recipes: %w", err)
		}
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
		s.forgetSessionLocked(victim.id)
		s.mu.Unlock()

		_ = s.closeSessionOffGPU(victim.session)
		if err := os.RemoveAll(s.sessionOutputDir(victim.id)); err != nil {
			slog.WarnContext(ctx, "remove transcode session directory during reload", "component", "transcodenode", "session", victim.id, "error", err)
		}

		// Drop only this victim from the tracker. A blanket Cleanup here would
		// also wipe unrelated tracker-only work, such as an active download
		// preparation, even though force reload does not stop that job.
		if s.tracker != nil {
			s.tracker.Remove(ctx, victim.id)
		}
		unlock()
	}

	s.mu.Lock()
	now := time.Now()
	s.pruneStopFencesLocked(now)
	if s.stoppedProgressiveRemuxes == nil {
		s.stoppedProgressiveRemuxes = make(map[string]time.Time)
	}
	progressiveVictims := make(map[string]progressiveRemuxRequest, len(s.progressiveRemuxes))
	for id, request := range s.progressiveRemuxes {
		progressiveVictims[id] = request
		s.stoppedProgressiveRemuxes[id] = now.Add(playback.MaxTokenTTL)
	}
	s.mu.Unlock()

	for id, request := range progressiveVictims {
		request.cancel()
		if s.tracker != nil {
			trackerSessionID := request.playbackSessionID
			if trackerSessionID == "" {
				trackerSessionID = id
			}
			s.tracker.Remove(ctx, trackerSessionID)
		}
	}

	waitCtx, cancelWait := context.WithTimeout(ctx, progressiveRemuxShutdownTimeout)
	defer cancelWait()
	for id, request := range progressiveVictims {
		if err := waitForProgressiveRemuxShutdown(waitCtx, id, request); err != nil {
			return errors.Join(authorityErr, err)
		}
	}
	return authorityErr
}

func waitForProgressiveRemuxShutdown(ctx context.Context, transportID string, request progressiveRemuxRequest) error {
	if request.done == nil {
		return nil
	}
	select {
	case <-request.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for progressive remux %s shutdown: %w", transportID, ctx.Err())
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	sessionIDs := make([]string, 0, len(s.sessions)+len(s.progressiveRemuxes))
	for id := range s.sessions {
		sessionIDs = append(sessionIDs, id)
	}
	for id := range s.progressiveRemuxes {
		if _, exists := s.sessions[id]; !exists {
			sessionIDs = append(sessionIDs, id)
		}
	}
	s.mu.RUnlock()

	snapshot := s.metrics.Snapshot()
	var artifacts *downloadstorage.Usage
	if usage, ok := s.artifactUsage(); ok {
		artifacts = &usage
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statusResponse{
		Status:      "ok",
		ActiveJobs:  s.activeJobs.Load(),
		Sessions:    sessionIDs,
		System:      snapshot.System,
		GPU:         snapshot.GPU,
		Attribution: snapshot.Attribution,
		SampledAt:   snapshot.SampledAt,
		Artifacts:   artifacts,
	})
}
