package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	"mime"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/mediaprobe"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback/dovirpu"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func init() {
	// Register .m4s so http.ServeFile sets the correct Content-Type for
	// fMP4 HLS segments. Go's default MIME database does not include it.
	_ = mime.AddExtensionType(".m4s", "video/mp4")
	// Pin .ts for MPEG-TS HLS segments: the system database Go reads first
	// (shared-mime-info, as in the server image) lists .ts as Qt Linguist
	// text before video/mp2t.
	_ = mime.AddExtensionType(".ts", mimeVideoMPEGTS)
}

// TranscodeOpts holds configuration for an HLS transcode session.
type TranscodeOpts struct {
	AudioOnly bool
	// InputPath is the concrete source opened by FFmpeg for this process.
	InputPath string
	// MediaFileID identifies the catalog row behind InputPath. It is runtime
	// routing context and is deliberately not needed in FFmpeg arguments.
	MediaFileID                      int
	VirtualSourceOwnerInstallationID int
	// CanonicalInputPath is the durable source identity used by reconstruction
	// cards. Virtual playback sets this to virtual://... while InputPath points
	// at a short-lived, server-local relay. It is never passed to FFmpeg.
	CanonicalInputPath string
	// InputCleanup releases any transient relay registered for InputPath.
	// Runtime-only: recipe cards deliberately do not serialize callbacks.
	InputCleanup func()
	// RefreshInput obtains a fresh concrete source before an FFmpeg restart.
	// Virtual providers may rotate signed URLs while a long session is active.
	RefreshInput func(context.Context) (resolvedPath string, cleanup func(), err error)
	// OnDemuxFailure is invoked once per session when FFmpeg reports repeated
	// input demux ("Error during demuxing") failures, which indicate a bad
	// source candidate rather than a transient blip. It receives the effective
	// media_files id and the durable source path so the embedding handler can
	// stamp the candidate known-bad for the next failure recovery to rotate.
	// Runtime-only: recipe cards serialize byte-affecting fields explicitly and
	// never carry callbacks. No-op when nil.
	OnDemuxFailure func(ctx context.Context, mediaFileID int, canonicalPath string) error
	// OnSourceRejected is invoked once per session when the decoder rejects the
	// source past the confidence threshold, which is evidence about the source
	// candidate rather than a transport blip. It receives the effective
	// media_files id and the durable source path, exactly like OnDemuxFailure,
	// so the embedding handler can stamp the candidate known-bad with the same
	// mechanism. A copy target never invokes it. Runtime-only: no-op when nil.
	OnSourceRejected func(ctx context.Context, mediaFileID int, canonicalPath string) error
	OutputDir        string // e.g., /tmp/silo-transcode/{session_id}/
	// subtitleFilterInputPath is the libass subtitles filter input: the staged
	// subtitle track, or a parser-safe local alias of the media file.
	subtitleFilterInputPath string
	// subtitleFilterFontsDir holds attached fonts for a staged subtitle track.
	subtitleFilterFontsDir string
	// OutputSubdir is the signed, root-relative reconstruction directory. Empty
	// retains the legacy flat {session_id} layout.
	OutputSubdir         string
	TranscodeTransportID string
	SessionID            string
	SourceVideoCodec     string
	SourceVideoProfile   string
	SourceVideoBitDepth  int
	VideoBitstreamFilter string // validated copy-mode BSF: DV7ToHDR10BitstreamFilter
	VideoSampleEntry     string // allowlisted copy-HLS sample entry: dvh1 or hvc1
	// VideoRange is the HLS VIDEO-RANGE a copy remux's subtitle rendition
	// variant names (CopyHLSVideoRange): "", PQ, or HLG.
	VideoRange string
	// CopyVideoMPEGTS packages copied video in MPEG-TS instead of fMP4. It is
	// durable because the segment extension and bytes must survive restarts.
	CopyVideoMPEGTS bool
	// CopyAudioCodec is the codec of the source audio track a "copy"
	// TargetCodecAudio keeps. fMP4 segments add aac_adtstoasc for a copied AAC
	// track; empty (older recipes and tokens) keeps the plain copy. It is
	// durable because reconstruction must write the same segment bytes.
	CopyAudioCodec string
	// KeyframePlaylist serves a copy-video stream that starts at the
	// beginning as one complete VOD playlist planned from the source's
	// keyframe index, instead of FFmpeg's growing playlist (#1466). It is
	// durable because a reconstructed session must keep serving the playlist
	// the player already has.
	KeyframePlaylist bool
	// Reconstructed marks a session rebuilt from its recipe after its server
	// or node lost it. It isn't durable: each rebuild sets it.
	Reconstructed bool
	SeekSeconds   float64
	// StreamOriginSeconds is the keyframe timestamp at which a copy-video
	// stream actually begins. SeekSeconds remains the client-requested -ss so
	// FFmpeg performs exactly one demuxer seek; this origin keeps response and
	// reconstruction timelines aligned with the resulting media pre-roll.
	StreamOriginSeconds float64
	// CopySeekAnchorResolved distinguishes a valid zero-second origin from
	// older/shared recipes that never resolved a copy seek anchor.
	CopySeekAnchorResolved bool
	TargetResolution       string // e.g., 1080p, 720p
	// SourceFrameRate and SourceHeight are the probed source video facts a
	// forced encode needs to derive a frame-accurate GOP and to skip a no-op
	// scale. Zero is the historical 30 fps / unknown-height fallback.
	SourceFrameRate         float64
	SourceHeight            int
	TargetCodecVideo        string // e.g., h264 (or hevc if allowed)
	TargetCodecAudio        string // e.g., aac
	SegmentDuration         int    // seconds, default 6
	SegmentRetentionSeconds int    // downloaded media retained behind the client; 0 disables pruning
	StartSegmentNumber      int    // -hls_segment_start_number, default 0
	FFmpegPath              string // optional explicit ffmpeg binary path
	HWAccel                 string // auto, qsv, vaapi, nvenc, videotoolbox, none
	InitialHWAccel          string // requested HWAccel before resolution (e.g. qsv)
	// EncoderHWAccel reports a remote executor's actual video encoder when
	// HWAccel remains a GPU backend for tone mapping. Execution re-derives it.
	EncoderHWAccel string
	HWDevice       string // e.g., /dev/dri/renderD128 (default if empty)
	// AvoidHWDevice asks the initial multi-device allocator to prefer any other
	// present render device. It is a process-local startup hint used after an
	// early GPU failure; the selected concrete device remains fully reserved and
	// is the value frozen into the session recipe.
	AvoidHWDevice string
	// SoftwareVideoDecode keeps a hardware encoder while decoding the source
	// on the CPU. Intel's VAAPI/QSV decoders cannot accept 10-bit AVC, but the
	// decoded frames can still be converted to NV12, uploaded, and encoded by
	// QSV/VAAPI. The flag is frozen into recipe cards so restarts do not put the
	// unsupported hardware decoder back.
	SoftwareVideoDecode bool
	// nvencSoftwareDecode lets the automatic pipeline's mixed stage keep NVENC
	// encoding for a CPU-decoded source. Without it, NVENC plus
	// SoftwareVideoDecode resolves to libx264, which remains the rule for an
	// explicit NVENC setting. It is deliberately unexported so recipe cards and
	// stream tokens never freeze it: a reconstruct under auto rebuilds the
	// pipeline and derives it again from live configuration.
	nvencSoftwareDecode bool
	// softwareEncode retains a frozen GPU tone-map graph while its final
	// SDR frames feed a software encoder: libx265 on a device without HEVC
	// encoding, or either codec on a VAAPI device with no capped rate
	// control. It is derived again on reconstruction.
	softwareEncode bool
	// vaapiRateControl is the capped rate-control mode the selected VAAPI
	// device offers for the target encoder ("VBR" or "CBR"; empty for an
	// uncapped encode); see resolveVAAPIRateControl. Derived on every start.
	vaapiRateControl string
	// preparedFileEncode marks a single-file download encode. Nothing waits on
	// it in real time, so it trades encode speed for quality; HLS never sets it.
	preparedFileEncode         bool
	ToneMapPolicy              tonemap.Policy
	ToneMapMode                tonemap.Mode
	ToneMapSourceKind          tonemap.SourceKind
	ToneMapFilter              string
	ToneMapRecipeVersion       string
	CopyFMP4RecipeVersion      string
	ToneMapPreflightRequired   bool
	ToneMapSourceRevision      tonemap.SourceRevision
	ToneMapDVConfigPresent     bool
	ToneMapDVBLCompatIDPresent bool
	ToneMapDVBLPresent         bool
	ToneMapDVRPUPresent        bool
	SubtitleTrackIndex         int // -1 = no subtitles
	SubtitleBurnIn             bool
	// SubtitleCodec is the probed codec of the burn-in track (e.g. "subrip",
	// "hdmv_pgs_subtitle"). Bitmap codecs (PGS/DVD/DVB) select the overlay
	// filter_complex pipeline; text codecs use the libass subtitles filter.
	// Empty preserves the legacy text path for callers minted before the field.
	SubtitleCodec string
	// SubtitleRendition emits the selected embedded text track
	// (SubtitleTrackIndex, never burned in) from this same ffmpeg process as
	// the WebVTT subtitle rendition of its HLS playlist, for a transcode or
	// an HLS copy remux. See subtitle_rendition.go.
	SubtitleRendition bool
	// SubtitleRenditionTracks, with SubtitleRendition, lists every embedded
	// text track as a rendition of its own, read on demand through the
	// source's index instead of by this ffmpeg process; SubtitleTrackIndex then
	// only names the selected one. See subtitle_rendition_tracks.go.
	SubtitleRenditionTracks []SubtitleRenditionTrack
	// SubtitleRenditionSource is the source version the on-demand renditions
	// were planned from; segments are read from that version only.
	SubtitleRenditionSource SubtitleRenditionSource
	// subtitleCueFile is the per-generation cue file the rendition output
	// writes. Process-local: every start and restart derives its own.
	subtitleCueFile string
	// PreparedTracks selects the multi-track stream layout of a prepared
	// download (PrepareFile only). Nil keeps the legacy single-audio,
	// subtitle-free layout that older artifacts were encoded with.
	PreparedTracks  *PreparedTracks
	AudioTrackIndex int // -1 = default (first track), >= 0 = specific track
	// RequireAudioTrack rejects sources missing the negotiated audio ordinal.
	RequireAudioTrack bool
	// SourceAudioChannels is the selected source stream's channel count. Zero
	// means unknown and deliberately disables stereo downmix gain: boosting an
	// already-stereo stream would change its authored level.
	SourceAudioChannels int
	// TargetAudioChannels selects mono (1), stereo (2/default), or 5.1 (6+)
	// output. Ignored for copy/passthrough audio targets.
	TargetAudioChannels int
	// TargetAudioBitrateKbps caps an encoded audio stream. Zero selects the
	// layout-appropriate AAC default. Ignored for copy/passthrough targets.
	TargetAudioBitrateKbps int
	TargetBitrateKbps      int     // max video bitrate in kbps; 0 = CRF-only (no cap)
	TotalDuration          float64 // total media duration in seconds (for VOD manifest)
	FastStart              bool    // use superfast preset for faster first-segment production
	// ThrottleSeconds is the resolved forward-buffer policy for this session.
	// Zero disables throttling. It is durable so a remote executor can preserve
	// the API server's policy across node reconstruction without reading settings.
	ThrottleSeconds int
	NodeType        string
	ExecutionMode   string
	FFmpegLogSink   FFmpegLogSink

	// HLSVariableSubstitution is the client's hls_variable_substitution_v1
	// delivery feature: FFmpeg's real playlist carries a credential-sized
	// query once through EXT-X-DEFINE instead of on every segment URI. It
	// changes playlist text only, never media bytes.
	HLSVariableSubstitution bool
	// PrepareProgressSink receives PrepareFile's live encode progress. Like
	// the log sink it is process-local and never frozen into a recipe.
	PrepareProgressSink PrepareProgressSink
}

// DV7ToHDR10BitstreamFilter turns a Dolby Vision stream into its plain HDR10
// base layer during a copy-mode remux. dovi_rpu removes the Dolby Vision
// configuration and RPUs. A single-track Profile 7 stream also interleaves its
// enhancement layer as NAL unit type 63 in the same video stream, which stream
// mapping cannot separate, so filter_units removes those units.
const DV7ToHDR10BitstreamFilter = "dovi_rpu=strip=1,filter_units=remove_types=63"

// CopyFMP4RecipeVersion identifies the byte-affecting copy-video HLS recipe.
// Remote starts attest it so rolling clusters never silently mix the old
// timestamp/bitstream recipe with the Jellyfin-compatible fMP4 recipe.
const CopyFMP4RecipeVersion = "2"

const (
	VideoSampleEntryDVH1 = "dvh1"
	VideoSampleEntryHVC1 = "hvc1"
)

func validVideoSampleEntry(value string) bool {
	return value == "" || value == VideoSampleEntryDVH1 || value == VideoSampleEntryHVC1
}

// VideoSampleEntryForDVCopy returns the sample entry a copy-video HLS session
// should tag when it preserves a Dolby Vision source as-is: dvh1 for the
// single-layer HEVC profiles (5 and 8), whose DOVI configuration record
// survives the copy and whose consumers key decoder selection off the sample
// entry. Every other profile keeps ffmpeg's default labeling.
func VideoSampleEntryForDVCopy(dvProfile int) string {
	if dvProfile == 5 || dvProfile == 8 {
		return VideoSampleEntryDVH1
	}
	return ""
}

const (
	transcodeCodecH264       = "h264"
	transcodeCodecHEVC       = "hevc"
	transcodeCodecAV1        = "av1"
	transcodeCodecVP9        = "vp9"
	HWAccelNone              = "none"
	transcodeHWQSV           = "qsv"
	transcodeHWVAAPI         = "vaapi"
	transcodeHWNVENC         = "nvenc"
	transcodeHWVideoToolbox  = "videotoolbox"
	transcodeHWNone          = "none"
	transcodeResolution328p  = "328p"
	transcodeResolution420p  = "420p"
	transcodeResolution480p  = "480p"
	transcodeResolution720p  = "720p"
	transcodeResolution1080p = "1080p"
	transcodeResolution2160p = "2160p"
	// vaapiHWDeviceAlias names the VAAPI device every non-QSV hardware command
	// line declares; filter graphs and probes reference it by this alias.
	vaapiHWDeviceAlias = "hw"
	// nvencFilterDeviceAlias names the CUDA device a CPU-decoded NVENC command
	// line declares for its hwupload filter.
	nvencFilterDeviceAlias = "cu"
)

// TranscodeSession manages a running ffmpeg HLS transcode process.
type TranscodeSession struct {
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	opts       TranscodeOpts
	outputDir  string
	running    bool
	restarting *restartFlight
	waitErr    error
	// processCanceled records that the last exit came from this session's own
	// context cancellation (a restart, a switchover teardown, or a stop) rather
	// than from ffmpeg deciding to die. It is the distinction copy_seek_anchor.go
	// draws for probe kills: our own kill says nothing about the source, the
	// recipe, or the client, so it must not be reported as a transcode failure.
	processCanceled bool
	// segmentRequested records that at least one segment request reached this
	// session. A failure recovery on a session that never served a request is a
	// client that walked away, not evidence against the plan.
	segmentRequested bool
	// manifestServed records that this session answered a client-facing
	// playlist request. With segmentRequested it separates a client that
	// reached the stream and then stopped reading from one whose failure never
	// got a playlist at all.
	manifestServed       bool
	stderr               *boundedTailBuffer
	mu                   sync.Mutex
	done                 chan struct{} // closed when the monitor goroutine finishes
	stdinPipe            io.WriteCloser
	lastRequestedSegment int
	lastCompletedSegment int
	lastPruneFloor       int
	lastPruneHighWater   int
	segmentPruneRunning  bool
	pruneBeforeStart     bool
	copyDurationMu       sync.Mutex
	copyDurationIndex    copyManifestDurationIndex
	copyTimelineMu       sync.Mutex
	copyTimeline         observedCopyTimeline
	segmentGeneration    uint64
	segmentIncarnation   string
	dv81Mu               sync.Mutex
	dv81Converted        map[string]uint64
	dv81Track            dovirpu.Track
	// restartStartSegment is the absolute HLS segment number the most recent
	// in-place restart began producing at. Zero means no restart has happened
	// (or the restart cut from zero, as a planned copy stream does), so segment
	// numbering after it cannot be assumed to continue the previous generation's
	// timeline. The serve layer uses it to answer a stale `sgen` request from the
	// live generation only when that generation provably owns the same absolute
	// timeline position (see TranscodeSession.RestartContinuationSegment).
	restartStartSegment int
	// restartPredecessorToken is the exact generation token displaced by the
	// most recent in-place restart. Only this immediate predecessor may be
	// served from the live generation: an older token from generation A is not
	// evidence that generation C emits the same codec, track or init segment,
	// even when the requested segment number is at or after C's restart point.
	// Empty means no restart continuity is available. Guarded by mu.
	restartPredecessorToken string
	// restartRecipeStable reports that the restart kept the emitted-byte
	// recipe. Segment numbers only describe the same timeline when the codec,
	// audio selection, burn-in and packaging did not change; an init segment or
	// segment from a different recipe is not interchangeable.
	restartRecipeStable bool
	// runningRecipe is the emitted-byte recipe of the currently-running ffmpeg
	// process. It is frozen when the process is spawned and is deliberately not
	// updated by SetAudioTrackIndex / SetSourceAudioChannels, which mutate opts
	// ahead of Restart. A restart compares this frozen recipe, not the mutated
	// opts, against the replacement so retained segments were actually produced
	// by the same recipe. Guarded by mu.
	runningRecipe      emittedStreamRecipe
	throttler          *TranscodeThrottler
	stderrLinesLogged  int
	stderrBytesLogged  int
	stderrDroppedLines int
	stderrCapLogged    bool
	restartCount       int
	stderrLineIndex    int
	stderrWriter       *ffmpegStderrWriter
	restartHook        func(context.Context)
	// demuxErrorCount counts input demux I/O failures within the current decay
	// window; lastDemuxErrorAt timestamps the newest one. demuxStamped is set
	// once the count crosses demuxErrorThreshold and then refuses restarts so
	// the caller rotates instead of rebuilding the same bad transport. All
	// three are guarded by mu.
	demuxErrorCount  int
	lastDemuxErrorAt time.Time
	demuxStamped     bool
	// asyncRestartErr latches the terminal failure of a detached far-seek
	// restart. The worker that scheduled the restart answers its own request
	// with stale_generation unconditionally, so without the latch a dead source
	// (demux stamp, decoder rejection, resolve/validation failure) would present
	// as a second stale_generation on the retry and burn the client's recovery
	// budget before the ordinary failure path. The next segment request surfaces
	// this verdict instead; the next successful restart clears it. Guarded by mu.
	asyncRestartErr error
	// lifecycleAlive, when non-nil, reports whether this session is still the
	// live mapped session. The manager installs it around a restart so the
	// restart cannot re-spawn ffmpeg for a session a concurrent stop already
	// unregistered. Nil means "not managed here" and is treated as alive.
	// Guarded by mu.
	lifecycleAlive func() bool
	// Decoder-verdict lifecycle. Each ffmpeg generation moves observing ->
	// suspected -> confirmed independently. decodeErrorCount counts decoder
	// failure lines within the current decay window and lastDecodeErrorAt
	// timestamps the newest; decodeSample keeps a representative matched line
	// for diagnostics. decodeErrorThreshold strikes inside the decay window set
	// decodeStage to suspected and anchor decodeSuspectAt; qualifying
	// current-generation video progress cancels the suspicion back to
	// observing, while decodeObservationWindow expiring without recovery
	// confirms it and sets sourceRejected. decodeStamped is the session-level
	// hardware latch: it survives a restart because it records that this
	// session's hardware decoder failed the source, which licenses a
	// software-decode rebuild, but it never blocks a replacement generation from
	// reaching its own verdict. decodeGeneration fences the evaluator and old
	// stderr writers; decodeWatchCancel stops the single evaluator on reset or
	// close. sourceRejectNotified is set once the confirmed rejection has been
	// handed to the source-candidate failure callback. All guarded by mu.
	decodeErrorCount     int
	lastDecodeErrorAt    time.Time
	decodeSample         string
	decodeStage          decodeVerdictStage
	decodeSuspectAt      time.Time
	decodeGeneration     uint64
	decodeWatchCancel    context.CancelFunc
	decodeStamped        bool
	sourceRejected       bool
	sourceRejectNotified bool
	// decodeNow and decodeProbe are test seams: the injected clock and the
	// generation-scoped progress probe. Production leaves both nil, using
	// time.Now and the output-directory scan.
	decodeNow   func() time.Time
	decodeProbe func(outputDir string, cutoff time.Time, videoFrames int64) bool
	// decodeReapTimeout is the per-session teardown bound for a confirmed
	// generation. Zero uses decodeReapTimeout. Tests shrink it without
	// mutating a package global.
	decodeReapTimeout time.Duration
	// decodeProbeMu guards decodeProbeRunning, the coalescing flag that keeps
	// concurrent verdict reads from multiplying output scans or serializing
	// behind a slow probe.
	decodeProbeMu      sync.Mutex
	decodeProbeRunning bool
	// generationStartedAt is when the currently-owning ffmpeg process was
	// spawned. Output in the shared directory older than this timestamp was
	// written by a previous generation (or a previous session sharing the
	// directory) and describes media this process has not produced yet.
	generationStartedAt time.Time
	// videoFrames is how many video frames the current generation's FFmpeg
	// -progress stream has reported, and videoOutTimeUS is the newest
	// out_time_us reading (microseconds of muxed output). lastVideoFrameAt is
	// when the count last advanced. A video encode writes a frame only after
	// decoding it, so these are the only server-side evidence that a muxed
	// segment carries decodable video at all; segment existence is not.
	// Copy/remux plans pass no -progress, so they report zero. Resetting them
	// with the generation keeps a replacement from inheriting a predecessor's
	// frames, and progressGeneration fences the reader goroutine of a
	// generation that has been replaced. All guarded by mu.
	videoFrames        int64
	videoOutTimeUS     int64
	lastVideoFrameAt   time.Time
	progressGeneration uint64
	// observedStartupLatency is the time from the owning generation's spawn to
	// its first produced segment, latched once per generation. It is reset
	// whenever a generation starts so a restarted generation is never judged by
	// what an earlier one cost, and it is latched on the first measurement so a
	// later scan whose earliest surviving segment the pruner already deleted
	// cannot inflate it. Zero means the current generation has no measurement
	// yet, which keeps the restart hold. Guarded by mu.
	observedStartupLatency time.Duration
	inputCleanupOnce       sync.Once
	// inheritedManifest is the stream.m3u8 an earlier generation left in the
	// output directory before this process spawned, or nil. FFmpeg replaces the
	// playlist through a temp file and rename on every write, so a manifest that
	// is still this same file was not written by the current process.
	inheritedManifest os.FileInfo
	// hwWorkloadDevice is the device this session's GPU workload is counted
	// against, or empty when it holds none. Each replacement ffmpeg process
	// reacquires this same device rather than re-running selection, so a restart
	// keeps its GPU affinity and stays visible in per-device reporting.
	hwWorkloadDevice string
	// copyPlan is the planned segment list served instead of FFmpeg's
	// playlist, or nil. It is fixed when the session starts.
	copyPlan *copySegmentPlan
	// subtitleSpans lists each generation's subtitle rendition cue file,
	// oldest first, including those an earlier session recorded in the same
	// output directory. Guarded by mu.
	subtitleSpans []subtitleCueSpan
	// subtitleCueBase is added to segmentGeneration to number cue files, so
	// a session rebuilt on an output directory never reuses a recorded name.
	subtitleCueBase uint64
	// subtitleRecordMu serializes writes of the span record.
	subtitleRecordMu sync.Mutex
	// outputRemoved is set once shutdown removes the output directory, so a
	// late span record write cannot recreate files in it. Guarded by mu.
	outputRemoved bool
	// subtitleOriginMu serializes observations of a span's origin, so
	// concurrent subtitle requests share one ffprobe run.
	subtitleOriginMu sync.Mutex
}

// NewTranscodeSessionForTest exposes only the output directory needed by tests
// in other packages that exercise the mounted transcode-node media routes.
func NewTranscodeSessionForTest(outputDir string) *TranscodeSession {
	// Give the fixture a session identity so its generation token is real: the
	// segment-generation tests fence on `sgen`, and a tokenless session can
	// never match one.
	return &TranscodeSession{outputDir: outputDir, opts: TranscodeOpts{SessionID: "test-transcode-session"}}
}

// SetRestartHook registers a callback fired after every successful Restart.
// The owning handler uses it to re-arm the transcode throttler and the exit
// monitor; firing it from Restart itself keeps every restart caller (web
// segment recovery, audio switch, jellycompat seek) consistent.
func (s *TranscodeSession) SetRestartHook(fn func(context.Context)) {
	s.mu.Lock()
	s.restartHook = fn
	s.mu.Unlock()
}

// SegmentProgress describes the media ffmpeg has actually produced on disk.
type SegmentProgress struct {
	ProducedCount   int
	ProducedHead    int
	LastProducedAt  time.Time
	ManifestModTime time.Time
	// ActiveTempAt and ActiveTempSize describe the newest partial HLS segment
	// (the temp_file write before ffmpeg renames it). A slow encode still
	// advances this even while ProducedHead is unchanged.
	ActiveTempAt   time.Time
	ActiveTempSize int64
	// GenerationStartedAt is when the ffmpeg process that currently owns the
	// output directory was spawned. It is the zero time for sessions that never
	// started a process. Output stamped before it belongs to an earlier
	// generation and must not be read as this process's progress.
	GenerationStartedAt time.Time
	// StartupLatency is how long the current generation needed from spawn to its
	// first produced segment: the encoder's observed cost of opening the source
	// at the position that generation starts at. Zero until the generation has
	// produced a segment, and for sessions that never started a process.
	StartupLatency time.Duration
	HasManifest    bool
	// ProducedHeadCurrentGeneration is ProducedHead restricted to segments this
	// generation's process actually wrote, judged by file mtime at or after
	// GenerationStartedAt. On a restart that keeps a prior generation's segments
	// (identical recipe), ProducedHead includes media the new process did not
	// produce; throttling must measure prefetch against this head so a retained
	// window ahead cannot pause a process that has produced nothing. Equal to
	// ProducedHead when GenerationStartedAt is zero.
	ProducedHeadCurrentGeneration int
	Running                       bool
	Restarting                    bool
	StartSegmentNumber            int
	SegmentDuration               int
	LastRequestedSegment          int
	// VideoFrames is how many video frames the current generation has reported
	// on its FFmpeg -progress stream. ProducedCount only counts muxed bytes, so
	// a generation that is writing audio-only segments has a growing
	// ProducedCount with VideoFrames still zero. Copy/remux plans report zero.
	VideoFrames int64
}

// SegmentRecoveryDecision tells the segment handler whether to briefly wait
// for ffmpeg or seek-restart immediately.
type SegmentRecoveryDecision struct {
	Wait             bool
	WaitTimeout      time.Duration
	RestartOnTimeout bool
	Reason           string
	Progress         SegmentProgress
}

// SegmentRecoveryFarSeekReason is the decision reason for a request whose
// segment lies past the produced window and cannot be reached by waiting on the
// active encode (an arbitrary seek, not the next sequential fragment). The
// recovery is a seek restart, and because any restart rotates the generation,
// the request that triggers it must be answered with the stale-generation
// verdict rather than held for bytes its own generation fence will refuse.
const SegmentRecoveryFarSeekReason = "request_beyond_produced_window"

// segmentReasonRestarting is the recovery reason while a restart is in flight.
const segmentReasonRestarting = "transcode_restarting"

// defaultSegmentDuration is the segment length when not specified. Short
// segments (2s) allow the player to start quickly while still maintaining
// efficient HTTP delivery. This matches the approach used by Plex.
const defaultSegmentDuration = 2

// DefaultSegmentDuration is the exported segment length used when a transcode
// request does not specify one. Callers minting a reconstruct recipe must embed
// a concrete (>0) value so the token passes the node's completeness gate and the
// embedded length matches what the node actually produces.
const DefaultSegmentDuration = defaultSegmentDuration

// maxSyntheticManifestSegments caps complete synthetic playlists by entry
// count. At the default two-second duration this preserves the historical
// 100,000-second limit; shorter fragments reach the same bound sooner.
const maxSyntheticManifestSegments = 50_000

// remountStartOffsetSeconds is a positive, effectively-zero HLS start offset.
// Media3 suppresses live-edge position projection for EVENT playlists only
// when EXT-X-START is positive. Anchoring one millisecond after the generation
// origin keeps an initial start indistinguishable from zero while giving a
// rebuilt MediaSource a stable timeline on which to restore its playhead.
const remountStartOffsetSeconds = 0.001

const maxPersistedFFmpegLines = 2000
const maxPersistedFFmpegBytes = 256 * 1024
const maxPersistedFFmpegChars = 2000

// Demux failure classification. A single input demux error (a provider blip)
// still takes the normal recovery path; demuxErrorThreshold of them inside
// demuxErrorDecay means the source candidate is bad. The window decays so
// unrelated failures separated by a long healthy stretch never accumulate into
// a false stamp.
const (
	demuxErrorThreshold = 3
	demuxErrorDecay     = 60 * time.Second
)

// Decoder failure classification. Only invalid-bitstream decoder lines count
// (see decodeErrorLine): a source the decoder truly cannot read emits them for
// every frame from the first one, so it crosses decodeErrorThreshold within
// milliseconds while an isolated warning never approaches it. Reference-list
// warnings ("Could not find ref with POC" / "Error constructing the frame RPS")
// are not counted: a decoder priming its reference lists on a Dolby Vision
// profile 8 stream emits a bounded burst of them and then produces valid
// segments (verified live), so counting them declared playable sources
// rejected. The window decays so failures separated by a long healthy stretch
// do not accumulate into a false stamp.
const (
	decodeErrorThreshold = 10
	decodeErrorDecay     = 60 * time.Second
)

// decodeObservationWindow is the internal grace between the strike threshold
// (suspected) and confirmation. A suspected generation can still prove recovery
// by muxing a segment within this window; expiry without recovery confirms the
// rejection. It is deliberately a code constant, not a user setting: the window
// is a safety margin sized to the segment cadence, not an operator preference.
const decodeObservationWindow = time.Second

// noVideoFramesGrace is how long a video encode may mux segments without
// reporting a single decoded frame before its start is declared unplayable. A
// healthy encode reports its first frames through FFmpeg's -progress stream
// before it can write the first segment, so the grace only elapses for a
// generation that is muxing audio without decoding video. It is deliberately a
// code constant, not a user setting: it trades a few seconds of start latency
// on a broken plan against the client's much longer startup guard.
const noVideoFramesGrace = 8 * time.Second

// reportsVideoFrames reports whether this plan's FFmpeg process is asked for a
// -progress stream whose frame counter can be read. Only a video encode decodes
// frames; a copy/remux passes no -progress, so its frame count is always zero
// and must never be read as evidence about the source.
func reportsVideoFrames(opts TranscodeOpts) bool {
	return !strings.EqualFold(opts.TargetCodecVideo, "copy")
}

// decodeVerdictStage is the internal decoder-verdict lifecycle for one ffmpeg
// generation: observing (no verdict), suspected (threshold crossed, awaiting a
// progress check or expiry), and confirmed (terminal for the generation).
type decodeVerdictStage uint8

const (
	decodeStageObserving decodeVerdictStage = iota
	decodeStageSuspected
	decodeStageConfirmed
)

// ManifestStartupTimeout is the maximum wait for FFmpeg's first safe playback
// window before the caller reports a retryable startup timeout.
const ManifestStartupTimeout = 30 * time.Second

// ManifestStartupBurnInTimeout is the longer first-window budget for a plan
// that composites a subtitle into the video. Burn-in runs extra filter work
// (libass rendering, bitmap overlay, and usually a tone-map) before the first
// segment can be muxed, so a slow-but-progressing burn-in must not be read as
// a dead release at the ordinary budget. It is deliberately bounded, not
// unbounded: a burn-in that truly produced nothing still reports a
// subtitle-local failure rather than rotating the release.
const ManifestStartupBurnInTimeout = 90 * time.Second

// ManifestStartupTimeoutFor returns the first-window budget for a session.
// A burn-in session gets the longer budget so a slow-but-progressing subtitle
// composite is not reclassified as a transport failure; every other plan keeps
// the historical timeout, so video-failure startup behavior is unchanged.
func ManifestStartupTimeoutFor(opts TranscodeOpts) time.Duration {
	if opts.SubtitleBurnIn && opts.SubtitleTrackIndex >= 0 {
		return ManifestStartupBurnInTimeout
	}
	return ManifestStartupTimeout
}

const (
	maxSequentialMissingSegments = 2
	activeSegmentWait            = 12 * time.Second
	// SegmentStallTimeout is how long the produced head may remain unchanged
	// before a segment wait is treated as stalled. A slow encoder that keeps
	// advancing is allowed to use the full wait; only a generation that has
	// stopped producing is restarted.
	SegmentStallTimeout    = 6 * time.Second
	segmentWaitGrace       = 1500 * time.Millisecond
	maxSegmentWait         = 6 * time.Second
	minSegmentWait         = 3 * time.Second
	minStaleProducedWindow = 5 * time.Second
)

// SegmentStallTimeoutFor scales the produced-head watchdog with the segment
// duration. A four-second segment can legitimately take longer than the default
// six seconds to encode; three segment durations is the minimum window that
// lets a slow-but-progressing encoder finish without being mistaken for a hang.
func SegmentStallTimeoutFor(opts TranscodeOpts) time.Duration {
	duration := time.Duration(opts.SegmentDuration) * 3 * time.Second
	if duration > SegmentStallTimeout {
		return duration
	}
	return SegmentStallTimeout
}

// NewReadyTranscodeSessionForTesting creates a TranscodeSession with an outputDir
// containing a valid stream.m3u8 and startup segments for unit testing.
func NewReadyTranscodeSessionForTesting(outputDir string, opts TranscodeOpts) (*TranscodeSession, error) {
	for _, name := range []string{"seg_00000.ts", "seg_00001.ts", "seg_00002.ts"} {
		if err := os.WriteFile(filepath.Join(outputDir, name), []byte("test-segment-bytes"), 0644); err != nil {
			return nil, err
		}
	}
	manifestPath := filepath.Join(outputDir, "stream.m3u8")
	manifestContent := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:4.000000,\nseg_00000.ts\n#EXTINF:4.000000,\nseg_00001.ts\n#EXTINF:4.000000,\nseg_00002.ts\n"
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		return nil, err
	}
	return &TranscodeSession{
		outputDir: outputDir,
		opts:      opts,
		running:   true,
	}, nil
}

