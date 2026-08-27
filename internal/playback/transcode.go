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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaprobe"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func init() {
	// Register .m4s so http.ServeFile sets the correct Content-Type for
	// fMP4 HLS segments. Go's default MIME database does not include it.
	_ = mime.AddExtensionType(".m4s", "video/mp4")
}

// TranscodeOpts holds configuration for an HLS transcode session.
type TranscodeOpts struct {
	InputPath string
	OutputDir string // e.g., /tmp/silo-transcode/{session_id}/
	// subtitleFilterInputPath is a parser-safe local alias used only by the
	// libass subtitles filter. FFmpeg still opens InputPath as the media input.
	subtitleFilterInputPath string
	// OutputSubdir is the signed, root-relative reconstruction directory. Empty
	// retains the legacy flat {session_id} layout.
	OutputSubdir         string
	TranscodeTransportID string
	SessionID            string
	SourceVideoCodec     string
	SourceVideoProfile   string
	SourceVideoBitDepth  int
	VideoBitstreamFilter string // validated copy-mode BSF, e.g. dovi_rpu=strip=1
	VideoSampleEntry     string // allowlisted copy-HLS sample entry: dvh1 or hvc1
	SeekSeconds          float64
	// StreamOriginSeconds is the keyframe timestamp at which a copy-video
	// stream actually begins. SeekSeconds remains the client-requested -ss so
	// FFmpeg performs exactly one demuxer seek; this origin keeps response and
	// reconstruction timelines aligned with the resulting media pre-roll.
	StreamOriginSeconds float64
	// CopySeekAnchorResolved distinguishes a valid zero-second origin from
	// older/shared recipes that never resolved a copy seek anchor.
	CopySeekAnchorResolved bool
	TargetResolution       string // e.g., 1080p, 720p
	TargetCodecVideo       string // e.g., h264 (or hevc if allowed)
	TargetCodecAudio       string // e.g., aac
	SegmentDuration        int    // seconds, default 6
	StartSegmentNumber     int    // -hls_segment_start_number, default 0
	FFmpegPath             string // optional explicit ffmpeg binary path
	HWAccel                string // auto, qsv, vaapi, nvenc, videotoolbox, none
	HWDevice               string // e.g., /dev/dri/renderD128 (default if empty)
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
	SoftwareVideoDecode        bool
	ToneMapPolicy              tonemap.Policy
	ToneMapMode                tonemap.Mode
	ToneMapSourceKind          tonemap.SourceKind
	ToneMapFilter              string
	ToneMapRecipeVersion       string
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
	SubtitleCodec   string
	AudioTrackIndex int // -1 = default (first track), >= 0 = specific track
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
	NodeType               string
	ExecutionMode          string
	FFmpegLogSink          FFmpegLogSink
}

// DV7ToHDR10BitstreamFilter strips Dolby Vision RPU metadata during a
// copy-mode HLS remux; the enhancement layer is dropped by stream mapping.
const DV7ToHDR10BitstreamFilter = "dovi_rpu=strip=1"

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
)

// TranscodeSession manages a running ffmpeg HLS transcode process.
type TranscodeSession struct {
	cmd                  *exec.Cmd
	cancel               context.CancelFunc
	opts                 TranscodeOpts
	outputDir            string
	running              bool
	restarting           *restartFlight
	waitErr              error
	stderr               *boundedTailBuffer
	mu                   sync.Mutex
	done                 chan struct{} // closed when the monitor goroutine finishes
	stdinPipe            io.WriteCloser
	lastRequestedSegment int
	throttler            *TranscodeThrottler
	stderrLinesLogged    int
	stderrBytesLogged    int
	stderrDroppedLines   int
	stderrCapLogged      bool
	restartCount         int
	stderrLineIndex      int
	stderrWriter         *ffmpegStderrWriter
	restartHook          func(context.Context)
	// generationStartedAt is when the currently-owning ffmpeg process was
	// spawned. Output in the shared directory older than this timestamp was
	// written by a previous generation (or a previous session sharing the
	// directory) and describes media this process has not produced yet.
	generationStartedAt time.Time
	// reserveHWDeviceOnRestart is true when StartTranscode selected and reserved
	// one device from a multi-device QSV/VAAPI setting. Each replacement ffmpeg
	// process reacquires that same concrete device.
	reserveHWDeviceOnRestart bool
}