// StartTranscode launches an ffmpeg process that produces HLS segments.
func StartTranscode(ctx context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
	if !validVideoSampleEntry(opts.VideoSampleEntry) ||
		opts.VideoSampleEntry != "" && !strings.EqualFold(opts.TargetCodecVideo, "copy") &&
			(opts.VideoSampleEntry != VideoSampleEntryHVC1 || !strings.EqualFold(opts.TargetCodecVideo, transcodeCodecHEVC)) {
		return nil, fmt.Errorf("unsupported video sample-entry recipe")
	}
	if !validCopyHLSVideoRange(opts) {
		return nil, errors.New("unsupported HLS video range")
	}
	if opts.CopyVideoMPEGTS && !strings.EqualFold(opts.TargetCodecVideo, "copy") {
		return nil, fmt.Errorf("copy-video MPEG-TS requires video copy")
	}
	if opts.VideoBitstreamFilter != "" &&
		(opts.VideoBitstreamFilter != DV7ToHDR10BitstreamFilter || !strings.EqualFold(opts.TargetCodecVideo, "copy")) {
		return nil, fmt.Errorf("unsupported video bitstream filter recipe")
	}
	if opts.SegmentDuration <= 0 {
		opts.SegmentDuration = defaultSegmentDuration
	}
	if opts.InitialHWAccel == "" {
		opts.InitialHWAccel = opts.HWAccel
	}
	opts = normalizeTranscodeOptsContext(ctx, opts)
	if err := validateToneMapOpts(opts); err != nil {
		return nil, err
	}
	// Resolve a multi-device hw_device list to one concrete GPU. Restarts reuse
	// the selected device, but each ffmpeg process owns its own reservation.
	// hwWorkloadDevice is whatever the allocator counted this workload under, so
	// the rule for which workloads are counted lives in one place.
	hwDevice, hwWorkloadDevice, releaseHWDevice := acquireHWDevice(opts.HWDevice, opts.HWAccel, opts.AvoidHWDevice)
	opts.HWDevice = hwDevice
	opts.AvoidHWDevice = ""
	var encoderErr error
	opts, encoderErr = resolveHEVCTranscodeEncoder(ctx, opts)
	if encoderErr != nil {
		releaseHWDevice()
		return nil, encoderErr
	}
	if opts, encoderErr = resolveVAAPIRateControl(ctx, opts); encoderErr != nil {
		releaseHWDevice()
		return nil, encoderErr
	}
	if opts.HWAccel == transcodeHWNone {
		releaseHWDevice()
		hwWorkloadDevice = ""
	}
	if err := validateToneMapSource(ctx, opts); err != nil {
		releaseHWDevice()
		return nil, err
	}

	// Ensure output directory exists.
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		releaseHWDevice()
		return nil, fmt.Errorf("create output dir: %w", err)
	}
	if err := prepareSubtitleFilterInput(ctx, &opts); err != nil {
		releaseHWDevice()
		return nil, err
	}
	copyPlan := resolveCopyPlan(ctx, opts)
	if copyPlan == nil && opts.Reconstructed && opts.KeyframePlaylist && ctx.Err() != nil {
		// Rebuilding without the plan would serve FFmpeg's playlist to a
		// player that may hold the planned one; a later request rebuilds.
		releaseHWDevice()
		return nil, fmt.Errorf("plan rebuilt keyframe playlist: %w", ctx.Err())
	}
	if copyPlan != nil {
		// The complete playlist is numbered from segment zero, so FFmpeg
		// starts at the planned boundary containing the requested position
		// and writes that segment under its playlist number. An initial
		// resume therefore skips the copied prefix without changing the
		// playlist the client receives.
		if start, segment, ok := copyPlan.seekTarget(opts.SeekSeconds); ok {
			opts.SeekSeconds, _ = copyPlan.segmentSeekPosition(segment)
			opts.StartSegmentNumber = segment
			opts.StreamOriginSeconds = start
			opts.CopySeekAnchorResolved = true
		}
		log.Printf("playback: session %s serves a %d-segment keyframe playlist from segment %d", opts.SessionID, len(copyPlan.durations), opts.StartSegmentNumber)
	}

	// The synchronous source guard above is bounded by the caller's startup
	// context. Once it succeeds, keep the established behavior where the
	// transcode process outlives a disconnected manifest request.
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	// Read before ffmpeg starts, while the playlist on disk is still the
	// earlier session's.
	var subtitleSpans []subtitleCueSpan
	var subtitleCueBase uint64
	if subtitleRenditionActive(opts) {
		subtitleSpans, subtitleCueBase = loadSubtitleSpans(opts, copyPlan)
		opts.subtitleCueFile = subtitleCueFileName(subtitleCueBase)
	}
	s := &TranscodeSession{
		cancel:               cancel,
		opts:                 opts,
		runningRecipe:        emittedRecipeOf(opts),
		outputDir:            opts.OutputDir,
		running:              true,
		done:                 make(chan struct{}),
		stderr:               newBoundedTailBuffer(stderrTailMaxBytes),
		lastRequestedSegment: opts.StartSegmentNumber,
		lastCompletedSegment: opts.StartSegmentNumber - 1,
		lastPruneFloor:       opts.StartSegmentNumber - 1,
		lastPruneHighWater:   opts.StartSegmentNumber - 1,
		segmentIncarnation:   uuid.NewString(),
		hwWorkloadDevice:     hwWorkloadDevice,
		copyPlan:             copyPlan,
		subtitleSpans:        subtitleSpans,
		subtitleCueBase:      subtitleCueBase,
	}

	args := buildFFmpegArgs(opts)
	bin := opts.FFmpegPath
	if bin == "" {
		bin = ffmpegBinary()
	}

	log.Printf("playback: ffmpeg cmd: %s %s", bin, strings.Join(args, " "))
	s.logFFmpegEvent(ctx, "ffmpeg process starting", "")

	cmd := fspath.CommandContext(ctx, bin, args...)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		releaseHWDevice()
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	cmd.Dir = opts.OutputDir
	cmd.Stderr = s.newStderrWriter(ctx)
	cmd.WaitDelay = 3 * time.Second
	progressPipe, err := attachProgressPipe(cmd, opts)
	if err != nil {
		cancel()
		releaseHWDevice()
		return nil, fmt.Errorf("create progress pipe: %w", err)
	}

	// Stamp the generation before the process can write anything, so every file
	// this ffmpeg produces is strictly newer than the stamp.
	inheritedManifest := statManifest(opts.OutputDir)
	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		processmetrics.Record(processmetrics.Transcode, nil, err, ctx.Err())
		cancel()
		releaseHWDevice()
		s.logFFmpegEvent(ctx, "ffmpeg process exit error", err.Error())
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	s.cmd = cmd
	s.stdinPipe = stdinPipe
	s.beginGeneration(startedAt)
	progressGeneration := s.progressGeneration
	s.inheritedManifest = inheritedManifest
	s.startSubtitleSpanLocked(opts)
	s.saveSubtitleSpans()
	s.logFFmpegEvent(ctx, "ffmpeg process started", "")
	if progressPipe != nil {
		go s.consumeProgress(progressPipe, progressGeneration)
	}

	// Monitor ffmpeg in background. The process-specific reservation is released
	// before done closes, so waiters can safely launch a replacement process.
	go s.monitorFFmpeg(ctx, cmd, s.done, releaseHWDevice)

	return s, nil
}

func (s *TranscodeSession) monitorFFmpeg(ctx context.Context, cmd *exec.Cmd, done chan struct{}, releaseHWDevice func()) {
	waitErr := cmd.Wait()
	// The child no longer owns the device once Wait returns. Release before
	// flushing/logging so slow diagnostics cannot make the allocator count a
	// process that has already exited.
	releaseHWDevice()
	processmetrics.Record(processmetrics.Transcode, cmd.ProcessState, waitErr, ctx.Err())
	s.flushStderr(ctx)
	// Read the cancellation once, after the reap: a process this session killed
	// itself surfaces here as a SIGKILL exit, which is indistinguishable from a
	// crash by err alone. The context is what tells them apart.
	canceled := ctx.Err() != nil
	s.mu.Lock()
	s.running = false
	s.waitErr = waitErr
	s.processCanceled = canceled
	s.mu.Unlock()
	s.logWaitResult(ctx, waitErr, canceled)
	close(done)
}

// IsMPEG2VideoCodec reports whether a probed video codec name identifies
// MPEG-2 video. It accepts common FFmpeg aliases because codec strings can
// come from scan metadata, direct probes, or client capability lists.
func IsMPEG2VideoCodec(codec string) bool {
	normalized := strings.NewReplacer(
		" ", "",
		"-", "",
		"_", "",
		".", "",
	).Replace(strings.ToLower(strings.TrimSpace(codec)))
	switch normalized {
	case "mpeg2video", "mpeg2", "mp2v":
		return true
	default:
		return false
	}
}

// IsMPEG4Part2VideoCodec reports whether a codec name identifies MPEG-4 Part 2
// video, commonly found in older XviD/DivX AVI files.
func IsMPEG4Part2VideoCodec(codec string) bool {
	normalized := strings.NewReplacer(
		" ", "",
		"-", "",
		"_", "",
		".", "",
	).Replace(strings.ToLower(strings.TrimSpace(codec)))
	switch normalized {
	case "mpeg4", "mp4v", "xvid", "divx", "dx50":
		return true
	default:
		return false
	}
}

// RequiresSoftwareVideoDecode reports sources that the installed hardware
// execution recipes must not feed to their video decoders. AVC High 10 is not
// supported by Intel VAAPI/QSV decode; scanner rows are not perfectly uniform,
// so either the probed bit depth or the normalized profile is sufficient.
func RequiresSoftwareVideoDecode(codec, profile string, bitDepth int) bool {
	if normalizeCodecV3(codec) != transcodeCodecH264 {
		return false
	}
	normalizedProfile := normalizeVideoProfile(profile)
	return bitDepth > 8 || normalizedProfile == "high10" || normalizedProfile == "high10intra" || normalizedProfile == "hi10p"
}

func normalizeVideoProfile(profile string) string {
	return strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(strings.TrimSpace(profile)))
}

// SourceVideoTranscodeFacts returns the primary source facts needed to choose
// a safe FFmpeg decoder. Track metadata fills gaps in legacy media-file rows.
func SourceVideoTranscodeFacts(file *models.MediaFile) (codec, profile string, bitDepth int) {
	if file == nil {
		return "", "", 0
	}
	codec = file.CodecVideo
	if len(file.VideoTracks) == 0 {
		return codec, "", 0
	}
	track := file.VideoTracks[0]
	if strings.TrimSpace(codec) == "" {
		codec = track.Codec
	}
	prof := track.Profile
	if track.DVProfile > 0 && !strings.Contains(strings.ToLower(prof), "profile") {
		prof = fmt.Sprintf("%s Profile %d", prof, track.DVProfile)
	} else if track.DolbyVision != "" && !strings.Contains(strings.ToLower(prof), "profile") {
		prof = strings.TrimSpace(prof + " " + track.DolbyVision)
	}
	return codec, strings.TrimSpace(prof), models.NormalizeVideoBitDepth(track.BitDepth, track.PixelFormat, track.Profile)
}

func resolveSoftwareVideoDecode(opts TranscodeOpts) TranscodeOpts {
	if RequiresSoftwareVideoDecode(opts.SourceVideoCodec, opts.SourceVideoProfile, opts.SourceVideoBitDepth) {
		opts.SoftwareVideoDecode = true
	}
	return opts
}

// resolveVideoToolboxToneMapDecode keeps hardware decoding only for the HEVC
// Main 10 HEVC source shape exercised by the VideoToolbox tone-map capability
// probe. Other source shapes may still use scale_vt and the hardware encoder
// after CPU decoding, but must upload their frames explicitly rather than
// requesting unprobed VideoToolbox decoder surfaces.
func resolveVideoToolboxToneMapDecode(opts TranscodeOpts) TranscodeOpts {
	if opts.ToneMapMode == tonemap.ModeHardware && opts.HWAccel == transcodeHWVideoToolbox && !videoToolboxToneMapHardwareDecodeSupported(opts) {
		opts.SoftwareVideoDecode = true
	}
	return opts
}

func videoToolboxToneMapHardwareDecodeSupported(opts TranscodeOpts) bool {
	return normalizeCodecV3(opts.SourceVideoCodec) == transcodeCodecHEVC &&
		normalizeVideoProfile(opts.SourceVideoProfile) == "main10" &&
		opts.SourceVideoBitDepth == 10
}

// normalizeTranscodeOpts resolves source-specific decode safety and the
// configured hardware execution mode in one place. Every FFmpeg entry point
// must pass through this helper so streaming and prepared-file recipes cannot
// disagree about whether a source may use hardware decode or encode.
func normalizeTranscodeOpts(opts TranscodeOpts) TranscodeOpts {
	return normalizeTranscodeOptsContext(context.Background(), opts)
}

func normalizeTranscodeOptsContext(ctx context.Context, opts TranscodeOpts) TranscodeOpts {
	opts.EncoderHWAccel = ""
	opts.FFmpegPath = ResolveFFmpegPath(opts.FFmpegPath)
	opts = resolveSoftwareVideoDecode(opts)
	if opts.ToneMapMode == tonemap.ModeSoftware {
		opts.SoftwareVideoDecode = true
		opts.HWAccel = HWAccelNone
		return opts
	}
	opts.HWAccel = resolveEffectiveTranscodeHWAccelContext(ctx, opts)
	opts = resolveVideoToolboxToneMapDecode(opts)
	return opts
}

// ResolveToneMapFilterV3 picks the tone-map filter for the resolved executor.
// The opt-in VPP tone map replaces the probed filter only when the setting is
// on, the mode is hardware, and the backend is QSV; every other combination
// keeps the probed filter unchanged.
func ResolveToneMapFilterV3(probedFilter string, mode tonemap.Mode, hwAccel string, vppEnabled bool) string {
	if vppEnabled && mode == tonemap.ModeHardware && strings.EqualFold(hwAccel, transcodeHWQSV) {
		return tonemap.HardwareFilterQSVVPP
	}
	return probedFilter
}

// validateToneMapOpts rejects partial, contradictory, or unsupported frozen
// recipes before any FFmpeg process can be started.
func validateToneMapOpts(opts TranscodeOpts) error {
	if opts.ToneMapMode == "" {
		if opts.ToneMapPolicy != "" || opts.ToneMapSourceKind != "" || opts.ToneMapFilter != "" || opts.ToneMapRecipeVersion != "" || opts.ToneMapPreflightRequired || !opts.ToneMapSourceRevision.IsZero() || opts.ToneMapDVConfigPresent || opts.ToneMapDVBLCompatIDPresent || opts.ToneMapDVBLPresent || opts.ToneMapDVRPUPresent {
			return fmt.Errorf("incomplete tone-map recipe")
		}
		return nil
	}
	if strings.EqualFold(opts.TargetCodecVideo, "copy") {
		return fmt.Errorf("tone mapping requires video encoding")
	}
	if !opts.ToneMapPolicy.Allows(opts.ToneMapMode) ||
		!tonemap.ValidSourceKind(opts.ToneMapSourceKind) || opts.ToneMapFilter == "" ||
		opts.ToneMapRecipeVersion != TransformationHDRToSDRToneMapRecipeVersionV3 ||
		opts.ToneMapSourceRevision.IsZero() {
		return fmt.Errorf("incomplete tone-map recipe")
	}
	switch opts.ToneMapMode {
	case tonemap.ModeSoftware:
		if opts.ToneMapFilter != tonemap.SoftwareFilterBT2390 && opts.ToneMapFilter != tonemap.SoftwareFilterHable {
			return fmt.Errorf("unsupported software tone-map filter %q", opts.ToneMapFilter)
		}
	case tonemap.ModeHardware:
		switch opts.HWAccel {
		case transcodeHWQSV:
			expected := tonemap.HardwareFilterOpenCL
			// The opt-in VPP tone map is a validated QSV alternative to the
			// default OpenCL recipe (playback.transcode_vpp_tone_map_enabled).
			vppOverride := opts.HWAccel == transcodeHWQSV && opts.ToneMapFilter == tonemap.HardwareFilterQSVVPP
			if opts.ToneMapFilter != expected && !vppOverride {
				return fmt.Errorf("unsupported %s tone-map filter %q", opts.HWAccel, opts.ToneMapFilter)
			}
		case transcodeHWVAAPI:
			// The probe picks tonemap_opencl on Intel GPUs, whose own VAAPI
			// tone mapper gives wrong colors, and tonemap_vaapi elsewhere.
			if opts.ToneMapFilter != tonemap.HardwareFilterVAAPI && opts.ToneMapFilter != tonemap.HardwareFilterOpenCL {
				return fmt.Errorf("unsupported vaapi tone-map filter %q", opts.ToneMapFilter)
			}
		case transcodeHWNVENC:
			if opts.ToneMapFilter != tonemap.HardwareFilterCUDA {
				return fmt.Errorf("unsupported nvenc tone-map filter %q", opts.ToneMapFilter)
			}
		case transcodeHWVideoToolbox:
			if opts.ToneMapFilter != tonemap.HardwareFilterVideoToolbox {
				return fmt.Errorf("unsupported videotoolbox tone-map filter %q", opts.ToneMapFilter)
			}
		default:
			return fmt.Errorf("hardware tone mapping requires qsv, vaapi, nvenc, or videotoolbox")
		}
	default:
		return fmt.Errorf("unsupported tone-map mode %q", opts.ToneMapMode)
	}
	if opts.ToneMapSourceKind == tonemap.SourceDolbyVision && !tonemap.FilterAppliesDolbyVision(opts.ToneMapFilter) {
		return fmt.Errorf("tone-map filter %q cannot apply a Dolby Vision RPU", opts.ToneMapFilter)
	}
	return nil
}

// ResolveToneMapExecutor validates a frozen tone-map recipe against the
// current FFmpeg binary and device, then fills the environment-specific
// backend and filter. The selected mode is never changed here.
func ResolveToneMapExecutor(ctx context.Context, opts TranscodeOpts) (TranscodeOpts, error) {
	if (opts.ToneMapPolicy == "" || opts.ToneMapPolicy == tonemap.PolicyNone) && opts.ToneMapMode == "" && opts.ToneMapSourceKind == "" && opts.ToneMapRecipeVersion == "" {
		opts.ToneMapPolicy = ""
		return opts, nil
	}
	if opts.ToneMapMode == "" || opts.ToneMapSourceKind == "" ||
		!opts.ToneMapPolicy.Allows(opts.ToneMapMode) ||
		opts.ToneMapRecipeVersion != TransformationHDRToSDRToneMapRecipeVersionV3 ||
		opts.ToneMapSourceRevision.IsZero() {
		return opts, fmt.Errorf("incomplete tone-map recipe")
	}
	backend := ResolveHWAccelWithFFmpegContext(ctx, opts.HWAccel, opts.FFmpegPath, opts.HWDevice)
	capabilities, err := tonemap.Probe(ctx, ResolveFFmpegPath(opts.FFmpegPath), backend, opts.HWDevice)
	if err != nil {
		return opts, fmt.Errorf("%w: probe tone-map executor: %w", ErrToneMapExecutorUnavailable, err)
	}
	if !capabilities.Supports(opts.ToneMapMode, opts.ToneMapSourceKind) {
		return opts, fmt.Errorf("tone-map executor is not validated")
	}
	// A recipe frozen with the opt-in VPP tone map keeps it across a
	// reconstruction only while the resolved backend is still QSV. If the
	// deployment moved to another backend, fall through to the probed filter
	// instead of forcing a chain that backend cannot run.
	if opts.ToneMapMode == tonemap.ModeHardware &&
		opts.ToneMapFilter == tonemap.HardwareFilterQSVVPP &&
		strings.EqualFold(backend, transcodeHWQSV) {
		opts.HWAccel = capabilities.BackendFor(opts.ToneMapMode, opts.ToneMapSourceKind)
		return opts, nil
	}
	opts.ToneMapFilter = capabilities.FilterFor(opts.ToneMapMode, opts.ToneMapSourceKind)
	if opts.ToneMapMode == tonemap.ModeHardware {
		opts.HWAccel = capabilities.BackendFor(opts.ToneMapMode, opts.ToneMapSourceKind)
	} else {
		opts.HWAccel = HWAccelNone
	}
	return opts, nil
}

// validateToneMapSource rechecks the source revision and, for ambiguous
// classifications, runs the selected executor's representative-frame preflight.
func validateToneMapSource(ctx context.Context, opts TranscodeOpts) error {
	if opts.ToneMapMode == "" {
		return nil
	}
	lowerInput := strings.ToLower(strings.TrimSpace(opts.InputPath))
	lowerCanonical := strings.ToLower(strings.TrimSpace(opts.CanonicalInputPath))
	isVirtualOrHTTP := strings.HasPrefix(lowerInput, "http://") || strings.HasPrefix(lowerInput, "https://") ||
		strings.HasPrefix(lowerInput, "virtual://") || strings.HasPrefix(lowerCanonical, "virtual://") ||
		opts.VirtualSourceOwnerInstallationID > 0
	if isVirtualOrHTTP {
		return nil
	}
	if err := opts.ToneMapSourceRevision.ValidatePath(ctx, opts.InputPath); err != nil {
		if errors.Is(err, tonemap.ErrSourceRevisionChanged) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrToneMapSourceValidationUnavailable, err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, ManifestStartupTimeout)
	defer cancel()
	liveTrack, err := mediaprobe.ProbePrimaryVideoTrack(
		probeCtx,
		mediaprobe.FFprobePathFromFFmpeg(ResolveFFmpegPath(opts.FFmpegPath)),
		opts.InputPath,
	)
	if err != nil {
		if errors.Is(err, mediaprobe.ErrPrimaryVideoNotFound) {
			return fmt.Errorf("%w: %w", tonemap.ErrSourceRevisionChanged, err)
		}
		return fmt.Errorf("%w: %w", ErrToneMapSourceValidationUnavailable, err)
	}
	if err := tonemap.ValidateLivePrimaryVideoTrack(opts.ToneMapSourceRevision, liveTrack); err != nil {
		return err
	}
	if !opts.ToneMapPreflightRequired {
		return nil
	}
	backend := opts.HWAccel
	if opts.ToneMapMode == tonemap.ModeSoftware {
		backend = tonemap.BackendSoftware
	}
	if err := tonemap.ValidateSource(ctx, tonemap.SourcePreflightRequest{
		FFmpegPath:          ResolveFFmpegPath(opts.FFmpegPath),
		InputPath:           opts.InputPath,
		DurationSeconds:     opts.TotalDuration,
		SourceBitDepth:      opts.SourceVideoBitDepth,
		SoftwareVideoDecode: opts.SoftwareVideoDecode,
		Mode:                opts.ToneMapMode,
		Backend:             backend,
		Filter:              opts.ToneMapFilter,
		Kind:                opts.ToneMapSourceKind,
		RecipeVersion:       opts.ToneMapRecipeVersion,
		HardwareDevice:      opts.HWDevice,
		SourceRevision:      opts.ToneMapSourceRevision,
	}); err != nil {
		return classifyToneMapPreflightError(err)
	}
	return nil
}

func classifyToneMapPreflightError(err error) error {
	if errors.Is(err, tonemap.ErrSourcePreflightUnavailable) {
		return fmt.Errorf("%w: tone-map source preflight failed: %w", ErrToneMapSourceValidationUnavailable, err)
	}
	return fmt.Errorf("tone-map source preflight failed: %w", err)
}

// hlsMuxMaxDelayMicros is the -max_delay, in microseconds, of every HLS
// transcode. Jellyfin uses the same value.
const hlsMuxMaxDelayMicros = 5_000_000

// HLSMPEGTSTimestampOffset90k is how far, in 90 kHz ticks, ffmpeg's MPEG-TS
// muxer shifts every PTS and DTS of an HLS MPEG-TS segment: twice
// -max_delay, 10 s for hlsMuxMaxDelayMicros. With -copyts a segment's video
// PTS is the source time plus this offset, so a WebVTT X-TIMESTAMP-MAP for
// those segments must add it too or players show cues that much early.
// fMP4 segments carry no such shift.
const HLSMPEGTSTimestampOffset90k = 2 * hlsMuxMaxDelayMicros * 90_000 / 1_000_000

// buildFFmpegArgs constructs the full ffmpeg argument list from TranscodeOpts.
func buildFFmpegArgs(opts TranscodeOpts) []string {
	opts = normalizeTranscodeOpts(opts)

	isVideoCopy := strings.EqualFold(opts.TargetCodecVideo, "copy")
	isAudioCopy := opts.TargetCodecAudio == "copy"

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
	}
	// A video encode reports machine-readable progress on stdout: the frame
	// counter is the only way to tell a muxing encode from one whose decoder
	// produced nothing. Copy/remux plans are unchanged: they decode nothing, so
	// a frame count would say nothing about them and their stdout stays unused.
	if reportsVideoFrames(opts) {
		args = append(args, "-progress", "pipe:1", "-nostats")
	}

	// Hardware acceleration — skip when copying video (no encoding needed).
	if !isVideoCopy {
		args = appendHWAccelArgs(args, opts)
	}

	lowerInput := strings.ToLower(strings.TrimSpace(opts.InputPath))
	isRemote := strings.HasPrefix(lowerInput, "http://") || strings.HasPrefix(lowerInput, "https://")
	// A video *encode* decodes what it writes, so its seek must be the one a
	// decoder needs. +fastseek seeks the demuxer without clamping to the
	// preceding keyframe, which on open-GOP content (HEVC in particular) starts
	// the decoder mid-GOP with reference frames it never saw: FFmpeg then muxes
	// segments that carry no decodable picture. Without +fastseek the ordinary
	// accurate seek lands on the keyframe and discards the leading pictures.
	// A copy/remux keeps +fastseek: it copies packets and never decodes, and
	// its own keyframe clamp lives in copy_seek_anchor.go.
	fflags := "+genpts"
	if isVideoCopy {
		fflags = "+genpts+fastseek"
	}
	analyzeDuration := "3000000" // 3 seconds (default 5s)
	probeSize := "5000000"       // 5 MB (default 5MB, explicit for clarity)
	if isRemote {
		fflags += "+nobuffer"
		analyzeDuration = "1000000" // 1 second for remote streams to avoid WAN stalling
		probeSize = "1000000"       // 1 MB
	}
	args = append(args,
		"-fflags", fflags,
		"-analyzeduration", analyzeDuration,
		"-probesize", probeSize,
	)
	if isRemote {
		args = append(args,
			"-seekable", "1",
			"-reconnect", "1",
			"-reconnect_at_eof", "1",
			"-reconnect_streamed", "1",
			"-reconnect_delay_max", "2",
		)
	}

	// Seek before input for fast seeking.
	if opts.SeekSeconds > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", opts.SeekSeconds))
		// When video is copied but audio is transcoded, accurate_seek causes
		// A/V desync: video must start at a keyframe but audio is trimmed to
		// the exact seek point. Disabling it keeps both streams aligned.
		if isVideoCopy && !isAudioCopy {
			args = append(args, "-noaccurate_seek")
		}
	}

	// Input file.
	args = append(args, "-i", opts.InputPath)
	args = append(args, "-map_metadata", "-1")
	args = append(args, "-map_chapters", "-1")
	args = appendStreamSelectionArgs(args, opts)
	args = appendTimestampNormalizationArgs(args, opts)

	// Video codec and encoding settings.
	if isVideoCopy {
		args = append(args, "-c:v", "copy")
		videoBitstreamFilter := ""
		if strings.EqualFold(opts.SourceVideoCodec, transcodeCodecHEVC) || strings.EqualFold(opts.SourceVideoCodec, "h265") {
			videoBitstreamFilter = "hevc_mp4toannexb"
		}
		if opts.VideoBitstreamFilter == DV7ToHDR10BitstreamFilter {
			if videoBitstreamFilter != "" {
				videoBitstreamFilter += ","
			}
			videoBitstreamFilter += opts.VideoBitstreamFilter
		}
		if videoBitstreamFilter != "" {
			args = append(args, "-bsf:v", videoBitstreamFilter)
		}
	} else {
		args = appendVideoArgs(args, opts)
	}
	// Chrome and Edge require HEVC fMP4 tracks to be hvc1-tagged so parameter
	// sets are carried in the initialization box. hvc1 is valid for copied
	// HEVC and for server-encoded HEVC; dvh1 remains copy-only.
	switch opts.VideoSampleEntry {
	case VideoSampleEntryDVH1:
		args = append(args, "-tag:v", VideoSampleEntryDVH1, "-strict", "unofficial")
	case VideoSampleEntryHVC1:
		args = append(args, "-tag:v", VideoSampleEntryHVC1)
	}

	// Copy-video sessions only do audio work on the filter/encode side.
	// ffmpeg's default thread selection spawns one filter thread per CPU
	// (observed 14 idle `af#0:1` threads for a 5.1→2.0 downmix), so pin
	// audio filter + encode to a single thread.
	if isVideoCopy && !isAudioCopy {
		args = append(args, "-threads", "1", "-filter_threads", "1", "-filter_complex_threads", "1")
	}

	// Audio codec.
	args = appendAudioArgs(args, opts)
	if HLSCopiesAACIntoFMP4(opts) {
		args = appendCopiedAACToMP4Args(args, opts.CopyAudioCodec)
	}

	// Subtitle burn-in and resolution scaling — only when encoding video.
	if !isVideoCopy {
		args = appendVideoFilterArgs(args, opts)
		args = appendSegmentBoundaryArgs(args, opts)
	}

	// HLS output options.
	// Codec-copy and HEVC sessions use fMP4 segments — no transmuxing needed in
	// hls.js, which avoids Safari MSE compatibility issues with certain codecs
	// in TS. MPEG-2 video is the exception: Apple consumes it as compatibility
	// HLS, so package it in MPEG-TS while still copying the video stream.
	// Actual transcoding uses MPEG-TS segments to avoid the hls.js endOfStream()
	// race with fMP4 (hls.js #6337).
	var segmentPattern string
	segmentType := HLSOutputContainer(opts)
	videoUsesFMP4 := videoUsesFMP4(opts)
	if videoUsesFMP4 {
		segmentType = "fmp4"
		segmentPattern = filepath.Join(opts.OutputDir, "seg_%05d.m4s")
	} else {
		segmentPattern = filepath.Join(opts.OutputDir, "seg_%05d.ts")
	}
	manifestPath := filepath.Join(opts.OutputDir, "stream.m3u8")

	args = append(args,
		"-max_muxing_queue_size", "2048",
		"-max_delay", strconv.Itoa(hlsMuxMaxDelayMicros),
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", opts.SegmentDuration),
		// Bound real playlists as well as synthetic ones. Segment files remain on
		// disk because delete_segments is not enabled, while the manifest itself
		// cannot grow without limit during multi-day sessions.
		"-hls_list_size", strconv.Itoa(maxSyntheticManifestSegments),
		"-hls_segment_type", segmentType,
		// Write segments to temp files first so the player never fetches a
		// partially-written segment during a quality switch.
		"-hls_flags", "independent_segments+temp_file",
		"-hls_segment_filename", segmentPattern,
	)
	// fMP4 segments need movflags=+frag_discont so each fragment writes
	// audio DTS/PTS including the initial delay into MOOF→TRAF→TFDT.
	// Without this, some browsers (notably Chromium on macOS) can experience
	// A/V sync issues during copy-mode HLS playback. Matches Jellyfin's
	// proven fMP4 HLS pipeline.
	if videoUsesFMP4 {
		args = append(args, "-hls_segment_options", "movflags=+frag_discont")
	}
	if opts.StartSegmentNumber > 0 {
		args = append(args, "-start_number", fmt.Sprintf("%d", opts.StartSegmentNumber))
	}
	args = append(args, manifestPath)
	args = appendSubtitleRenditionOutputArgs(args, opts)

	return args
}

// resolveEffectiveTranscodeHWAccel returns the backend that will actually execute the recipe.
func resolveEffectiveTranscodeHWAccel(opts TranscodeOpts) string {
	return resolveEffectiveTranscodeHWAccelContext(context.Background(), opts)
}

func resolveEffectiveTranscodeHWAccelContext(ctx context.Context, opts TranscodeOpts) string {
	// The device goes with the backend: resolution probes it, so a host whose
	// first render node belongs to another vendor is not verified on hardware
	// the transcode will never open.
	hwAccel := ResolveHWAccelWithFFmpegContext(ctx, opts.HWAccel, opts.FFmpegPath, opts.HWDevice)
	if hwAccel == "" {
		return ""
	}
	if strings.EqualFold(opts.TargetCodecVideo, "copy") {
		return HWAccelNone
	}
	if IsMPEG4Part2VideoCodec(opts.SourceVideoCodec) {
		return HWAccelNone
	}
	if hwAccel == transcodeHWVideoToolbox {
		// A frozen HEVC tone-map recipe retains its GPU graph; the target
		// validation after allocation can encode its converted frames on CPU.
		if ok, reason := videoToolboxSupportsTargetCodecContext(ctx, opts.FFmpegPath, opts.TargetCodecVideo); !ok &&
			(!strings.EqualFold(opts.TargetCodecVideo, transcodeCodecHEVC) || opts.ToneMapMode != tonemap.ModeHardware) {
			slog.WarnContext(ctx, "VideoToolbox target encoder unavailable; using software encoding",
				"target_codec", opts.TargetCodecVideo, "reason", reason)
			return transcodeHWNone
		}
		// A fully unconstrained request (no ladder rung, no bitrate cap —
		// e.g. the jellycompat paths) has always encoded with quality-based
		// CRF. VideoToolbox has no portable constant-quality mode and a flat
		// bitrate fallback visibly degrades high-resolution sources, so keep
		// ordinary unconstrained transcodes on the software encoder. A frozen
		// hardware tone-map recipe cannot change executors here; VideoToolbox's
		// default bitrate remains the safe executable form of that recipe.
		if opts.TargetBitrateKbps <= 0 && opts.TargetResolution == "" && opts.ToneMapMode != tonemap.ModeHardware {
			slog.InfoContext(ctx, "VideoToolbox skipped for unconstrained transcode; using quality-based software encoding")
			return transcodeHWNone
		}
	}
	// An explicit NVENC setting keeps the established libx264 fallback for a
	// source the CUDA decoder must not receive. Intel QSV/VAAPI have the
	// explicit upload paths below and retain hardware encoding. Only the
	// automatic pipeline's mixed stage, which falls back to software itself if
	// the CUDA upload fails, keeps NVENC encoding after a CPU decode.
	if opts.SoftwareVideoDecode && hwAccel == transcodeHWNVENC &&
		(!opts.nvencSoftwareDecode || opts.ToneMapMode != "") {
		return HWAccelNone
	}
	return hwAccel
}

// bitmapBurnInActive reports whether this transcode composites a bitmap
// subtitle track (PGS/VOBSUB/DVB) into the video via the overlay
// filter_complex pipeline. Bitmap burn-in requires a video encode: copy-video
// sessions never activate it (the API layer forces an encoding recipe before
// starting a burn-in transcode, this is a defensive backstop).
func bitmapBurnInActive(opts TranscodeOpts) bool {
	return opts.SubtitleBurnIn &&
		opts.SubtitleTrackIndex >= 0 &&
		NeedsBurnIn(opts.SubtitleCodec) &&
		!strings.EqualFold(opts.TargetCodecVideo, "copy")
}

// burnInEncodeActive reports whether this transcode encodes video while
// compositing a subtitle. Unlike bitmapBurnInActive it also covers text
// burn-in, because both paths share the realtime-sensitive quality-based rate
// control.
func burnInEncodeActive(opts TranscodeOpts) bool {
	return opts.SubtitleBurnIn &&
		opts.SubtitleTrackIndex >= 0 &&
		!strings.EqualFold(opts.TargetCodecVideo, "copy")
}

// burnInPeakCapKbps is the hard peak bitrate ceiling for a burn-in encode. It
// is keyed by output resolution rather than the requested target bitrate so a
// source-preserving encode can never run at the source bitrate.
func burnInPeakCapKbps(opts TranscodeOpts) int {
	switch resolutionHeightV3(opts.TargetResolution) {
	case 2160:
		return 40_000
	case 1080:
		return 10_000
	case 720:
		return 4_000
	}
	return opts.TargetBitrateKbps
}

// burnInPeakCapArgs emits the peak cap shared by every hardware burn-in
// encoder. It never emits -b:v: the quantizer drives quality, and -maxrate is a
// ceiling only.
func burnInPeakCapArgs(opts TranscodeOpts) []string {
	capKbps := burnInPeakCapKbps(opts)
	if capKbps <= 0 {
		return nil
	}
	return []string{
		"-maxrate", fmt.Sprintf("%dk", capKbps),
		"-bufsize", fmt.Sprintf("%dk", capKbps*2),
	}
}

// appendStreamSelectionArgs limits output to primary video/audio streams.
// When bitmap burn-in is active the video output comes from the overlay
// filter_complex graph's labeled pad instead of the raw input stream —
// mapping both would emit two video streams into the HLS mux.
func appendStreamSelectionArgs(args []string, opts TranscodeOpts) []string {
	audioTrackIndex := opts.AudioTrackIndex
	if opts.AudioOnly {
		// Audio-only prepared media has no video stream to map.
	} else if bitmapBurnInActive(opts) {
		args = append(args, "-map", "[vout]")
	} else {
		args = append(args, "-map", "0:v:0")
	}
	if audioTrackIndex >= 0 {
		audioMap := fmt.Sprintf("0:a:%d", audioTrackIndex)
		if !opts.RequireAudioTrack {
			audioMap += "?"
		}
		args = append(args, "-map", audioMap)
	} else {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args, "-sn")
	args = append(args, "-dn")
	return args
}

// HLSOutputContainer is shared by FFmpeg argument construction and activity
// reporting so copied MPEG-2/forced-TS streams cannot be mislabeled as fMP4.
// Encoded AV1 also packages as fMP4 (see transcodeVideoUsesFMP4).
func HLSOutputContainer(opts TranscodeOpts) string {
	if videoUsesFMP4(opts) {
		return OutputContainerFMP4
	}
	return OutputContainerMPEGTS
}

// HLSCopiesAACIntoFMP4 reports whether this plan copies an AAC audio track
// into fMP4 segments, which need aac_adtstoasc.
func HLSCopiesAACIntoFMP4(opts TranscodeOpts) bool {
	return !TranscodesAudio(opts.TargetCodecAudio) &&
		HLSOutputContainer(opts) == OutputContainerFMP4 &&
		copiedAudioIsAAC(opts.CopyAudioCodec)
}

// copyVideoUsesFMP4 reports whether copied video is packaged as fragmented MP4
// rather than MPEG-TS. Shared by segment-type selection and timestamp policy
// so the two cannot disagree.
func copyVideoUsesFMP4(opts TranscodeOpts) bool {
	return strings.EqualFold(opts.TargetCodecVideo, "copy") &&
		!opts.CopyVideoMPEGTS &&
		!IsMPEG2VideoCodec(opts.SourceVideoCodec)
}

// transcodeVideoUsesFMP4 reports whether an encoded video stream must be
// packaged as fragmented MP4. AV1 has no MPEG-TS stream type: FFmpeg muxes it
// as an unregistered private stream that HLS readers cannot identify, so an
// AV1 transcode always uses fMP4. Server-encoded HEVC also requires an
// hvc1-tagged ISO-BMFF track for the negotiated MSE recipe. Every other
// encoded codec keeps the established MPEG-TS packaging.
func transcodeVideoUsesFMP4(opts TranscodeOpts) bool {
	return !strings.EqualFold(opts.TargetCodecVideo, "copy") &&
		(strings.EqualFold(opts.TargetCodecVideo, transcodeCodecAV1) ||
			strings.EqualFold(opts.TargetCodecVideo, transcodeCodecHEVC))
}

// videoUsesFMP4 is the single packaging decision shared by the muxer arguments
// and the on-disk segment extension.
func videoUsesFMP4(opts TranscodeOpts) bool {
	return copyVideoUsesFMP4(opts) || transcodeVideoUsesFMP4(opts)
}

// Negative timestamps must still be lifted. When the audio is re-encoded to
// AAC the encoder's 1024-sample priming delay places the first audio packet
// before zero, and with "disabled" the mov muxer writes that value straight
// into the first fragment's tfdt (baseMediaDecodeTime -1024). ExoPlayer/Media3
// rejects any tfdt with the sign bit set ("Top bit not zero"). Both copy-video
// and encoded HEVC use fMP4, so both need make_non_negative. It shifts every
// stream by the same minimal offset only when a timestamp is negative,
// retaining positive source timestamps on seeks. MPEG-TS has no tfdt and
// keeps the source timestamps untouched.
func appendTimestampNormalizationArgs(args []string, opts TranscodeOpts) []string {
	const negativeTSDisabled = "disabled"
	negativeTS := negativeTSDisabled
	if videoUsesFMP4(opts) {
		negativeTS = "make_non_negative"
	}
	args = append(args,
		"-copyts",
		"-avoid_negative_ts", negativeTS,
	)
	if strings.EqualFold(opts.TargetCodecVideo, "copy") {
		args = append(args, "-start_at_zero")
	}
	return args
}

// framesPerSegment is the GOP length that makes one segment an integer number
// of source frames. The historical 30 fps ceiling is used when the source rate
// is unknown, so zero-valued legacy recipes keep their old GOP.
func framesPerSegment(opts TranscodeOpts) int {
	fps := opts.SourceFrameRate
	if fps <= 0 || math.IsNaN(fps) || math.IsInf(fps, 0) {
		fps = 30
	}
	segmentDuration := opts.SegmentDuration
	if segmentDuration <= 0 {
		segmentDuration = defaultSegmentDuration
	}
	frames := int(math.Round(fps * float64(segmentDuration)))
	if frames < 1 {
		frames = segmentDuration
	}
	return frames
}

// appendSegmentBoundaryArgs forces keyframes on segment boundaries so each HLS
// fragment starts cleanly and can be appended independently by the player.
//
// FFmpeg's force_key_frames expression clock starts at the first encoded
// frame, even with -copyts. Subtracting the source seek would suppress forced
// keyframes until that much output had elapsed. Segments would then follow the
// encoder's GOP instead of the synthetic manifest's fixed-duration timeline,
// giving the same segment number different source times after a seek restart.
func appendSegmentBoundaryArgs(args []string, opts TranscodeOpts) []string {
	if opts.softwareEncode {
		opts.HWAccel = transcodeHWNone
	}
	args = append(args, "-sc_threshold", "0")
	args = append(args, "-force_key_frames",
		fmt.Sprintf("expr:gte(t,n_forced*%d)", opts.SegmentDuration))

	// Hardware encoders (QSV, VAAPI, NVENC, VideoToolbox) may not reliably
	// honor force_key_frames expressions. Set explicit GOP size so segment
	// boundaries always start with an intra frame. Derive it from the real
	// source frame rate so a 23.976 fps source gets a 2.0 s GOP instead of the
	// 30 fps ceiling's 2.5 s fragments. Unknown rates keep the historical 30 fps
	// assumption.
	if opts.HWAccel == transcodeHWQSV || opts.HWAccel == transcodeHWVAAPI ||
		opts.HWAccel == transcodeHWNVENC || opts.HWAccel == transcodeHWVideoToolbox {
		gopSize := fmt.Sprintf("%d", framesPerSegment(opts))
		args = append(args, "-g", gopSize, "-keyint_min", gopSize)
	}
	// QSV otherwise encodes force_key_frames requests as non-IDR intra frames.
	// The HLS muxer cannot split independent segments on those frames, so a
	// 23.976 fps source with a 60-frame GOP produces 2.5-5 second fragments and
	// breaks the fixed-duration VOD manifest/restart timeline. Promote requested
	// keyframes to IDRs so the muxer cuts at the requested boundaries.
	if opts.HWAccel == transcodeHWQSV {
		args = append(args, "-forced_idr", "1")
	}

	return args
}

// appendHWAccelArgs adds hardware acceleration flags based on the HWAccel setting.
// The caller must resolve "auto" via ResolveHWAccel before calling this.
func appendHWAccelArgs(args []string, opts TranscodeOpts) []string {
	switch opts.HWAccel {
	case "qsv":
		hwDevice := PickRenderDevice(opts.HWDevice)
		if hwDevice == "" {
			slog.Warn("no GPU render device found, QSV transcode may fail")
			hwDevice = "/dev/dri/renderD128" // last-resort fallback
		}
		// VAAPI→QSV hardware pipeline: derive QSV from VAAPI device.
		args = append(args, tonemap.QSVInitDeviceArgs(hwDevice)...)
		if opts.ToneMapMode == tonemap.ModeHardware && opts.ToneMapFilter == tonemap.HardwareFilterOpenCL {
			args = append(args, "-init_hw_device", "opencl=ocl@va")
		}
		args = append(args, "-filter_hw_device", "va")
		if !opts.SoftwareVideoDecode {
			args = append(args, "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi")
		}
		args = append(args, "-noautorotate")
	case "vaapi":
		vaapiDevice := PickRenderDevice(opts.HWDevice)
		if vaapiDevice == "" {
			vaapiDevice = "/dev/dri/renderD128" // last-resort fallback
		}
		args = append(args, tonemap.VAAPIInitDeviceArgs(vaapiHWDeviceAlias, vaapiDevice)...)
		if opts.ToneMapMode == tonemap.ModeHardware {
			args = append(args, tonemap.VAAPIOpenCLInitArgs(vaapiHWDeviceAlias, opts.ToneMapFilter, opts.ToneMapSourceKind)...)
		}
		args = append(args, "-filter_hw_device", vaapiHWDeviceAlias)
		if !opts.SoftwareVideoDecode {
			args = append(args, "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi")
		}
	case transcodeHWNVENC:
		if opts.SoftwareVideoDecode {
			// CPU decode for the automatic pipeline's mixed stage. The filter
			// graph uploads with the generic hwupload filter, which honors
			// -filter_hw_device; hwupload_cuda would always open CUDA device 0.
			// No -noautorotate: frames are decoded to system memory, so
			// FFmpeg's automatic transpose runs before the upload, as it does
			// for the libx264 path.
			cudaDevice := "cuda=" + nvencFilterDeviceAlias
			if hwDevice := strings.TrimSpace(opts.HWDevice); hwDevice != "" {
				cudaDevice += ":" + hwDevice
			}
			return append(args,
				"-init_hw_device", cudaDevice,
				"-filter_hw_device", nvencFilterDeviceAlias,
			)
		}
		args = append(args,
			"-hwaccel", "cuda",
			"-hwaccel_output_format", "cuda",
			"-noautorotate",
		)
		if hwDevice := strings.TrimSpace(opts.HWDevice); hwDevice != "" {
			args = append(args, "-hwaccel_device", hwDevice)
		}
	case transcodeHWVideoToolbox:
		// Hardware tone mapping keeps decoded frames as IOSurfaces for
		// scale_vt/VTPixelTransferSession. Other VideoToolbox transcodes land
		// frames in system memory so ordinary scale and subtitle filters apply
		// unchanged and the encoder uploads internally.
		// H.264 Hi10P decodes in software (VideoToolbox cannot), same as the
		// QSV/VAAPI paths; the encoder still runs in hardware.
		if opts.ToneMapMode == tonemap.ModeHardware && opts.SoftwareVideoDecode {
			args = append(args, "-init_hw_device", "videotoolbox=vt", "-filter_hw_device", "vt")
		} else if !opts.SoftwareVideoDecode {
			args = append(args, "-hwaccel", "videotoolbox")
			if opts.ToneMapMode == tonemap.ModeHardware {
				args = append(args, "-hwaccel_output_format", "videotoolbox_vld")
			}
		}
	}
	return args
}

// videoPreset returns an encoder-compatible preset. CPU encoders use a faster
// fast-start preset for initial playback, while QSV stays on the fastest
// preset family it supports.
func videoPreset(opts TranscodeOpts, hwAccel string) string {
	if opts.preparedFileEncode {
		// Software HEVC at "medium" takes hours for a 4K film; "fast" keeps a
		// download's preparation practical while still beating veryfast.
		if hwAccel != "qsv" && strings.EqualFold(opts.TargetCodecVideo, transcodeCodecHEVC) {
			return "fast"
		}
		return "medium"
	}
	if hwAccel == "qsv" {
		return "veryfast"
	}
	if opts.FastStart {
		return "superfast"
	}
	return "veryfast"
}