// NewTranscodeSessionForTest exposes only the output directory needed by tests
// in other packages that exercise the mounted transcode-node media routes.
func NewTranscodeSessionForTest(outputDir string) *TranscodeSession {
	return &TranscodeSession{outputDir: outputDir}
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
	ProducedHead    int
	ProducedCount   int
	LastProducedAt  time.Time
	ManifestModTime time.Time
	// GenerationStartedAt is when the ffmpeg process that currently owns the
	// output directory was spawned. It is the zero time for sessions that never
	// started a process. Output stamped before it belongs to an earlier
	// generation and must not be read as this process's progress.
	GenerationStartedAt  time.Time
	HasManifest          bool
	Running              bool
	Restarting           bool
	StartSegmentNumber   int
	SegmentDuration      int
	LastRequestedSegment int
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

// Large synthetic playlists used to repeat the full access and reconstruction
// query on every segment URI. For a feature-length title that turns a small
// index into several megabytes of duplicate JWT text, delaying both transfer
// and hls.js parsing. HLS variable substitution keeps the query once while
// preserving the exact resolved segment URLs. Keep small playlists on the
// simpler legacy form; below this byte threshold the saving is immaterial.
const minManifestQuerySubstitutionSavings = 64 * 1024

const manifestQueryVariable = "silo_query"

// remountStartOffsetSeconds is a positive, effectively-zero HLS start offset.
// Media3 suppresses live-edge position projection for EVENT playlists only
// when EXT-X-START is positive. Anchoring one millisecond after the generation
// origin keeps an initial start indistinguishable from zero while giving a
// rebuilt MediaSource a stable timeline on which to restore its playhead.
const remountStartOffsetSeconds = 0.001

const maxPersistedFFmpegLines = 2000
const maxPersistedFFmpegBytes = 256 * 1024
const maxPersistedFFmpegChars = 2000

// ManifestStartupTimeout is the maximum wait for FFmpeg's first safe playback
// window before the caller reports a retryable startup timeout.
const ManifestStartupTimeout = 30 * time.Second

const (
	maxSequentialMissingSegments = 2
	activeSegmentWait            = 12 * time.Second
	segmentWaitGrace             = 1500 * time.Millisecond
	maxSegmentWait               = 6 * time.Second
	minSegmentWait               = 3 * time.Second
	minStaleProducedWindow       = 5 * time.Second
)

// StartTranscode launches an ffmpeg process that produces HLS segments.
func StartTranscode(ctx context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
	if !validVideoSampleEntry(opts.VideoSampleEntry) ||
		opts.VideoSampleEntry != "" && !strings.EqualFold(opts.TargetCodecVideo, "copy") {
		return nil, fmt.Errorf("unsupported video sample-entry recipe")
	}
	if opts.VideoBitstreamFilter != "" &&
		(opts.VideoBitstreamFilter != DV7ToHDR10BitstreamFilter || !strings.EqualFold(opts.TargetCodecVideo, "copy")) {
		return nil, fmt.Errorf("unsupported video bitstream filter recipe")
	}
	if opts.SegmentDuration <= 0 {
		opts.SegmentDuration = defaultSegmentDuration
	}
	opts = normalizeTranscodeOptsContext(ctx, opts)
	if err := validateToneMapOpts(opts); err != nil {
		return nil, err
	}
	configuredHWDevices := ParseHWDeviceSet(opts.HWDevice)
	reserveHWDeviceOnRestart := configuredHWDevices.Multi() && hwAccelBalancesRenderDevices(opts.HWAccel)
	// Resolve a multi-device hw_device list to one concrete GPU. Restarts reuse
	// the selected device, but each ffmpeg process owns its own reservation.
	hwDevice, releaseHWDevice := acquireHWDevice(opts.HWDevice, opts.HWAccel, opts.AvoidHWDevice)
	opts.HWDevice = hwDevice
	opts.AvoidHWDevice = ""
	if err := validateToneMapSource(ctx, opts); err != nil {
		releaseHWDevice()
		return nil, err
	}

	// Ensure output directory exists.
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		releaseHWDevice()
		return nil, fmt.Errorf("create output dir: %w", err)
	}
	if err := prepareSubtitleFilterInput(&opts); err != nil {
		releaseHWDevice()
		return nil, err
	}

	// The synchronous source guard above is bounded by the caller's startup
	// context. Once it succeeds, keep the established behavior where the
	// transcode process outlives a disconnected manifest request.
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &TranscodeSession{
		cancel:                   cancel,
		opts:                     opts,
		outputDir:                opts.OutputDir,
		running:                  true,
		done:                     make(chan struct{}),
		stderr:                   newBoundedTailBuffer(stderrTailMaxBytes),
		lastRequestedSegment:     opts.StartSegmentNumber,
		reserveHWDeviceOnRestart: reserveHWDeviceOnRestart,
	}

	args := buildFFmpegArgs(opts)
	bin := opts.FFmpegPath
	if bin == "" {
		bin = ffmpegBinary()
	}

	log.Printf("playback: ffmpeg cmd: %s %s", bin, strings.Join(args, " "))
	s.logFFmpegEvent(ctx, "ffmpeg process starting", "")

	cmd := exec.CommandContext(ctx, bin, args...)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		releaseHWDevice()
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	cmd.Dir = opts.OutputDir
	cmd.Stderr = s.newStderrWriter(ctx)
	cmd.WaitDelay = 3 * time.Second

	// Stamp the generation before the process can write anything, so every file
	// this ffmpeg produces is strictly newer than the stamp.
	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		cancel()
		releaseHWDevice()
		s.logFFmpegEvent(ctx, "ffmpeg process exit error", err.Error())
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	s.cmd = cmd
	s.stdinPipe = stdinPipe
	s.generationStartedAt = startedAt
	s.logFFmpegEvent(ctx, "ffmpeg process started", "")

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
	s.flushStderr(ctx)
	s.mu.Lock()
	s.running = false
	s.waitErr = waitErr
	s.mu.Unlock()
	s.logWaitResult(ctx, waitErr)
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
	return codec, track.Profile, models.NormalizeVideoBitDepth(track.BitDepth, track.PixelFormat, track.Profile)
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
		case transcodeHWQSV, transcodeHWVAAPI:
			expected := tonemap.HardwareFilterVAAPI
			if opts.HWAccel == transcodeHWQSV {
				expected = tonemap.HardwareFilterOpenCL
			}
			if opts.ToneMapFilter != expected {
				return fmt.Errorf("unsupported %s tone-map filter %q", opts.HWAccel, opts.ToneMapFilter)
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
	backend := ResolveHWAccelWithFFmpegContext(ctx, opts.HWAccel, opts.FFmpegPath)
	capabilities, err := tonemap.Probe(ctx, ResolveFFmpegPath(opts.FFmpegPath), backend, opts.HWDevice)
	if err != nil {
		return opts, fmt.Errorf("%w: probe tone-map executor: %w", ErrToneMapExecutorUnavailable, err)
	}
	if !capabilities.Supports(opts.ToneMapMode, opts.ToneMapSourceKind) {
		return opts, fmt.Errorf("tone-map executor is not validated")
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
	if err := opts.ToneMapSourceRevision.ValidatePath(opts.InputPath); err != nil {
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

// buildFFmpegArgs constructs the full ffmpeg argument list from TranscodeOpts.
func buildFFmpegArgs(opts TranscodeOpts) []string {
	opts = normalizeTranscodeOpts(opts)

	isVideoCopy := strings.EqualFold(opts.TargetCodecVideo, "copy")
	isAudioCopy := opts.TargetCodecAudio == "copy"

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
	}

	// Hardware acceleration — skip when copying video (no encoding needed).
	if !isVideoCopy {
		args = appendHWAccelArgs(args, opts)
	}

	// Limit input probing to speed up startup, especially on network storage.
	// -fflags +genpts generates PTS for files with missing timestamps;
	// +fastseek enables faster input seeking (matches Jellyfin).
	args = append(args,
		"-fflags", "+genpts+fastseek",
		"-analyzeduration", "3000000", // 3 seconds (default 5s)
		"-probesize", "5000000", // 5 MB (default 5MB, explicit for clarity)
	)

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
		if opts.VideoBitstreamFilter == DV7ToHDR10BitstreamFilter {
			args = append(args, "-bsf:v", opts.VideoBitstreamFilter)
		}
		switch opts.VideoSampleEntry {
		case VideoSampleEntryDVH1:
			args = append(args, "-tag:v", VideoSampleEntryDVH1, "-strict", "unofficial")
		case VideoSampleEntryHVC1:
			args = append(args, "-tag:v", VideoSampleEntryHVC1)
		}
	} else {
		args = appendVideoArgs(args, opts)
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

	// Subtitle burn-in and resolution scaling — only when encoding video.
	if !isVideoCopy {
		args = appendVideoFilterArgs(args, opts)
		args = appendSegmentBoundaryArgs(args, opts)
	}

	// HLS output options.
	// Codec-copy sessions usually use fMP4 segments — no transmuxing needed in
	// hls.js, which avoids Safari MSE compatibility issues with certain codecs
	// in TS. MPEG-2 video is the exception: Apple consumes it as compatibility
	// HLS, so package it in MPEG-TS while still copying the video stream.
	// Actual transcoding uses MPEG-TS segments to avoid the hls.js endOfStream()
	// race with fMP4 (hls.js #6337).
	var segmentPattern string
	segmentType := "mpegts"
	copyVideoUsesFMP4 := isVideoCopy && !IsMPEG2VideoCodec(opts.SourceVideoCodec)
	if copyVideoUsesFMP4 {
		segmentType = "fmp4"
		segmentPattern = filepath.Join(opts.OutputDir, "seg_%05d.m4s")
	} else {
		segmentPattern = filepath.Join(opts.OutputDir, "seg_%05d.ts")
	}
	manifestPath := filepath.Join(opts.OutputDir, "stream.m3u8")

	args = append(args,
		"-max_muxing_queue_size", "2048",
		"-max_delay", "5000000",
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
	if copyVideoUsesFMP4 {
		args = append(args, "-hls_segment_options", "movflags=+frag_discont")
	}
	if opts.StartSegmentNumber > 0 {
		args = append(args, "-start_number", fmt.Sprintf("%d", opts.StartSegmentNumber))
	}
	args = append(args, manifestPath)

	return args
}

// resolveEffectiveTranscodeHWAccel returns the backend that will actually execute the recipe.
func resolveEffectiveTranscodeHWAccel(opts TranscodeOpts) string {
	return resolveEffectiveTranscodeHWAccelContext(context.Background(), opts)
}

func resolveEffectiveTranscodeHWAccelContext(ctx context.Context, opts TranscodeOpts) string {
	hwAccel := ResolveHWAccelWithFFmpegContext(ctx, opts.HWAccel, opts.FFmpegPath)
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
		if ok, reason := videoToolboxSupportsTargetCodecContext(ctx, opts.FFmpegPath, opts.TargetCodecVideo); !ok {
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
	// The bundled CUDA software-decode upload path has not been validated.
	// Prefer the established libx264 fallback over selecting a decoder known
	// not to accept this source. Intel QSV/VAAPI have the explicit upload paths
	// below and retain hardware encoding.
	if opts.SoftwareVideoDecode && hwAccel == transcodeHWNVENC {
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

// appendStreamSelectionArgs limits output to primary video/audio streams.
// When bitmap burn-in is active the video output comes from the overlay
// filter_complex graph's labeled pad instead of the raw input stream —
// mapping both would emit two video streams into the HLS mux.
func appendStreamSelectionArgs(args []string, opts TranscodeOpts) []string {
	audioTrackIndex := opts.AudioTrackIndex
	if bitmapBurnInActive(opts) {
		args = append(args, "-map", "[vout]")
	} else {
		args = append(args, "-map", "0:v:0")
	}
	if audioTrackIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:a:%d?", audioTrackIndex))
	} else {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args, "-sn")
	args = append(args, "-dn")
	return args
}

// appendTimestampNormalizationArgs selects timestamp handling based on the
// playback mode. Copy-video full-file starts use zero-based timestamps so
// fMP4 fragments always have sane local durations. Copy-video resumes
// preserve source timestamps so each fragment's TFDT matches its playlist
// position (segment K sits at playlist-time K*segDur); zero-basing here
// makes seg_K carry TFDT=0, and strict players (Jellyfin Android TV /
// ExoPlayer) read EXT-X-START, jump to seg_K expecting media at K*segDur,
// see TFDT=0, treat the gap as a discontinuity, reload init.mp4, and
// eventually abort — the symptom that crashes ATV on a second resume.
// Encoded transcodes keep the source-timestamp policy unconditionally.
func appendTimestampNormalizationArgs(args []string, opts TranscodeOpts) []string {
	if strings.EqualFold(opts.TargetCodecVideo, "copy") {
		if opts.SeekSeconds > 0 {
			return append(args,
				"-copyts",
				"-avoid_negative_ts", "disabled",
			)
		}
		return append(args,
			"-avoid_negative_ts", "make_zero",
		)
	}
	return append(args,
		"-copyts",
		"-avoid_negative_ts", "disabled",
	)
}

// appendSegmentBoundaryArgs forces keyframes on segment boundaries so each HLS
// fragment starts cleanly and can be appended independently by the player.
//
// With -copyts, the output timestamp t starts at the seek position rather than
// 0. Subtracting SeekSeconds prevents a "catch-up storm" where n_forced races
// from 0 to seek_position/segment_duration, making every frame an I-frame and
// grinding encoding to a halt for large seeks.
func appendSegmentBoundaryArgs(args []string, opts TranscodeOpts) []string {
	args = append(args, "-sc_threshold", "0")
	if opts.SeekSeconds > 0 {
		args = append(args, "-force_key_frames",
			fmt.Sprintf("expr:gte(t-%.3f,n_forced*%d)", opts.SeekSeconds, opts.SegmentDuration))
	} else {
		args = append(args, "-force_key_frames",
			fmt.Sprintf("expr:gte(t,n_forced*%d)", opts.SegmentDuration))
	}

	// Hardware encoders (QSV, VAAPI, NVENC, VideoToolbox) may not reliably
	// honor force_key_frames expressions. Set explicit GOP size so segment
	// boundaries always start with an intra frame. We assume 30 fps as a
	// safe ceiling — the GOP will be at most segmentDuration * 30 frames.
	// Matches Jellyfin's approach for hardware encoders.
	if opts.HWAccel == transcodeHWQSV || opts.HWAccel == transcodeHWVAAPI ||
		opts.HWAccel == transcodeHWNVENC || opts.HWAccel == transcodeHWVideoToolbox {
		gopSize := fmt.Sprintf("%d", opts.SegmentDuration*30)
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
		args = append(args,
			"-init_hw_device", qsvVAAPIInitDevice(hwDevice),
			"-init_hw_device", "qsv=qs@va",
		)
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
		args = append(args,
			"-init_hw_device", fmt.Sprintf("vaapi=hw:%s", vaapiDevice),
			"-filter_hw_device", "hw",
		)
		if !opts.SoftwareVideoDecode {
			args = append(args, "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi")
		}
	case transcodeHWNVENC:
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

func qsvVAAPIInitDevice(device string) string {
	return fmt.Sprintf("vaapi=va:%s,driver=iHD,kernel_driver=i915,vendor_id=0x8086", device)
}

// videoPreset returns an encoder-compatible preset. CPU encoders use a faster
// fast-start preset for initial playback, while QSV stays on the fastest
// preset family it supports.
func videoPreset(opts TranscodeOpts, hwAccel string) string {
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
	codec := opts.TargetCodecVideo
	if codec == "" {
		codec = transcodeCodecH264
	}

	if codec == "copy" {
		return append(args, "-c:v", "copy")
	}

	preset := videoPreset(opts, opts.HWAccel)
	hasBitrateCap := opts.TargetBitrateKbps > 0

	switch {
	case opts.HWAccel == "qsv" && codec == transcodeCodecH264:
		if hasBitrateCap {
			// VBR mode with bitrate cap instead of global_quality.
			args = append(args, "-c:v", "h264_qsv", "-preset", preset,
				"-b:v", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-maxrate", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-bufsize", fmt.Sprintf("%dk", opts.TargetBitrateKbps*2))
		} else {
			args = append(args, "-c:v", "h264_qsv", "-preset", preset, "-global_quality", "23")
		}
	case opts.HWAccel == "qsv" && codec == transcodeCodecHEVC:
		if hasBitrateCap {
			args = append(args, "-c:v", "hevc_qsv", "-preset", preset,
				"-b:v", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-maxrate", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-bufsize", fmt.Sprintf("%dk", opts.TargetBitrateKbps*2))
		} else {
			args = append(args, "-c:v", "hevc_qsv", "-preset", preset, "-global_quality", "28")
		}
	case opts.HWAccel == "vaapi" && codec == transcodeCodecH264:
		args = append(args, "-c:v", "h264_vaapi", "-qp", "23")
		if hasBitrateCap {
			args = append(args,
				"-maxrate", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-bufsize", fmt.Sprintf("%dk", opts.TargetBitrateKbps*2))
		}
	case opts.HWAccel == "vaapi" && codec == transcodeCodecHEVC:
		args = append(args, "-c:v", "hevc_vaapi", "-qp", "28")
		if hasBitrateCap {
			args = append(args,
				"-maxrate", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-bufsize", fmt.Sprintf("%dk", opts.TargetBitrateKbps*2))
		}
	case opts.HWAccel == transcodeHWNVENC && codec == transcodeCodecH264:
		args = append(args, "-c:v", "h264_nvenc", "-rc:v", "vbr")
		if hasBitrateCap {
			args = append(args,
				"-b:v", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-maxrate", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-bufsize", fmt.Sprintf("%dk", opts.TargetBitrateKbps*2))
		} else {
			args = append(args, "-cq:v", "23", "-b:v", "0")
		}
	case opts.HWAccel == transcodeHWNVENC && codec == transcodeCodecHEVC:
		args = append(args, "-c:v", "hevc_nvenc", "-rc:v", "vbr")
		if hasBitrateCap {
			args = append(args,
				"-b:v", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-maxrate", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-bufsize", fmt.Sprintf("%dk", opts.TargetBitrateKbps*2))
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
		// pix_fmt is left to the input: 10-bit sources encode as p010
		// (HDR10 passthrough), matching the other hardware HEVC paths.
		args = append(args, "-c:v", "hevc_videotoolbox")
		args = appendVideoToolboxRateControl(args, opts)
	default:
		// CPU fallback — match Jellyfin's proven browser-compatible settings.
		// Force yuv420p to ensure 8-bit output (10-bit sources produce High 10
		// Profile which browsers cannot decode via MSE).
		if codec == transcodeCodecHEVC {
			args = append(args, "-c:v", "libx265", "-preset", preset, "-crf", "28", "-pix_fmt", "yuv420p")
		} else {
			args = append(args, "-c:v", "libx264", "-preset", preset, "-crf", "23",
				"-pix_fmt", "yuv420p", "-profile:v", "high", "-level", "4.1")
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
		if opts.ToneMapMode == tonemap.ModeSoftware || opts.HWAccel == transcodeHWVideoToolbox {
			args = append(args, "-colorspace", "bt709")
		}
	}

	return args
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
			return softwareToneMapUploadFilter(opts) + tonemap.QSVFilter(opts.ToneMapSourceKind) + "," + qsvToneMapScaleFilter(opts.TargetResolution) + "," + tonemap.HDRMetadataRemovalFilter()
		case transcodeHWVAAPI:
			return softwareToneMapUploadFilter(opts) + tonemap.VAAPIFilter(opts.ToneMapSourceKind) + "," + vaapiScaleFilter(opts.TargetResolution) + "," + tonemap.HDRMetadataRemovalFilter()
		case transcodeHWNVENC:
			if tonemap.IsSDRSource(opts.ToneMapSourceKind) {
				filter := nvencSDRFallbackDownload(opts) + "," + tonemap.SoftwareFilter(opts.ToneMapSourceKind, "")
				if scale := resolutionToScale(opts.TargetResolution); scale != "" {
					filter += "," + scale
				}
				return filter + ",format=nv12,hwupload_cuda"
			}
			return tonemap.SourceParameters(opts.ToneMapSourceKind) + "," + tonemap.CUDAFilter() + "," + nvencScaleFilter(opts.TargetResolution) + "," + tonemap.HDRMetadataRemovalFilter()
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
		return softwareToneMapUploadFilter(opts) + tonemap.QSVFilter(opts.ToneMapSourceKind) + ",hwdownload,format=nv12," + cpuTail + ",format=nv12,hwupload,hwmap=derive_device=qsv:mode=read+write,format=qsv," + tonemap.HDRMetadataRemovalFilter()
	case transcodeHWVAAPI:
		return softwareToneMapUploadFilter(opts) + tonemap.VAAPIFilter(opts.ToneMapSourceKind) + ",hwdownload,format=nv12," + cpuTail + ",format=nv12,hwupload," + tonemap.HDRMetadataRemovalFilter()
	case transcodeHWNVENC:
		if tonemap.IsSDRSource(opts.ToneMapSourceKind) {
			return nvencSDRFallbackDownload(opts) + "," + tonemap.SoftwareFilter(opts.ToneMapSourceKind, "") + "," + cpuTail + ",format=nv12,hwupload_cuda"
		}
		return tonemap.SourceParameters(opts.ToneMapSourceKind) + "," + tonemap.CUDAFilter() + ",hwdownload,format=nv12," + cpuTail + ",format=nv12,hwupload_cuda," + tonemap.HDRMetadataRemovalFilter()
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
		graph = "[0:v:0]" + softwareToneMapUploadFilter(opts) + tonemap.QSVFilter(opts.ToneMapSourceKind) + "[vmain];" +
			subInput + "format=bgra,hwupload[sub];[vmain][sub]overlay_vaapi=eof_action=pass," +
			qsvToneMapScaleFilter(opts.TargetResolution) + "," + tonemap.HDRMetadataRemovalFilter() + "[vout]"
	case transcodeHWVAAPI:
		graph = "[0:v:0]" + softwareToneMapUploadFilter(opts) + tonemap.VAAPIFilter(opts.ToneMapSourceKind) + "[vmain];" +
			subInput + "format=bgra,hwupload[sub];[vmain][sub]overlay_vaapi=eof_action=pass," +
			vaapiScaleFilter(opts.TargetResolution) + "," + tonemap.HDRMetadataRemovalFilter() + "[vout]"
	case transcodeHWNVENC:
		if tonemap.IsSDRSource(opts.ToneMapSourceKind) {
			graph = "[0:v:0]" + nvencSDRFallbackDownload(opts) + "," + tonemap.SoftwareFilter(opts.ToneMapSourceKind, "") + "[vmain];[vmain]" + subInput + "overlay=eof_action=pass"
		} else {
			graph = "[0:v:0]" + tonemap.SourceParameters(opts.ToneMapSourceKind) + "," + tonemap.CUDAFilter() + ",hwdownload,format=nv12[vmain];[vmain]" +
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

// softwareToneMapUploadFilter moves CPU-decoded frames onto the configured
// Intel or VAAPI device before invoking the selected hardware tone-map graph.
func softwareToneMapUploadFilter(opts TranscodeOpts) string {
	if opts.SoftwareVideoDecode && (opts.HWAccel == transcodeHWQSV || opts.HWAccel == transcodeHWVAAPI) {
		format := "nv12"
		if !tonemap.IsSDRSource(opts.ToneMapSourceKind) {
			format = "p010le"
		}
		return "format=" + format + ",hwupload,"
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
	switch resolution {
	case transcodeResolution2160p:
		return "-2", "2160"
	case transcodeResolution1080p:
		return "-2", "1080"
	case transcodeResolution720p:
		return "-2", "720"
	case transcodeResolution480p:
		return "-2", "480"
	case transcodeResolution420p:
		return "-2", "420"
	case transcodeResolution328p:
		return "-2", "328"
	default:
		return "iw", "ih"
	}
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

const stereoDownmixBoostFilterV3 = "aresample=out_chlayout=stereo,alimiter=level_in=2:limit=0.794328235:attack=5:release=50:level=false:latency=true"

// appendStereoDownmixBoostArgs applies the playback downmix policy only after
// the source is explicitly rematrixed to stereo. The order matters: limiting
// the source channels before FFmpeg sums them would still allow the final
// stereo signal to clip. The limiter's input gain is +6.0206 dB; its -2 dBFS
// sample ceiling leaves headroom for lossy-codec and inter-sample overshoot.
func appendStereoDownmixBoostArgs(args []string, sourceChannels, outputChannels int) []string {
	if sourceChannels <= 2 || outputChannels != 2 {
		return args
	}
	return append(args, "-af", stereoDownmixBoostFilterV3)
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
		args = append(args, "-c:a", "copy")
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
		channels, bitrateKbps := resolvedAACOutputV3(opts.TargetAudioChannels, opts.TargetAudioBitrateKbps)
		args = append(args, "-c:a", "aac", "-b:a", strconv.Itoa(bitrateKbps)+"k", "-ac", strconv.Itoa(channels))
		if IsAudioToAACStereoDownmixV3(opts.SourceAudioChannels, opts.TargetCodecAudio, opts.TargetAudioChannels) {
			args = appendStereoDownmixBoostArgs(args, opts.SourceAudioChannels, channels)
		}
	}

	return args
}

func resolvedAACOutputV3(targetChannels, targetBitrateKbps int) (int, int) {
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
		// VAAPI video surface, scale, then map to QSV for the encoder. The scale
		// helper already appends the hwmap=derive_device=qsv tail.
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
		if opts.HWAccel == transcodeHWNVENC {
			// Download to CPU for the overlay, then re-upload to CUDA.
			graph = "[0:v:0]hwdownload,format=yuv420p[vmain];[vmain]" + cpuFilters +
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
// then re-uploaded: hwdownload → format=yuv420p → [scale,] subtitles → hwupload → hwmap.
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
		// VAAPI→QSV pipeline: download from VAAPI surface to CPU, apply subtitle
		// and scale filters, convert to nv12 (required by hwupload for VAAPI
		// surfaces), upload back to VAAPI, then map to QSV for the encoder.
		vf := "hwdownload,format=yuv420p," + cpuFilters + ",format=nv12,hwupload,hwmap=derive_device=qsv,format=qsv"
		args = append(args, "-vf", vf)
	case "vaapi":
		if opts.SoftwareVideoDecode {
			vf := "format=yuv420p," + cpuFilters + ",format=nv12,hwupload"
			return append(args, "-vf", vf)
		}
		// VAAPI-only: download, apply CPU filters, convert to nv12, upload back.
		vf := "hwdownload,format=yuv420p," + cpuFilters + ",format=nv12,hwupload"
		args = append(args, "-vf", vf)
	case transcodeHWNVENC:
		// NVENC/CUDA: download to CPU for subtitle rendering, then upload back.
		vf := "hwdownload,format=yuv420p," + cpuFilters + ",format=nv12,hwupload_cuda"
		args = append(args, "-vf", vf)
	default:
		// CPU encoding: filters run directly on decoded frames.
		args = append(args, "-vf", cpuFilters)
	}

	return args
}

// resolutionToScale returns an ffmpeg scale filter string for the target resolution.
func resolutionToScale(res string) string {
	switch res {
	case "2160p":
		return "scale=-2:2160"
	case "1080p":
		return "scale=-2:1080"
	case "720p":
		return "scale=-2:720"
	case "480p":
		return "scale=-2:480"
	case transcodeResolution420p:
		return "scale=-2:420"
	case transcodeResolution328p:
		return "scale=-2:328"
	default:
		return ""
	}
}

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
		return "scale_vaapi=w=-2:h=2160:format=nv12," + hwmap + ",format=qsv"
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
		return "scale_vaapi=format=nv12," + hwmap + ",format=qsv"
	}
}

// qsvToneMapScaleFilter maps VAAPI tone-map output with read/write access. The default
// read-only mapping succeeds for synthetic upload probes but fails against
// real decoded HEVC surfaces on Intel with ENOSYS during the first frame.
func qsvToneMapScaleFilter(res string) string {
	return qsvScaleFilterWithMapMode(res, "read+write")
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
		return "scale_vaapi=w=-2:h=2160:format=nv12"
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
		return "scale_vaapi=format=nv12"
	}
}

func vaapiSoftwareDecodeFilter(res string) string {
	cpuFilters := ""
	if scale := resolutionToScale(res); scale != "" {
		cpuFilters = scale + ","
	}
	return cpuFilters + "format=nv12,hwupload"
}

func nvencScaleFilter(res string) string {
	switch res {
	case "2160p":
		return "scale_cuda=w=-2:h=2160:format=nv12"
	case "1080p":
		return "scale_cuda=w=-2:h=1080:format=nv12"
	case "720p":
		return "scale_cuda=w=-2:h=720:format=nv12"
	case "480p":
		return "scale_cuda=w=-2:h=480:format=nv12"
	case transcodeResolution420p:
		return "scale_cuda=w=-2:h=420:format=nv12"
	case transcodeResolution328p:
		return "scale_cuda=w=-2:h=328:format=nv12"
	default:
		return "scale_cuda=format=nv12"
	}
}

// filterPathReplacer escapes special characters in file paths for ffmpeg filter syntax.
var filterPathReplacer = strings.NewReplacer(
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
func prepareSubtitleFilterInput(opts *TranscodeOpts) error {
	if !opts.SubtitleBurnIn || opts.SubtitleTrackIndex < 0 || NeedsBurnIn(opts.SubtitleCodec) {
		return nil
	}

	aliasPath := filepath.Join(opts.OutputDir, subtitleFilterAliasName)
	target, err := os.Readlink(aliasPath)
	switch {
	case err == nil:
		if target != opts.InputPath {
			return fmt.Errorf("prepare subtitle filter input: alias targets unexpected source")
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.Symlink(opts.InputPath, aliasPath); err != nil {
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
const minCopyManifestSegments = 2

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
		case transcodeHWQSV, transcodeHWVAAPI, transcodeHWNVENC:
			if bitmapBurnInActive(opts) {
				return 1
			}
			return minFreshHardwareManifestSegments
		}
	}
	return minManifestSegments
}

// GetManifest returns the HLS m3u8 manifest content.
// It returns ErrManifestNotReady if the manifest does not yet contain enough
// segments for reliable HLS playback (see minManifestSegments).
func (s *TranscodeSession) GetManifest() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	manifestPath := filepath.Join(s.outputDir, "stream.m3u8")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			if !s.running {
				if s.restarting != nil {
					return nil, ErrManifestNotReady
				}
				if s.waitErr != nil {
					stderr := truncateStderr(s.stderr.String())
					if stderr != "" {
						return nil, fmt.Errorf("%w: %v (stderr: %s)", ErrTranscodeFailed, s.waitErr, stderr)
					}
					return nil, fmt.Errorf("%w: %v", ErrTranscodeFailed, s.waitErr)
				}
				return nil, ErrTranscodeFailed
			}
			return nil, ErrManifestNotReady
		}
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	requiredSegments := startupSegmentRequirement(s.opts)

	// Wait until enough startup media exists before serving. Counting #EXTINF
	// lines alone is not enough for FFmpeg's live-written manifest because the
	// playlist can reference copy-mode segments before the files are fully
	// flushed to disk, especially on resumed sessions with a non-zero media
	// sequence. Requiring the referenced startup files prevents the browser from
	// stalling on its very first segment fetch.
	if s.running && !startupFilesReady(data, s.outputDir, requiredSegments) {
		return nil, ErrManifestNotReady
	}
	if strings.EqualFold(s.opts.TargetCodecVideo, "copy") {
		if err := validateCopyPlaybackManifest(data); err != nil {
			return nil, fmt.Errorf("invalid copy playback manifest: %w", err)
		}
	}

	return data, nil
}

// WaitForManifest polls until the manifest is ready for playback or the timeout
// expires. It keeps the initial request open long enough for FFmpeg to write
// the first safe playback window instead of forcing the client to race a 503.
func (s *TranscodeSession) WaitForManifest(timeout time.Duration) ([]byte, error) {
	deadline := time.After(timeout)
	for {
		manifest, err := s.GetManifest()
		if err == nil {
			return manifest, nil
		}
		if err != nil && err != ErrManifestNotReady {
			return nil, err
		}

		select {
		case <-deadline:
			return nil, s.manifestTimeoutError(timeout)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// BuildPlaybackManifest returns the manifest we should expose to clients.
//
// Copy-video sessions always expose FFmpeg's real manifest so the playlist
// timing matches the variable-length fragments FFmpeg actually writes and the
// seekable window reflects what FFmpeg has produced so far. Encoded transcodes
// use the synthetic full VOD manifest only while its segment count is bounded;
// longer media uses FFmpeg's real sliding playlist.
func (s *TranscodeSession) BuildPlaybackManifest(segPrefix, rawQuery string) ([]byte, error) {
	opts := s.Opts()
	if strings.EqualFold(opts.TargetCodecVideo, "copy") ||
		!CanGenerateSyntheticManifest(opts.TotalDuration, opts.SegmentDuration) {
		// Copy-video, unknown-duration, or oversized sessions must use FFmpeg's
		// real manifest.
		manifest, err := s.WaitForManifest(ManifestStartupTimeout)
		if err != nil {
			return nil, err
		}
		manifest = stabilizeCopyHLSRemountTimeline(manifest, opts)
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
	manifest, err := s.BuildPlaybackManifest(segPrefix, rawQuery)
	if err != nil {
		return nil, err
	}
	opts := s.Opts()
	usesRealManifest := strings.EqualFold(opts.TargetCodecVideo, "copy") ||
		!CanGenerateSyntheticManifest(opts.TotalDuration, opts.SegmentDuration)
	if opts.SeekSeconds <= 0 || !usesRealManifest {
		return manifest, nil
	}

	gapURI := segPrefix + "source_timeline_gap" + hlsSegmentExtension(opts)
	if rawQuery != "" {
		gapURI += "?" + rawQuery
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
	if gapDuration <= 0 {
		return manifest, nil
	}
	if gapURI == "" {
		gapURI = "source_timeline_gap" + hlsSegmentExtension(opts)
	}

	lines := bytes.Split(manifest, []byte("\n"))
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

func manifestStartupFiles(manifest []byte, maxSegments int) ([]string, int) {
	files := make([]string, 0, maxSegments+1)
	segmentCount := 0

	for line := range bytes.SplitSeq(manifest, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if bytes.HasPrefix(trimmed, []byte("#EXT-X-MAP:")) {
			if uri := extractMapURI(trimmed); uri != "" {
				files = append(files, manifestURIToFilename(uri))
			}
			continue
		}
		if trimmed[0] == '#' {
			continue
		}

		files = append(files, manifestURIToFilename(string(trimmed)))
		segmentCount++
		if segmentCount >= maxSegments {
			break
		}
	}

	return files, segmentCount
}

func startupFilesReady(manifest []byte, outputDir string, requiredSegments int) bool {
	files, segmentCount := manifestStartupFiles(manifest, requiredSegments)
	if segmentCount < requiredSegments {
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
	if strings.EqualFold(opts.TargetCodecVideo, "copy") && !IsMPEG2VideoCodec(opts.SourceVideoCodec) {
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
		ProducedHead:         opts.StartSegmentNumber - 1,
		Running:              s.running,
		Restarting:           s.restarting != nil,
		StartSegmentNumber:   opts.StartSegmentNumber,
		SegmentDuration:      opts.SegmentDuration,
		LastRequestedSegment: s.lastRequestedSegment,
		GenerationStartedAt:  s.generationStartedAt,
	}
	s.mu.Unlock()

	if progress.SegmentDuration <= 0 {
		progress.SegmentDuration = defaultSegmentDuration
	}

	manifestPath := filepath.Join(s.outputDir, "stream.m3u8")
	manifestInfo, statErr := os.Stat(manifestPath)
	if statErr != nil {
		return progress
	}
	progress.HasManifest = true
	progress.ManifestModTime = manifestInfo.ModTime()

	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return progress
	}
	timeline, err := parseManifestTimeline(manifest)
	if err != nil {
		return progress
	}

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
	}

	return progress
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
		decision.Reason = "transcode_restarting"
	case !progress.Running:
		decision.Reason = "transcode_not_running"
	case segNum < progress.StartSegmentNumber:
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
		decision.Reason = "request_beyond_produced_window"
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
	opts := s.Opts()
	totalDur := opts.TotalDuration
	segDur := opts.SegmentDuration
	if segDur <= 0 {
		segDur = defaultSegmentDuration
	}
	if totalDur <= 0 {
		totalDur = float64(segDur) // fallback: single segment
	}

	segCount := int(math.Ceil(totalDur / float64(segDur)))
	if segCount < 1 {
		segCount = 1
	}

	queryDefinition, suffix, queryVersion := syntheticManifestQuery(segCount, rawQuery)

	segExt := hlsSegmentExtension(opts)
	hlsVersion := 3
	if segExt == ".m4s" {
		hlsVersion = 7
	}
	if queryVersion > hlsVersion {
		hlsVersion = queryVersion
	}

	var buf bytes.Buffer
	buf.WriteString("#EXTM3U\n")
	buf.WriteString(fmt.Sprintf("#EXT-X-VERSION:%d\n", hlsVersion))
	buf.WriteString(queryDefinition)
	buf.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", segDur))
	buf.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	buf.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")

	if segExt == ".m4s" {
		buf.WriteString(fmt.Sprintf("#EXT-X-MAP:URI=\"%sinit.mp4%s\"\n", segPrefix, suffix))
	}

	for i := range segCount {
		dur := float64(segDur)
		if i == segCount-1 {
			// Last segment covers the remainder.
			dur = totalDur - float64(i)*float64(segDur)
			if dur <= 0 {
				dur = float64(segDur)
			}
		}
		buf.WriteString(fmt.Sprintf("#EXTINF:%.6f,\n", dur))
		buf.WriteString(fmt.Sprintf("%sseg_%05d%s%s\n", segPrefix, i, segExt, suffix))
	}

	buf.WriteString("#EXT-X-ENDLIST\n")
	return buf.Bytes()
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

// Close terminates the ffmpeg process and removes the temporary output directory.
func (s *TranscodeSession) Close() error {
	return s.shutdown(true)
}

// CloseProcess terminates the ffmpeg process but leaves the output directory in
// place. It is used when another session owns the same output directory (e.g. a
// concurrent reconstruct race loser): tearing down this duplicate must not wipe
// the segments and init.mp4 the winning session is actively serving.
func (s *TranscodeSession) CloseProcess() error {
	return s.shutdown(false)
}

// shutdown kills the ffmpeg process and, when removeOutput is true, removes the
// temporary output directory.
func (s *TranscodeSession) shutdown(removeOutput bool) error {
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

	s.mu.Lock()
	defer s.mu.Unlock()

	s.running = false

	// Clean up temporary directory.
	if removeOutput && s.outputDir != "" {
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
			os.Remove(filepath.Join(s.outputDir, name))
			continue
		}
		if name == "init.mp4" {
			continue
		}
		segNum, parseErr := ParseSegmentNumber(name)
		if parseErr != nil {
			continue
		}
		if segNum >= startSegment {
			os.Remove(filepath.Join(s.outputDir, name))
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
}

func emittedRecipeOf(opts TranscodeOpts) emittedStreamRecipe {
	return emittedStreamRecipe{
		videoCodec:      strings.ToLower(strings.TrimSpace(opts.TargetCodecVideo)),
		bitstreamFilter: strings.TrimSpace(opts.VideoBitstreamFilter),
		toneMapMode:     opts.ToneMapMode,
		toneMapFilter:   strings.TrimSpace(opts.ToneMapFilter),
		hwAccel:         strings.ToLower(strings.TrimSpace(opts.HWAccel)),
	}
}

// cleanStaleOutputForRestart removes output the replacement generation must not
// inherit. Old segments are only reusable when the previous and next generation
// emit the same recipe: a recipe change (copy to an encoded target, a tone-map
// mode or filter switch, a different hardware backend) leaves both
// wrong-generation segments at or after the restart point and a manifest
// describing a stream the new process will never produce. Serving either mixes
// generations, and reading the stale manifest as produced progress makes the
// throttler pause a process that has not produced anything yet.
//
// Copy-mode restarts always clean, recipe change or not: a copy generation's
// segment boundaries follow source keyframes, so a re-seek re-cuts the timeline
// even when the recipe is identical.
//
// It reports whether it cleaned, so callers can log the decision.
func (s *TranscodeSession) cleanStaleOutputForRestart(previous, next TranscodeOpts, startSegment int) bool {
	if !strings.EqualFold(next.TargetCodecVideo, "copy") && emittedRecipeOf(previous) == emittedRecipeOf(next) {
		return false
	}
	s.cleanStaleSegments(startSegment)
	return true
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
	s.mu.Lock()
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

	// Kill current process without removing output directory.
	if cancelCurrent != nil {
		cancelCurrent()
	}
	if done != nil {
		<-done
	}

	s.mu.Lock()
	s.running = false
	s.waitErr = nil
	if s.stderr != nil {
		s.stderr.Reset()
	}
	s.restartCount++
	reserveHWDevice := s.reserveHWDeviceOnRestart
	s.mu.Unlock()

	previousOpts := opts

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

	// Old segments are only reusable when this generation emits the same recipe
	// as the previous one; otherwise they, and the manifest describing them,
	// have to go before the replacement process starts.
	if s.cleanStaleOutputForRestart(previousOpts, opts, startSegment) {
		log.Printf("playback: cleaned stale transcode output at/after segment %d before restart (video %q -> %q)",
			startSegment, previousOpts.TargetCodecVideo, opts.TargetCodecVideo)
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
	cmd := exec.CommandContext(ctx, bin, args...)
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

	// The previous process released its reservation before closing done. Keep
	// this session on the same concrete GPU while accounting for the replacement
	// process as a new active workload.
	releaseHWDevice := func() {}
	if reserveHWDevice {
		releaseHWDevice = reserveConcreteHWDevice(opts.HWDevice)
	}

	// As in StartTranscode, stamp the generation before the process can write.
	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
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
		s.stdinPipe.Close()
	}
	s.cmd = cmd
	s.cancel = cancel
	s.opts = opts
	s.running = true
	s.restarting = nil
	s.stdinPipe = stdinPipe
	s.lastRequestedSegment = startSegment
	s.generationStartedAt = startedAt
	s.done = make(chan struct{})
	hook := s.restartHook
	s.mu.Unlock()
	close(flight.done)

	go s.monitorFFmpeg(ctx, cmd, s.done, releaseHWDevice)

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
		s.mu.Unlock()

		if restarting {
			select {
			case <-deadline:
				return "", ErrSegmentNotFound
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}

		if !running && waitErr != nil {
			return "", fmt.Errorf("%w: %v", ErrTranscodeFailed, waitErr)
		}
		// If ffmpeg finished cleanly but the segment doesn't exist,
		// it won't appear later — fail fast.
		if !running {
			return "", ErrSegmentNotFound
		}

		select {
		case <-deadline:
			return "", ErrSegmentNotFound
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// RewriteManifestPaths prefixes relative segment references in an HLS manifest
// with segPrefix (e.g. "segment/") and optionally appends rawQuery as a query
// string. This ensures the HLS player's segment requests match server routes
// and preserve any auth or cache-busting parameters from the manifest URL.
func RewriteManifestPaths(manifest []byte, segPrefix, rawQuery string) ([]byte, error) {
	if err := validateManifestHeader(manifest); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	var suffix string
	if rawQuery != "" {
		suffix = "?" + rawQuery
	}

	lines := bytes.Split(manifest, []byte("\n"))
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
// segment and #EXT-X-MAP init URI in an HLS media playlist, preserving any query
// the URI already carries (using "?" or "&" as appropriate). The value is
// appended verbatim — callers must supply a URL-safe value (a signed stream token
// is base64url and safe). A manifest without a valid #EXTM3U header is returned
// unchanged so a non-manifest body is never corrupted.
//
// It exists for the API proxy boundary: a transcode node builds its manifest from
// the forwarded request query, which deliberately omits the signed stream token
// ("st") so the token never reaches the node URL or its logs. That leaves the
// node-built segment URIs token-less, so a later segment fetch after a node/API
// restart cannot reconstruct the session. Re-injecting the client-facing token
// here keeps reconstruction working without ever exposing it to the node.
func AppendManifestQueryParam(manifest []byte, key, value string) []byte {
	if key == "" || validateManifestHeader(manifest) != nil {
		return manifest
	}
	param := key + "=" + value
	lines := bytes.Split(manifest, []byte("\n"))
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if bytes.HasPrefix(trimmed, []byte("#EXT-X-MAP:")) {
			lines[i] = appendMapURIQueryParam(trimmed, param)
			continue
		}
		if trimmed[0] == '#' {
			continue
		}
		lines[i] = appendURIQueryParam(trimmed, param)
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
		return fmt.Errorf("%w after %s: ffmpeg exited: %v (stderr: %s)", ErrManifestNotReady, timeout, waitErr, stderr)
	case waitErr != nil:
		return fmt.Errorf("%w after %s: ffmpeg exited: %v", ErrManifestNotReady, timeout, waitErr)
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

// SegmentStartTime reports the source-timeline start time of the requested
// segment using the current on-disk manifest. The bool return is false when
// the segment is not present in the manifest yet.
func (s *TranscodeSession) SegmentStartTime(segNum int) (float64, bool, error) {
	s.mu.Lock()
	manifestPath := filepath.Join(s.outputDir, "stream.m3u8")
	baseSeekSeconds := s.opts.SeekSeconds
	if strings.EqualFold(s.opts.TargetCodecVideo, "copy") && s.opts.CopySeekAnchorResolved {
		baseSeekSeconds = s.opts.StreamOriginSeconds
	}
	s.mu.Unlock()

	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, ErrManifestNotReady
		}
		return 0, false, fmt.Errorf("read manifest: %w", err)
	}

	timeline, err := parseManifestTimeline(manifest)
	if err != nil {
		return 0, false, fmt.Errorf("parse manifest timeline: %w", err)
	}

	if len(timeline.entries) == 0 {
		return 0, false, ErrManifestNotReady
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
		return 0, false, nil
	}

	segDuration := defaultSegmentDuration
	if opts := s.Opts(); opts.SegmentDuration > 0 {
		segDuration = opts.SegmentDuration
	}
	return float64(segNum * segDuration), true, nil
}

// ReportSegmentDownloaded records that the client has downloaded the given
// segment number. Only updates if segNum exceeds the current high-water mark.
func (s *TranscodeSession) ReportSegmentDownloaded(segNum int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if segNum > s.lastRequestedSegment {
		s.lastRequestedSegment = segNum
	}
}

// LastRequestedSegment returns the highest segment number downloaded by the client.
func (s *TranscodeSession) LastRequestedSegment() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRequestedSegment
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
	partial []byte
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
		w.session.logFFmpegLine(w.ctx, line)
		w.partial = append([]byte(nil), w.partial[idx+1:]...)
	}
	return len(p), nil
}

func (w *ffmpegStderrWriter) Flush() {
	if len(w.partial) == 0 {
		return
	}
	w.session.logFFmpegLine(w.ctx, strings.TrimRight(string(w.partial), "\r"))
	w.partial = nil
}

func (s *TranscodeSession) newStderrWriter(ctx context.Context) io.Writer {
	lineWriter := &ffmpegStderrWriter{session: s, ctx: ctx}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stderr == nil {
		s.stderr = newBoundedTailBuffer(stderrTailMaxBytes)
	}
	s.stderrWriter = lineWriter
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

func (s *TranscodeSession) logFFmpegLine(ctx context.Context, line string) {
	if s == nil || s.opts.FFmpegLogSink == nil {
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

	s.mu.Lock()
	defer s.mu.Unlock()

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

func (s *TranscodeSession) logWaitResult(ctx context.Context, waitErr error) {
	if waitErr == nil {
		s.logFFmpegEvent(ctx, "ffmpeg process exited", "")
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
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return fmt.Sprintf("exit_code=%d: %v", status.ExitStatus(), err)
		}
	}
	return err.Error()
}