// appendVideoArgs adds video codec arguments.
func appendVideoArgs(args []string, opts TranscodeOpts) []string {
	if opts.softwareEncode {
		opts.HWAccel = transcodeHWNone
	}
	codec := opts.TargetCodecVideo
	if codec == "" {
		codec = transcodeCodecH264
	}

	if codec == "copy" {
		return append(args, "-c:v", "copy")
	}

	preset := videoPreset(opts, opts.HWAccel)
	hasBitrateCap := opts.TargetBitrateKbps > 0

	burnInEncode := burnInEncodeActive(opts)

	switch {
	// A burn-in encode composites subtitles (and usually a tone-map) on top of
	// the encode, so the encode itself must stay light. Use quality-based rate
	// control with a hard peak cap instead of CBR at the target bitrate: a CBR
	// encode at a source-class rung still burns the encoder trying to hit it.
	case burnInEncode && opts.HWAccel == transcodeHWQSV && codec == transcodeCodecH264:
		args = append(args, "-c:v", "h264_qsv", "-preset", preset, "-global_quality", "23", "-look_ahead", "0")
		args = append(args, burnInPeakCapArgs(opts)...)
	case burnInEncode && opts.HWAccel == transcodeHWQSV && codec == transcodeCodecHEVC:
		args = append(args, "-c:v", "hevc_qsv", "-preset", preset, "-global_quality", "26", "-look_ahead", "0")
		args = append(args, burnInPeakCapArgs(opts)...)
	case burnInEncode && opts.HWAccel == transcodeHWQSV && codec == transcodeCodecAV1:
		// AV1 is the burn-in last resort: loosest quantizer, no look-ahead.
		args = append(args, "-c:v", "av1_qsv", "-preset", preset, "-global_quality", "30")
		args = append(args, burnInPeakCapArgs(opts)...)
	case burnInEncode && opts.HWAccel == transcodeHWVAAPI && codec == transcodeCodecH264:
		args = append(args, "-c:v", "h264_vaapi", "-qp", "23")
		args = append(args, burnInPeakCapArgs(opts)...)
	case burnInEncode && opts.HWAccel == transcodeHWVAAPI && codec == transcodeCodecHEVC:
		args = append(args, "-c:v", "hevc_vaapi", "-qp", "26")
		args = append(args, burnInPeakCapArgs(opts)...)
	case opts.HWAccel == "qsv" && codec == transcodeCodecH264:
		args = append(args, "-c:v", "h264_qsv", "-preset", preset)
		if hasBitrateCap {
			args = appendCappedVBRArgs(args, opts.TargetBitrateKbps)
		} else {
			args = append(args, "-global_quality", "23")
		}
	case opts.HWAccel == "qsv" && codec == transcodeCodecHEVC:
		args = append(args, "-c:v", "hevc_qsv", "-preset", preset)
		if hasBitrateCap {
			args = appendCappedVBRArgs(args, opts.TargetBitrateKbps)
		} else {
			args = append(args, "-global_quality", "28")
		}
	case opts.HWAccel == "qsv" && codec == transcodeCodecAV1:
		// AV1 is selected only when the client declares hardware decode and the
		// deployment runs QSV; see selectTargetVideoCodecV3.
		if hasBitrateCap {
			args = append(args, "-c:v", "av1_qsv", "-preset", preset,
				"-b:v", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-maxrate", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-bufsize", fmt.Sprintf("%dk", opts.TargetBitrateKbps*2))
		} else {
			args = append(args, "-c:v", "av1_qsv", "-preset", preset, "-global_quality", "30")
		}
	case opts.HWAccel == "vaapi" && codec == transcodeCodecH264:
		args = append(args, "-c:v", "h264_vaapi")
		if hasBitrateCap {
			// An explicit -qp selects CQP, which ignores -maxrate entirely.
			args = appendCappedVBRArgs(appendVAAPIRateControl(args, opts), opts.TargetBitrateKbps)
		} else {
			args = append(args, "-qp", "23")
		}
	case opts.HWAccel == "vaapi" && codec == transcodeCodecHEVC:
		args = append(args, "-c:v", "hevc_vaapi")
		if hasBitrateCap {
			args = appendCappedVBRArgs(appendVAAPIRateControl(args, opts), opts.TargetBitrateKbps)
		} else {
			args = append(args, "-qp", "28")
		}
	case opts.HWAccel == transcodeHWNVENC && codec == transcodeCodecH264:
		args = append(args, "-c:v", "h264_nvenc", "-rc:v", "vbr")
		if hasBitrateCap {
			args = appendCappedVBRArgs(args, opts.TargetBitrateKbps)
		} else {
			args = append(args, "-cq:v", "23", "-b:v", "0")
		}
	case opts.HWAccel == transcodeHWNVENC && codec == transcodeCodecHEVC:
		args = append(args, "-c:v", "hevc_nvenc", "-rc:v", "vbr")
		if hasBitrateCap {
			args = appendCappedVBRArgs(args, opts.TargetBitrateKbps)
		} else {
			args = append(args, "-cq:v", "28", "-b:v", "0")
		}
	case opts.HWAccel == transcodeHWVideoToolbox && codec == transcodeCodecH264:
		// 8-bit output for browser MSE compatibility (mirrors the libx264
		// path); VideoToolbox has no 10-bit H.264 encode. Hardware tone
		// mapping already emits NV12. Re-requesting yuv420p at the encoder
		// boundary makes FFmpeg 9.0.1 crash on 2160p scale_vt output.
		args = append(args, "-c:v", "h264_videotoolbox")
		if opts.ToneMapMode != tonemap.ModeHardware {
			args = append(args, "-pix_fmt", "yuv420p")
		}
		args = append(args, "-profile:v", "high")
		args = appendVideoToolboxRateControl(args, opts)
	case opts.HWAccel == transcodeHWVideoToolbox && codec == transcodeCodecHEVC:
		// HLS HEVC delivery promises Main 8-bit SDR output. Hardware tone
		// mapping already produces an NV12 frame, so do not add a second
		// format/profile request at the encoder boundary.
		args = append(args, "-c:v", "hevc_videotoolbox")
		if opts.ToneMapMode != tonemap.ModeHardware {
			args = append(args, "-pix_fmt", pixelFormatYUV420P, "-profile:v", hevcMainProfileV3)
		}
		args = appendVideoToolboxRateControl(args, opts)
	default:
		// CPU fallback — match Jellyfin's proven browser-compatible settings.
		// Force yuv420p to ensure 8-bit output (10-bit sources produce High 10
		// Profile which browsers cannot decode via MSE).
		if codec == transcodeCodecHEVC {
			args = append(args, "-c:v", "libx265", "-preset", preset, "-crf", "28", "-pix_fmt", "yuv420p")
		} else {
			args = append(args, "-c:v", "libx264", "-preset", preset, "-crf", "23",
				"-pix_fmt", "yuv420p", "-profile:v", "high")
			// Level 4.1 caps H.264 at 1080p30; a download may be 4K or 60 fps,
			// so libx264 picks the level from the actual output instead.
			if !opts.preparedFileEncode {
				args = append(args, "-level", "4.1")
			}
		}
		if hasBitrateCap {
			args = append(args,
				"-maxrate", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-bufsize", fmt.Sprintf("%dk", opts.TargetBitrateKbps*2))
		}
	}
	if opts.ToneMapMode != "" {
		args = append(args,
			"-color_range", "tv",
			"-color_primaries", "bt709",
			"-color_trc", "bt709",
		)
		// FFmpeg treats -colorspace as a requested pixel conversion for VAAPI,
		// QSV, and CUDA frames. VideoToolbox has already downloaded an NV12
		// software frame here and needs the explicit matrix because its encoder
		// otherwise preserves the source BT.2020 matrix in the H.264 stream.
		if opts.ToneMapMode == tonemap.ModeSoftware || opts.HWAccel == transcodeHWVideoToolbox || opts.softwareEncode {
			args = append(args, "-colorspace", "bt709")
		}
	}

	return args
}

// appendVAAPIRateControl forces the capped mode resolveVAAPIRateControl
// found on the device; FFmpeg's automatic mode could pick AVBR, which does
// not honor -maxrate.
func appendVAAPIRateControl(args []string, opts TranscodeOpts) []string {
	if opts.vaapiRateControl != "" {
		return append(args, "-rc_mode", opts.vaapiRateControl)
	}
	return args
}

// appendCappedVBRArgs makes a bitrate cap a ceiling for the hardware encoders.
// Setting -b:v equal to -maxrate selects CBR on QSV, which pads easy content
// up to the cap; averaging at 90% of the cap keeps VBR engaged. Measured on
// Intel QSV and VAAPI, this matched CBR's SSIM at a lower bitrate and never
// exceeded the cap.
func appendCappedVBRArgs(args []string, capKbps int) []string {
	return append(args,
		"-b:v", fmt.Sprintf("%dk", capKbps*9/10),
		"-maxrate", fmt.Sprintf("%dk", capKbps),
		"-bufsize", fmt.Sprintf("%dk", capKbps*2))
}

func appendVideoToolboxRateControl(args []string, opts TranscodeOpts) []string {
	bitrateKbps := opts.TargetBitrateKbps
	if bitrateKbps <= 0 {
		bitrateKbps = map[string]int{
			transcodeResolution480p:  1_500,
			transcodeResolution720p:  2_000,
			transcodeResolution1080p: 6_000,
			transcodeResolution2160p: 20_000,
		}[opts.TargetResolution]
		if bitrateKbps == 0 {
			bitrateKbps = 6_000
		}
	}
	return append(args,
		"-b:v", fmt.Sprintf("%dk", bitrateKbps),
		"-maxrate", fmt.Sprintf("%dk", bitrateKbps),
		"-bufsize", fmt.Sprintf("%dk", bitrateKbps*2),
	)
}

// appendVideoFilterArgs appends the -vf selection for an encoding (non-copy)
// video stream: the subtitle burn-in chain (which includes scaling and hw
// download/upload for QSV/VAAPI) or the hwaccel-appropriate standalone scale
// filter. The ONE home of this decision — the HLS builder and the single-file
// prepare builder must always produce identical filter chains (a fix landing
// in only one of them silently ships wrong cached artifacts).
func appendVideoFilterArgs(args []string, opts TranscodeOpts) []string {
	if opts.ToneMapMode != "" {
		return appendToneMapFilterArgs(args, opts)
	}
	switch {
	case bitmapBurnInActive(opts):
		return appendBitmapSubtitleBurnInArgs(args, opts)
	case opts.SubtitleBurnIn && opts.SubtitleTrackIndex >= 0:
		return appendSubtitleBurnInArgs(args, opts)
	case opts.HWAccel == "qsv" && opts.SoftwareVideoDecode:
		return append(args, "-vf", qsvSoftwareDecodeFilter(opts.TargetResolution))
	case opts.HWAccel == "vaapi" && opts.SoftwareVideoDecode:
		return append(args, "-vf", vaapiSoftwareDecodeFilter(opts.TargetResolution))
	case opts.HWAccel == transcodeHWNVENC && opts.SoftwareVideoDecode:
		return append(args, "-vf", nvencSoftwareDecodeFilter(opts.TargetResolution))
	case opts.HWAccel == "qsv":
		return append(args, "-vf", qsvScaleFilter(opts.TargetResolution))
	case opts.HWAccel == "vaapi":
		return append(args, "-vf", vaapiScaleFilter(opts.TargetResolution))
	case opts.HWAccel == transcodeHWNVENC:
		return append(args, "-vf", nvencScaleFilter(opts.TargetResolution))
	case opts.TargetResolution != "":
		if scale := resolutionToScale(opts.TargetResolution); scale != "" {
			return append(args, "-vf", scale)
		}
	}
	return args
}

// appendToneMapFilterArgs selects the subtitle-aware or scale-only tone-map
// graph and leaves args unchanged if no valid graph exists.
func appendToneMapFilterArgs(args []string, opts TranscodeOpts) []string {
	start := len(args)
	args = appendToneMapExecutorFilterArgs(args, opts)
	if opts.softwareEncode && opts.HWAccel != transcodeHWVideoToolbox {
		// Each GPU graph ends in NV12 surfaces. VideoToolbox already returns
		// CPU frames; the other executors download only after conversion,
		// scaling, subtitle composition, and HDR metadata removal.
		const download = ",hwdownload,format=nv12"
		for i := start; i+1 < len(args); i++ {
			switch args[i] {
			case "-vf": //nolint:goconst // FFmpeg flag stays inline with its -filter_complex twin below.
				args[i+1] += download
			case "-filter_complex":
				args[i+1] = strings.TrimSuffix(args[i+1], "[vout]") + download + "[vout]"
			}
		}
	}
	return args
}

func appendToneMapExecutorFilterArgs(args []string, opts TranscodeOpts) []string {
	switch {
	case bitmapBurnInActive(opts):
		return appendToneMappedBitmapSubtitleArgs(args, opts)
	case opts.SubtitleBurnIn && opts.SubtitleTrackIndex >= 0:
		filter := toneMappedTextSubtitleFilter(opts)
		if filter == "" {
			return args
		}
		return append(args, "-vf", filter)
	default:
		filter := toneMapScaleFilter(opts)
		if filter == "" {
			return args
		}
		return append(args, "-vf", filter)
	}
}

// toneMapScaleFilter builds the subtitle-free graph for the frozen software or
// hardware executor, including output scaling and HDR metadata removal.
func toneMapScaleFilter(opts TranscodeOpts) string {
	switch opts.ToneMapMode {
	case tonemap.ModeSoftware:
		filter := tonemap.SoftwareFilter(opts.ToneMapSourceKind, opts.ToneMapFilter)
		if scale := resolutionToScale(opts.TargetResolution); scale != "" {
			filter += "," + scale
		}
		return filter
	case tonemap.ModeHardware:
		switch opts.HWAccel {
		case transcodeHWQSV:
			if opts.ToneMapFilter == tonemap.HardwareFilterQSVVPP {
				return softwareToneMapUploadFilter(opts) + tonemap.QSVVPPToneMapFilter(opts.ToneMapSourceKind) + "," + qsvVPPInputScaleFilter(opts.TargetResolution) + "," + tonemap.HDRMetadataRemovalFilter()
			}
			// The OpenCL tone map is unchanged; only its tail moves. The result
			// maps directly to QSV and scaling runs on the media engine, which
			// drops the VAAPI scale and one device hop without touching quality.
			return softwareToneMapUploadFilter(opts) + tonemap.QSVVPPTailFilter(opts.ToneMapSourceKind) + "," + qsvVPPInputScaleFilter(opts.TargetResolution) + "," + tonemap.HDRMetadataRemovalFilter()
		case transcodeHWVAAPI:
			if vaapiOpenCLToneMap(opts) {
				height, _ := scaleTargetHeight(opts.TargetResolution)
				return softwareToneMapUploadFilter(opts) + tonemap.IntelFilter(opts.ToneMapSourceKind, height) + "," + tonemap.HDRMetadataRemovalFilter()
			}
			return softwareToneMapUploadFilter(opts) + tonemap.VAAPIFilter(opts.ToneMapSourceKind) + "," + vaapiScaleFilter(opts.TargetResolution) + "," + tonemap.HDRMetadataRemovalFilter()
		case transcodeHWNVENC:
			if tonemap.IsSDRSource(opts.ToneMapSourceKind) {
				filter := nvencSDRFallbackDownload(opts) + "," + tonemap.SoftwareFilter(opts.ToneMapSourceKind, "")
				if scale := resolutionToScale(opts.TargetResolution); scale != "" {
					filter += "," + scale
				}
				return filter + ",format=nv12,hwupload_cuda"
			}
			return tonemap.SourceParameters(opts.ToneMapSourceKind) + "," + tonemap.CUDAFilter(opts.ToneMapSourceKind) + "," + nvencScaleFilter(opts.TargetResolution) + "," + tonemap.HDRMetadataRemovalFilter()
		case transcodeHWVideoToolbox:
			return videoToolboxToneMapCPUFilter(opts) + "," + tonemap.HDRMetadataRemovalFilter()
		}
	}
	return ""
}

// toneMappedTextSubtitleFilter places CPU subtitle rendering after conversion
// and downloads or uploads frames only where the selected backend requires it.
func toneMappedTextSubtitleFilter(opts TranscodeOpts) string {
	subtitleInputPath := opts.InputPath
	if opts.subtitleFilterInputPath != "" {
		subtitleInputPath = opts.subtitleFilterInputPath
	}
	subFilter := fmt.Sprintf("subtitles='%s':si=%d", escapeFilterPath(subtitleInputPath), opts.SubtitleTrackIndex)
	scale := resolutionToScale(opts.TargetResolution)
	cpuTail := subFilter
	if scale != "" {
		cpuTail = scale + "," + subFilter
	}

	if opts.ToneMapMode == tonemap.ModeSoftware {
		return tonemap.SoftwareFilter(opts.ToneMapSourceKind, opts.ToneMapFilter) + "," + cpuTail
	}
	switch opts.HWAccel {
	case transcodeHWQSV:
		return softwareToneMapUploadFilter(opts) + tonemap.IntelFilter(opts.ToneMapSourceKind, 0) + ",hwdownload,format=nv12," + cpuTail + ",format=nv12,hwupload,hwmap=derive_device=qsv:mode=read+write,format=qsv," + tonemap.HDRMetadataRemovalFilter()
	case transcodeHWVAAPI:
		filter := tonemap.VAAPIFilter(opts.ToneMapSourceKind)
		if vaapiOpenCLToneMap(opts) {
			filter = tonemap.IntelFilter(opts.ToneMapSourceKind, 0)
		}
		return softwareToneMapUploadFilter(opts) + filter + ",hwdownload,format=nv12," + cpuTail + ",format=nv12,hwupload," + tonemap.HDRMetadataRemovalFilter()
	case transcodeHWNVENC:
		if tonemap.IsSDRSource(opts.ToneMapSourceKind) {
			return nvencSDRFallbackDownload(opts) + "," + tonemap.SoftwareFilter(opts.ToneMapSourceKind, "") + "," + cpuTail + ",format=nv12,hwupload_cuda"
		}
		return tonemap.SourceParameters(opts.ToneMapSourceKind) + "," + tonemap.CUDAFilter(opts.ToneMapSourceKind) + ",hwdownload,format=nv12," + cpuTail + ",format=nv12,hwupload_cuda," + tonemap.HDRMetadataRemovalFilter()
	case transcodeHWVideoToolbox:
		return videoToolboxToneMapCPUFilter(opts) + "," + subFilter + "," + tonemap.HDRMetadataRemovalFilter()
	default:
		return ""
	}
}

// appendToneMappedBitmapSubtitleArgs builds a complex graph that converts the
// video before overlaying a bitmap subtitle and then restores encoder surfaces.
func appendToneMappedBitmapSubtitleArgs(args []string, opts TranscodeOpts) []string {
	subInput := fmt.Sprintf("[0:s:%d]", opts.SubtitleTrackIndex)
	var graph string
	if opts.ToneMapMode == tonemap.ModeSoftware {
		filters := tonemap.SoftwareFilter(opts.ToneMapSourceKind, opts.ToneMapFilter)
		graph = "[0:v:0]" + filters + "[vmain];[vmain]" + subInput + "overlay=eof_action=pass"
		if scale := resolutionToScale(opts.TargetResolution); scale != "" {
			graph += "," + scale
		}
		graph += "[vout]"
		return append(args, "-filter_complex", graph)
	}

	switch opts.HWAccel {
	case transcodeHWQSV:
		if tonemap.IsSDRSource(opts.ToneMapSourceKind) {
			graph = "[0:v:0]" + softwareToneMapUploadFilter(opts) + tonemap.IntelFilter(opts.ToneMapSourceKind, 0) + "[vmain];" +
				subInput + "format=bgra,hwupload[sub];[vmain][sub]overlay_vaapi=eof_action=pass," +
				qsvToneMapScaleFilter(opts.TargetResolution) + "," + tonemap.HDRMetadataRemovalFilter() + "[vout]"
		} else {
			graph = "[0:v:0]" + softwareToneMapUploadFilter(opts) + openCLBitmapSubtitleGraph(opts, subInput)
		}
	case transcodeHWVAAPI:
		if vaapiOpenCLToneMap(opts) {
			graph = "[0:v:0]" + softwareToneMapUploadFilter(opts) + openCLBitmapSubtitleGraph(opts, subInput)
		} else {
			graph = "[0:v:0]" + softwareToneMapUploadFilter(opts) + tonemap.VAAPIFilter(opts.ToneMapSourceKind) + "[vmain];" +
				subInput + "format=bgra,hwupload[sub];[vmain][sub]overlay_vaapi=eof_action=pass," +
				vaapiScaleFilter(opts.TargetResolution) + "," + tonemap.HDRMetadataRemovalFilter() + "[vout]"
		}
	case transcodeHWNVENC:
		if tonemap.IsSDRSource(opts.ToneMapSourceKind) {
			graph = "[0:v:0]" + nvencSDRFallbackDownload(opts) + "," + tonemap.SoftwareFilter(opts.ToneMapSourceKind, "") + "[vmain];[vmain]" + subInput + "overlay=eof_action=pass"
		} else {
			graph = "[0:v:0]" + tonemap.SourceParameters(opts.ToneMapSourceKind) + "," + tonemap.CUDAFilter(opts.ToneMapSourceKind) + ",hwdownload,format=nv12[vmain];[vmain]" +
				subInput + "overlay=eof_action=pass"
		}
		if scale := resolutionToScale(opts.TargetResolution); scale != "" {
			graph += "," + scale
		}
		graph += ",format=nv12,hwupload_cuda," + tonemap.HDRMetadataRemovalFilter() + "[vout]"
	case transcodeHWVideoToolbox:
		graph = "[0:v:0]" + videoToolboxToneMapCPUFilter(opts) + "[vmain];[vmain]" +
			subInput + "overlay=eof_action=pass," + tonemap.HDRMetadataRemovalFilter() + "[vout]"
	}
	if graph == "" {
		return args
	}
	return append(args, "-filter_complex", graph)
}

// openCLBitmapSubtitleGraph composites a bitmap subtitle onto an HDR frame
// while it is still on the OpenCL device after tone mapping, then scales it
// there, for QSV and for VAAPI on an Intel GPU. overlay_vaapi reading the
// tone-mapped surface corrupts frames the same way a scale_vaapi does (see
// tonemap.IntelFilter). The subtitle goes on at the source resolution before
// the scale, as with overlay_vaapi, and its RGB frames are converted with the
// BT.709 matrix of the tone-mapped frame.
//
// scale_opencl, overlay_opencl's eof_action and YUVA overlays, and a shared
// OpenCL device for both hwupload and hwmap come from Jellyfin FFmpeg, which
// the OpenCL tone-map path already requires for tonemap_opencl's bt2390 curve.
// The probe's listing gate checks that both filters exist.
func openCLBitmapSubtitleGraph(opts TranscodeOpts, subInput string) string {
	scale := ""
	if height, ok := scaleTargetHeight(opts.TargetResolution); ok {
		scale = fmt.Sprintf("scale_opencl=w=-2:h=%d,", height)
	}
	tail := tonemap.IntelOpenCLReturnFilter()
	if opts.HWAccel == transcodeHWQSV {
		tail += "," + tonemap.QSVInteropFilter()
	}
	return tonemap.IntelOpenCLToneMapFilter(opts.ToneMapSourceKind, 0) + "[vmain];" +
		subInput + "scale=out_color_matrix=bt709:out_range=tv,format=yuva420p,hwupload=derive_device=opencl[sub];" +
		"[vmain][sub]overlay_opencl=eof_action=pass," + scale +
		tail + "," + tonemap.HDRMetadataRemovalFilter() + "[vout]"
}

// vaapiOpenCLToneMap reports whether a VAAPI recipe tone maps on OpenCL, as
// the probe selects on Intel GPUs.
func vaapiOpenCLToneMap(opts TranscodeOpts) bool {
	return opts.HWAccel == transcodeHWVAAPI && tonemap.OpenCLToneMaps(opts.ToneMapFilter, opts.ToneMapSourceKind)
}

// softwareToneMapUploadFilter moves CPU-decoded frames onto the configured
// Intel or VAAPI device before invoking the selected hardware tone-map graph.
func softwareToneMapUploadFilter(opts TranscodeOpts) string {
	if opts.SoftwareVideoDecode && (opts.HWAccel == transcodeHWQSV || opts.HWAccel == transcodeHWVAAPI) {
		return tonemap.SoftwareUploadFilter(opts.ToneMapSourceKind)
	}
	return ""
}

// nvencSDRFallbackDownload preserves the decoded bit depth while bringing an
// SDR Dolby Vision base layer to the CPU for unsupported CUDA color conversion.
func nvencSDRFallbackDownload(opts TranscodeOpts) string {
	return "hwdownload,format=" + tonemap.NVENCSoftwareFallbackPixelFormat(opts.SourceVideoBitDepth)
}

// videoToolboxToneMapCPUFilter converts on an IOSurface, integrates scaling,
// then returns an 8-bit software frame for VideoToolbox H.264 encoding or CPU
// subtitle composition. Software-decoded sources are uploaded with explicit
// source color attachments first.
func videoToolboxToneMapCPUFilter(opts TranscodeOpts) string {
	width, height := videoToolboxScaleDimensions(opts.TargetResolution)
	filter := ""
	if opts.SoftwareVideoDecode {
		filter = tonemap.VideoToolboxUploadFilter(opts.ToneMapSourceKind, opts.SourceVideoBitDepth) + ","
	}
	return filter + tonemap.VideoToolboxFilter(width, height) + "," + tonemap.VideoToolboxDownloadFilter(opts.SourceVideoBitDepth)
}

func videoToolboxScaleDimensions(resolution string) (string, string) {
	if height, ok := scaleTargetHeight(resolution); ok {
		return "-2", strconv.Itoa(height)
	}
	return "iw", "ih"
}

// TranscodesAudio reports whether a transcode with the given target audio
// codec re-encodes the audio stream. Only an explicit "copy" passes audio
// through; an empty codec runs ffmpeg's AAC default (see appendAudioArgs), so
// every consumer of the session's audio decision — live stream state, recipe
// cards, and the compat mirror — must share this predicate or the activity
// bucket flips between remux and audio across restarts.
func TranscodesAudio(targetCodecAudio string) bool {
	return !strings.EqualFold(strings.TrimSpace(targetCodecAudio), "copy")
}

// IsAudioToAACStereoDownmixV3 reports whether the frozen audio facts select
// the versioned surround-to-stereo AAC recipe. A zero target channel count is
// the historical AAC default and therefore resolves to stereo; every other
// codec or output layout remains on its ordinary recipe.
func IsAudioToAACStereoDownmixV3(sourceChannels int, targetCodecAudio string, targetAudioChannels int) bool {
	codec := strings.TrimSpace(targetCodecAudio)
	return sourceChannels > 2 &&
		(codec == "" || strings.EqualFold(codec, "aac")) &&
		(targetAudioChannels == 0 || targetAudioChannels == 2)
}

const (
	aacTimestampNormalizeFilterV3 = "aresample=async=1"
	stereoDownmixBoostFilterV3    = "aresample=out_chlayout=stereo:async=1,alimiter=level_in=2:limit=0.794328235:attack=5:release=50:level=false:latency=true"
)

// appendStereoDownmixBoostArgs applies the playback downmix policy only after
// the source is explicitly rematrixed to stereo. The order matters: limiting
// the source channels before FFmpeg sums them would still allow the final
// stereo signal to clip. The limiter's input gain is +6.0206 dB; its -2 dBFS
// sample ceiling leaves headroom for lossy-codec and inter-sample overshoot.
// async=1 enables FFmpeg's timestamp-matching fill/trim behavior while
// retaining the source clock and first packet timestamp. On the affected
// inputs, the resulting fixed-duration AAC packets no longer carry the small
// PTS gaps that Firefox renders as audible zero-fill crackle.
func appendStereoDownmixBoostArgs(args []string, sourceChannels, outputChannels int) []string {
	if sourceChannels <= 2 || outputChannels != 2 {
		return args
	}
	return append(args, "-af", stereoDownmixBoostFilterV3)
}

// appendAACEncodeFilterArgs gives every AAC encode a continuous sample clock.
// Matroska commonly represents fixed 1024-sample AAC frames on a millisecond
// timebase, so their packet PTS alternate between rounded 21 ms and 22 ms
// steps. Carrying those timestamps into fragmented MP4 leaves sub-frame gaps
// that Firefox renders as audible crackle. The resampler corrects only the
// timestamp jitter; surround-to-stereo conversions retain the existing
// rematrix and limiter policy.
func appendAACEncodeFilterArgs(args []string, sourceChannels int, targetCodec string, targetChannels, outputChannels int) []string {
	if IsAudioToAACStereoDownmixV3(sourceChannels, targetCodec, targetChannels) && outputChannels == 2 {
		return appendStereoDownmixBoostArgs(args, sourceChannels, outputChannels)
	}
	return append(args, "-af", aacTimestampNormalizeFilterV3)
}

// appendAudioArgs adds audio codec arguments. Supports "copy" for passthrough,
// plus opus / aac / eac3 / ac3 as re-encode targets. EAC3 and AC3 are useful
// when we must transcode video but want to preserve surround channels for an
// HDMI receiver — both are legal in HLS fMP4 (not MPEG-TS; ensure the HLS
// packager is fMP4 when emitting these).
func appendAudioArgs(args []string, opts TranscodeOpts) []string {
	// Case-insensitive so the switch agrees with TranscodesAudio for any
	// client-supplied spelling.
	codec := strings.ToLower(strings.TrimSpace(opts.TargetCodecAudio))
	if codec == "" {
		codec = "aac"
	}

	switch codec {
	case "copy":
		args = append(args, "-c:a", "copy", "-strict", "-2")
	case "opus":
		args = append(args, "-c:a", "libopus", "-b:a", "192k", "-ac", "2")
	case "eac3":
		// Typical Dolby Digital Plus 5.1 bitrate; let the source dictate channel
		// count so we preserve surround when possible.
		args = append(args, "-c:a", "eac3", "-b:a", "384k")
	case "ac3":
		// Legacy Dolby Digital; universal AVR support.
		args = append(args, "-c:a", "ac3", "-b:a", "448k")
	default:
		channels, bitrateKbps := ResolveAACOutputV3(opts.TargetAudioChannels, opts.TargetAudioBitrateKbps)
		args = append(args, "-c:a", "aac", "-b:a", strconv.Itoa(bitrateKbps)+"k", "-ac", strconv.Itoa(channels))
		args = appendAACEncodeFilterArgs(args, opts.SourceAudioChannels, opts.TargetCodecAudio, opts.TargetAudioChannels, channels)
	}

	return args
}

// ResolveAACOutputV3 returns the encoded channel count and bitrate in kbps,
// applying the encoder defaults when no bitrate is specified. Negotiation and
// FFmpeg argument construction must use the same output facts.
func ResolveAACOutputV3(targetChannels, targetBitrateKbps int) (int, int) {
	channels := 2
	if targetChannels == 1 {
		channels = 1
	} else if targetChannels >= 6 {
		channels = 6
	}
	if targetBitrateKbps > 0 {
		return channels, targetBitrateKbps
	}
	switch channels {
	case 1:
		return channels, 128
	case 6:
		return channels, 384
	default:
		return channels, 192
	}
}

// hwSurfaceToCPUFilter brings hardware-decoded frames to the CPU for software
// subtitle rendering. It is two conversions, not one: hwdownload can only write
// the surface's own software format (nv12 at 8-bit, p010le at 10-bit), so the
// yuv420p that overlay and libass need has to come from a separate format=
// after the download. Asking hwdownload for yuv420p directly fails the graph
// with "Invalid output format yuv420p for hwframe download" before the encoder
// ever opens.
func hwSurfaceToCPUFilter(opts TranscodeOpts) string {
	return "hwdownload,format=" + tonemap.SurfaceDownloadPixelFormat(opts.SourceVideoBitDepth) + ",format=yuv420p"
}

// appendBitmapSubtitleBurnInArgs adds burn-in arguments for BITMAP subtitle
// codecs (PGS/VOBSUB/DVB). libass's subtitles= filter cannot render bitmap
// tracks, so the decoded subtitle stream is composited onto the video with an
// overlay in a -filter_complex graph (the "Plex route"). The graph's output
// pad [vout] replaces the raw video stream in stream mapping (see
// appendStreamSelectionArgs), so -vf must never be emitted alongside this.
//
// Overlay runs at the source's native resolution FIRST and any target scaling
// happens after, so bitmap subtitle geometry is never distorted by a
// pre-scaling mismatch between the video and subtitle planes.
// eof_action=pass keeps the video flowing untouched once the subtitle stream
// ends instead of freezing the last overlay frame on screen.
//
// QSV/VAAPI composite ON the GPU via overlay_vaapi: the decoded video never
// leaves its VAAPI surface, and only the small, low-frequency subtitle bitmap is
// uploaded. This avoids the full-frame GPU→CPU→GPU roundtrip a software overlay
// forces — that roundtrip runs below realtime on 1080p sources (~0.7x), starving
// the client, and can crash the QSV buffer path with SIGBUS. See
// appendSubtitleBurnInArgs for the TEXT path, which must stay on CPU because
// libass is a software renderer. NVENC and CPU encodes keep the software overlay:
// overlay_cuda is unverified on this build, so the CUDA path retains the safe
// (if slower) roundtrip rather than risk a broken graph.
func appendBitmapSubtitleBurnInArgs(args []string, opts TranscodeOpts) []string {
	// [0:s:N] indexes subtitle streams only, matching the si=N semantics of
	// the text path — SubtitleTrackIndex is the embedded subtitle ordinal.
	subInput := fmt.Sprintf("[0:s:%d]", opts.SubtitleTrackIndex)

	var graph string
	switch opts.HWAccel {
	case "qsv":
		if opts.SoftwareVideoDecode {
			graph = softwareDecodedBitmapBurnInGraph(opts, subInput, true)
			break
		}
		// GPU composite: upload only the subtitle bitmap, overlay it onto the
		// QSV video surface, then scale for the encoder. Frames stay on the
		// QSV device end-to-end (no VAAPI surface derivation, which deadlocks
		// against remote HLS inputs).
		graph = subInput + "format=bgra,hwupload[sub];" +
			"[0:v:0][sub]overlay_vaapi=eof_action=pass," + qsvScaleFilter(opts.TargetResolution) + "[vout]"
	case "vaapi":
		if opts.SoftwareVideoDecode {
			graph = softwareDecodedBitmapBurnInGraph(opts, subInput, false)
			break
		}
		// GPU composite: same as QSV but the frames stay on VAAPI through the
		// encoder, so no cross-device map is needed.
		graph = subInput + "format=bgra,hwupload[sub];" +
			"[0:v:0][sub]overlay_vaapi=eof_action=pass," + vaapiScaleFilter(opts.TargetResolution) + "[vout]"
	default:
		// NVENC and CPU: software overlay on CPU frames. Build the overlay
		// fragment (subtitle input + optional post-scale) once, then wire it into
		// the encode-specific pipeline.
		cpuFilters := subInput + "overlay=eof_action=pass"
		if scale := resolutionToScale(opts.TargetResolution); scale != "" {
			cpuFilters += "," + scale
		}
		if opts.HWAccel == transcodeHWNVENC && opts.SoftwareVideoDecode {
			// CPU-decoded frames need no download; upload the composite to the
			// configured CUDA device.
			graph = softwareDecodedBitmapBurnInGraph(opts, subInput, false)
		} else if opts.HWAccel == transcodeHWNVENC {
			// Download to CPU for the overlay, then re-upload to CUDA.
			graph = "[0:v:0]" + hwSurfaceToCPUFilter(opts) + "[vmain];[vmain]" + cpuFilters +
				",format=nv12,hwupload_cuda[vout]"
		} else {
			// CPU encoding: overlay directly on decoded frames.
			graph = "[0:v:0]" + cpuFilters + "[vout]"
		}
	}

	return append(args, "-filter_complex", graph)
}

// softwareDecodedBitmapBurnInGraph composites decoded CPU frames and uploads
// the finished NV12 frames for the hardware encoder. It is the Hi10 AVC
// counterpart of the all-hardware overlay_vaapi graph above.
func softwareDecodedBitmapBurnInGraph(opts TranscodeOpts, subInput string, qsv bool) string {
	filters := subInput + "overlay=eof_action=pass"
	if scale := resolutionToScale(opts.TargetResolution); scale != "" {
		filters += "," + scale
	}
	filters += ",format=nv12,hwupload"
	if qsv {
		filters += ",hwmap=derive_device=qsv,format=qsv"
	}
	return "[0:v:0]format=yuv420p[vmain];[vmain]" + filters + "[vout]"
}

// appendSubtitleBurnInArgs adds subtitle burn-in filter arguments for TEXT
// subtitle codecs (SRT/ASS/…) via the libass-based subtitles= filter; bitmap
// codecs take the overlay path in appendBitmapSubtitleBurnInArgs.
// For CPU encoding, the filter chain is: [scale,]subtitles.
// For QSV/VAAPI, frames must be downloaded from hardware, processed on CPU,
// then re-uploaded: hwdownload → format=nv12|p010le → format=yuv420p →
// [scale,] subtitles → hwupload → hwmap.
func appendSubtitleBurnInArgs(args []string, opts TranscodeOpts) []string {
	scale := resolutionToScale(opts.TargetResolution)
	subtitleInputPath := opts.InputPath
	if opts.subtitleFilterInputPath != "" {
		subtitleInputPath = opts.subtitleFilterInputPath
	}
	subFilter := fmt.Sprintf("subtitles=filename='%s':si=%d",
		escapeFilterPath(subtitleInputPath), opts.SubtitleTrackIndex)

	// Build the CPU filter portion: scale (if any) then subtitle overlay.
	// Scale must come before subtitles so text is rendered at target resolution.
	var cpuFilters string
	if scale != "" {
		cpuFilters = scale + "," + subFilter
	} else {
		cpuFilters = subFilter
	}

	switch opts.HWAccel {
	case "qsv":
		if opts.SoftwareVideoDecode {
			vf := "format=yuv420p," + cpuFilters + ",format=nv12,hwupload,hwmap=derive_device=qsv,format=qsv"
			return append(args, "-vf", vf)
		}
		// QSV decode path: download from the QSV surface to CPU, apply subtitle
		// and scale filters, convert to nv12, upload back, then map to QSV for
		// the encoder. hwSurfaceToCPUFilter picks the surface's own download
		// format (nv12/p010le) so 10-bit sources do not fail the graph.
		vf := hwSurfaceToCPUFilter(opts) + "," + cpuFilters + ",format=nv12,hwupload,hwmap=derive_device=qsv,format=qsv"
		args = append(args, "-vf", vf)
	case "vaapi":
		if opts.SoftwareVideoDecode {
			vf := "format=yuv420p," + cpuFilters + ",format=nv12,hwupload"
			return append(args, "-vf", vf)
		}
		// VAAPI-only: download, apply CPU filters, convert to nv12, upload back.
		vf := hwSurfaceToCPUFilter(opts) + "," + cpuFilters + ",format=nv12,hwupload"
		args = append(args, "-vf", vf)
	case transcodeHWNVENC:
		if opts.SoftwareVideoDecode {
			// CPU-decoded frames need no download. The generic hwupload filter
			// targets the configured CUDA device.
			vf := "format=yuv420p," + cpuFilters + ",format=nv12,hwupload"
			return append(args, "-vf", vf)
		}
		// NVENC/CUDA: download to CPU for subtitle rendering, then upload back.
		vf := hwSurfaceToCPUFilter(opts) + "," + cpuFilters + ",format=nv12,hwupload_cuda"
		args = append(args, "-vf", vf)
	default:
		// CPU encoding: filters run directly on decoded frames.
		args = append(args, "-vf", cpuFilters)
	}

	return args
}

// resolutionToScale returns an ffmpeg scale filter string for the target resolution.
func resolutionToScale(res string) string {
	if height, ok := scaleTargetHeight(res); ok {
		return fmt.Sprintf("scale=-2:%d", height)
	}
	return ""
}

// qsvScaleFilter returns the pure-QSV scale filter with optional resolution
// scaling. Width must use -1 (keep aspect) rather than -2: the iHD driver
// rejects scale_qsv auto-width values below -1 ("Size values less than -1 are
// not acceptable"), so the filter fails to configure and no segment is ever
// produced.
// scaleTargetHeight parses a target resolution label into the output height
// the scale filters produce: a ladder label such as "1080p" or an exact
// box-fit height such as "800p" for a cinema-aspect download, or the short
// frame an unusually wide source fits to. Odd heights, which 4:2:0 output
// cannot take, and heights above 4320 leave the source unscaled, as unknown
// labels always have.
func scaleTargetHeight(res string) (int, bool) {
	label := strings.ToLower(strings.TrimSpace(res))
	digits, ok := strings.CutSuffix(label, "p")
	if !ok {
		return 0, false
	}
	height, err := strconv.Atoi(digits)
	if err != nil || height < 2 || height > 4320 || height%2 != 0 {
		return 0, false
	}
	return height, true
}

// vaapiNV12Filter converts VAAPI frames to NV12 without scaling.
const vaapiNV12Filter = "scale_vaapi=format=nv12"

// qsvScaleFilter returns the VAAPI→QSV filter chain with optional resolution scaling.
func qsvScaleFilter(res string) string {
	return qsvScaleFilterWithMapMode(res, "")
}

func qsvScaleFilterWithMapMode(res, mapMode string) string {
	hwmap := "hwmap=derive_device=qsv"
	if mapMode != "" {
		hwmap += ":mode=" + mapMode
	}

	switch res {
	case "2160p":
		// min(2160,ih) mirrors vaapiScaleFilter: a 2160p target must not
		// upscale a shorter source (for example a 1440p version) that reached
		// this path without the planner's source-height clamp.
		return "scale_vaapi=w=-2:h=min(2160\\,ih):format=nv12," + hwmap + ",format=qsv"
	case "1080p":
		return "scale_vaapi=w=-2:h=1080:format=nv12," + hwmap + ",format=qsv"
	case "720p":
		return "scale_vaapi=w=-2:h=720:format=nv12," + hwmap + ",format=qsv"
	case "480p":
		return "scale_vaapi=w=-2:h=480:format=nv12," + hwmap + ",format=qsv"
	case transcodeResolution420p:
		return "scale_vaapi=w=-2:h=420:format=nv12," + hwmap + ",format=qsv"
	case transcodeResolution328p:
		return "scale_vaapi=w=-2:h=328:format=nv12," + hwmap + ",format=qsv"
	default:
		// The ladder fit can name an exact height (a 3840x1600 film at the
		// 1080p class encodes 1920x800): scale it like a named rung instead
		// of passing the source through at full size.
		if height, ok := scaleTargetHeight(res); ok {
			return fmt.Sprintf("scale_vaapi=w=-2:h=%d:format=nv12,", height) + hwmap + ",format=qsv"
		}
		return vaapiNV12Filter + "," + hwmap + ",format=qsv"
	}
}

// qsvToneMapScaleFilter maps VAAPI tone-map output with read/write access. The default
// read-only mapping succeeds for synthetic upload probes but fails against
// real decoded HEVC surfaces on Intel with ENOSYS during the first frame.
func qsvToneMapScaleFilter(res string) string {
	return qsvScaleFilterWithMapMode(res, "read+write")
}

// qsvToneMapTailFilter maps VAAPI tone-map output onto the QSV encoder device.
// When the target height is not below the source height the full-frame
// scale_vaapi is a no-op, so a map-only tail avoids one shader pass over every
// frame. A real reduction keeps the scale. Unknown dimensions keep the
// established filter so no driver interop step is skipped blindly.
func qsvToneMapTailFilter(opts TranscodeOpts) string {
	targetHeight := resolutionHeightV3(opts.TargetResolution)
	if targetHeight > 0 && opts.SourceHeight > 0 && targetHeight >= opts.SourceHeight {
		return "hwmap=derive_device=qsv:mode=read+write,format=qsv"
	}
	return qsvToneMapScaleFilter(opts.TargetResolution)
}

// qsvVPPInputScaleFilter scales frames already mapped to the QSV device with
// the media-engine VPP scaler. Width must use -1 rather than -2: the iHD driver
// rejects any auto-width below -1 ("Size values less than -1 are not
// acceptable"), the same constraint qsvScaleFilter documents for scale_qsv.
func qsvVPPInputScaleFilter(res string) string {
	switch res {
	case "2160p":
		return "vpp_qsv=w=-1:h=min(2160\\,ih):format=nv12"
	case "1080p":
		return "vpp_qsv=w=-1:h=1080:format=nv12"
	case "720p":
		return "vpp_qsv=w=-1:h=720:format=nv12"
	case "480p":
		return "vpp_qsv=w=-1:h=480:format=nv12"
	case transcodeResolution420p:
		return "vpp_qsv=w=-1:h=420:format=nv12"
	case transcodeResolution328p:
		return "vpp_qsv=w=-1:h=328:format=nv12"
	default:
		// Exact ladder-fit heights scale here too; width stays -1: the iHD
		// driver rejects scale_qsv auto-width values below -1.
		if height, ok := scaleTargetHeight(res); ok {
			return fmt.Sprintf("vpp_qsv=w=-1:h=%d:format=nv12", height)
		}
		return "vpp_qsv=format=nv12"
	}
}

func qsvSoftwareDecodeFilter(res string) string {
	cpuFilters := ""
	if scale := resolutionToScale(res); scale != "" {
		cpuFilters = scale + ","
	}
	// High 10 AVC is decoded on the CPU. Scale those software frames before
	// upload, matching the proven text-subtitle path; uploading first and then
	// invoking scale_vaapi can leave the VAAPI/QSV graph alive without ever
	// producing its initial HLS window.
	return cpuFilters + "format=nv12,hwupload,hwmap=derive_device=qsv,format=qsv"
}

// vaapiScaleFilter keeps VAAPI frames in hardware and converts them to a
// browser-compatible encoder format. Using the CPU scale filter on VAAPI frames
// causes FFmpeg auto_scale format-negotiation failures.
func vaapiScaleFilter(res string) string {

	switch res {
	case "2160p":
		return "scale_vaapi=w=-2:h=min(2160\\,ih):format=nv12"
	case "1080p":
		return "scale_vaapi=w=-2:h=1080:format=nv12"
	case "720p":
		return "scale_vaapi=w=-2:h=720:format=nv12"
	case "480p":
		return "scale_vaapi=w=-2:h=480:format=nv12"
	case transcodeResolution420p:
		return "scale_vaapi=w=-2:h=420:format=nv12"
	case transcodeResolution328p:
		return "scale_vaapi=w=-2:h=328:format=nv12"
	default:
		// Exact ladder-fit heights (see qsvScaleFilterWithMapMode).
		if height, ok := scaleTargetHeight(res); ok {
			return fmt.Sprintf("scale_vaapi=w=-2:h=%d:format=nv12", height)
		}
		return vaapiNV12Filter
	}
}

func vaapiSoftwareDecodeFilter(res string) string {
	cpuFilters := ""
	if scale := resolutionToScale(res); scale != "" {
		cpuFilters = scale + ","
	}
	return cpuFilters + "format=nv12,hwupload"
}

// nvencSoftwareDecodeFilter uploads CPU-decoded NV12 frames to the CUDA device
// named by -filter_hw_device, then scales and encodes on the GPU.
func nvencSoftwareDecodeFilter(res string) string {
	return "format=nv12,hwupload," + nvencScaleFilter(res)
}

func nvencScaleFilter(res string) string {
	if height, ok := scaleTargetHeight(res); ok {
		return fmt.Sprintf("scale_cuda=w=-2:h=%d:format=nv12", height)
	}
	return "scale_cuda=format=nv12"
}

// filterPathReplacer escapes special characters in file paths for ffmpeg
// filter syntax. Backslash is escaped FIRST so it cannot re-interpret the
// escapes introduced for the other characters (a trailing '\' in a URL or
// path would otherwise escape the closing quote of the subtitles= argument).
var filterPathReplacer = strings.NewReplacer(
	"\\", "\\\\",
	"'", "'\\''",
	"[", "\\[",
	"]", "\\]",
	";", "\\;",
	",", "\\,",
)

// escapeFilterPath escapes special characters in file paths for ffmpeg filter syntax.
func escapeFilterPath(path string) string {
	return filterPathReplacer.Replace(path)
}

const subtitleFilterAliasName = "subtitle-source.media"

// prepareSubtitleFilterInput avoids passing the library filename through
// FFmpeg's nested filter parsers. In particular, an apostrophe can be consumed
// by the filtergraph parser even after it was escaped for the subtitles filter.
// A stable alias in the session directory keeps the media input unchanged and
// is retained for seek restarts with the rest of TranscodeOpts.
// textSubtitleFilter is the libass subtitles filter of a text burn-in: the
// staged track, the only stream of its file, with its fonts, or else the
// track's index in the media file.
func textSubtitleFilter(opts TranscodeOpts) string {
	if opts.subtitleFilterFontsDir != "" {
		return fmt.Sprintf("subtitles=filename='%s':fontsdir='%s'",
			escapeFilterPath(opts.subtitleFilterInputPath), escapeFilterPath(opts.subtitleFilterFontsDir))
	}
	subtitleInputPath := opts.InputPath
	if opts.subtitleFilterInputPath != "" {
		subtitleInputPath = opts.subtitleFilterInputPath
	}
	return fmt.Sprintf("subtitles=filename='%s':si=%d", escapeFilterPath(subtitleInputPath), opts.SubtitleTrackIndex)
}

// prepareSubtitleFilterInput gives the libass subtitles filter of a text
// burn-in its input. It stages the track on its own first
// (stageBurnInSubtitle), because given the media file libass reads all of it
// before the first frame. When the track can't be staged, the filter reads
// the media file through a stable alias in the session directory, which
// avoids passing the library filename through FFmpeg's nested filter
// parsers: an apostrophe can be consumed by the filtergraph parser even after
// it was escaped for the subtitles filter. Both are retained for seek
// restarts with the rest of TranscodeOpts.
func prepareSubtitleFilterInput(ctx context.Context, opts *TranscodeOpts) error {
	if !opts.SubtitleBurnIn || opts.SubtitleTrackIndex < 0 || NeedsBurnIn(opts.SubtitleCodec) {
		return nil
	}

	staged, err := stageBurnInSubtitle(ctx, *opts)
	if err == nil {
		opts.subtitleFilterInputPath = staged.trackPath
		opts.subtitleFilterFontsDir = staged.fontsDir
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	slog.InfoContext(ctx, "burn-in subtitle not staged; libass reads the media file",
		"component", "playback", "playback_session_id", opts.SessionID, "subtitle_track", opts.SubtitleTrackIndex, "error", err)
	opts.subtitleFilterFontsDir = ""

	aliasPath := filepath.Join(opts.OutputDir, subtitleFilterAliasName)
	// InputPath is fspath-encoded; the link must point at the real file name.
	target, err := fspath.Readlink(aliasPath)
	switch {
	case err == nil:
		if target != opts.InputPath {
			return fmt.Errorf("prepare subtitle filter input: alias targets unexpected source")
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.Symlink(fspath.Decode(opts.InputPath), aliasPath); err != nil {
			return fmt.Errorf("prepare subtitle filter input: %w", err)
		}
	default:
		return fmt.Errorf("prepare subtitle filter input: inspect alias: %w", err)
	}

	opts.subtitleFilterInputPath = aliasPath
	return nil
}

// minManifestSegments is the standard startup lead for actively encoded HLS.
// True video transcodes benefit from a larger cushion so playback does not
// outrun the encoder immediately after the first frame appears.
const minManifestSegments = 3

// minCopyManifestSegments is the startup lead for codec-copy sessions.
// Copying video while transcoding only audio can produce startup files far
// faster than real-time encoding, so waiting for 3 full segments adds
// unnecessary latency at playback start.
const minCopyManifestSegments = 1

// minFreshHardwareManifestSegments keeps one complete fragment of headroom
// after the first playable fragment. A fresh hardware encoder produces that
// window comfortably ahead of real time, while CPU encodes and reconstructed
// generations retain the larger three-fragment safety margin below.
const minFreshHardwareManifestSegments = 2

func startupSegmentRequirement(opts TranscodeOpts) int {
	if strings.EqualFold(opts.TargetCodecVideo, "copy") {
		return minCopyManifestSegments
	}
	if opts.FastStart {
		switch opts.HWAccel {
		case transcodeHWQSV, transcodeHWVAAPI, transcodeHWNVENC, transcodeHWVideoToolbox:
			return minFreshHardwareManifestSegments
		}
	}
	return minManifestSegments
}

// GetManifest returns the HLS m3u8 manifest content.
// It returns ErrManifestNotReady if the manifest does not yet contain enough
// segments for reliable HLS playback (see minManifestSegments).
func (s *TranscodeSession) GetManifest() ([]byte, error) {
	return s.getManifest(false)
}

// getManifest implements GetManifest. With currentGeneration set, the
// stream.m3u8 an earlier generation left in a reused output directory is
// treated as absent: it says nothing about whether this process can produce
// output. The check compares file identity rather than timestamps, so a
// filesystem whose clock differs from this host's cannot hide fresh output.
func (s *TranscodeSession) getManifest(currentGeneration bool) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	manifestPath := filepath.Join(s.outputDir, "stream.m3u8")
	var inherited os.FileInfo
	if currentGeneration {
		inherited = s.inheritedManifest
	}
	data, _, err := readManifestFile(manifestPath, inherited)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !s.running {
				if s.restarting != nil {
					return nil, ErrManifestNotReady
				}
				if s.processCanceled {
					// This generation was stopped by the session itself, so its
					// playlist will never become ready. Retryable, not a
					// transcode verdict: the replacement generation may be
					// producing right now.
					return nil, ErrManifestNotReady
				}
				if s.waitErr != nil {
					stderr := truncateStderr(s.stderr.String())
					if stderr != "" {
						return nil, fmt.Errorf("%w: %w (stderr: %s)", ErrTranscodeFailed, s.waitErr, stderr)
					}
					return nil, fmt.Errorf("%w: %w", ErrTranscodeFailed, s.waitErr)
				}
				return nil, ErrTranscodeFailed
			}
			return nil, ErrManifestNotReady
		}
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	requiredSegments := startupSegmentRequirement(s.opts)

	// A copy-mode manifest with a zero TARGETDURATION or non-positive EXTINF
	// durations is broken output, not playable media: fail fast with the
	// validation error so the broken fixture surfaces "invalid copy playback
	// manifest" instead of "manifest not ready after 30s". This runs before
	// the readiness gate because a zero-duration playlist can never satisfy
	// it: without the fail-fast it would block for ManifestStartupTimeout.
	// It also runs when the session stopped before the readiness check so
	// broken output from a finished-but-invalid transcode cannot leak to the
	// player either.
	if startupCopyManifestIsBroken(data) {
		if err := validateCopyPlaybackManifest(data); err != nil {
			return nil, fmt.Errorf("invalid copy playback manifest: %w", err)
		}
	}

	// Wait until enough startup media exists before serving. Counting #EXTINF
	// lines alone is not enough for FFmpeg's live-written manifest because the
	// playlist can reference copy-mode segments before the files are fully
	// flushed to disk, especially on resumed sessions with a non-zero media
	// sequence. Requiring the referenced startup files prevents the browser from
	// stalling on its very first segment fetch.
	if !startupFilesReady(data, s.outputDir, requiredSegments) {
		if s.running || s.restarting != nil {
			return nil, ErrManifestNotReady
		}
		if s.processCanceled {
			// Same self-teardown verdict as above: the missing startup media
			// belongs to a generation nobody is producing any more.
			return nil, ErrManifestNotReady
		}
		if s.cmd == nil && s.waitErr == nil {
			return data, nil
		}
		if s.waitErr != nil {
			stderr := truncateStderr(s.stderr.String())
			if stderr != "" {
				return nil, fmt.Errorf("%w: %w (stderr: %s)", ErrTranscodeFailed, s.waitErr, stderr)
			}
			return nil, fmt.Errorf("%w: %w", ErrTranscodeFailed, s.waitErr)
		}
		return nil, ErrTranscodeFailed
	}
	if strings.EqualFold(s.opts.TargetCodecVideo, "copy") {
		if err := validateCopyPlaybackManifest(data); err != nil {
			return nil, fmt.Errorf("invalid copy playback manifest: %w", err)
		}
	}
	// Only readiness reads are gated on decoded video (see
	// videoFramesVerdictLocked): a manifest re-read for a session that already
	// served one must keep its prompt answer while a replacement warms up.
	if currentGeneration || !s.manifestServed {
		if err := s.videoFramesVerdictLocked(data, time.Now()); err != nil {
			return nil, err
		}
	}

	return data, nil
}

// videoFramesVerdictLocked decides whether a video encode generation has
// produced decodable video or whether its start must be held back. A video
// encode proves it can play only by decoding frames: segments on disk prove
// the muxer wrote bytes, and an encode whose decoder produced nothing still
// writes a non-empty segment for every audio packet, which is how a start once
// committed a plan that never showed a frame and left the client to burn its
// own startup guard. So the start is held until the generation's frame counter
// moves, and a generation that keeps muxing past the grace without one is a
// typed verdict the caller acts on instead of waiting out its budget.
//
// It reports nil for every plan that cannot answer the question: a copy/remux
// plan (which passes no -progress), a finished generation (whose exit is judged
// by the process outcome), and a session with no spawned generation (the
// historical in-test fixtures). A completed playlist is also tolerated without
// frames: a very short clip, or a seek that lands in the last fragment, can
// legitimately finish with an audio-only tail, exactly as startupFilesReady
// already tolerates an end-listed window below the live runway.
//
// Callers must hold mu.
func (s *TranscodeSession) videoFramesVerdictLocked(manifest []byte, now time.Time) error {
	if !s.running || s.generationStartedAt.IsZero() || !reportsVideoFrames(s.opts) {
		return nil
	}
	if s.videoFrames > 0 {
		return nil
	}
	// A completed playlist needs no further frames: the generation has already
	// reached the end of what it can produce.
	if bytes.Contains(manifest, []byte("#EXT-X-ENDLIST")) {
		return nil
	}
	elapsed := now.Sub(s.generationStartedAt)
	if elapsed < noVideoFramesGrace {
		// Still inside the grace: hold the start rather than call it sick. A
		// healthy encoder reports its first frames before it can write its
		// first segment, so this normally lasts one poll.
		return ErrManifestNotReady
	}
	return fmt.Errorf("%w: 0 frames decoded after %s while muxing (out_time_us=%d)",
		ErrTranscodeNoVideoFrames, elapsed.Round(time.Millisecond), s.videoOutTimeUS)
}

// statManifest returns the output directory's current stream.m3u8, or nil.
func statManifest(outputDir string) os.FileInfo {
	if outputDir == "" {
		return nil
	}
	info, err := os.Stat(filepath.Join(outputDir, "stream.m3u8"))
	if err != nil {
		return nil
	}
	return info
}

// readManifestFile reads a playlist. When the open file is still inherited,
// it reports os.ErrNotExist without reading the old playlist, which a startup
// wait would otherwise read in full on every check. The comparison uses the
// open file: FFmpeg replaces the playlist by rename, so a separate Stat by path
// could describe a newer file than the bytes returned and let an inherited
// playlist pass as current.
func readManifestFile(path string, inherited os.FileInfo) ([]byte, os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if inherited != nil && sameManifestFile(info, inherited) {
		return nil, info, os.ErrNotExist
	}
	data, err := io.ReadAll(f)
	return data, info, err
}

// sameManifestFile reports whether current is still the inherited playlist.
// Size and modification time guard against the filesystem reusing the old
// inode for a later rename.
func sameManifestFile(current, inherited os.FileInfo) bool {
	return os.SameFile(current, inherited) &&
		current.Size() == inherited.Size() &&
		current.ModTime().Equal(inherited.ModTime())
}

// outputPollInterval is the pause between checks while a request waits for
// FFmpeg output. FFmpeg tells the server nothing when it renames a finished
// segment into place or rewrites the playlist, so a finished file can go
// unnoticed for up to one interval. Every transcoded startup and seek waits on
// output at least once. A check opens or stats one file, or reads the playlist
// and stats its startup segments, so a short interval costs little.
const outputPollInterval = 10 * time.Millisecond

// WaitForManifest polls until the manifest is ready for playback or the timeout
// expires. It keeps the initial request open long enough for FFmpeg to write
// the first safe playback window instead of forcing the client to race a 503.
func (s *TranscodeSession) WaitForManifest(timeout time.Duration) ([]byte, error) {
	return s.WaitForManifestContext(context.Background(), timeout)
}

// WaitForManifestContext is WaitForManifest with an owner deadline: the wait
// ends at whichever comes first — the per-wait timeout or the context's end.
// Transport startup passes its single startup context here so a slow provider
// resolve leaves the manifest wait only the remainder of the budget instead of
// a fresh full timeout. A nil ctx behaves like WaitForManifest.
func (s *TranscodeSession) WaitForManifestContext(ctx context.Context, timeout time.Duration) ([]byte, error) {
	return s.waitForManifest(ctx, timeout, false)
}

// WaitForManifestContextPolls is WaitForManifestContext that also reports how
// many times it polled for the manifest. The start path records the wait as its
// own phase and reports the count as a log attribute, which distinguishes a
// manifest that appeared promptly (one or two polls) from one that rode the
// deadline out (hundreds). The wait itself is unchanged: same deadline, same
// owner-cancel handling, same single manifest read per tick.
func (s *TranscodeSession) WaitForManifestContextPolls(ctx context.Context, timeout time.Duration) ([]byte, int, error) {
	return s.waitForManifestPolls(ctx, timeout, false)
}

// WaitForGenerationManifest is WaitForManifest for startup readiness: it
// accepts only a manifest the current FFmpeg generation wrote. A reconstruct
// or replacement that reuses an output directory still holds the previous
// generation's stream.m3u8 and segments, which would otherwise report a
// process that exited without output as ready.
func (s *TranscodeSession) WaitForGenerationManifest(timeout time.Duration) ([]byte, error) {
	return s.WaitForGenerationManifestContext(context.Background(), timeout)
}

// WaitForGenerationManifestContext is WaitForGenerationManifest with an owner deadline.
func (s *TranscodeSession) WaitForGenerationManifestContext(ctx context.Context, timeout time.Duration) ([]byte, error) {
	manifest, _, err := s.waitForManifestPolls(ctx, timeout, true)
	return manifest, err
}

// waitForManifest is the historical (manifest, error) form of the poll loop. It
// wraps waitForManifestPolls and drops the count, so existing callers are
// unchanged.
func (s *TranscodeSession) waitForManifest(ctx context.Context, timeout time.Duration, currentGeneration bool) ([]byte, error) {
	manifest, _, err := s.waitForManifestPolls(ctx, timeout, currentGeneration)
	return manifest, err
}

// waitForManifestPolls polls until the manifest is ready, the owner context
// ends, or timeout expires, reporting how many polls it ran. The count is
// observation only; every exit carries the same result the wait always
// produced.
func (s *TranscodeSession) waitForManifestPolls(ctx context.Context, timeout time.Duration, currentGeneration bool) ([]byte, int, error) {
	deadline := time.After(timeout)
	polls := 0
	for {
		polls++
		manifest, err := s.getManifest(currentGeneration)
		if err == nil {
			return manifest, polls, nil
		}

		// A revoked generation surfaces its decode verdict here instead of
		// the deadline's generic startup timeout — but only for virtual
		// inputs. Virtual rotation needs the verdict pre-commit (it is the
		// only path that substitutes another provider candidate), while a
		// local file commits and recovers through the replan software path,
		// which requires the committed session the verdict must not preempt.
		// The production SPS/PPS incident decayed through this exact wait.
		// The check runs every 100ms tick alongside the manifest stat, so a
		// verdict surfaces within one tick without any notification channel.
		//
		// The verdict outranks every other wait outcome, including the
		// process teardown that confirmation itself starts: reaping the
		// rejected generation makes getManifest report the killed process
		// ("transcode process failed: signal: killed") on the next poll. If
		// that generic error were returned first, the caller would rotate the
		// transport silently instead of surfacing ErrSourceDecodeRejected to
		// the candidate-rotation loop.
		s.mu.Lock()
		virtualInput := isVirtualInputPath(s.opts.CanonicalInputPath)
		s.mu.Unlock()
		if virtualInput && s.IsSourceRejected() {
			return nil, polls, ErrSourceDecodeRejected
		}
		if !errors.Is(err, ErrManifestNotReady) {
			return nil, polls, err
		}

		select {
		case <-ctx.Done():
			return nil, polls, ctx.Err()
		case <-deadline:
			return nil, polls, s.manifestTimeoutError(timeout)
		case <-time.After(outputPollInterval):
		}
	}
}

// isVirtualInputPath reports whether an ffmpeg input path names a virtual
// catalog source. Virtual playback carries the virtual:// URI in
// CanonicalInputPath while InputPath points at the loopback relay, so only
// the canonical path identifies it.
func isVirtualInputPath(path string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(path)), "virtual://")
}

// BuildPlaybackManifest returns the manifest we should expose to clients.
//
// Copy-video sessions expose their keyframe plan or FFmpeg's real manifest, so
// the playlist timing matches the variable-length fragments FFmpeg actually
// writes and, without a plan, the seekable window reflects what FFmpeg has
// produced so far. Encoded transcodes
// use the synthetic full VOD manifest only while its segment count is bounded;
// longer media uses FFmpeg's real sliding playlist.
//
// A session with a subtitle rendition answers its multivariant playlist, or
// the media playlist rawQuery selects (see buildSubtitleRenditionPlaylist).
//
// A rawQuery longer than maxManifestQueryBytes is ErrManifestQueryTooLong.
func (s *TranscodeSession) BuildPlaybackManifest(segPrefix, rawQuery string) ([]byte, error) {
	rawQuery, err := manifestQuery(rawQuery)
	if err != nil {
		return nil, err
	}
	manifest, err := s.buildPlaybackManifest(segPrefix, s.generationScopedQuery(rawQuery))
	s.noteManifestServed(err)
	return manifest, err
}

// GenerationQueryParam names the query parameter carrying a generation token.
const GenerationQueryParam = "sgen"

// generationScopedQuery binds manifest segment URLs to this session generation.
func (s *TranscodeSession) generationScopedQuery(rawQuery string) string {
	token := s.GenerationToken()
	if token == "" {
		return rawQuery
	}
	encoded := "sgen=" + url.QueryEscape(token)
	if rawQuery == "" {
		return encoded
	}
	parts := strings.Split(rawQuery, "&")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		key, _, _ := strings.Cut(part, "=")
		if key != GenerationQueryParam {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) == 0 {
		return encoded
	}
	return strings.Join(filtered, "&") + "&" + encoded
}

// buildPlaybackManifest is BuildPlaybackManifest for a query manifestQuery
// has already accepted.
func (s *TranscodeSession) buildPlaybackManifest(segPrefix, rawQuery string) ([]byte, error) {
	if s.HasSubtitleRendition() {
		playlist := s.buildSubtitleRenditionPlaylist(segPrefix, rawQuery)
		if playlist == nil {
			return nil, ErrSubtitleRenditionNotFound
		}
		return playlist, nil
	}
	return s.buildVideoPlaylist(segPrefix, rawQuery)
}

// buildVideoPlaylist is the session's video media playlist.
func (s *TranscodeSession) buildVideoPlaylist(segPrefix, rawQuery string) ([]byte, error) {
	opts := s.Opts()
	if s.copyPlan != nil {
		return s.copyPlan.manifest(opts, segPrefix, rawQuery), nil
	}
	if strings.EqualFold(opts.TargetCodecVideo, "copy") ||
		!CanGenerateSyntheticManifest(opts.TotalDuration, opts.SegmentDuration) {
		// Copy-video, unknown-duration, or oversized sessions must use FFmpeg's
		// real manifest.
		manifest, err := s.WaitForManifest(ManifestStartupTimeout)
		if err != nil {
			return nil, err
		}
		manifest = stabilizeCopyHLSRemountTimeline(manifest, opts)
		if opts.HLSVariableSubstitution {
			return RewriteManifestPathsWithSubstitution(manifest, segPrefix, rawQuery)
		}
		return RewriteManifestPaths(manifest, segPrefix, rawQuery)
	}

	return s.GenerateFullManifest(segPrefix, rawQuery), nil
}

// stabilizeCopyHLSRemountTimeline marks a bounded copy-HLS playlist as an
// append-only EVENT and gives it a precise start at the generation origin.
//
// FFmpeg's real playlist has no end tag while it is growing, so players treat
// it as live. Media3 consequently chooses the production edge whenever a track
// change rebuilds its MediaSource, even though protocol v3 deliberately reused
// the same A/V generation and the requested historical position is still in
// the playlist. EVENT plus a positive EXT-X-START disables that projection and
// leaves the client free to restore the source position on the stable timeline.
//
// The EVENT promise is made only when the known complete media fits within the
// same 50,000-segment bound passed to FFmpeg. Such a playlist never evicts an
// entry, and segment files are retained because delete_segments is not enabled.
// Unknown or oversized real playlists remain ordinary sliding live playlists.
func stabilizeCopyHLSRemountTimeline(manifest []byte, opts TranscodeOpts) []byte {
	if !strings.EqualFold(opts.TargetCodecVideo, "copy") ||
		opts.TotalDuration <= 0 ||
		!CanGenerateSyntheticManifest(opts.TotalDuration, opts.SegmentDuration) ||
		validateManifestHeader(manifest) != nil {
		return manifest
	}

	lines := bytes.Split(manifest, []byte("\n"))
	hasPlaylistType := false
	hasStart := false
	insertAfter := 0
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		switch {
		case bytes.HasPrefix(trimmed, []byte("#EXT-X-PLAYLIST-TYPE:")):
			hasPlaylistType = true
		case bytes.HasPrefix(trimmed, []byte("#EXT-X-START:")):
			hasStart = true
		case bytes.HasPrefix(trimmed, []byte("#EXT-X-VERSION:")):
			insertAfter = i + 1
		}
	}
	if hasPlaylistType && hasStart {
		return manifest
	}

	tags := make([][]byte, 0, 2)
	if !hasPlaylistType {
		tags = append(tags, []byte("#EXT-X-PLAYLIST-TYPE:EVENT"))
	}
	if !hasStart {
		tags = append(tags, []byte(fmt.Sprintf("#EXT-X-START:TIME-OFFSET=%.3f,PRECISE=YES", remountStartOffsetSeconds)))
	}
	result := make([][]byte, 0, len(lines)+len(tags))
	result = append(result, lines[:insertAfter]...)
	result = append(result, tags...)
	result = append(result, lines[insertAfter:]...)
	return bytes.Join(result, []byte("\n"))
}

// SourceTimelineQueryParam opts a real transcode manifest into source-time
// alignment for compatibility clients that apply their resume position to the
// HLS timeline themselves.
const SourceTimelineQueryParam = "source_timeline"

// BuildSourceAlignedPlaybackManifest builds the normal playback manifest and,
// when it is a seeked real playlist, prepends a virtual unavailable span so the
// first produced segment retains its source-time position. Synthetic manifests
// already cover the full source timeline and need no adjustment.
func (s *TranscodeSession) BuildSourceAlignedPlaybackManifest(segPrefix, rawQuery string) ([]byte, error) {
	manifest, err := s.buildSourceAlignedPlaybackManifest(segPrefix, rawQuery)
	s.noteManifestServed(err)
	return manifest, err
}

// buildSourceAlignedPlaybackManifest is BuildSourceAlignedPlaybackManifest
// without the client-request bookkeeping, so the manifest path records exactly
// one served playlist per answered request.
func (s *TranscodeSession) buildSourceAlignedPlaybackManifest(segPrefix, rawQuery string) ([]byte, error) {
	rawQuery, err := manifestQuery(rawQuery)
	if err != nil {
		return nil, err
	}
	rawQuery = s.generationScopedQuery(rawQuery)
	if s.copyPlan != nil {
		// A planned playlist starts at the source's beginning and doesn't
		// depend on FFmpeg's progress or restarts.
		return s.buildPlaybackManifest(segPrefix, rawQuery)
	}
	s.mu.Lock()
	opts, generation, restarting := s.opts, s.segmentGeneration, s.restarting != nil
	s.mu.Unlock()
	if restarting {
		return nil, ErrManifestNotReady
	}
	manifest, err := s.buildPlaybackManifest(segPrefix, rawQuery)
	if err != nil {
		return nil, err
	}
	usesRealManifest := strings.EqualFold(opts.TargetCodecVideo, "copy") ||
		!CanGenerateSyntheticManifest(opts.TotalDuration, opts.SegmentDuration)
	if !usesRealManifest {
		return manifest, nil
	}

	gapURI := segPrefix + "source_timeline_gap" + hlsSegmentExtension(opts)
	if rawQuery != "" {
		// Gap URIs take the query form the real segment URIs got.
		gapQuery := rawQuery
		if definition, reference := manifestVariable(manifestQueryVariable, rawQuery, minRewrittenURISavings); definition != "" && bytes.Contains(manifest, []byte(definition)) {
			gapQuery = reference
		}
		gapURI += "?" + gapQuery
	}
	if strings.EqualFold(opts.TargetCodecVideo, "copy") && opts.CopySeekAnchorResolved {
		timeline, err := parseManifestTimeline(manifest)
		if err != nil {
			return nil, err
		}
		origin, err := s.copyManifestOrigin(opts, generation, timeline)
		if err != nil {
			return nil, err
		}
		opts.StreamOriginSeconds = origin
		opts.StartSegmentNumber = timeline.entries[0].number
		opts.SeekSeconds = max(opts.SeekSeconds, origin)
	}
	if opts.SeekSeconds <= 0 {
		return manifest, nil
	}
	return AlignRealManifestToSourceTimeline(manifest, opts, gapURI)
}

// AlignRealManifestToSourceTimeline prepends bounded EXT-X-GAP segments to a
// seeked FFmpeg playlist. The gaps contribute the omitted source time while
// allowing clients to seek to the original source position.
func AlignRealManifestToSourceTimeline(manifest []byte, opts TranscodeOpts, gapURI string) ([]byte, error) {
	if opts.SeekSeconds <= 0 {
		return manifest, nil
	}
	timeline, err := parseManifestTimeline(manifest)
	if err != nil {
		return nil, err
	}
	if len(timeline.entries) == 0 {
		return nil, fmt.Errorf("manifest contains no media segments")
	}

	segmentDuration := opts.SegmentDuration
	if segmentDuration <= 0 {
		segmentDuration = defaultSegmentDuration
	}
	firstSegment := timeline.entries[0].number
	advancedSegments := max(0, firstSegment-opts.StartSegmentNumber)
	gapDuration := opts.SeekSeconds + float64(advancedSegments*segmentDuration)
	if strings.EqualFold(opts.TargetCodecVideo, "copy") && opts.CopySeekAnchorResolved {
		// A copied stream begins at the demuxer-selected keyframe, which can
		// precede the requested seek. Its fragment timestamps must use that origin.
		if advancedSegments > 0 {
			return nil, fmt.Errorf("cannot align an evicted copy manifest without its original fragment timeline")
		}
		gapDuration = opts.StreamOriginSeconds
	}
	// BuildPlaybackManifest's generation-relative start (0.001) must not point
	// into the unavailable prefix after source-time alignment. Keep this hint
	// at the requested source position on every reload/remount, including a
	// seek whose copied keyframe is at source zero and needs no gap.
	lines := bytes.Split(manifest, []byte("\n"))
	startTag := []byte(fmt.Sprintf("#EXT-X-START:TIME-OFFSET=%.6f,PRECISE=YES", opts.SeekSeconds))
	foundStart := false
	for i, line := range lines {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("#EXT-X-START:")) {
			lines[i] = startTag
			foundStart = true
		}
	}
	if !foundStart {
		lines = append(lines[:1], append([][]byte{startTag}, lines[1:]...)...)
	}
	if gapDuration <= 0 {
		return bytes.Join(lines, []byte("\n")), nil
	}
	if gapURI == "" {
		gapURI = "source_timeline_gap" + hlsSegmentExtension(opts)
	}

	targetDuration := segmentDuration
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("#EXT-X-TARGETDURATION:")) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(string(trimmed), "#EXT-X-TARGETDURATION:"))
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed <= 0 {
			return nil, fmt.Errorf("parse manifest target duration %q", value)
		}
		targetDuration = parsed
		break
	}

	// Keep the real segment's media sequence aligned with its FFmpeg segment
	// number when possible. Very large seeks remain bounded by the same limit as
	// generated VOD manifests; their gap durations grow instead of their count.
	gapCount := max(1, firstSegment)
	if gapCount > maxSyntheticManifestSegments {
		gapCount = maxSyntheticManifestSegments
	}
	gapSegmentDuration := gapDuration / float64(gapCount)
	requiredTargetDuration := int(math.Ceil(gapSegmentDuration))
	if requiredTargetDuration > targetDuration {
		targetDuration = requiredTargetDuration
	}
	mediaSequence := max(0, firstSegment-gapCount)

	result := make([][]byte, 0, len(lines)+gapCount*3)
	insertedGap := false
	foundSequence := false
	foundVersion := false
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		switch {
		case bytes.HasPrefix(trimmed, []byte("#EXT-X-VERSION:")):
			foundVersion = true
			value := strings.TrimSpace(strings.TrimPrefix(string(trimmed), "#EXT-X-VERSION:"))
			version, parseErr := strconv.Atoi(value)
			if parseErr != nil {
				return nil, fmt.Errorf("parse manifest version %q: %w", value, parseErr)
			}
			if version < 8 {
				line = []byte("#EXT-X-VERSION:8")
			}
		case bytes.HasPrefix(trimmed, []byte("#EXT-X-TARGETDURATION:")):
			line = []byte(fmt.Sprintf("#EXT-X-TARGETDURATION:%d", targetDuration))
		case bytes.HasPrefix(trimmed, []byte("#EXT-X-MEDIA-SEQUENCE:")):
			foundSequence = true
			line = []byte(fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d", mediaSequence))
		case bytes.HasPrefix(trimmed, []byte("#EXTINF:")) && !insertedGap:
			if !foundVersion {
				result = append(result, []byte("#EXT-X-VERSION:8"))
				foundVersion = true
			}
			if !foundSequence {
				result = append(result, []byte(fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d", mediaSequence)))
				foundSequence = true
			}
			for range gapCount {
				result = append(result,
					[]byte("#EXT-X-GAP"),
					[]byte(fmt.Sprintf("#EXTINF:%.6f,", gapSegmentDuration)),
					[]byte(gapURI),
				)
			}
			insertedGap = true
		}
		result = append(result, line)
	}
	if !insertedGap {
		return nil, fmt.Errorf("manifest contains no segment duration")
	}
	return bytes.Join(result, []byte("\n")), nil
}

// CanGenerateSyntheticManifest reports whether a complete VOD playlist fits
// within the shared segment-count bound. Callers outside playback use the same
// decision so native and compatibility manifests cannot drift.
func CanGenerateSyntheticManifest(totalDuration float64, segmentDuration int) bool {
	if totalDuration <= 0 || math.IsNaN(totalDuration) || math.IsInf(totalDuration, 0) {
		return false
	}
	if segmentDuration <= 0 {
		segmentDuration = defaultSegmentDuration
	}
	return totalDuration <= float64(segmentDuration)*maxSyntheticManifestSegments
}

func firstNonEmptyManifestLine(manifest []byte) []byte {
	for line := range bytes.SplitSeq(manifest, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			return trimmed
		}
	}
	return nil
}

func validateManifestHeader(manifest []byte) error {
	if len(bytes.TrimSpace(manifest)) == 0 {
		return fmt.Errorf("manifest is empty")
	}
	if line := firstNonEmptyManifestLine(manifest); !bytes.Equal(line, []byte("#EXTM3U")) {
		return fmt.Errorf("manifest missing #EXTM3U header")
	}
	return nil
}

func parseTargetDuration(manifest []byte) (int, error) {
	for line := range bytes.SplitSeq(manifest, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("#EXT-X-TARGETDURATION:")) {
			value := strings.TrimSpace(strings.TrimPrefix(string(trimmed), "#EXT-X-TARGETDURATION:"))
			targetDuration, err := strconv.Atoi(value)
			if err != nil {
				return 0, fmt.Errorf("parse target duration %q: %w", value, err)
			}
			return targetDuration, nil
		}
	}
	return 0, fmt.Errorf("manifest missing #EXT-X-TARGETDURATION")
}

func validateCopyPlaybackManifest(manifest []byte) error {
	if err := validateManifestHeader(manifest); err != nil {
		return err
	}

	targetDuration, err := parseTargetDuration(manifest)
	if err != nil {
		return err
	}
	if targetDuration <= 0 {
		return fmt.Errorf("manifest target duration must be positive, got %d", targetDuration)
	}

	timeline, err := parseManifestTimeline(manifest)
	if err != nil {
		return err
	}
	if len(timeline.entries) == 0 {
		return fmt.Errorf("manifest contains no playable media segments")
	}
	for _, entry := range timeline.entries {
		if entry.duration <= 0 {
			return fmt.Errorf("segment %d has non-positive duration %.6f", entry.number, entry.duration)
		}
	}
	return nil
}

func extractMapURI(line []byte) string {
	const marker = `URI="`
	text := string(line)
	start := strings.Index(text, marker)
	if start < 0 {
		return ""
	}
	start += len(marker)
	end := strings.Index(text[start:], `"`)
	if end < 0 {
		return ""
	}
	return text[start : start+end]
}

func manifestURIToFilename(uri string) string {
	base, _, _ := strings.Cut(uri, "?")
	return filepath.Base(base)
}

func manifestStartupFiles(manifest []byte, maxSegments int) ([]string, int, float64, bool, bool) {
	files := make([]string, 0, maxSegments+1)
	segmentCount := 0
	duration := 0.0
	endList := false
	allValidDurations := true
	var pendingDuration float64
	var havePending bool

	// Scan the whole playlist: an EXTINF duration belongs to the URI line that
	// follows it, so durations accumulate per completed segment. Stop only
	// when BOTH the segment count and the duration runway hold — stopping at
	// the count alone would demand 4s inside a 1-segment prefix that can
	// never satisfy it (e.g. copy mode).
	complete := func() bool {
		return segmentCount >= maxSegments && duration >= minManifestDurationSeconds
	}

	for line := range bytes.SplitSeq(manifest, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if bytes.HasPrefix(trimmed, []byte("#EXT-X-ENDLIST")) {
			endList = true
			continue
		}
		if bytes.HasPrefix(trimmed, []byte("#EXT-X-MAP:")) {
			if uri := extractMapURI(trimmed); uri != "" {
				files = append(files, manifestURIToFilename(uri))
			}
			continue
		}
		if bytes.HasPrefix(trimmed, []byte("#EXTINF:")) {
			pendingDuration, havePending = 0, false
			raw := strings.TrimSpace(strings.TrimPrefix(string(trimmed), "#EXTINF:"))
			val, _, _ := strings.Cut(raw, ",")
			val = strings.TrimSpace(val)
			if seconds, err := strconv.ParseFloat(val, 64); err == nil && seconds > 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
				pendingDuration, havePending = seconds, true
			}
			continue
		}
		if trimmed[0] == '#' {
			continue
		}

		if !havePending || pendingDuration <= 0 {
			allValidDurations = false
		} else {
			duration += pendingDuration
			pendingDuration = 0
			havePending = false
		}
		files = append(files, manifestURIToFilename(string(trimmed)))
		segmentCount++
		if complete() {
			break
		}
	}

	return files, segmentCount, duration, endList, allValidDurations
}

// minManifestDurationSeconds is the minimum contiguous completed-segment duration
// required to declare transcode startup ready. Two 2s segments provide the
// ~4s runway the consumer needs to fetch the init map and first media without
// immediately catching the encoder head.
const minManifestDurationSeconds = 4.0

// startupCopyManifestIsBroken reports whether a manifest is a broken
// copy-mode playlist (zero TARGETDURATION or non-positive EXTINF durations).
// Callers gate on evidence completeness first (startupFilesReady true) so an
// incomplete live manifest waits for more media instead of asserting broken
// output.
func startupCopyManifestIsBroken(manifest []byte) bool {
	if err := validateManifestHeader(manifest); err != nil {
		return false
	}
	_, segmentCount, _, _, _ := manifestStartupFiles(manifest, 2)
	if segmentCount < 2 {
		return false
	}
	targetDuration, err := parseTargetDuration(manifest)
	if err != nil || targetDuration <= 0 {
		return true
	}
	timeline, err := parseManifestTimeline(manifest)
	if err != nil || len(timeline.entries) == 0 {
		return true
	}
	for _, entry := range timeline.entries {
		if entry.duration <= 0 {
			return true
		}
	}
	return false
}

func startupFilesReady(manifest []byte, outputDir string, requiredSegments int) bool {
	files, segmentCount, duration, endList, allValidDurations := manifestStartupFiles(manifest, requiredSegments)
	if !allValidDurations || segmentCount == 0 {
		return false
	}
	// A completed playlist with nonempty referenced files is valid content
	// even below the live runway: short clips and near-end seeks must start.
	if endList {
		if duration <= 0 {
			return false
		}
		for _, name := range files {
			info, err := os.Stat(filepath.Join(outputDir, name))
			if err != nil || info.Size() <= 0 {
				return false
			}
		}
		return true
	}
	if segmentCount < requiredSegments {
		return false
	}
	if duration < minManifestDurationSeconds {
		return false
	}

	for _, name := range files {
		info, err := os.Stat(filepath.Join(outputDir, name))
		if err != nil || info.Size() <= 0 {
			return false
		}
	}

	return true
}

type manifestSegmentEntry struct {
	number   int
	duration float64
}

type manifestTimeline struct {
	mediaSequence int
	entries       []manifestSegmentEntry
}

func parseManifestTimeline(manifest []byte) (manifestTimeline, error) {
	if err := validateManifestHeader(manifest); err != nil {
		return manifestTimeline{}, err
	}

	timeline := manifestTimeline{}
	currentNumber := 0
	var pendingDuration float64
	var haveDuration bool

	for line := range bytes.SplitSeq(manifest, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}

		if bytes.HasPrefix(trimmed, []byte("#EXT-X-MEDIA-SEQUENCE:")) {
			value := strings.TrimSpace(strings.TrimPrefix(string(trimmed), "#EXT-X-MEDIA-SEQUENCE:"))
			sequence, err := strconv.Atoi(value)
			if err != nil {
				return manifestTimeline{}, fmt.Errorf("parse media sequence %q: %w", value, err)
			}
			timeline.mediaSequence = sequence
			currentNumber = sequence
			continue
		}

		if bytes.HasPrefix(trimmed, []byte("#EXTINF:")) {
			value := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(string(trimmed), "#EXTINF:"), ","))
			duration, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return manifestTimeline{}, fmt.Errorf("parse segment duration %q: %w", value, err)
			}
			pendingDuration = duration
			haveDuration = true
			continue
		}

		if trimmed[0] == '#' {
			continue
		}

		if !haveDuration {
			continue
		}

		segmentNumber := currentNumber
		if parsed, err := ParseSegmentNumber(filepath.Base(string(trimmed))); err == nil {
			segmentNumber = parsed
		}

		timeline.entries = append(timeline.entries, manifestSegmentEntry{
			number:   segmentNumber,
			duration: pendingDuration,
		})
		currentNumber = segmentNumber + 1
		haveDuration = false
	}

	return timeline, nil
}

func hlsSegmentExtension(opts TranscodeOpts) string {
	if videoUsesFMP4(opts) {
		return ".m4s"
	}
	return ".ts"
}

func segmentFilename(segNum int, opts TranscodeOpts) string {
	return fmt.Sprintf("seg_%05d%s", segNum, hlsSegmentExtension(opts))
}

func segmentWaitTimeout(segmentDuration int) time.Duration {
	if segmentDuration <= 0 {
		segmentDuration = defaultSegmentDuration
	}
	timeout := time.Duration(segmentDuration)*time.Second + segmentWaitGrace
	if timeout < minSegmentWait {
		timeout = minSegmentWait
	}
	if timeout > maxSegmentWait {
		timeout = maxSegmentWait
	}
	return timeout
}

func staleProducedWindow(segmentDuration int) time.Duration {
	if segmentDuration <= 0 {
		segmentDuration = defaultSegmentDuration
	}
	window := 2*time.Duration(segmentDuration)*time.Second + segmentWaitGrace
	if window < minStaleProducedWindow {
		window = minStaleProducedWindow
	}
	return window
}

// SegmentProgress reports the highest manifest-referenced segment that exists
// on disk with data. This is the produced media source of truth.
func (s *TranscodeSession) SegmentProgress(time.Time) SegmentProgress {
	s.mu.Lock()
	opts := s.opts
	progress := SegmentProgress{
		ProducedHead:                  opts.StartSegmentNumber - 1,
		ProducedHeadCurrentGeneration: opts.StartSegmentNumber - 1,
		Running:                       s.running,
		Restarting:                    s.restarting != nil,
		StartSegmentNumber:            opts.StartSegmentNumber,
		SegmentDuration:               opts.SegmentDuration,
		LastRequestedSegment:          s.lastRequestedSegment,
		GenerationStartedAt:           s.generationStartedAt,
		VideoFrames:                   s.videoFrames,
	}
	s.mu.Unlock()

	if progress.SegmentDuration <= 0 {
		progress.SegmentDuration = defaultSegmentDuration
	}

	manifestPath := filepath.Join(s.outputDir, "stream.m3u8")
	// The earliest segment this generation wrote is its first output; the gap
	// from the spawn stamp to it is how long the encoder needed to open the
	// source at this generation's start position.
	var firstGenerationSegmentAt time.Time
	manifestInfo, statErr := os.Stat(manifestPath)
	var timeline manifestTimeline
	var parseOK bool
	if statErr == nil {
		if manifest, err := os.ReadFile(manifestPath); err == nil {
			if parsed, err := parseManifestTimeline(manifest); err == nil {
				timeline = parsed
				parseOK = true
				progress.HasManifest = true
				progress.ManifestModTime = manifestInfo.ModTime()
			}
		}
	}

	if parseOK {
		for _, entry := range timeline.entries {
			segmentPath := filepath.Join(s.outputDir, segmentFilename(entry.number, opts))
			info, err := os.Stat(segmentPath)
			if err != nil || info.Size() <= 0 {
				continue
			}
			progress.ProducedCount++
			if entry.number > progress.ProducedHead {
				progress.ProducedHead = entry.number
			}
			if info.ModTime().After(progress.LastProducedAt) {
				progress.LastProducedAt = info.ModTime()
			}
			if progress.GenerationStartedAt.IsZero() || !info.ModTime().Before(progress.GenerationStartedAt) {
				if entry.number > progress.ProducedHeadCurrentGeneration {
					progress.ProducedHeadCurrentGeneration = entry.number
				}
				if firstGenerationSegmentAt.IsZero() || info.ModTime().Before(firstGenerationSegmentAt) {
					firstGenerationSegmentAt = info.ModTime()
				}
			}
		}
	} else if s.outputDir != "" {
		// Fallback: If the manifest is temporarily unreadable or being rewritten
		// by FFmpeg, scan the output directory for completed segment files so
		// ProducedHead does not falsely reset to -1.
		if entries, err := os.ReadDir(s.outputDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				segNum, err := ParseSegmentNumber(entry.Name())
				if err != nil || segNum < opts.StartSegmentNumber {
					continue
				}
				info, err := entry.Info()
				if err != nil || info.Size() <= 0 {
					continue
				}
				progress.ProducedCount++
				if segNum > progress.ProducedHead {
					progress.ProducedHead = segNum
				}
				if info.ModTime().After(progress.LastProducedAt) {
					progress.LastProducedAt = info.ModTime()
				}
				if progress.GenerationStartedAt.IsZero() || !info.ModTime().Before(progress.GenerationStartedAt) {
					if segNum > progress.ProducedHeadCurrentGeneration {
						progress.ProducedHeadCurrentGeneration = segNum
					}
					if firstGenerationSegmentAt.IsZero() || info.ModTime().Before(firstGenerationSegmentAt) {
						firstGenerationSegmentAt = info.ModTime()
					}
				}
			}
			if statErr == nil && progress.ProducedCount > 0 {
				progress.HasManifest = true
				progress.ManifestModTime = manifestInfo.ModTime()
			}
		}
	}
	if latency := startupLatencyOf(progress.GenerationStartedAt, firstGenerationSegmentAt); latency > 0 {
		progress.StartupLatency = latency
		s.mu.Lock()
		// Latch the first measurement for the generation this scan measured.
		// A generation that started while the scan was running owns a different
		// spawn stamp, so its (unmeasured) slot is left alone; a later scan of
		// this same generation cannot replace the first value, which is the one
		// a pruning floor cannot reach.
		if s.generationStartedAt.Equal(progress.GenerationStartedAt) && s.observedStartupLatency == 0 {
			s.observedStartupLatency = latency
		}
		s.mu.Unlock()
	}
	scanActiveTempProgress(s.outputDir, &progress)
	return progress
}

// beginGeneration records that a new owning ffmpeg generation spawned at
// startedAt. Callers hold mu, except StartTranscode before it publishes the
// session. Startup latency is measured per generation, so a replacement starts
// unmeasured instead of inheriting what an earlier generation cost. The frame
// counters belong to the same generation: a replacement that has not decoded a
// frame yet must not look like one that has, and progressGeneration fences the
// reader of the process that was just replaced.
func (s *TranscodeSession) beginGeneration(startedAt time.Time) {
	s.generationStartedAt = startedAt
	s.observedStartupLatency = 0
	s.videoFrames = 0
	s.videoOutTimeUS = 0
	s.lastVideoFrameAt = time.Time{}
	s.progressGeneration++
}

// startupLatencyOf reports how long a generation needed from its spawn stamp to
// its first produced segment, or zero when either stamp is missing.
func startupLatencyOf(generationStartedAt, firstSegmentAt time.Time) time.Duration {
	if generationStartedAt.IsZero() || firstSegmentAt.IsZero() {
		return 0
	}
	if latency := firstSegmentAt.Sub(generationStartedAt); latency > 0 {
		return latency
	}
	return 0
}

// StartupLatency reports the owning generation's measured startup: the time
// between its spawn and its first produced segment. The measurement belongs to
// one generation and is latched on its first scan, so it is zero until the
// current generation has produced output and never describes an earlier
// generation. Zero keeps the restart hold.
func (s *TranscodeSession) StartupLatency() time.Duration {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observedStartupLatency
}

func scanActiveTempProgress(outputDir string, progress *SegmentProgress) {
	if outputDir == "" {
		return
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return
	}
	var newest os.FileInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "seg_") || !strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() <= 0 {
			continue
		}
		if newest == nil || info.ModTime().After(newest.ModTime()) {
			newest = info
		}
	}
	if newest != nil {
		progress.ActiveTempAt = newest.ModTime()
		progress.ActiveTempSize = newest.Size()
	}
}

// SegmentRecoveryDecision determines whether a missing segment should briefly
// wait for ffmpeg or immediately use the seek-restart path.
func (s *TranscodeSession) SegmentRecoveryDecision(segNum int, now time.Time) SegmentRecoveryDecision {
	progress := s.SegmentProgress(now)
	decision := SegmentRecoveryDecision{
		WaitTimeout:      segmentWaitTimeout(progress.SegmentDuration),
		RestartOnTimeout: true,
		Progress:         progress,
	}
	if s.copyPlan != nil {
		return s.plannedSegmentRecoveryDecision(segNum, decision)
	}

	switch {
	// Restarting must be checked before Running: the restart window runs
	// with running=false, and a concurrent segment request must wait out
	// the in-flight restart rather than trigger another one. Dueling
	// restarts keep preempting the segment the player is blocked on,
	// which surfaces as a seek/intro-skip freeze (issue #243).
	case progress.Restarting:
		decision.Wait = true
		decision.WaitTimeout = activeSegmentWait
		decision.RestartOnTimeout = false
		decision.Reason = segmentReasonRestarting
	case !progress.Running:
		decision.Reason = "transcode_not_running"
	case segNum < progress.StartSegmentNumber:
		if anchor := s.playlistStartSegment(); anchor > 0 && segNum < anchor {
			// A seek-anchored playlist begins at this segment. A request below
			// it cannot name a segment this transport publishes, so it is a
			// stale playlist (or one the server never served) rather than a
			// seek this generation can satisfy. Restarting for it would kill a
			// healthy encoder and still leave the segment unlisted; let the
			// request fail so the client reloads the playlist it has.
			decision.Wait = false
			decision.RestartOnTimeout = false
			decision.Reason = "before_playlist_start"
			break
		}
		decision.Reason = "before_start_segment"
	case segNum <= progress.ProducedHead:
		decision.Reason = "segment_missing_behind_produced_head"
	case !progress.HasManifest:
		if segNum <= progress.StartSegmentNumber+1 {
			decision.Wait = true
			decision.WaitTimeout = activeSegmentWait
			decision.RestartOnTimeout = false
			decision.Reason = "startup_manifest_not_ready"
		} else {
			decision.Reason = "startup_request_beyond_window"
		}
	case segNum > progress.ProducedHead+maxSequentialMissingSegments:
		// If the player is fetching sequentially from the last requested segment
		// (e.g. normal continuous playback) and the encoder is actively running,
		// this is continuous streaming, not an arbitrary seek jump. Wait for FFmpeg
		// instead of killing the process.
		if progress.Running && segNum == progress.LastRequestedSegment+1 &&
			(progress.LastProducedAt.IsZero() || now.Sub(progress.LastProducedAt) <= staleProducedWindow(progress.SegmentDuration)) {
			decision.Wait = true
			decision.WaitTimeout = activeSegmentWait
			decision.RestartOnTimeout = false
			decision.Reason = "near_produced_head"
		} else {
			decision.Reason = SegmentRecoveryFarSeekReason
		}
	case progress.ProducedHead >= progress.StartSegmentNumber && now.Sub(progress.LastProducedAt) > staleProducedWindow(progress.SegmentDuration):
		decision.Reason = "produced_output_stale"
	default:
		decision.Wait = true
		decision.WaitTimeout = activeSegmentWait
		decision.RestartOnTimeout = false
		decision.Reason = "near_produced_head"
	}

	return decision
}

// GenerateFullManifest builds a complete VOD-style HLS manifest that lists
// every segment for the full media duration, matching Jellyfin's approach.
// The player can seek to any position immediately; the backend produces
// segments on demand when they are requested via HandleGetTranscodeSegment.
//
// segPrefix is prepended to each segment filename (e.g. "segment/") and
// rawQuery is appended as a query string (e.g. auth tokens).
func (s *TranscodeSession) GenerateFullManifest(segPrefix, rawQuery string) []byte {
	segExt := hlsSegmentExtension(s.Opts())
	return s.generateSyntheticPlaylist(segPrefix, rawQuery, "seg_", segExt, segExt == ".m4s")
}

// playlistStartSegment is the first segment this session's complete VOD
// playlist lists, or zero for the historical whole-timeline playlist.
func (s *TranscodeSession) playlistStartSegment() int {
	opts := s.Opts()
	return syntheticPlaylistStartSegment(opts, syntheticSegmentCount(opts.TotalDuration, opts.SegmentDuration))
}

// syntheticSegmentCount is the number of fixed-duration segments the
// synthetic playlist lists for the media.
func syntheticSegmentCount(totalDuration float64, segmentDuration int) int {
	if segmentDuration <= 0 {
		segmentDuration = defaultSegmentDuration
	}
	if totalDuration <= 0 {
		totalDuration = float64(segmentDuration) // fallback: single segment
	}
	return max(1, int(math.Ceil(totalDuration/float64(segmentDuration))))
}

// generateSyntheticPlaylist lists the fixed-duration segments of the media
// under namePrefix%05d+ext. The video playlist and the subtitle rendition
// playlist share it, so both carry exactly the same segment timeline.
//
// A session whose transport begins at a seek anchor lists from that anchor
// (see syntheticPlaylistStartSegment) and publishes it as the media sequence,
// so the playlist describes only what the generation produces.
func (s *TranscodeSession) generateSyntheticPlaylist(segPrefix, rawQuery, namePrefix, segExt string, initMap bool) []byte {
	opts := s.Opts()
	totalDur := opts.TotalDuration
	segDur := opts.SegmentDuration
	if segDur <= 0 {
		segDur = defaultSegmentDuration
	}
	if totalDur <= 0 {
		totalDur = float64(segDur) // fallback: single segment
	}
	segCount := syntheticSegmentCount(totalDur, segDur)
	start := syntheticPlaylistStartSegment(opts, segCount)

	queryDefinition, suffix, queryVersion := syntheticManifestQuery(segCount-start, rawQuery)

	hlsVersion := 3
	if initMap {
		hlsVersion = 7
	}
	if queryVersion > hlsVersion {
		hlsVersion = queryVersion
	}

	var buf bytes.Buffer
	buf.WriteString("#EXTM3U\n")
	fmt.Fprintf(&buf, "#EXT-X-VERSION:%d\n", hlsVersion)
	buf.WriteString(queryDefinition)
	// Target duration is the maximum segment duration rounded to the nearest
	// integer. A frame-accurate GOP can slightly exceed the nominal segment
	// length (23.976 fps * 2 s rounds to 48 frames = 2.002 s), so advertise
	// SegDuration+1. EXTINF keeps the nominal duration and segment indexing is
	// unchanged.
	fmt.Fprintf(&buf, "#EXT-X-TARGETDURATION:%d\n", segDur+1)
	fmt.Fprintf(&buf, "#EXT-X-MEDIA-SEQUENCE:%d\n", start)
	buf.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")

	if initMap {
		fmt.Fprintf(&buf, "#EXT-X-MAP:URI=\"%sinit.mp4%s\"\n", segPrefix, suffix)
	}

	for i := start; i < segCount; i++ {
		dur := float64(segDur)
		if i == segCount-1 {
			// Last segment covers the remainder.
			dur = totalDur - float64(i)*float64(segDur)
			if dur <= 0 {
				dur = float64(segDur)
			}
		}
		fmt.Fprintf(&buf, "#EXTINF:%.6f,\n", dur)
		fmt.Fprintf(&buf, "%s%s%05d%s%s\n", segPrefix, namePrefix, i, segExt, suffix)
	}

	buf.WriteString("#EXT-X-ENDLIST\n")
	return buf.Bytes()
}

// syntheticPlaylistStartSegment is the first segment of a complete VOD
// playlist. An encoded generation that begins at a seek anchor produces
// segment files from that anchor; listing the earlier ones advertises media
// this generation cannot produce, and a player that prefetches the playlist's
// first entry would request one of them, making the segment layer restart a
// healthy encoder for bytes the player never plays. v3 plans anchored on a
// seek publish the same position as their timeline origin (see
// configureHLSTimelineV3), so the playlist's own clock and the client's agree.
//
// Every other caller keeps the historical whole-timeline playlist from segment
// zero: v1, Jellyfin compatibility, and seek restarts of a session that began
// at the source start all publish source-zero playlists.
func syntheticPlaylistStartSegment(opts TranscodeOpts, segCount int) int {
	if opts.StreamOriginSeconds <= 0 || strings.EqualFold(opts.TargetCodecVideo, "copy") {
		return 0
	}
	segDur := opts.SegmentDuration
	if segDur <= 0 {
		segDur = defaultSegmentDuration
	}
	start := int(math.Round(opts.StreamOriginSeconds / float64(segDur)))
	if start <= 0 {
		return 0
	}
	if segCount <= 0 {
		return 0
	}
	// A duration that rounds short of the anchor still lists its last segment
	// rather than an empty playlist.
	return min(start, segCount-1)
}

// GetSegment returns the file path of a named segment if it exists.
func (s *TranscodeSession) GetSegment(name string) (string, error) {
	// Sanitize the name to prevent directory traversal.
	clean := filepath.Base(name)
	segPath := filepath.Join(s.outputDir, clean)

	info, err := os.Stat(segPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrSegmentNotFound
		}
		return "", fmt.Errorf("stat segment: %w", err)
	}
	if info.Size() <= 0 {
		// ffmpeg can create init.mp4 before it has written any bytes. Treat
		// zero-byte files as not-ready so callers fall back to WaitForSegment.
		return "", ErrSegmentNotFound
	}
	return segPath, nil
}

// SegmentLease binds an opened segment descriptor to the FFmpeg generation it
// came from. Keeping the descriptor open makes it a filesystem-backed lease: a
// concurrent prune may unlink the directory entry, but the response can still
// read the complete file on POSIX filesystems.
type SegmentLease struct {
	File            *os.File
	Info            os.FileInfo
	Generation      uint64
	GenerationToken string
}

// Close releases the opened segment descriptor.
func (l *SegmentLease) Close() error {
	return l.File.Close()
}

// OpenSegment opens a completed segment for serving and captures its FFmpeg
// generation atomically with the open.
//
// name must be a single file name inside the output directory. The open goes
// through os.Root, so a symlink in the directory cannot lead outside it: the
// subtitle filter alias points at the source media and must never be served as
// a segment.
func (s *TranscodeSession) OpenSegment(name string) (*SegmentLease, error) {
	lease, dv81, sessionID, err := s.openSegment(name)
	if err != nil || !dv81 {
		return lease, err
	}
	if err := s.convertDV81(name, lease, sessionID); err != nil {
		_ = lease.Close()
		return nil, err
	}
	return lease, nil
}

// openSegment is OpenSegment under s.mu. dv81 reports a
// DV7ToDV81BitstreamFilter session, whose file OpenSegment converts next;
// sessionID is read here because a restart replaces s.opts under s.mu.
func (s *TranscodeSession) openSegment(name string) (lease *SegmentLease, dv81 bool, sessionID string, err error) {
	if !servableSegmentName(name) {
		return nil, false, "", ErrSegmentNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Every serve attempt reaches OpenSegment (directly or through a wait loop),
	// and only a client request can start one. Record it before any early
	// return so a request for a generation that is already gone still counts as
	// the client trying.
	s.segmentRequested = true
	if s.restarting != nil {
		return nil, false, "", ErrSegmentNotFound
	}

	root, err := os.OpenRoot(s.outputDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, "", ErrSegmentNotFound
		}
		return nil, false, "", fmt.Errorf("open segment directory: %w", err)
	}
	// Files opened through a Root hold their own descriptor and stay valid
	// after the Root closes.
	defer func() { _ = root.Close() }()
	// A DV7ToDV81BitstreamFilter session rewrites the leased file itself, so
	// a restart that replaces the name meanwhile cannot split what is
	// converted from what is served.
	flag := os.O_RDONLY
	if s.opts.VideoBitstreamFilter == DV7ToDV81BitstreamFilter {
		flag = os.O_RDWR
	}
	segment, err := root.OpenFile(name, flag, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, "", ErrSegmentNotFound
		}
		return nil, false, "", fmt.Errorf("open segment: %w", err)
	}
	info, err := segment.Stat()
	if err != nil {
		_ = segment.Close()
		return nil, false, "", fmt.Errorf("stat segment: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		_ = segment.Close()
		return nil, false, "", ErrSegmentNotFound
	}
	if s.segmentIncarnation == "" {
		s.segmentIncarnation = uuid.NewString()
	}
	return &SegmentLease{
		File:            segment,
		Info:            info,
		Generation:      s.segmentGeneration,
		GenerationToken: s.segmentGenerationTokenLocked(),
	}, s.opts.VideoBitstreamFilter == DV7ToDV81BitstreamFilter, s.opts.SessionID, nil
}

// convertDV81 rewrites a served file of a DV7ToDV81BitstreamFilter session
// in place before it is handed out: init.mp4's DOVI record, or a media
// segment's RPUs. dv81Mu serializes it, so no request sees a file mid-rewrite,
// without holding s.mu over the file I/O. It fails closed: a segment it cannot
// convert is not served under the plan's Dolby Vision 8.1 claim, so the client
// replans onto the next recipe. An init.mp4 that does not parse yet reads as
// not ready, like an empty one; one that parses but cannot carry 8.1 fails.
func (s *TranscodeSession) convertDV81(name string, lease *SegmentLease, sessionID string) error {
	s.dv81Mu.Lock()
	defer s.dv81Mu.Unlock()
	if name != hlsInitSegmentName && (!strings.HasSuffix(name, ".m4s") || s.dv81Converted[name] == lease.Generation+1) {
		return nil
	}
	// init.mp4 is converted only through its own lease. A media segment needs
	// just its track id and NAL length size: from the init already served, or
	// else read from the current init.mp4 without writing to it, which FFmpeg
	// may be rewriting after a restart.
	track := s.dv81Track
	if name == hlsInitSegmentName || track.NALLength == 0 {
		initTrack, err := s.dv81InitTrack(name, lease)
		switch {
		case errors.Is(err, dovirpu.ErrUnsupportedInit):
			slog.Error("playback: dolby vision 8.1 init segment cannot be converted", "session", sessionID, "error", err)
			return fmt.Errorf("convert init segment to dolby vision 8.1: %w", err)
		case err != nil:
			slog.Warn("playback: dolby vision 8.1 init segment not convertible yet", "session", sessionID, "error", err)
			return ErrSegmentNotFound
		}
		track, s.dv81Track = initTrack, initTrack
	}
	if name == hlsInitSegmentName {
		return nil
	}
	// A segment that cannot be converted, including one where no RPU
	// converted (dovirpu.ErrNoRPUConverted), is left unchanged and fails
	// every request.
	stats, err := dovirpu.ConvertSegmentFile(lease.File, track)
	if err != nil {
		slog.Error("playback: dolby vision 8.1 segment conversion failed", "session", sessionID, "segment", name, "error", err)
		return fmt.Errorf("convert segment to dolby vision 8.1: %w", err)
	}
	if stats.Dropped > 0 {
		slog.Warn("playback: dolby vision 8.1 RPUs left as HDR10", "session", sessionID, "segment", name,
			"dropped", stats.Dropped, "converted", stats.Converted)
	}
	if s.dv81Converted == nil {
		s.dv81Converted = make(map[string]uint64)
	}
	// Stored as generation+1 so the zero value means "never converted".
	s.dv81Converted[name] = lease.Generation + 1
	return nil
}

// dv81InitTrack converts the leased init.mp4 in place and returns its video
// track. For a media segment it reads the current init.mp4 read-only.
func (s *TranscodeSession) dv81InitTrack(name string, lease *SegmentLease) (dovirpu.Track, error) {
	if name == hlsInitSegmentName {
		return dovirpu.ConvertInitFile(lease.File)
	}
	root, err := os.OpenRoot(s.outputDir)
	if err != nil {
		return dovirpu.Track{}, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(hlsInitSegmentName)
	if err != nil {
		return dovirpu.Track{}, err
	}
	defer func() { _ = f.Close() }()
	return dovirpu.InitTrack(f)
}

// servableSegmentName reports whether name is a plain file name the segment
// route may open: local, a single path element, and not the subtitle filter
// alias.
func servableSegmentName(name string) bool {
	return filepath.IsLocal(name) && filepath.Base(name) == name && name != "." &&
		name != subtitleFilterAliasName
}

// OpenSegmentForGeneration opens a segment only when its generation token still
// names the generation that produced the opened descriptor. Empty tokens retain
// compatibility with URLs minted before generation fencing was introduced.
func (s *TranscodeSession) OpenSegmentForGeneration(name, generationToken string) (*SegmentLease, error) {
	lease, err := s.OpenSegment(name)
	if err != nil {
		return nil, err
	}
	if generationToken != "" && lease.GenerationToken != generationToken {
		_ = lease.Close()
		return nil, ErrStaleSegmentGeneration
	}
	return lease, nil
}

// FenceSegmentLease applies the generation check after a recovery or wait path
// that opened a descriptor before the request's token was revalidated.
func FenceSegmentLease(lease *SegmentLease, generationToken string) (*SegmentLease, error) {
	if lease == nil || generationToken == "" {
		return lease, nil
	}
	if lease.GenerationToken != generationToken {
		_ = lease.Close()
		return nil, ErrStaleSegmentGeneration
	}
	return lease, nil
}

// Close terminates the ffmpeg process and removes the temporary output directory.
func (s *TranscodeSession) Close() error {
	return s.shutdown(true)
}

// CloseProcess terminates the ffmpeg process but leaves the output directory in
// place. It is used when another session owns the same output directory (e.g. a
// concurrent reconstruct race loser): tearing down this duplicate must not wipe
// the segments and init.mp4 the winning session is actively serving.
//
// It advances the numeric generation, so a token minted before the stop no
// longer matches. Callers that are keeping the directory servable as a retained
// switchover predecessor must use CloseProcessRetained instead.
func (s *TranscodeSession) CloseProcess() error {
	return s.shutdown(false)
}

// CloseProcessRetained stops the process but keeps the output directory and the
// generation identity intact. A retained switchover predecessor must keep
// serving the exact sgen token its in-flight playlist was minted with; advancing
// the generation here would make every one-behind segment request a 412, which
// is the bug this method exists to avoid. Only the retained-overlap path may use
// it: every other stop advances the generation so a stopped session's stale
// timeline is fenced out.
func (s *TranscodeSession) CloseProcessRetained() error {
	return s.shutdownProcess(false, false)
}

// shutdown kills the ffmpeg process and, when removeOutput is true, removes the
// temporary output directory. It advances the numeric generation.
func (s *TranscodeSession) shutdown(removeOutput bool) error {
	return s.shutdownProcess(removeOutput, true)
}

// shutdownProcess kills the ffmpeg process and, when removeOutput is true,
// removes the temporary output directory. bumpGeneration is false only for the
// retained switchover path, which must preserve the predecessor's published
// generation token (see CloseProcessRetained).
func (s *TranscodeSession) shutdownProcess(removeOutput, bumpGeneration bool) error {
	s.StopThrottler()
	// Cancel the context to kill the process (no mutex needed for cancel).
	if s.cancel != nil {
		s.cancel()
	}

	// Wait for the monitor goroutine to finish reaping the process.
	// This avoids a deadlock: the goroutine needs s.mu to mark running=false,
	// so we must not hold s.mu while waiting.
	// done is nil when no process was started (e.g. test-only sessions).
	if s.done != nil {
		<-s.done
	}

	if removeOutput {
		// A span record write still in flight (an origin probe can outlive
		// the request) must finish before the directory goes away.
		s.subtitleRecordMu.Lock()
		defer s.subtitleRecordMu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.running = false
	// Release the decoder-verdict evaluator so a closed session never keeps a
	// goroutine (or confirms a generation nobody is serving) alive.
	if s.decodeWatchCancel != nil {
		s.decodeWatchCancel()
		s.decodeWatchCancel = nil
	}
	s.inputCleanupOnce.Do(func() {
		if s.opts.InputCleanup != nil {
			s.opts.InputCleanup()
		}
	})
	if bumpGeneration {
		s.segmentGeneration++
		// A stopped/closed generation is not a restart successor: the old
		// playlist's timeline ended here, so retire any continuation lease
		// rather than letting a token from the last in-place restart cross it.
		s.restartStartSegment = 0
		s.restartPredecessorToken = ""
		s.restartRecipeStable = false
	}
	s.segmentPruneRunning = false

	// Clean up temporary directory.
	if removeOutput && s.outputDir != "" {
		s.outputRemoved = true
		if err := os.RemoveAll(s.outputDir); err != nil {
			return fmt.Errorf("remove output dir: %w", err)
		}
	}
	return nil
}

// Done returns a channel that closes when the current ffmpeg process exits.
func (s *TranscodeSession) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// IsRunning reports whether the ffmpeg process is still running.
func (s *TranscodeSession) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// WaitError returns the error from the last ffmpeg process exit, or nil if
// the process exited cleanly. A nil return means all output was written
// successfully and segments should remain servable.
func (s *TranscodeSession) WaitError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitErr
}

func IsHardwareTranscode(hwAccel string) bool {
	switch strings.ToLower(strings.TrimSpace(hwAccel)) {
	case "qsv", "vaapi", "nvenc", "cuda", "videotoolbox", "amf", "auto":
		return true
	default:
		return false
	}
}

// Opts returns the TranscodeOpts used to create this session (for testing).
func (s *TranscodeSession) Opts() TranscodeOpts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts
}

// SetAudioTrackIndex updates the audio track index in the session's opts.
// Must be called before Restart() to take effect on the new ffmpeg process.
func (s *TranscodeSession) SetAudioTrackIndex(index int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.AudioTrackIndex = index
}

// SetRequireAudioTrack updates whether ffmpeg must fail rather than drop the
// selected audio track. Must be called before Restart() to take effect.
func (s *TranscodeSession) SetRequireAudioTrack(required bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.RequireAudioTrack = required
}

// SetSourceAudioChannels updates the selected source track's channel count in
// the session's opts. Must be called before Restart() so the rebuilt ffmpeg
// command applies (or removes) the stereo-downmix loudness filter.
func (s *TranscodeSession) SetSourceAudioChannels(channels int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.SourceAudioChannels = channels
}

// cleanStaleSegments removes segment files at or after startSegment and the
// old manifest so a restarted copy-mode FFmpeg process writes clean output.
// The init.mp4 is preserved — its codec configuration is derived from the
// source file and is identical across restarts.
func (s *TranscodeSession) cleanStaleSegments(startSegment int) {
	entries, err := os.ReadDir(s.outputDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "stream.m3u8" {
			_ = os.Remove(filepath.Join(s.outputDir, name))
			continue
		}
		if name == hlsInitSegmentName {
			continue
		}
		segNum, parseErr := ParseSegmentNumber(name)
		if parseErr != nil {
			continue
		}
		if segNum >= startSegment {
			_ = os.Remove(filepath.Join(s.outputDir, name))
		}
	}
}

// emittedStreamRecipe captures the opts fields that decide what bytes ffmpeg
// writes into the output directory. Two generations that agree on all of them
// emit interchangeable segments, so the older ones stay reusable for a backward
// seek; any difference makes the older segments wrong-generation media and
// their manifest a description of a stream that no longer exists.
type emittedStreamRecipe struct {
	videoCodec      string
	bitstreamFilter string
	toneMapMode     tonemap.Mode
	toneMapFilter   string
	hwAccel         string
	softwareEncode  bool
	// Copy/remux packaging. A copy generation writes the source bytes into a
	// chosen container: the sample entry, the versioned copy recipe, and the
	// MPEG-TS vs. fMP4 packaging all change those bytes even when the video
	// codec string is unchanged.
	videoSampleEntry string
	copyFMP4Version  string
	copyVideoMPEGTS  bool
	// Audio bytes. Copying the selected track, encoding it to a target codec,
	// and the encoded channel layout/bitrate all change the segment contents.
	// SourceAudioChannels chooses the stereo-downmix gain filter, so it changes
	// the emitted audio even when the target codec and layout are unchanged.
	targetAudioCodec       string
	audioTrackIndex        int
	sourceAudioChannels    int
	targetAudioChannels    int
	targetAudioBitrateKbps int
	// Burned-in subtitles are rendered into the video, so the burn-in choice,
	// track, and codec change the emitted bytes.
	subtitleBurnIn     bool
	subtitleTrackIndex int
	subtitleCodec      string
	// Scaling and a video bitrate cap change encoded pixels; the segment
	// duration renumbers the output timeline.
	targetResolution  string
	targetBitrateKbps int
	segmentDuration   int
}

func emittedRecipeOf(opts TranscodeOpts) emittedStreamRecipe {
	return emittedStreamRecipe{

		videoCodec:             strings.ToLower(strings.TrimSpace(opts.TargetCodecVideo)),
		bitstreamFilter:        strings.TrimSpace(opts.VideoBitstreamFilter),
		toneMapMode:            opts.ToneMapMode,
		toneMapFilter:          strings.TrimSpace(opts.ToneMapFilter),
		hwAccel:                strings.ToLower(strings.TrimSpace(opts.HWAccel)),
		softwareEncode:         opts.softwareEncode,
		videoSampleEntry:       strings.ToLower(strings.TrimSpace(opts.VideoSampleEntry)),
		copyFMP4Version:        strings.TrimSpace(opts.CopyFMP4RecipeVersion),
		copyVideoMPEGTS:        opts.CopyVideoMPEGTS,
		targetAudioCodec:       strings.ToLower(strings.TrimSpace(opts.TargetCodecAudio)),
		audioTrackIndex:        opts.AudioTrackIndex,
		sourceAudioChannels:    opts.SourceAudioChannels,
		targetAudioChannels:    opts.TargetAudioChannels,
		targetAudioBitrateKbps: opts.TargetAudioBitrateKbps,
		subtitleBurnIn:         opts.SubtitleBurnIn,
		subtitleTrackIndex:     opts.SubtitleTrackIndex,
		subtitleCodec:          strings.ToLower(strings.TrimSpace(opts.SubtitleCodec)),
		targetResolution:       strings.ToLower(strings.TrimSpace(opts.TargetResolution)),
		targetBitrateKbps:      opts.TargetBitrateKbps,
		segmentDuration:        opts.SegmentDuration,
	}
}

// cleanStaleOutputForRestart removes output the replacement generation must not
// inherit. Old segments are only reusable when the previous and next generation
// emit the identical recipe: a recipe change (copy to an encoded target, a
// tone-map mode or filter switch, a different hardware backend, a different
// sample entry or copy recipe version, an audio/packaging change) leaves both
// wrong-generation segments at or after the restart point and a manifest
// describing a stream the new process will never produce. Serving either mixes
// generations, and reading the stale manifest as produced progress makes the
// throttler pause a process that has not produced anything yet.
//
// A copy/remux restart with an unchanged recipe keeps its segments: the source
// bytes at a given timeline position are identical, so a seek back inside the
// retention window is served from the retained files instead of re-spawning
// FFmpeg. Segment boundaries for copy video follow source keyframes, but the
// numbering and origin are re-derived from the manifest on every restart, so
// retained files remain addressable.
//
// The previous recipe is read from the running process, not from the caller's
// options: SetAudioTrackIndex and SetSourceAudioChannels mutate s.opts before
// Restart, so a caller-supplied previous recipe would already describe the
// replacement and wrongly retain segments emitted with the earlier audio
// selection or filter.
//
// It reports whether it cleaned, so callers can log the decision.
func (s *TranscodeSession) cleanStaleOutputForRestart(first interface{}, rest ...interface{}) bool {
	switch v := first.(type) {
	case TranscodeOpts:
		if len(rest) < 2 {
			return false
		}
		previous := emittedRecipeOf(v)
		var next emittedStreamRecipe
		if nextOpts, ok := rest[0].(TranscodeOpts); ok {
			next = emittedRecipeOf(nextOpts)
		} else if nextRecipe, ok := rest[0].(emittedStreamRecipe); ok {
			next = nextRecipe
		}
		startSegment, _ := rest[1].(int)
		// Identical emitted recipes can safely reuse retained segments, including
		// copy-video restarts: absolute numbering keeps them on the same timeline.
		// Recipe changes must discard the replacement range to avoid wrong-track
		// or wrong-filter media being served.
		if previous == next {
			return false
		}
		s.cleanStaleSegments(startSegment)
		return true
	case emittedStreamRecipe:
		next := v
		var startSegment int
		if len(rest) > 0 {
			startSegment, _ = rest[0].(int)
		}
		s.mu.Lock()
		previous := s.runningRecipe
		s.mu.Unlock()
		if previous == next {
			return false
		}
		s.cleanStaleSegments(startSegment)
		return true
	default:
		return false
	}
}

// Restart kills the current ffmpeg process and starts a new one seeking to
// the given position. startSegment sets -hls_segment_start_number so that
// output filenames align with the expected segment numbering. Existing segment
// files are preserved so backward seeks can reuse them, but only while the
// replacement generation emits the same recipe; copy-mode restarts and
// recipe-changing restarts clean the manifest and the segments at or after the
// restart point (see cleanStaleOutputForRestart).
func (s *TranscodeSession) Restart(ctx context.Context, seekSeconds float64, startSegment int) error {
	return s.restart(ctx, seekSeconds, startSegment, 0, false)
}

// RestartWithCopySeekAnchor restarts a copy-video stream with the keyframe
// origin resolved for this specific seek. Keeping this metadata explicit
// prevents a prior seek's origin from being reused after an audio switch.
func (s *TranscodeSession) RestartWithCopySeekAnchor(
	ctx context.Context,
	seekSeconds float64,
	startSegment int,
	streamOriginSeconds float64,
) error {
	return s.restart(ctx, seekSeconds, startSegment, streamOriginSeconds, true)
}

// RestartSegment resolves and applies one missing-segment recovery as a single
// operation. Callers hold their session lifecycle lock around this method so
// the manifest used for copy-anchor mapping cannot be replaced by another
// restart before the resolved numbering is applied.
func (s *TranscodeSession) RestartSegment(ctx context.Context, segNum int) (SegmentRecoveryTarget, bool, error) {
	// A request can wait on the lifecycle lock behind another restart, and a
	// player that seeks again abandons it. Restarting for it would move FFmpeg
	// to a position nobody wants and delay the restart the player now waits
	// on. Once started, a restart runs to completion (see restart).
	if err := ctx.Err(); err != nil {
		return SegmentRecoveryTarget{}, false, err
	}
	target, ok, err := s.ResolveSegmentRecoveryTarget(ctx, segNum)
	if err != nil || !ok {
		return SegmentRecoveryTarget{}, ok, err
	}

	if target.CopySeekAnchorResolved {
		err = s.RestartWithCopySeekAnchor(
			ctx,
			target.SeekSeconds,
			target.StartSegmentNumber,
			target.StreamOriginSeconds,
		)
	} else {
		err = s.Restart(ctx, target.SeekSeconds, target.StartSegmentNumber)
	}
	if err != nil {
		return SegmentRecoveryTarget{}, false, err
	}
	return target, true, nil
}

// restartFlight carries the outcome of an in-flight restart so a concurrent
// caller waits for it and receives the result instead of assuming success and
// falling through to a stream the failed restart never produced.
type restartFlight struct {
	done chan struct{}
	err  error
}

// restartToneMapValidationTimeout bounds the tone-map source recheck on a
// restart. It is deliberately shorter than the caller's post-restart segment
// wait (30s) so a slow validation cannot consume the entire recovery window.
var restartToneMapValidationTimeout = 20 * time.Second

func (s *TranscodeSession) restart(
	ctx context.Context,
	seekSeconds float64,
	startSegment int,
	streamOriginSeconds float64,
	copySeekAnchorResolved bool,
) error {
	// Drive the shared evaluator before taking the lock: a suspected
	// generation whose observation deadline has passed confirms here exactly as
	// it would for serving or a waiter, so restart refuses the same way every
	// other verdict path does.
	s.evaluateDecodeVerdict()
	s.mu.Lock()
	// A source candidate already stamped known-bad must not be rebuilt: the
	// caller rotates on the next failure recovery instead of looping on the
	// same bad transport.
	if s.demuxStamped {
		s.mu.Unlock()
		return ErrVirtualSourceDemuxFailed
	}
	// A decoder-rejected generation must not be rebuilt either: the bytes are
	// undecodable, so a restart would only re-encode the same failure. The
	// gated verdict keeps healthy mid-stream sessions restartable; only a
	// storm with no output since this generation started refuses.
	if s.sourceRejectedLocked() {
		s.mu.Unlock()
		return ErrSourceDecodeRejected
	}
	// Single-flight: a second caller arriving while a restart is in
	// progress must not kill the process the first restart just started.
	// It waits for the in-flight restart's outcome and returns it, so a
	// failed validation is never reported as a successful restart.
	if s.restarting != nil {
		flight := s.restarting
		s.mu.Unlock()
		<-flight.done
		return flight.err
	}
	flight := &restartFlight{done: make(chan struct{})}
	s.restarting = flight
	opts := s.opts
	// The running process's recipe, not the possibly-mutated opts: a caller
	// that changed the audio selection before Restart leaves s.opts describing
	// the replacement, and only the frozen recipe proves the retained segments
	// came from the same emitted bytes.
	previousRecipe := s.runningRecipe
	refreshInput := s.opts.RefreshInput
	cancelCurrent := s.cancel
	done := s.done
	s.mu.Unlock()
	// A tone-map recipe is valid only for the frozen source revision. Recheck it
	// while the current process is still serving so replacement bytes can never
	// displace a valid generation or inherit its cached preflight verdict. The
	// recheck is bounded by a restart-specific budget shorter than the caller's
	// segment wait and detached from request cancellation so a client disconnect
	// cannot abort a restart mid-flight.
	validationCtx, cancelValidation := context.WithTimeout(context.WithoutCancel(ctx), restartToneMapValidationTimeout)
	if err := validateToneMapSource(validationCtx, opts); err != nil {
		cancelValidation()
		flight.err = fmt.Errorf("validate tone-map source before restart: %w", err)
		s.mu.Lock()
		s.restarting = nil
		s.mu.Unlock()
		close(flight.done)
		return flight.err
	}
	cancelValidation()
	s.StopThrottler()

	var refreshedPath string
	var refreshedCleanup func()
	if refreshInput != nil {
		var err error
		refreshedPath, refreshedCleanup, err = refreshInput(ctx)
		if err != nil {
			flight.err = fmt.Errorf("refresh transcode input: %w", err)
			s.mu.Lock()
			s.restarting = nil
			s.mu.Unlock()
			close(flight.done)
			return flight.err
		}
	}

	// Kill current process without removing output directory.
	if cancelCurrent != nil {
		cancelCurrent()
	}
	if done != nil {
		<-done
	}
	s.closeSubtitleSpan()

	// A planned stream's complete VOD playlist keeps the same segment numbers
	// across a restart, so a seek restart lands on the plan's exact segment
	// boundary and FFmpeg continues from there. Backward segments stay on
	// disk; the requested segment remains the throttler's target.
	if s.copyPlan != nil {
		if !copySeekAnchorResolved {
			// A direct restart (an audio switch, or a caller that resolved no
			// anchor) still starts at the planned boundary containing the
			// request, so the replacement cuts the plan's own segments.
			if segmentStart, segment, ok := s.copyPlan.seekTarget(seekSeconds); ok {
				seekSeconds, _ = s.copyPlan.segmentSeekPosition(segment)
				startSegment = segment
				streamOriginSeconds = segmentStart
				copySeekAnchorResolved = true
			}
		}
		if copySeekAnchorResolved && startSegment >= 0 && startSegment < len(s.copyPlan.durations) {
			// The caller's request already names a planned segment; add its
			// bias so the demuxer lands on that boundary keyframe.
			if request, ok := s.copyPlan.segmentSeekPosition(startSegment); ok {
				seekSeconds = request
			}
		}
	}
	requestedSegment := startSegment

	s.mu.Lock()
	s.running = false
	s.waitErr = nil
	if s.stderr != nil {
		s.stderr.Reset()
	}
	// Open the decoder-verdict generation as soon as the old process is dead
	// and before the replacement's stderr writer is created: this fences the
	// dead process's late stderr and any in-flight evaluator away from the
	// replacement, and lets the new writer capture the new generation.
	s.resetDecodeVerdictLocked()
	s.restartCount++
	if refreshedPath != "" {
		if refreshedPath == opts.InputPath {
			// A refresh that hands back the same path is a reuse of the pinned
			// transport: the resource the existing cleanup releases is exactly
			// the one the replacement opens, so retaining that cleanup is what
			// keeps the live relay alive. A cleanup the refresh returned for the
			// same path is a redundant registration, not the retained input's
			// owner, so release it here rather than leaking it until session
			// close (#158).
			if refreshedCleanup != nil {
				refreshedCleanup()
			}
		} else {
			// A genuinely new input: release the previous cleanup exactly once,
			// then adopt the replacement. The replacement cleanup is installed
			// for release on session close and is deliberately never invoked
			// here, so the swap can neither leak the old input nor tear down the
			// replacement (#158).
			if opts.InputCleanup != nil {
				opts.InputCleanup()
			}
			opts.InputPath = refreshedPath
			opts.InputCleanup = refreshedCleanup
			s.opts.InputPath = refreshedPath
			s.opts.InputCleanup = refreshedCleanup
		}
	}
	nextRecipe := emittedRecipeOf(opts)
	recipeUnchanged := previousRecipe == nextRecipe
	// Record the exact generation this restart displaces. Continuation serving
	// is fenced on this relationship rather than on the segment number alone:
	// only the immediate predecessor can be the same timeline, and only a
	// restart that kept the emitted recipe can reuse its init/track identity.
	if s.segmentIncarnation == "" {
		s.segmentIncarnation = uuid.NewString()
	}
	predecessorToken := s.segmentGenerationTokenLocked()
	s.segmentGeneration++
	s.restartPredecessorToken = predecessorToken
	// Record the absolute segment the replacement generation starts producing
	// at. A client still holding the displaced generation's playlist can then be
	// served from this generation for segments at or after this number, because
	// absolute segment numbering makes them the same timeline position.
	s.restartStartSegment = startSegment
	s.restartRecipeStable = recipeUnchanged
	s.segmentPruneRunning = false
	preStartRangePrunable := opts.SegmentRetentionSeconds > 0 && s.lastPruneFloor < startSegment
	s.lastRequestedSegment = startSegment
	s.lastCompletedSegment = startSegment - 1
	if preStartRangePrunable {
		// Every restart preserves files below startSegment. Keep an older prune
		// cursor reachable until replacement downloads age out that range, even
		// when the reported restart position trails the download high-water mark.
		s.pruneBeforeStart = true
	} else {
		s.lastPruneFloor = startSegment - 1
		s.pruneBeforeStart = false
	}
	s.lastPruneHighWater = startSegment - 1
	hwWorkloadDevice := s.hwWorkloadDevice
	s.mu.Unlock()

	opts.SeekSeconds = seekSeconds
	opts.StartSegmentNumber = startSegment
	if strings.EqualFold(opts.TargetCodecVideo, "copy") {
		// A seek restart describes a new emitted timeline. Never retain the
		// previous copy seek's keyframe origin when the caller has not resolved
		// a replacement; falling back to SeekSeconds is conservative and matches
		// the behavior of recipes created before copy anchors were introduced.
		opts.StreamOriginSeconds = streamOriginSeconds
		opts.CopySeekAnchorResolved = copySeekAnchorResolved
	}
	opts.FastStart = false // seek-restarts use veryfast for better quality
	if subtitleRenditionActive(opts) {
		s.mu.Lock()
		opts.subtitleCueFile = subtitleCueFileName(s.subtitleCueBase + s.segmentGeneration)
		s.mu.Unlock()
	}

	// Old segments are only reusable when this generation emits the same recipe
	// as the previous one; otherwise they, and the manifest describing them,
	// have to go before the replacement process starts. The comparison uses the
	// running process's frozen recipe, not the caller's opts (which already
	// carry any pre-restart audio selection mutation).
	var cleanedOutput bool
	cleanedFrom := startSegment
	if s.copyPlan != nil {
		// A planned generation's segment boundaries are fixed by the plan, but
		// a replacement that starts inside the produced window must not leave
		// fragments another restart cut. With the recipe unchanged, the bytes
		// before the target are the same media: regenerate the range at and
		// after it and keep the prefix for backward seeking.
		//
		// A recipe change invalidates that prefix too. Segments emitted before
		// the restart hold the old track, filter, or packaging under numbering
		// the replacement generation keeps serving, so a backward seek would
		// deliver them as current media. Discard the whole output, exactly as
		// the pre-plan restart did when it restarted planned streams at zero.
		if !recipeUnchanged {
			cleanedFrom = 0
		}
		s.cleanStaleSegments(cleanedFrom)
		cleanedOutput = true
	} else {
		cleanedOutput = s.cleanStaleOutputForRestart(nextRecipe, startSegment)
	}
	if cleanedOutput {
		log.Printf("playback: cleaned stale transcode output at/after segment %d before restart (video %q -> %q)",
			cleanedFrom, previousRecipe.videoCodec, opts.TargetCodecVideo)
	}

	args := buildFFmpegArgs(opts)
	bin := opts.FFmpegPath
	if bin == "" {
		bin = ffmpegBinary()
	}

	log.Printf("playback: ffmpeg restart cmd: %s %s", bin, strings.Join(args, " "))
	s.logFFmpegEvent(ctx, "ffmpeg process restart", "")

	// As on initial start, the caller bounds synchronous validation; the new
	// FFmpeg process keeps running after the segment request completes.
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := fspath.CommandContext(ctx, bin, args...)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		s.mu.Lock()
		flight.err = err
		s.restarting = nil
		s.waitErr = err
		s.mu.Unlock()
		close(flight.done)
		return fmt.Errorf("create stdin pipe: %w", err)
	}
	cmd.Dir = opts.OutputDir
	cmd.Stderr = s.newStderrWriter(ctx)
	cmd.WaitDelay = 3 * time.Second
	progressPipe, progressErr := attachProgressPipe(cmd, opts)
	if progressErr != nil {
		cancel()
		s.mu.Lock()
		flight.err = progressErr
		s.restarting = nil
		s.waitErr = progressErr
		s.mu.Unlock()
		close(flight.done)
		return fmt.Errorf("create progress pipe: %w", progressErr)
	}

	// The previous process released its reservation before closing done. Keep
	// this session on the same concrete GPU while accounting for the replacement
	// process as a new active workload.
	releaseHWDevice := func() {}
	if hwWorkloadDevice != "" {
		releaseHWDevice = reserveConcreteHWDevice(hwWorkloadDevice)
	}

	// As in StartTranscode, stamp the generation before the process can write.
	inheritedManifest := statManifest(opts.OutputDir)
	startedAt := time.Now()
	// A concurrent stop can unregister this session between the manager's
	// guarded check and this respawn. Re-check under no session lock right
	// before spawning so a closed session is not rebuilt at all.
	if !s.lifecycleAliveNow() {
		cancel()
		releaseHWDevice()
		if progressPipe != nil {
			_ = progressPipe.Close()
		}
		s.mu.Lock()
		flight.err = ErrSessionSuperseded
		s.restarting = nil
		s.waitErr = ErrSessionSuperseded
		s.mu.Unlock()
		close(flight.done)
		return ErrSessionSuperseded
	}
	if err := cmd.Start(); err != nil {
		processmetrics.Record(processmetrics.Transcode, nil, err, ctx.Err())
		cancel()
		releaseHWDevice()
		s.mu.Lock()
		flight.err = err
		s.restarting = nil
		s.waitErr = err
		s.mu.Unlock()
		close(flight.done)
		s.logFFmpegEvent(ctx, "ffmpeg process exit error", err.Error())
		return fmt.Errorf("restart ffmpeg: %w", err)
	}

	s.mu.Lock()
	if s.stdinPipe != nil {
		_ = s.stdinPipe.Close()
	}
	s.cmd = cmd
	s.cancel = cancel
	s.opts = opts
	s.runningRecipe = nextRecipe
	s.running = true
	s.restarting = nil
	// A successful replacement generation supersedes any latched failure from a
	// prior detached restart: the segment request path must not keep answering
	// the old verdict for a session that is producing again.
	s.asyncRestartErr = nil
	s.stdinPipe = stdinPipe
	s.lastRequestedSegment = requestedSegment
	s.lastCompletedSegment = startSegment - 1
	s.beginGeneration(startedAt)
	progressGeneration := s.progressGeneration
	// The verdict generation was already opened when the old process died,
	// before this replacement's stderr writer was created; do not bump it
	// again here or that writer would be fenced as stale.
	s.inheritedManifest = inheritedManifest
	s.startSubtitleSpanLocked(opts)
	s.done = make(chan struct{})
	hook := s.restartHook
	s.mu.Unlock()
	s.saveSubtitleSpans()
	close(flight.done)

	go s.monitorFFmpeg(ctx, cmd, s.done, releaseHWDevice)
	if progressPipe != nil {
		go s.consumeProgress(progressPipe, progressGeneration)
	}

	// Re-check after spawning: a stop that unregistered the session while this
	// process was starting already closed the previous one, so it would not have
	// killed this replacement. Stop it and wait for the reap so no ffmpeg
	// survives the close.
	if !s.lifecycleAliveNow() {
		cancel()
		if done := s.Done(); done != nil {
			<-done
		}
		return ErrSessionSuperseded
	}

	if hook != nil {
		hook(ctx)
	}

	return nil
}

// WaitForSegment polls until the named segment file exists on disk or the
// timeout expires. Returns the segment file path on success.
//
// Segments are served as soon as they appear on disk. The -hls_flags temp_file
// flag ensures ffmpeg writes to a .tmp file and atomically renames on completion,
// so a successful stat means the segment is fully written.
func (s *TranscodeSession) WaitForSegment(name string, timeout time.Duration) (string, error) {
	clean := filepath.Base(name)
	segPath := filepath.Join(s.outputDir, clean)

	deadline := time.After(timeout)
	for {
		info, statErr := os.Stat(segPath)
		segReady := statErr == nil && info.Size() > 0
		if segReady {
			return segPath, nil
		}

		s.mu.Lock()
		running := s.running
		restarting := s.restarting != nil
		waitErr := s.waitErr
		canceled := s.processCanceled
		virtualInput := isVirtualInputPath(s.opts.CanonicalInputPath)
		s.mu.Unlock()

		// A revoked generation reports its typed verdict ahead of every other
		// wait outcome: the bytes will never arrive, so the deadline,
		// exit-error, and restart branches below must not reclassify it.
		// Virtual-scoped like the manifest wait: local segment waits keep
		// their legacy timeout behavior. The check runs every 100ms tick
		// alongside the stat, so no notification channel is needed.
		if virtualInput && s.IsSourceRejected() {
			return "", ErrSourceDecodeRejected
		}

		if restarting {
			select {
			case <-deadline:
				return "", ErrSegmentNotFound
			case <-time.After(outputPollInterval):
				continue
			}
		}

		if !running && waitErr != nil {
			if canceled {
				// Our own teardown killed this generation. Its bytes are gone
				// with it, so answer the stale timeline (the client reloads the
				// manifest) instead of a transcode failure the client would
				// report as failure_recovery against a recipe that never failed.
				return "", ErrStaleSegmentGeneration
			}
			stderr := ""
			if s.stderr != nil {
				stderr = truncateStderr(s.stderr.String())
			}
			if stderr != "" {
				return "", fmt.Errorf("%w: %w (stderr: %s)", ErrTranscodeFailed, waitErr, stderr)
			}
			return "", fmt.Errorf("%w: %w", ErrTranscodeFailed, waitErr)
		}
		// If ffmpeg finished cleanly but the segment doesn't exist,
		// it won't appear later — fail fast.
		if !running {
			return "", ErrSegmentNotFound
		}

		select {
		case <-deadline:
			return "", ErrSegmentNotFound
		case <-time.After(outputPollInterval):
		}
	}
}

// WaitForOpenSegment is the opened-file counterpart to WaitForSegment. It
// closes the stat-to-open race for HTTP serving while preserving the same
// restart and ffmpeg-exit error contract.
func (s *TranscodeSession) WaitForOpenSegment(name string, timeout time.Duration) (*SegmentLease, error) {
	return s.WaitForOpenSegmentContext(context.Background(), name, timeout)
}

// WaitForOpenSegmentContext stops polling when the request context ends.
func (s *TranscodeSession) WaitForOpenSegmentContext(ctx context.Context, name string, timeout time.Duration) (*SegmentLease, error) {
	return s.waitForOpenSegmentContext(ctx, name, timeout, 0)
}

// WaitForOpenSegmentWithStall is WaitForOpenSegment with a produced-head
// watchdog. A wait may use the full timeout while the encoder keeps advancing
// the head; if the head does not move for stall, it returns ErrSegmentStalled
// so the caller can restart the generation instead of treating a slow encode
// as a dead segment.
func (s *TranscodeSession) WaitForOpenSegmentWithStall(name string, timeout, stall time.Duration) (*SegmentLease, error) {
	return s.waitForOpenSegment(name, timeout, stall)
}

// checkSegmentStall samples produced-head progress at most twice a second and
// reports ErrSegmentStalled when the head has not moved for stall. It is split
// from the wait loop so the recovery condition stays readable.
func (s *TranscodeSession) checkSegmentStall(now time.Time, stall time.Duration, lastCheck *time.Time, lastHead *int, lastHeadAt, lastTempAt *time.Time, lastTempSize *int64) error {
	if !lastCheck.IsZero() && now.Sub(*lastCheck) < 500*time.Millisecond {
		return nil
	}
	progress := s.SegmentProgress(now)
	*lastCheck = now
	if !progress.HasManifest || progress.ProducedHead < progress.StartSegmentNumber {
		return nil
	}
	if progress.ProducedHead != *lastHead {
		*lastHead = progress.ProducedHead
		*lastHeadAt = now
	}
	// A growing partial segment is encoder progress even though the published
	// head cannot move until that segment is complete.
	if progress.ActiveTempAt.After(*lastTempAt) || progress.ActiveTempSize != *lastTempSize {
		*lastTempAt = progress.ActiveTempAt
		*lastTempSize = progress.ActiveTempSize
		*lastHeadAt = now
	}
	if !lastHeadAt.IsZero() && now.Sub(*lastHeadAt) >= stall {
		return ErrSegmentStalled
	}
	return nil
}

func (s *TranscodeSession) waitForOpenSegment(name string, timeout, stall time.Duration) (*SegmentLease, error) {
	return s.waitForOpenSegmentContext(context.Background(), name, timeout, stall)
}

func (s *TranscodeSession) waitForOpenSegmentContext(ctx context.Context, name string, timeout, stall time.Duration) (*SegmentLease, error) {
	deadline := time.After(timeout)
	numbered := false
	if stall > 0 {
		_, parseErr := ParseSegmentNumber(name)
		numbered = parseErr == nil
	}
	lastHead := -1
	lastHeadAt := time.Time{}
	lastStallCheck := time.Time{}
	lastTempAt := time.Time{}
	var lastTempSize int64
	for {
		segment, err := s.OpenSegment(name)
		if err == nil {
			return segment, nil
		}
		if !errors.Is(err, ErrSegmentNotFound) {
			return nil, err
		}

		s.mu.Lock()
		running := s.running
		restarting := s.restarting != nil
		waitErr := s.waitErr
		canceled := s.processCanceled
		virtualInput := isVirtualInputPath(s.opts.CanonicalInputPath)
		s.mu.Unlock()

		// Revocation outranks every other wait outcome (see WaitForSegment).
		// Virtual-scoped like the manifest wait; local segment waits keep
		// their legacy timeout behavior.
		if virtualInput && s.IsSourceRejected() {
			return nil, ErrSourceDecodeRejected
		}

		if restarting {
			lastHead = -1
			lastHeadAt = time.Time{}
			lastStallCheck = time.Time{}
			lastTempAt = time.Time{}
			lastTempSize = 0
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-deadline:
				return nil, ErrSegmentNotFound
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}
		if !running && waitErr != nil {
			if canceled {
				// Same self-teardown verdict as WaitForSegment.
				return nil, ErrStaleSegmentGeneration
			}
			return nil, fmt.Errorf("%w: %w", ErrTranscodeFailed, waitErr)
		}
		if !running {
			return nil, ErrSegmentNotFound
		}

		if numbered && stall > 0 {
			if err := s.checkSegmentStall(time.Now(), stall, &lastStallCheck, &lastHead, &lastHeadAt, &lastTempAt, &lastTempSize); err != nil {
				return nil, err
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, ErrSegmentNotFound
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// RewriteManifestPaths prefixes relative segment references in an HLS manifest
// with segPrefix (e.g. "segment/") and optionally appends rawQuery as a query
// string. This ensures the HLS player's segment requests match server routes
// and preserve any auth or cache-busting parameters from the manifest URL.
func RewriteManifestPaths(manifest []byte, segPrefix, rawQuery string) ([]byte, error) {
	return rewriteManifestPaths(manifest, segPrefix, rawQuery, false)
}

// RewriteManifestPathsWithSubstitution is RewriteManifestPaths for a session
// whose client negotiated hls_variable_substitution_v1: a credential-sized
// query (see minRewrittenURISavings) is carried once through variable
// substitution.
func RewriteManifestPathsWithSubstitution(manifest []byte, segPrefix, rawQuery string) ([]byte, error) {
	return rewriteManifestPaths(manifest, segPrefix, rawQuery, true)
}

func rewriteManifestPaths(manifest []byte, segPrefix, rawQuery string, substitute bool) ([]byte, error) {
	if err := validateManifestHeader(manifest); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	lines := bytes.Split(manifest, []byte("\n"))
	var suffix, definition string
	if rawQuery != "" {
		suffix = "?" + rawQuery
	}
	// Never define a variable beside the input's own definitions.
	if substitute && rawQuery != "" && !bytes.Contains(manifest, []byte("#EXT-X-DEFINE:")) {
		var reference string
		definition, reference = manifestVariable(manifestQueryVariable, rawQuery, minRewrittenURISavings)
		if definition != "" {
			suffix = "?" + reference
		}
	}

	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}

		// Rewrite #EXT-X-MAP:URI="filename"
		if bytes.HasPrefix(trimmed, []byte("#EXT-X-MAP:")) {
			rewritten, err := rewriteMapURI(trimmed, segPrefix, suffix)
			if err != nil {
				return nil, err
			}
			lines[i] = rewritten
			continue
		}

		// Skip other tags/comments.
		if trimmed[0] == '#' {
			continue
		}

		// Segment filename line.
		lines[i] = []byte(segPrefix + string(trimmed) + suffix)
	}
	if definition != "" {
		lines = insertManifestDefinition(lines, definition)
	}
	return bytes.Join(lines, []byte("\n")), nil
}

// rewriteMapURI rewrites the URI value inside an #EXT-X-MAP tag.
func rewriteMapURI(line []byte, segPrefix, suffix string) ([]byte, error) {
	uriStart := bytes.Index(line, []byte(`URI="`))
	if uriStart < 0 {
		return nil, fmt.Errorf("invalid #EXT-X-MAP line: missing URI attribute")
	}
	uriStart += 5 // skip past URI="
	uriEnd := bytes.IndexByte(line[uriStart:], '"')
	if uriEnd < 0 {
		return nil, fmt.Errorf("invalid #EXT-X-MAP line: unterminated URI attribute")
	}
	uriEnd += uriStart

	oldURI := string(line[uriStart:uriEnd])
	newURI := segPrefix + oldURI + suffix

	result := make([]byte, 0, len(line)+len(newURI)-len(oldURI))
	result = append(result, line[:uriStart]...)
	result = append(result, []byte(newURI)...)
	result = append(result, line[uriEnd:]...)
	return result, nil
}

// AppendManifestQueryParam appends a single "key=value" query parameter to every
// segment, variant, #EXT-X-MAP init, and #EXT-X-MEDIA rendition URI in an HLS
// playlist, preserving any query the URI already carries (using "?" or "&" as
// appropriate). The value is appended verbatim — callers must supply a URL-safe
// value (a signed stream token is base64url and safe). A manifest without a valid #EXTM3U header is returned
// unchanged so a non-manifest body is never corrupted.
//
// It exists for the API proxy boundary: a transcode node builds its manifest from
// the forwarded request query, which deliberately omits the signed stream token
// ("st") so the token never reaches the node URL or its logs. That leaves the
// node-built segment URIs token-less, so a later segment fetch after a node/API
// restart cannot reconstruct the session. Re-injecting the client-facing token
// here keeps reconstruction working without ever exposing it to the node.
func AppendManifestQueryParam(manifest []byte, key, value string) []byte {
	return appendManifestQueryParam(manifest, key, value, false)
}

// AppendManifestQueryParamWithSubstitution is AppendManifestQueryParam for a
// session whose client negotiated hls_variable_substitution_v1: in a media
// playlist the value is carried once through variable substitution (a
// "silo_<key>" EXT-X-DEFINE) under the same rule as
// RewriteManifestPathsWithSubstitution. A multivariant playlist keeps the
// literal value.
func AppendManifestQueryParamWithSubstitution(manifest []byte, key, value string) []byte {
	return appendManifestQueryParam(manifest, key, value, true)
}

func appendManifestQueryParam(manifest []byte, key, value string, substitute bool) []byte {
	if key == "" || validateManifestHeader(manifest) != nil {
		return manifest
	}
	param := key + "=" + value
	lines := bytes.Split(manifest, []byte("\n"))
	var definition string
	name := manifestParamVariablePrefix + key
	if substitute && isManifestVariableName(name) && isMediaPlaylist(lines) && !bytes.Contains(manifest, []byte(`NAME="`+name+`"`)) {
		var reference string
		definition, reference = manifestVariable(name, value, minRewrittenURISavings)
		if definition != "" {
			param = key + "=" + reference
		}
	}
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		// A multivariant playlist's subtitle rendition carries its playlist
		// URI as an attribute, exactly like an init segment.
		if bytes.HasPrefix(trimmed, []byte("#EXT-X-MAP:")) || bytes.HasPrefix(trimmed, []byte("#EXT-X-MEDIA:")) {
			lines[i] = appendMapURIQueryParam(trimmed, param)
			continue
		}
		if trimmed[0] == '#' {
			continue
		}
		lines[i] = appendURIQueryParam(trimmed, param)
	}
	if definition != "" {
		lines = insertManifestDefinition(lines, definition)
	}
	return bytes.Join(lines, []byte("\n"))
}

// appendURIQueryParam appends a "key=value" param to a bare URI line.
func appendURIQueryParam(uri []byte, param string) []byte {
	sep := []byte("?")
	if bytes.IndexByte(uri, '?') >= 0 {
		sep = []byte("&")
	}
	out := make([]byte, 0, len(uri)+len(sep)+len(param))
	out = append(out, uri...)
	out = append(out, sep...)
	out = append(out, param...)
	return out
}

// appendMapURIQueryParam appends a "key=value" param to the URI inside an
// #EXT-X-MAP tag. A line without a well-formed URI attribute is returned
// unchanged.
func appendMapURIQueryParam(line []byte, param string) []byte {
	uriStart := bytes.Index(line, []byte(`URI="`))
	if uriStart < 0 {
		return line
	}
	uriStart += 5 // skip past URI="
	uriEnd := bytes.IndexByte(line[uriStart:], '"')
	if uriEnd < 0 {
		return line
	}
	uriEnd += uriStart

	sep := []byte("?")
	if bytes.IndexByte(line[uriStart:uriEnd], '?') >= 0 {
		sep = []byte("&")
	}
	result := make([]byte, 0, len(line)+len(sep)+len(param))
	result = append(result, line[:uriEnd]...)
	result = append(result, sep...)
	result = append(result, param...)
	result = append(result, line[uriEnd:]...)
	return result
}

func (s *TranscodeSession) manifestTimeoutError(timeout time.Duration) error {
	s.mu.Lock()
	running := s.running
	waitErr := s.waitErr
	stderr := ""
	if s.stderr != nil {
		stderr = truncateStderr(s.stderr.String())
	}
	s.mu.Unlock()

	switch {
	case waitErr != nil && stderr != "":
		return fmt.Errorf("%w after %s: ffmpeg exited: %w (stderr: %s)", ErrManifestNotReady, timeout, waitErr, stderr)
	case waitErr != nil:
		return fmt.Errorf("%w after %s: ffmpeg exited: %w", ErrManifestNotReady, timeout, waitErr)
	case running:
		return fmt.Errorf("%w after %s: ffmpeg still running", ErrManifestNotReady, timeout)
	default:
		return fmt.Errorf("%w after %s: ffmpeg is no longer running", ErrManifestNotReady, timeout)
	}
}

// IsCopyVideo reports whether this session is repackaging video without
// re-encoding. Copy-mode manifests must reflect FFmpeg's real fragment timing.
func (s *TranscodeSession) IsCopyVideo() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.EqualFold(s.opts.TargetCodecVideo, "copy")
}

// CopyVODPlan reports the complete VOD timeline a planned copy session serves.
// A false result means the session uses FFmpeg's growing playlist instead, so
// the caller must keep the copy pre-roll timeline rather than claim the media
// is seekable.
func (s *TranscodeSession) CopyVODPlan() (CopyVODPlan, bool) {
	s.mu.Lock()
	plan := s.copyPlan
	s.mu.Unlock()
	if plan == nil || len(plan.durations) == 0 {
		return CopyVODPlan{}, false
	}
	return CopyVODPlan{Origin: plan.origin, Duration: plan.totalDuration(), Segments: len(plan.durations)}, true
}

// SegmentStartTime reports the source-timeline start time of the requested
// segment using the current on-disk manifest. The bool return is false when
// the segment is not present in the manifest yet.
func (s *TranscodeSession) SegmentStartTime(segNum int) (float64, bool, error) {
	baseSeekSeconds, timeline, err := s.manifestTimelineSnapshot()
	if err != nil {
		return 0, false, err
	}

	currentTime := baseSeekSeconds
	for _, entry := range timeline.entries {
		if entry.number == segNum {
			return currentTime, true, nil
		}
		currentTime += entry.duration
	}

	return 0, false, nil
}

func (s *TranscodeSession) manifestTimelineSnapshot() (float64, manifestTimeline, error) {
	s.mu.Lock()
	manifestPath := filepath.Join(s.outputDir, "stream.m3u8")
	baseSeekSeconds := s.opts.SeekSeconds
	opts := s.opts
	generation, restarting := s.segmentGeneration, s.restarting != nil
	if strings.EqualFold(s.opts.TargetCodecVideo, "copy") && s.opts.CopySeekAnchorResolved {
		baseSeekSeconds = s.opts.StreamOriginSeconds
	}
	s.mu.Unlock()
	if restarting {
		return 0, manifestTimeline{}, ErrManifestNotReady
	}

	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, manifestTimeline{}, ErrManifestNotReady
		}
		return 0, manifestTimeline{}, fmt.Errorf("read manifest: %w", err)
	}

	timeline, err := parseManifestTimeline(manifest)
	if err != nil {
		return 0, manifestTimeline{}, fmt.Errorf("parse manifest timeline: %w", err)
	}

	if len(timeline.entries) == 0 {
		return 0, manifestTimeline{}, ErrManifestNotReady
	}
	if strings.EqualFold(opts.TargetCodecVideo, "copy") && opts.CopySeekAnchorResolved {
		baseSeekSeconds, err = s.copyManifestOrigin(opts, generation, timeline)
		if err != nil {
			return 0, manifestTimeline{}, err
		}
	}
	return baseSeekSeconds, timeline, nil
}

const copySegmentStartMatchTolerance = 50 * time.Millisecond

// segmentNumberAtSourceTime maps an actual copied packet timestamp back onto
// the existing playlist. Copy HLS segment durations follow source keyframes,
// so fixed-duration arithmetic cannot preserve the URI numbering after a
// restart. The timestamp must match a real segment boundary; returning false is
// safer than publishing shifted content when the bounded manifest no longer
// contains the preceding anchor.
func (s *TranscodeSession) segmentNumberAtSourceTime(sourceSeconds float64) (int, bool, error) {
	baseSeekSeconds, timeline, err := s.manifestTimelineSnapshot()
	if err != nil {
		return 0, false, err
	}

	currentTime := baseSeekSeconds
	for _, entry := range timeline.entries {
		if math.Abs(currentTime-sourceSeconds) <= copySegmentStartMatchTolerance.Seconds() {
			return entry.number, true, nil
		}
		currentTime += entry.duration
	}

	return 0, false, nil
}

// SegmentRecoveryTarget describes the FFmpeg seek and HLS numbering for one
// missing-segment recovery. Copy-video recovery may begin before the requested
// segment when the demuxer emits pre-roll; encoded recovery remains exact.
type SegmentRecoveryTarget struct {
	SeekSeconds            float64
	StreamOriginSeconds    float64
	StartSegmentNumber     int
	CopySeekAnchorResolved bool
}

// ResolveSegmentRecoveryTarget preserves the existing manifest's
// URI-to-source-time mapping across a missing-segment restart.
func (s *TranscodeSession) ResolveSegmentRecoveryTarget(ctx context.Context, segNum int) (SegmentRecoveryTarget, bool, error) {
	if s.copyPlan != nil {
		// The plan already knows every segment's source-timeline start, so a
		// seek restarts FFmpeg at exactly that boundary with the segment
		// numbering the client's complete VOD playlist already names. Nothing
		// about the served playlist changes, and the transport is reused.
		seekSeconds, ok := s.copyPlan.segmentStart(segNum)
		if !ok {
			return SegmentRecoveryTarget{}, false, nil
		}
		requestSeconds, _ := s.copyPlan.segmentSeekPosition(segNum)
		return SegmentRecoveryTarget{
			SeekSeconds:            requestSeconds,
			StreamOriginSeconds:    seekSeconds,
			StartSegmentNumber:     segNum,
			CopySeekAnchorResolved: true,
		}, true, nil
	}
	seekSeconds, ok, err := s.RestartSeekTarget(segNum)
	if err != nil || !ok {
		return SegmentRecoveryTarget{}, ok, err
	}

	target := SegmentRecoveryTarget{SeekSeconds: seekSeconds, StartSegmentNumber: segNum}
	opts := s.Opts()
	if !strings.EqualFold(opts.TargetCodecVideo, "copy") {
		return target, true, nil
	}

	streamOriginSeconds, _, err := ResolveCopySeekAnchor(
		ctx,
		opts.FFmpegPath,
		opts.InputPath,
		seekSeconds,
		opts.SegmentDuration,
	)
	if err != nil {
		return SegmentRecoveryTarget{}, false, err
	}
	startSegmentNumber, mapped, err := s.segmentNumberAtSourceTime(streamOriginSeconds)
	if err != nil {
		return SegmentRecoveryTarget{}, false, err
	}
	if !mapped {
		// The probed anchor lies past every URI in the current manifest (or
		// between boundaries). Only a segment the manifest does not list at
		// all is the bounded forward jump RestartSeekTarget accepted; an
		// in-manifest segment whose anchor could not be aligned keeps the
		// retryable miss and lets the session finish producing real timing.
		if _, inManifest, segmentErr := s.SegmentStartTime(segNum); segmentErr != nil {
			return SegmentRecoveryTarget{}, false, segmentErr
		} else if inManifest {
			return SegmentRecoveryTarget{}, false, nil
		}
		if _, bounded := s.copyForwardJumpSeekTarget(segNum); !bounded {
			return SegmentRecoveryTarget{}, false, nil
		}
		startSegmentNumber = segNum
	}

	target.StreamOriginSeconds = streamOriginSeconds
	target.StartSegmentNumber = startSegmentNumber
	target.CopySeekAnchorResolved = true
	return target, true, nil
}

// RestartSeekTarget resolves the source-timeline time to restart FFmpeg for
// the requested segment. Copy-mode sessions prefer the current manifest's real
// timing when available; encoded sessions use fixed-duration seek math
// matching the synthetic VOD manifest.
func (s *TranscodeSession) RestartSeekTarget(segNum int) (float64, bool, error) {
	if strings.EqualFold(s.Opts().TargetCodecVideo, "copy") {
		seekSeconds, ok, err := s.SegmentStartTime(segNum)
		switch {
		case err == nil && ok:
			return seekSeconds, true, nil
		case err != nil && !errors.Is(err, ErrManifestNotReady):
			return 0, false, err
		}
		// Copy-mode fragments have variable durations, so the encoded
		// `seg×dur` math would seek FFmpeg to the wrong source time and
		// desync A/V after restart. When the manifest can't resolve this
		// segment yet (ok=false with no error, including ErrManifestNotReady
		// in a freshly reconstructed window), report the seek target as
		// unresolved (0, false, nil) rather than guessing. The caller treats
		// this as a retryable miss so the session keeps producing manifest
		// until real timing is available.
		//
		// A forward jump past the produced head is the bounded exception:
		// when the session knows the media duration, restart FFmpeg in place
		// at the estimated position for the requested segment instead of
		// forcing the client through a 404 -> replan -> provider re-resolve
		// -> transport startup cycle. Unknown-duration and before-start
		// targets still report unresolved.
		if seekSeconds, bounded := s.copyForwardJumpSeekTarget(segNum); bounded {
			return seekSeconds, true, nil
		}
		return 0, false, nil
	}

	segDuration := defaultSegmentDuration
	if opts := s.Opts(); opts.SegmentDuration > 0 {
		segDuration = opts.SegmentDuration
	}
	return float64(segNum * segDuration), true, nil
}

// copyForwardJumpSeekTarget estimates the source-timeline position for a
// copy-mode segment beyond the produced manifest window. The current
// generation begins at StreamOriginSeconds (or SeekSeconds when no keyframe
// origin was resolved) with StartSegmentNumber as its first URI, so the delta
// to the requested segment is the segment duration. The nominal hls_time is
// only a lower bound: keyframe-aligned copy fragments run longer, and the
// undershoot grows with the jump distance. When the manifest has at least two
// real timings their average is used instead of the nominal duration. The
// estimate is only valid when the media duration is known and it lands inside
// that envelope, so a target before the session start or past the end never
// fabricates a position.
func (s *TranscodeSession) copyForwardJumpSeekTarget(segNum int) (float64, bool) {
	opts := s.Opts()
	if opts.TotalDuration <= 0 || segNum < opts.StartSegmentNumber {
		return 0, false
	}
	segDuration := float64(opts.SegmentDuration)
	if segDuration <= 0 {
		segDuration = float64(defaultSegmentDuration)
	}
	if avg, ok := s.averageManifestSegmentDuration(); ok {
		segDuration = avg
	}
	base := opts.SeekSeconds
	if opts.CopySeekAnchorResolved {
		base = opts.StreamOriginSeconds
	}
	seekSeconds := base + float64(segNum-opts.StartSegmentNumber)*segDuration
	if seekSeconds <= 0 || seekSeconds > opts.TotalDuration {
		return 0, false
	}
	return seekSeconds, true
}

// averageManifestSegmentDuration returns the mean duration of the segments the
// manifest currently lists AND whose files exist on disk. It reports false when
// fewer than two produced entries are available, so callers fall back to the
// nominal segment duration. Copy-mode fragments are keyframe-aligned, so this
// real average is the better estimator for a jump past the produced head.
func (s *TranscodeSession) averageManifestSegmentDuration() (float64, bool) {
	_, timeline, err := s.manifestTimelineSnapshot()
	if err != nil || len(timeline.entries) < 2 {
		return 0, false
	}
	opts := s.Opts()
	var total float64
	produced := 0
	for _, entry := range timeline.entries {
		if entry.duration <= 0 {
			continue
		}
		info, statErr := os.Stat(filepath.Join(s.outputDir, segmentFilename(entry.number, opts)))
		if statErr != nil || info.Size() <= 0 {
			continue
		}
		total += entry.duration
		produced++
	}
	if produced < 2 {
		return 0, false
	}
	return total / float64(produced), true
}

// ReportSegmentDownloaded records that the client has downloaded the given
// segment number. Only updates if segNum exceeds the current high-water mark.
func (s *TranscodeSession) ReportSegmentDownloaded(segNum int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reportSegmentDownloadedLocked(segNum)
}

// SegmentGeneration identifies the current ffmpeg timeline. Callers that hold
// an open segment across a restart use it to prevent the old response from
// advancing the replacement process's download position.
func (s *TranscodeSession) SegmentGeneration() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.segmentGeneration
}

// ReportSegmentDownloadedForGeneration records completion only if the served
// file belongs to the current ffmpeg timeline.
func (s *TranscodeSession) ReportSegmentDownloadedForGeneration(segNum int, generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.segmentGeneration {
		return
	}
	s.reportSegmentDownloadedLocked(segNum)
}

// ReportSegmentDownloadedForGenerationToken records a completion relayed over
// HTTP only when it belongs to this exact session object and FFmpeg timeline.
// The opaque incarnation prevents a delayed proxy acknowledgement from matching
// a reconstructed session whose numeric generation restarted at zero.
func (s *TranscodeSession) ReportSegmentDownloadedForGenerationToken(segNum int, generationToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generationToken == "" || generationToken != s.segmentGenerationTokenLocked() {
		return
	}
	s.reportSegmentDownloadedLocked(segNum)
}

// GenerationToken returns this session's current opaque generation token. The
// token binds the session identity, the per-session incarnation (a replacement
// or reconstructed session object is a fresh attempt and gets a new
// incarnation), and the numeric FFmpeg timeline generation. Manifest builders
// put it on every segment URI so a read landing after a switch can be refused
// rather than answered from the wrong generation's bytes. An empty token means
// the session has no generation identity to fence on and callers keep the
// pre-fencing behavior.
func (s *TranscodeSession) GenerationToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opts.SessionID == "" {
		return ""
	}
	if s.segmentIncarnation == "" {
		s.segmentIncarnation = uuid.NewString()
	}
	return s.segmentGenerationTokenLocked()
}

// MatchesGenerationToken reports whether token names this session's current
// generation. An empty token never matches: callers that need the pre-fencing
// back-compat path must check for the empty token before calling this.
func (s *TranscodeSession) MatchesGenerationToken(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.segmentIncarnation == "" {
		return false
	}
	return token == s.segmentGenerationTokenLocked()
}

// RestartContinuationSegment reports the absolute segment the live generation
// started producing at when token names the exact generation displaced by the
// most recent in-place restart and that restart kept the emitted-byte recipe.
// Segment numbers are absolute in the HLS timeline, so a request at or after
// this point names the same timeline position in the successor. Requiring the
// immediate predecessor token — not merely a segment number — keeps an older
// generation's token from being answered by a later generation whose codec,
// selected track, or init segment may differ. It returns false for an empty or
// unrelated token, for a restart that changed the recipe, and after the session
// stopped (which retires the relationship).
//
// successorToken names the one generation allowed to answer the request. The
// serve path must re-check the lease against it after any wait or recovery: a
// second restart while that request was waiting displaces the successor, so the
// older token no longer names the live timeline even though it is still the
// predecessor of the generation that replaced its successor.
func (s *TranscodeSession) RestartContinuationSegment(token, segmentName string) (startSegment int, successorToken string, ok bool) {
	if token == "" || segmentName == "" {
		return 0, "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.restartStartSegment <= 0 || !s.restartRecipeStable ||
		s.restartPredecessorToken == "" || token != s.restartPredecessorToken {
		return 0, "", false
	}
	startSegment = s.restartStartSegment
	successorToken = s.segmentGenerationTokenLocked()
	if strings.EqualFold(segmentName, "init.mp4") {
		return startSegment, successorToken, true
	}
	requestedSegment, err := ParseSegmentNumber(segmentName)
	if err != nil || requestedSegment < startSegment {
		return 0, "", false
	}
	return startSegment, successorToken, true
}

// SeekRestartServeTimeout bounds how long a far-seek segment request waits for
// the restart it scheduled to produce its segment, measured from the start of
// the request's recovery rather than from the moment the detached restart was
// scheduled. FFmpeg spawn is normally 1-2s, but a request that already waited
// for a stalled head passes only the remainder of this budget to the
// continuation, so the composed hold cannot roughly double. Exceeding it falls
// back to the stale-generation verdict, which makes the client reload the
// playlist and retry once the restart has produced it. Kept below the 8s
// default HTTP read timeout of the ExoPlayer-based clients so the held request
// still delivers headers before their transport gives up. Shared by the API and
// remote-node segment handlers so both deployment shapes hold a seek for the
// same bound.
const SeekRestartServeTimeout = 6 * time.Second

// SeekRestartStallTimeout is the produced-head watchdog during a restart
// continuation. It is deliberately shorter than SeekRestartServeTimeout: the
// ordinary SegmentStallTimeout floor is six seconds, which equals the whole
// continuation ceiling, so a stall detector using it could never fire before
// the timeout. Three seconds reports a dead restart well before the client's
// read timeout while still leaving room for a normal spawn plus first segment.
const SeekRestartStallTimeout = 3 * time.Second

// RestartContinuationRemaining returns the part of SeekRestartServeTimeout a
// held segment request may still spend on the restart continuation. started is
// when the request began its recovery (before any stalled-head wait), so the
// wait before the restart and the wait after it draw from one budget instead of
// composing to roughly twice the client's premise. A non-positive result means
// the restart keeps running detached and the request answers the stale-
// generation verdict immediately.
func RestartContinuationRemaining(started time.Time) time.Duration {
	remaining := SeekRestartServeTimeout - time.Since(started)
	if remaining <= 0 {
		return 0
	}
	return remaining
}

// WaitForRestartContinuation waits for the segment an asynchronous seek
// restart was scheduled to produce. A successful restart is served through the
// absolute-segment continuity rule; a terminal restart failure is surfaced
// immediately, and a wait timeout or stall becomes the stale-generation verdict
// so the client refetches the playlist. Sharing this sequence keeps the API
// handler and the remote transcode node handler on one implementation.
func (s *TranscodeSession) WaitForRestartContinuation(name string) (*SegmentLease, error) {
	return s.WaitForRestartContinuationWithin(name, SeekRestartServeTimeout)
}

// WaitForRestartContinuationWithin is WaitForRestartContinuation with an
// explicit remaining budget. Callers that already spent request time waiting
// for a stalled head use it so the composed hold stays inside
// SeekRestartServeTimeout. A spent budget answers the stale-generation verdict
// without waiting; the detached restart still produces the segment for the
// client's playlist reload.
//
// The hold is also skipped when the current generation's own startup is known
// to need longer than the stall window to produce its first segment. A virtual
// provider that opens in 4.7-10s cannot reach the stall window, so holding the
// request spends the client's read budget on a wait that is already lost — the
// production 3.5s-then-412 for a 2062s seek. Answering the stale-generation
// verdict immediately is the same outcome the client gets after the hold, minus
// the wait; the restart keeps producing for the playlist reload it triggers. A
// session whose restarts start inside the stall window keeps the hold, so a
// seek that the encoder serves in a few hundred milliseconds is still served.
// The measurement describes one generation, and a generation that has not
// produced a segment yet has none, so its hold is never skipped unmeasured.
func (s *TranscodeSession) WaitForRestartContinuationWithin(name string, budget time.Duration) (*SegmentLease, error) {
	if budget <= 0 {
		if restartErr := s.AsyncRestartFailure(); restartErr != nil {
			return nil, restartErr
		}
		return nil, ErrStaleSegmentGeneration
	}
	stall := SeekRestartStallTimeout
	if budget < stall {
		stall = budget
	}
	if startup := s.StartupLatency(); startup > 0 && startup > stall {
		if restartErr := s.AsyncRestartFailure(); restartErr != nil {
			return nil, restartErr
		}
		slog.Debug("transcode restart continuation answered without a hold: this session's restarts start slower than the stall window",
			"component", "playback",
			"segment", name,
			"startup_latency_ms", startup.Milliseconds(),
			"stall_ms", stall.Milliseconds(),
			"budget_ms", budget.Milliseconds(),
			"session", s.Opts().SessionID,
		)
		return nil, ErrStaleSegmentGeneration
	}
	lease, err := s.WaitForOpenSegmentWithStall(name, budget, stall)
	if err == nil {
		return lease, nil
	}
	if restartErr := s.AsyncRestartFailure(); restartErr != nil {
		return nil, restartErr
	}
	if errors.Is(err, ErrSegmentNotFound) || errors.Is(err, ErrSegmentStalled) {
		return nil, ErrStaleSegmentGeneration
	}
	return nil, err
}

func (s *TranscodeSession) segmentGenerationTokenLocked() string {
	return s.opts.SessionID + ":" + s.segmentIncarnation + ":" + strconv.FormatUint(s.segmentGeneration, 10)
}

func (s *TranscodeSession) reportSegmentDownloadedLocked(segNum int) {
	if segNum > s.lastRequestedSegment {
		s.lastRequestedSegment = segNum
	}
	if segNum > s.lastCompletedSegment {
		s.lastCompletedSegment = segNum
	}
	s.scheduleSegmentPruneLocked()
}

// LastRequestedSegment returns the highest segment number downloaded by the client.
func (s *TranscodeSession) LastRequestedSegment() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRequestedSegment
}

// AbandonedBeforeFirstSegment reports that this session answered a playlist
// request and never received a single segment request afterwards. A failure
// recovery on such a session is a client that reached the stream and then
// walked away, not evidence about the recipe: the encode had a playlist ready
// and delivered nothing to a player that asked, so replanning it spends the
// adaptation budget on a route that never failed.
//
// The conditions are deliberately narrow. A session that never served a
// playlist is a failure the client met before reading anything (a resolve, a
// seek, a decoder, or a not-ready playlist), and a generation that is dead,
// mid-restart, no longer servable, or carries a decoder verdict is an ordinary
// failure; all of those keep the existing handling.
func (s *TranscodeSession) AbandonedBeforeFirstSegment() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	requested := s.segmentRequested
	manifestServed := s.manifestServed
	running := s.running
	restarting := s.restarting != nil
	s.mu.Unlock()
	if requested || !manifestServed || !running || restarting || s.IsDecodeFailed() {
		return false
	}
	// GetManifest is the same readiness a client sees: it succeeds only once
	// the playlist exists and its startup media is on disk. Failing it (a
	// playlist still being written) keeps the ordinary failure handling.
	if _, err := s.GetManifest(); err != nil {
		return false
	}
	return true
}

// noteManifestServed marks a client-facing playlist request as answered. Only a
// request the session could serve counts: a not-ready playlist is a failure the
// client met inside the stream, not a client that abandoned it.
func (s *TranscodeSession) noteManifestServed(err error) {
	if s == nil || err != nil {
		return
	}
	s.mu.Lock()
	s.manifestServed = true
	s.mu.Unlock()
}

// StartThrottler creates and starts a throttler for this session.
// No-op if thresholdSeconds <= 0 or stdinPipe is nil.
func (s *TranscodeSession) StartThrottler(thresholdSeconds int) {
	s.mu.Lock()
	if s.stdinPipe == nil || thresholdSeconds <= 0 {
		s.mu.Unlock()
		return
	}
	t := NewTranscodeThrottler(s, s.stdinPipe, thresholdSeconds, s.opts.SegmentDuration)
	s.throttler = t
	s.mu.Unlock()
	t.Start()
}

// StopThrottler stops the throttler if one is running.
func (s *TranscodeSession) StopThrottler() {
	s.mu.Lock()
	t := s.throttler
	s.throttler = nil
	s.mu.Unlock()
	if t != nil {
		t.Stop()
	}
}

type ffmpegStderrWriter struct {
	session *TranscodeSession
	ctx     context.Context
	// generation is the decoder-verdict generation this writer belongs to.
	// Lines emitted after a reset (a replacement process is running) are
	// dropped so a dead process's stderr can never touch the replacement.
	generation uint64
	partial    []byte
}

func (w *ffmpegStderrWriter) logLine(line string) {
	if !w.session.logFFmpegLineForGeneration(w.ctx, w.generation, line) {
		return
	}
}

func (w *ffmpegStderrWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.partial = append(w.partial, p...)
	for {
		idx := bytes.IndexByte(w.partial, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(string(w.partial[:idx]), "\r")
		w.logLine(line)
		w.partial = append([]byte(nil), w.partial[idx+1:]...)
	}
	return len(p), nil
}

func (w *ffmpegStderrWriter) Flush() {
	if len(w.partial) == 0 {
		return
	}
	w.logLine(strings.TrimRight(string(w.partial), "\r"))
	w.partial = nil
}

// logFFmpegLineForGeneration handles one stderr line emitted by the writer that
// owns generation. It validates the generation atomically with the observation
// it performs, so a reset interleaved between the writer's check and the
// observation can never let a dead process's line touch the replacement. The
// full pipeline runs under one hold of mu: a generation fence at the head, the
// demux and decode observations — which mutate generation-scoped counters,
// latching hardware state, stage, and evaluator — and finally the diagnostic
// sink. Because the observations record the writer's generation rather than
// re-reading the live one, a reset that waits on mu either lands before the
// whole line (dropping it) or after it, never between check and mutation. It
// reports whether the line's generation was still current, so the caller can
// skip work for a discarded line.
func (s *TranscodeSession) logFFmpegLineForGeneration(ctx context.Context, generation uint64, line string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	if generation != s.decodeGeneration {
		s.mu.Unlock()
		return false
	}
	// Both verdicts indict the video source candidate, so both require the line
	// to resolve to the video stream. A demux or decoder failure with no video
	// identity is left unstamped rather than risk stamping the release for a
	// corrupt audio or subtitle stream sharing the input container.
	video := videoStreamEvidenceV3(line)
	if video && demuxInputErrorLine(line) {
		notifyCtx := ctx
		if notifyCtx == nil {
			notifyCtx = context.Background()
		}
		if s.observeDemuxErrorLocked(s.decodeClock()) {
			go s.notifyDemuxFailure(notifyCtx)
		}
	}
	if video && decodeErrorLine(line) {
		// Observation only: a decoder line never confirms by itself. The
		// shared evaluator (a later progress sample or the observation
		// deadline) confirms and then notifies.
		s.observeDecodeErrorLocked(generation, line, s.decodeClock())
	}
	s.writeFFmpegLogLocked(ctx, line)
	s.mu.Unlock()
	return true
}

// logFFmpegLine handles one already-split stderr line for any caller that names
// no generation (tests and diagnostics). It resolves the live generation under
// mu and delegates to logFFmpegLineForGeneration, so a direct call observes for
// whatever generation is current when it starts and is fenced exactly like a
// writer's line.
func (s *TranscodeSession) logFFmpegLine(ctx context.Context, line string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	generation := s.decodeGeneration
	s.mu.Unlock()
	s.logFFmpegLineForGeneration(ctx, generation, line)
}

// writeFFmpegLogLocked persists one stderr line to the diagnostic sink,
// honoring the per-session cap. Callers hold mu.
func (s *TranscodeSession) writeFFmpegLogLocked(ctx context.Context, line string) {
	if s.opts.FFmpegLogSink == nil {
		return
	}
	line = strings.ToValidUTF8(line, "\uFFFD")
	if strings.TrimSpace(line) == "" {
		return
	}
	trimmed, truncated := truncateUTF8String(line, maxPersistedFFmpegChars)
	if truncated {
		trimmed += "...[truncated]"
	}
	if s.stderrLinesLogged >= maxPersistedFFmpegLines || s.stderrBytesLogged+len(trimmed) > maxPersistedFFmpegBytes {
		s.stderrDroppedLines++
		if !s.stderrCapLogged {
			s.stderrCapLogged = true
			attrs := s.ffmpegAttrsLocked()
			attrs.DroppedLines = s.stderrDroppedLines
			s.opts.FFmpegLogSink.WriteEvent(ctx, s.opts.SessionID, attrs, "ffmpeg stderr logging capped")
		}
		return
	}
	s.stderrLinesLogged++
	s.stderrBytesLogged += len(trimmed)
	s.stderrLineIndex++
	attrs := s.ffmpegAttrsLocked()
	attrs.LineIndex = s.stderrLineIndex
	s.opts.FFmpegLogSink.WriteLine(ctx, s.opts.SessionID, attrs, trimmed)
}

func (s *TranscodeSession) newStderrWriter(ctx context.Context) io.Writer {
	s.mu.Lock()
	lineWriter := &ffmpegStderrWriter{session: s, ctx: ctx, generation: s.decodeGeneration}
	if s.stderr == nil {
		s.stderr = newBoundedTailBuffer(stderrTailMaxBytes)
	}
	s.stderrWriter = lineWriter
	s.mu.Unlock()
	return io.MultiWriter(s.stderr, lineWriter)
}

func (s *TranscodeSession) flushStderr(ctx context.Context) {
	s.mu.Lock()
	writer := s.stderrWriter
	s.stderrWriter = nil
	s.mu.Unlock()
	if writer != nil {
		writer.Flush()
	}
}

// demuxInputErrorLine reports whether an FFmpeg stderr line is an input
// demuxing I/O failure. Only failures reading the input container count;
// output errors and transient network reconnect chatter do not match.
func demuxInputErrorLine(line string) bool {
	return strings.Contains(line, "Error during demuxing")
}

// resetDecodeVerdictLocked opens a fresh decoder-verdict generation: strikes,
// window, sample, stage, deadline, and marker latch never cross a generation
// boundary, so a replacement process re-proves from zero instead of completing
// a threshold its predecessor accumulated. It bumps decodeGeneration so any
// evaluator or stderr writer still running for the dead generation is fenced,
// and cancels the dead generation's evaluator. decodeStamped deliberately
// persists: it records that this session's hardware decoder failed the source,
// which still licenses a software-decode rebuild after an unrelated seek
// restart, without blocking the replacement's own verdict.
//
// Callers must hold mu. Fresh sessions start zeroed, so the spawn path needs no
// call; the returned cancel function (if any) may be invoked after unlocking.
func (s *TranscodeSession) resetDecodeVerdictLocked() {
	s.decodeErrorCount = 0
	s.lastDecodeErrorAt = time.Time{}
	s.decodeSample = ""
	s.decodeStage = decodeStageObserving
	s.decodeSuspectAt = time.Time{}
	s.sourceRejected = false
	s.sourceRejectNotified = false
	s.decodeGeneration++
	if s.decodeWatchCancel != nil {
		s.decodeWatchCancel()
		s.decodeWatchCancel = nil
	}
}

// decodeClock returns the injected clock when a test set one, else time.Now.
func (s *TranscodeSession) decodeClock() time.Time {
	if s.decodeNow != nil {
		return s.decodeNow()
	}
	return time.Now()
}

// decodeErrorLine reports whether an FFmpeg stderr line is a decoder rejecting
// the source bitstream. Only failures that mean the bitstream itself is invalid
// count; encoder, muxer, output, HLS, and network chatter ([h264_qsv], Error
// writing, Broken pipe, HTTP error, reconnecting) never match, mirroring
// demuxInputErrorLine's discipline.
//
// Reference-list warnings are deliberately excluded. "Could not find ref with
// POC" / "Error constructing the frame RPS" are emitted by the HEVC decoder as
// a bounded opening burst while it primes its reference lists on a Dolby Vision
// profile 8 stream, and the decoder keeps producing valid, muxable frames
// afterwards: live sessions with 50-170 of those lines produced 17 to 1600
// decodable segments while the session was wrongly stamped as source-rejected.
// Counting them fatalised playable content. The lines that do mean the bytes
// are undecodable (a bad NAL split, an invalid NAL size, the decoder
// refusing a packet, or parameter sets the decoder never received) are matched
// instead. The SPS/PPS-missing family below is the production shape of a
// provider stream ffmpeg cannot open at all: repeated "SPS 0 does not exist"
// and "PPS id out of range" lines with zero segments produced.
//
// The pattern alone does not name a stream: a corrupt subtitle stream can make
// the same decoder refuse a packet, so logFFmpegLine only counts a match that
// videoStreamEvidenceV3 resolves to the video stream.
func decodeErrorLine(line string) bool {
	switch {
	case strings.Contains(line, "Error submitting packet to decoder"):
		return true
	case strings.Contains(line, "Invalid NAL unit size"):
		return true
	case strings.Contains(line, "Error splitting the input into NAL units"):
		return true
	case strings.Contains(line, "Failed to decode"):
		return true
	case strings.Contains(line, "decode_slice_header error"):
		return true
	case strings.Contains(line, "SPS") && strings.Contains(line, "does not exist"):
		return true
	case strings.Contains(line, "PPS id out of range"):
		return true
	case strings.Contains(line, "non-existing PPS"):
		return true
	default:
		return false
	}
}

// videoIdentityTokensV3 are the codec and bitstream identifiers FFmpeg prints
// when a line names a video stream without the media-type stream-class prefix.
var videoIdentityTokensV3 = []string{
	transcodeCodecH264, "avc1", transcodeCodecHEVC, "h265", "hev1", "hvc1", "dvh1",
	transcodeCodecAV1, transcodeCodecVP9, "vp8", "mpeg2video", "mpeg4", "mpegvideo",
	"vc1", "prores", "dnxhd", "theora", "h263", "wmv3",
	"nal unit", "bitstream", "slice_header",
}

// audioIdentityTagsV3 are the FFmpeg decoder tags that name an audio codec.
// FFmpeg prints the offending decoder in brackets when it rejects a packet
// ("[aac @ 0x...] Error submitting packet to decoder"), so the tag is matched
// with its opening bracket. Matching the bare codec substring would silence a
// video indictment whenever a hex address happened to contain "aac", "ac3", or
// "dts"; the bracket anchor keeps that collision out even though a missed
// indictment is cheaper than a wrong one. dca is the DTS decoder's FFmpeg name,
// and pcm_ covers the linear PCM family.
var audioIdentityTagsV3 = []string{
	"[aac", "[ac3", "[eac3", "[ec3", "[truehd", "[mlp", "[dts", "[dca",
	"[flac", "[opus", "[vorbis", "[alac", "[mp3", "[mp2", "[mp1",
	"[pcm_", "[adpcm_", "[s302m",
}

// audioStreamEvidenceV3 reports whether an FFmpeg stderr line names an audio
// stream as the subject of its error. The decoder tag is the strongest signal;
// the explicit "audio stream" wording catches the panics FFmpeg reports without
// a codec tag. It is deliberately narrower than a bare "audio" substring so an
// unrelated mention cannot silence a genuine video failure.
func audioStreamEvidenceV3(lower string) bool {
	for _, tag := range audioIdentityTagsV3 {
		if strings.Contains(lower, tag) {
			return true
		}
	}
	return strings.Contains(lower, "audio stream")
}

// videoStreamEvidenceV3 reports whether an FFmpeg stderr line positively
// identifies a video stream as the subject of its error. FFmpeg tags
// stream-scoped lines with a media-type prefix: vist#<file>:<stream>/<codec>
// is a video stream, aist#/sist# name audio and subtitle streams, and ost#
// names an output stream. A line with no stream-class tag falls back to an
// audio identity first, then a video codec or bitstream identifier. A
// container-level line such as "[in#0/matroska,webm @ 0x...] Error during
// demuxing" carries none of these: the same input holds the audio and subtitle
// streams, and a corrupt subtitle demuxes through the same container and can
// produce the same error. That ambiguity must not indict the video candidate —
// a missed indictment costs one failed start, a wrong one costs the viewer
// their release.
//
// The audio check runs before the video-token fallback because an audio decoder
// error can contain a video-shaped word ("bitstream", "NAL") while naming an
// audio codec; without the ordering an audio failure would stamp the video
// candidate or reject the source.
func videoStreamEvidenceV3(line string) bool {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "vist#"):
		return true
	case strings.Contains(lower, "aist#"), strings.Contains(lower, "sist#"), strings.Contains(lower, "ost#"):
		return false
	}
	if audioStreamEvidenceV3(lower) {
		return false
	}
	for _, token := range videoIdentityTokensV3 {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// decodeWatchInterval is the cadence of the suspicion evaluator. It is a code
// constant, not a setting, and is deliberately finer than the segment cadence
// so confirmation lands within one tick of the observation deadline.
const decodeWatchInterval = 100 * time.Millisecond

// decodeReapTimeout bounds the server-owned teardown of a confirmed generation.
const decodeReapTimeout = 2 * time.Second

// observeDecodeErrorLocked records one decoder failure line emitted by the
// writer that owns generation, binding the observation to that generation
// rather than re-reading the live one. The caller already holds mu and has
// verified generation's currency, so a reset cannot interleave between the
// check and this mutation; a line from a dead process can never misattribute a
// strike to the replacement. It advances the generation's verdict lifecycle:
// the counter resets when more than decodeErrorDecay elapsed since the previous
// failure, so decoder blips that recover are forgiven. Crossing
// decodeErrorThreshold enters the suspected stage and starts the single
// evaluator; it no longer confirms by itself — qualifying progress cancels
// suspicion and expiry without recovery confirms it (see
// evaluateDecodeVerdict). It reports true only on the call that enters
// suspicion, for logging. A copy target never observes: a decoder error there
// is an output/remux anomaly, not a decode verdict.
//
// It reads time through the session clock so production and tests share one
// timeline. Callers must hold mu.
func (s *TranscodeSession) observeDecodeErrorLocked(generation uint64, sample string, now time.Time) bool {
	if s == nil {
		return false
	}
	if strings.EqualFold(s.opts.TargetCodecVideo, "copy") {
		return false
	}
	// A generation can only be observed while it is live. A line that names a
	// generation the session has already replaced is stale and dropped.
	if generation != s.decodeGeneration {
		return false
	}
	// A confirmed generation is terminal: keep the diagnostic evidence
	// current but never reopen the verdict.
	if s.decodeStage == decodeStageConfirmed {
		s.recordDecodeEvidenceLocked(now, sample)
		return false
	}
	if !s.lastDecodeErrorAt.IsZero() && now.Sub(s.lastDecodeErrorAt) > decodeErrorDecay {
		s.decodeErrorCount = 0
	}
	s.decodeErrorCount++
	s.recordDecodeEvidenceLocked(now, sample)
	if s.decodeStage == decodeStageSuspected {
		return false
	}
	if s.decodeErrorCount < decodeErrorThreshold {
		return false
	}
	s.decodeStage = decodeStageSuspected
	s.decodeSuspectAt = now
	// The session-level hardware latch records that this session's hardware
	// decoder rejected the source; it licenses the reactive software-decode
	// rebuild and is deliberately set at suspicion, not confirmation, so the
	// client's replan can act on it without waiting out the observation window.
	// A software plan has no further decoder to fall back to and never latches.
	if !s.opts.SoftwareVideoDecode {
		s.decodeStamped = true
	}
	s.startDecodeWatchLocked()
	return true
}

// observeDecodeError records one decoder failure line for the generation that is
// current when it is called. It exists for callers that name no generation;
// production stderr flows through observeDecodeErrorLocked with the writer's
// own generation.
func (s *TranscodeSession) observeDecodeError(sample string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observeDecodeErrorLocked(s.decodeGeneration, sample, s.decodeClock())
}

// recordDecodeEvidenceLocked advances the newest-failure timestamp and sample.
// Callers must hold mu.
func (s *TranscodeSession) recordDecodeEvidenceLocked(now time.Time, sample string) {
	s.lastDecodeErrorAt = now
	if strings.TrimSpace(sample) != "" {
		s.decodeSample = sample
	}
}

// startDecodeWatchLocked launches the one evaluator for a suspected generation.
// The watcher is generation-fenced by context: a reset or confirmation cancels
// it, and evaluateDecodeVerdict discards results whose generation moved on.
// Callers must hold mu.
func (s *TranscodeSession) startDecodeWatchLocked() {
	if s.decodeWatchCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.decodeWatchCancel = cancel
	generation := s.decodeGeneration
	go s.watchDecodeSuspicion(ctx, generation)
}

// watchDecodeSuspicion drives the shared evaluator while this generation is
// suspected. Running it here, independent of further stderr, is what makes the
// deadline fire even when decoding goes quiet.
func (s *TranscodeSession) watchDecodeSuspicion(ctx context.Context, generation uint64) {
	ticker := time.NewTicker(decodeWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.evaluateDecodeVerdict()
		}
	}
}

// IsSourceRejected reports whether the running plan's decoder confirmed that
// the source is undecodable. It drives the shared evaluator first so serving,
// waiters, restart, and notification all observe the same lifecycle: a read
// after the observation deadline confirms exactly like the watcher would. The
// serve path treats a confirmed verdict as permanent: once true, the client
// should replan (and, for a hardware plan, get the software-decode variant)
// instead of waiting out its own startup timeout.
//
// A read is cheap and never scans under the mutex; only a suspected generation
// samples progress, through one coalesced generation-scoped probe.
func (s *TranscodeSession) IsSourceRejected() bool {
	if s == nil {
		return false
	}
	s.evaluateDecodeVerdict()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sourceRejected
}

// sourceRejectedLocked reads the confirmed verdict without driving the
// evaluator. It exists for callers already holding mu (restart, notification).
func (s *TranscodeSession) sourceRejectedLocked() bool {
	return s.sourceRejected
}

// evaluateDecodeVerdict is the one shared evaluator for a suspected generation.
// It snapshots state under mu, samples progress outside the mutex, then applies
// the result only if the generation has not moved on. Recovery cancels
// suspicion; expiry confirms it and hands off to notification and bounded
// teardown. It is a no-op while observing or confirmed.
func (s *TranscodeSession) evaluateDecodeVerdict() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.decodeStage != decodeStageSuspected {
		s.mu.Unlock()
		return
	}
	generation := s.decodeGeneration
	outputDir := s.outputDir
	suspectAt := s.decodeSuspectAt
	probe := s.decodeProbe
	videoFrames := s.videoFrames
	s.mu.Unlock()

	recovered, ok := s.runDecodeProbe(generation, outputDir, suspectAt, videoFrames, probe)
	if !ok {
		// Another probe owns the sampling path; a later tick applies its own
		// result. Never block a verdict read behind slow I/O.
		return
	}
	s.applyDecodeVerdict(generation, recovered)
}

// runDecodeProbe samples generation-scoped progress outside the session mutex,
// coalescing concurrent probes: while one probe is in flight, other callers
// return ok=false rather than launching a scan or serializing behind the first.
// A session with no output directory (unit fixtures) reports no recovery so the
// suspicion can still confirm on expiry. videoFrames is the generation's frame
// count snapshot: muxed output never counts as recovery without it.
func (s *TranscodeSession) runDecodeProbe(generation uint64, outputDir string, cutoff time.Time, videoFrames int64, probe func(string, time.Time, int64) bool) (recovered bool, ok bool) {
	if outputDir == "" {
		return false, true
	}
	s.decodeProbeMu.Lock()
	if s.decodeProbeRunning {
		s.decodeProbeMu.Unlock()
		return false, false
	}
	s.decodeProbeRunning = true
	s.decodeProbeMu.Unlock()

	if probe == nil {
		probe = videoProgressAfter
	}
	recovered = probe(outputDir, cutoff, videoFrames)

	s.decodeProbeMu.Lock()
	s.decodeProbeRunning = false
	s.decodeProbeMu.Unlock()
	return recovered, true
}

// applyDecodeVerdict folds a completed probe into the lifecycle, validating the
// generation first so a stale probe can never indict or clear a replacement.
func (s *TranscodeSession) applyDecodeVerdict(generation uint64, recovered bool) {
	s.mu.Lock()
	if generation != s.decodeGeneration || s.decodeStage != decodeStageSuspected {
		s.mu.Unlock()
		return
	}
	if recovered {
		// Qualifying progress retires the episode: strikes, suspicion
		// deadline, and pending notification all clear, so a later isolated
		// error needs fresh evidence.
		s.decodeErrorCount = 0
		s.lastDecodeErrorAt = time.Time{}
		s.decodeStage = decodeStageObserving
		s.decodeSuspectAt = time.Time{}
		cancel := s.decodeWatchCancel
		s.decodeWatchCancel = nil
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	now := s.decodeClock()
	if s.decodeSuspectAt.IsZero() || now.Sub(s.decodeSuspectAt) < decodeObservationWindow {
		s.mu.Unlock()
		return
	}
	s.confirmDecodeRejectionLocked()
	cancel := s.decodeWatchCancel
	notify, notifyOK := s.claimSourceRejectNotificationLocked()
	s.decodeWatchCancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Preserve the typed verdict before teardown: the process exit must not
	// reclassify a revoked generation, and notification must not depend on a
	// client replan or a marker landing. Teardown is server-owned and bounded.
	// The marker was claimed in the same critical section as the verdict, so a
	// reader that sees the confirmed verdict also sees the claim.
	if notifyOK {
		s.invokeSourceRejected(notify)
	}
	go s.reapRejectedGeneration(generation)
}

// decodeRejectNotification carries the identity a confirmed generation's marker
// callback needs, captured under mu so the callback can run detached.
type decodeRejectNotification struct {
	ctx       context.Context
	callback  func(context.Context, int, string) error
	fileID    int
	canonical string
}

// claimSourceRejectNotificationLocked claims the confirmed generation's marker
// exactly once, atomically with confirmation, and returns the callback identity
// to invoke. It reports false when the marker was already claimed or no
// callback is wired. Callers must hold mu.
func (s *TranscodeSession) claimSourceRejectNotificationLocked() (decodeRejectNotification, bool) {
	if !s.sourceRejected || s.sourceRejectNotified {
		return decodeRejectNotification{}, false
	}
	s.sourceRejectNotified = true
	cb := s.opts.OnSourceRejected
	if cb == nil {
		return decodeRejectNotification{}, false
	}
	canonical := strings.TrimSpace(s.opts.CanonicalInputPath)
	if canonical == "" {
		canonical = strings.TrimSpace(s.opts.InputPath)
	}
	return decodeRejectNotification{
		ctx:       context.Background(),
		callback:  cb,
		fileID:    s.opts.MediaFileID,
		canonical: canonical,
	}, true
}

// confirmDecodeRejectionLocked marks the generation confirmed. The
// session-level hardware latch was already set at suspicion entry; a software
// plan never sets it. Callers must hold mu.
func (s *TranscodeSession) confirmDecodeRejectionLocked() {
	s.decodeStage = decodeStageConfirmed
	s.sourceRejected = true
}

// reapRejectedGeneration tears down a confirmed generation's ffmpeg process
// within decodeReapTimeout, with no dependency on a client replan, waiters, or
// the marker callback. It is generation-fenced: a reset that installed a
// replacement first makes this a no-op, so cleanup racing reset cannot kill the
// new process.
func (s *TranscodeSession) reapRejectedGeneration(generation uint64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if generation != s.decodeGeneration || !s.sourceRejected {
		s.mu.Unlock()
		return
	}
	cancel := s.cancel
	done := s.done
	reapTimeout := s.decodeReapTimeout
	if reapTimeout <= 0 {
		reapTimeout = decodeReapTimeout
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(reapTimeout):
		}
	}
}

// videoProgressAfter reports whether the generation both decoded video and
// muxed non-empty segment output newer than cutoff. A segment alone says
// nothing about the decoder: an encode that decoded no frame still writes a
// non-empty segment for every audio packet, so segment existence must never
// cancel a decode suspicion. Manifests, empty files, unparseable names, and a
// generation that has not advanced its frame counter never count. It runs
// outside the session mutex.
func videoProgressAfter(outputDir string, cutoff time.Time, videoFrames int64) bool {
	if videoFrames <= 0 {
		return false
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, err := ParseSegmentNumber(entry.Name()); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() <= 0 {
			continue
		}
		if info.ModTime().After(cutoff) {
			return true
		}
	}
	return false
}

// DecodeErrorHeader names the machine-readable decode verdict on a media
// response that revokes a generation whose decoder rejected the source.
// DecodeErrorSourceRejectedCode is its only value today.
const (
	DecodeErrorHeader             = "X-Vio-Decode-Error"
	DecodeErrorSourceRejectedCode = "source_decode_failed"
)

// IsDecodeFailed reports whether repeated decoder failures stamped this
// session's hardware decoder as unable to handle the source. The handler reads
// it during failure recovery to decide whether to attempt a software-decode
// rebuild at replan.
//
// The evaluator call advances lifecycle processing — a suspected generation
// with no fresh progress still needs a probe sample before it can confirm —
// and shares that one verdict with the serving path. It does not make the
// hardware latch equivalent to current-generation rejection: decodeStamped
// intentionally survives recovery and restart, so a true read stays true even
// when no generation is suspected.
func (s *TranscodeSession) IsDecodeFailed() bool {
	if s == nil {
		return false
	}
	s.evaluateDecodeVerdict()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decodeStamped
}

// DecodeFailureEvidence returns a representative matched stderr line and the
// number of decoder failures counted for this session, for the replan decision
// log and the durable route event. Both are zero for a session that never
// matched a decoder failure.
func (s *TranscodeSession) DecodeFailureEvidence() (string, int) {
	if s == nil {
		return "", 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decodeSample, s.decodeErrorCount
}

// observeDemuxErrorLocked records one input demux failure and reports whether
// it is the occurrence that crosses the known-bad threshold. The counter resets
// when more than demuxErrorDecay elapsed since the previous failure, so blips
// that recover are forgiven. It reports true at most once per session:
// demuxStamped is set under mu before returning, keeping the failure marker
// idempotent when concurrent stderr lines arrive. Callers must hold mu; the
// generation fence belongs to logFFmpegLineForGeneration, which calls this
// inside the same critical section as the check, so a stale line cannot
// misattribute the stamp to a replacement.
func (s *TranscodeSession) observeDemuxErrorLocked(now time.Time) bool {
	if s == nil {
		return false
	}
	if s.demuxStamped {
		return false
	}
	if !s.lastDemuxErrorAt.IsZero() && now.Sub(s.lastDemuxErrorAt) > demuxErrorDecay {
		s.demuxErrorCount = 0
	}
	s.demuxErrorCount++
	s.lastDemuxErrorAt = now
	if s.demuxErrorCount < demuxErrorThreshold {
		return false
	}
	s.demuxStamped = true
	return true
}

// observeDemuxError records one input demux failure for the current session. It
// exists for callers that name no generation; production stderr flows through
// observeDemuxErrorLocked inside the generation-fenced line path.
func (s *TranscodeSession) observeDemuxError(now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observeDemuxErrorLocked(now)
}

// IsDemuxFailed reports whether repeated input demux failures stamped this
// session's source candidate known-bad. Once true, restarts are refused so the
// caller rotates instead of rebuilding the same bad transport.
func (s *TranscodeSession) IsDemuxFailed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.demuxStamped
}

// RecordAsyncRestartFailure latches a terminal detached-restart failure so the
// next segment request answers the real verdict rather than scheduling another
// restart and returning stale_generation a second time. The first failure wins:
// a later failure from a restart racing the same dead generation is not more
// specific, and keeping the earliest preserves the cause the client can act on.
func (s *TranscodeSession) RecordAsyncRestartFailure(err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	if s.asyncRestartErr == nil {
		s.asyncRestartErr = err
	}
	s.mu.Unlock()
}

// AsyncRestartFailure returns the latched terminal failure of a detached
// far-seek restart, or nil when none is pending. Segment handlers consult it
// before their recovery machinery so a dead generation surfaces its own verdict
// instead of a second stale_generation.
func (s *TranscodeSession) AsyncRestartFailure() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asyncRestartErr
}

// setLifecycleLiveness installs the predicate restart consults to confirm this
// session is still the live mapped one. The manager sets it while holding the
// per-session lifecycle lock, so only one restart observes it at a time, and
// clears it when the restart returns. Nil means the session is not managed and
// is treated as alive.
func (s *TranscodeSession) setLifecycleLiveness(fn func() bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lifecycleAlive = fn
	s.mu.Unlock()
}

// lifecycleAliveNow reports whether the installed liveness predicate still
// accepts this session. It copies the predicate under mu and calls it after
// releasing mu, so the predicate may take other locks (the manager's map lock)
// without a lock-order inversion.
func (s *TranscodeSession) lifecycleAliveNow() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	fn := s.lifecycleAlive
	s.mu.Unlock()
	return fn == nil || fn()
}

// notifyDemuxFailure invokes the wiring callback exactly once, off the stderr
// goroutine so a slow persistence write cannot stall FFmpeg's stderr pipe. The
// callback is read under mu alongside the identity it needs, then invoked with
// a detached, bounded context.
func (s *TranscodeSession) notifyDemuxFailure(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	cb := s.opts.OnDemuxFailure
	fileID := s.opts.MediaFileID
	canonical := strings.TrimSpace(s.opts.CanonicalInputPath)
	if canonical == "" {
		canonical = strings.TrimSpace(s.opts.InputPath)
	}
	s.mu.Unlock()
	if cb == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := cb(callCtx, fileID, canonical); err != nil {
			log.Printf("playback: mark virtual candidate failed after repeated demux errors (file_id=%d): %v", fileID, err)
		}
	}()
}

// notifySourceRejected invokes the source-candidate failure callback exactly
// once, when the decoder rejection has been confirmed. It is the decode
// counterpart of notifyDemuxFailure and reuses the same identity (effective
// media file id + durable canonical path) so the embedding handler stamps the
// candidate through the one existing failed_at mechanism. The callback is read
// under mu, then invoked off the evaluator goroutine with a detached, bounded
// context so a slow write cannot stall the verdict. It is a no-op when the
// source was never confirmed or the callback is unset.
//
// It drives the shared evaluator first, exactly like serving, waiters, and
// restart: a suspected generation whose observation deadline has passed is
// confirmed here too, so notification observes the same lifecycle as every
// other verdict read instead of depending on one of them happening to run
// first. Calling it from applyDecodeVerdict after confirmation is harmless —
// the evaluator is a no-op once the stage is confirmed, and confirmation
// already claimed the marker in the same critical section, so this does not
// double-invoke.
func (s *TranscodeSession) notifySourceRejected(ctx context.Context) {
	if s == nil {
		return
	}
	s.evaluateDecodeVerdict()
	s.mu.Lock()
	notify, ok := s.claimSourceRejectNotificationLocked()
	s.mu.Unlock()
	if !ok {
		return
	}
	if ctx != nil {
		notify.ctx = ctx
	}
	s.invokeSourceRejected(notify)
}

// invokeSourceRejected hands a claimed marker to its callback off the evaluator
// goroutine, with a detached, bounded context so a slow persistence write
// cannot stall the verdict. An unset callback is a no-op.
func (s *TranscodeSession) invokeSourceRejected(notify decodeRejectNotification) {
	if notify.callback == nil {
		return
	}
	ctx := notify.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := notify.callback(callCtx, notify.fileID, notify.canonical); err != nil {
			log.Printf("playback: mark virtual candidate failed after decoder rejection (file_id=%d): %v", notify.fileID, err)
		}
	}()
}

func (s *TranscodeSession) logFFmpegEvent(ctx context.Context, message, exitError string) {
	if s == nil || s.opts.FFmpegLogSink == nil {
		return
	}
	s.mu.Lock()
	attrs := s.ffmpegAttrsLocked()
	attrs.ExitError = exitError
	s.mu.Unlock()
	s.opts.FFmpegLogSink.WriteEvent(ctx, s.opts.SessionID, attrs, message)
}

func (s *TranscodeSession) logWaitResult(ctx context.Context, waitErr error, canceled bool) {
	if waitErr == nil {
		s.logFFmpegEvent(ctx, "ffmpeg process exited", "")
		return
	}
	if canceled {
		// Teardown of our own generation: a restart, a switchover, or a stop.
		// The exit status is our kill, not a verdict on the stream, so it stays
		// out of the error-shaped ffmpeg event log that failure-recovery triage
		// reads.
		slog.DebugContext(ctx, "ffmpeg process stopped by session teardown",
			"component", ffmpegComponent,
			"playback_session_id", s.opts.SessionID,
			"exit_error", formatWaitError(waitErr),
		)
		return
	}
	s.logFFmpegEvent(ctx, "ffmpeg process exit error", formatWaitError(waitErr))
}

func (s *TranscodeSession) ffmpegAttrsLocked() FFmpegLogAttrs {
	return FFmpegLogAttrs{
		NodeType:           s.opts.NodeType,
		ExecutionMode:      s.opts.ExecutionMode,
		InputPath:          s.opts.InputPath,
		OutputDir:          s.opts.OutputDir,
		TargetResolution:   s.opts.TargetResolution,
		TargetVideoCodec:   s.opts.TargetCodecVideo,
		TargetAudioCodec:   s.opts.TargetCodecAudio,
		HWAccel:            s.opts.HWAccel,
		SeekSeconds:        s.opts.SeekSeconds,
		StartSegmentNumber: s.opts.StartSegmentNumber,
		RestartCount:       s.restartCount,
		DroppedLines:       s.stderrDroppedLines,
	}
}

func formatWaitError(err error) string {
	if err == nil {
		return ""
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return fmt.Sprintf("exit_code=%d: %v", status.ExitStatus(), err)
		}
	}
	return err.Error()
}
func syntheticManifestQuery(segmentCount int, rawQuery string) (definition, suffix string, minVersion int) {
	if rawQuery == "" {
		return "", "", 0
	}
	legacySuffix := "?" + rawQuery
	variableSuffix := "?{$" + manifestQueryVariable + "}"
	savingsPerSegment := len(legacySuffix) - len(variableSuffix)
	if savingsPerSegment <= 0 || int64(segmentCount)*int64(savingsPerSegment) < minManifestQuerySubstitutionSavings || strings.ContainsAny(rawQuery, "\"\r\n") {
		return "", legacySuffix, 0
	}
	definition = fmt.Sprintf("#EXT-X-DEFINE:NAME=\"%s\",VALUE=\"%s\"\n", manifestQueryVariable, rawQuery)
	return definition, variableSuffix, 8
}

// GetSegment returns the file path of a named segment if it exists.

// SegmentLease binds an opened segment descriptor to the FFmpeg generation it
// came from. Keeping the descriptor open makes it a filesystem-backed lease: a

const DV7ToDV81BitstreamFilter = "filter_units=remove_types=63"
