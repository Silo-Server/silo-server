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
)

func init() {
	// Register .m4s so http.ServeFile sets the correct Content-Type for
	// fMP4 HLS segments. Go's default MIME database does not include it.
	_ = mime.AddExtensionType(".m4s", "video/mp4")
}

// TranscodeOpts holds configuration for an HLS transcode session.
type TranscodeOpts struct {
	InputPath          string
	OutputDir          string // e.g., /tmp/silo-transcode/{session_id}/
	SessionID          string
	SourceVideoCodec   string
	SeekSeconds        float64
	TargetResolution   string // e.g., 1080p, 720p
	TargetCodecVideo   string // e.g., h264 (or hevc if allowed)
	TargetCodecAudio   string // e.g., aac
	SegmentDuration    int    // seconds, default 6
	StartSegmentNumber int    // -hls_segment_start_number, default 0
	FFmpegPath         string // optional explicit ffmpeg binary path
	HWAccel            string // auto, qsv, vaapi, none
	HWDevice           string // e.g., /dev/dri/renderD128 (default if empty)
	SubtitleTrackIndex int    // -1 = no subtitles
	SubtitleBurnIn     bool
	AudioTrackIndex    int     // -1 = default (first track), >= 0 = specific track
	TargetBitrateKbps  int     // max video bitrate in kbps; 0 = CRF-only (no cap)
	TotalDuration      float64 // total media duration in seconds (for VOD manifest)
	FastStart          bool    // use superfast preset for faster first-segment production
	NodeType           string
	ExecutionMode      string
	FFmpegLogSink      FFmpegLogSink
}

// TranscodeSession manages a running ffmpeg HLS transcode process.
type TranscodeSession struct {
	cmd                  *exec.Cmd
	cancel               context.CancelFunc
	opts                 TranscodeOpts
	outputDir            string
	running              bool
	restarting           bool
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
}

// SegmentProgress describes the media ffmpeg has actually produced on disk.
type SegmentProgress struct {
	ProducedHead         int
	ProducedCount        int
	LastProducedAt       time.Time
	ManifestModTime      time.Time
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
const maxPersistedFFmpegLines = 2000
const maxPersistedFFmpegBytes = 256 * 1024
const maxPersistedFFmpegChars = 2000

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
	if opts.SegmentDuration <= 0 {
		opts.SegmentDuration = defaultSegmentDuration
	}
	opts.HWAccel = resolveEffectiveTranscodeHWAccel(opts)

	// Ensure output directory exists.
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	s := &TranscodeSession{
		cancel:               cancel,
		opts:                 opts,
		outputDir:            opts.OutputDir,
		running:              true,
		done:                 make(chan struct{}),
		stderr:               newBoundedTailBuffer(stderrTailMaxBytes),
		lastRequestedSegment: opts.StartSegmentNumber,
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
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	cmd.Dir = opts.OutputDir
	cmd.Stderr = s.newStderrWriter(ctx)
	cmd.WaitDelay = 3 * time.Second

	if err := cmd.Start(); err != nil {
		cancel()
		s.logFFmpegEvent(ctx, "ffmpeg process exit error", err.Error())
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	s.cmd = cmd
	s.stdinPipe = stdinPipe
	s.logFFmpegEvent(ctx, "ffmpeg process started", "")

	// Monitor ffmpeg in background.
	go func() {
		waitErr := cmd.Wait()
		s.flushStderr(ctx)
		s.mu.Lock()
		s.running = false
		s.waitErr = waitErr
		s.mu.Unlock()
		s.logWaitResult(ctx, waitErr)
		close(s.done)
	}()

	return s, nil
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

// buildFFmpegArgs constructs the full ffmpeg argument list from TranscodeOpts.
func buildFFmpegArgs(opts TranscodeOpts) []string {
	// Resolve "auto" into a concrete accel method once so all downstream
	// helpers (appendHWAccelArgs, appendVideoArgs, etc.) see the real value.
	opts.HWAccel = resolveEffectiveTranscodeHWAccel(opts)

	isVideoCopy := opts.TargetCodecVideo == "copy"
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
	args = appendStreamSelectionArgs(args, opts.AudioTrackIndex)
	args = appendTimestampNormalizationArgs(args, opts)

	// Video codec and encoding settings.
	if isVideoCopy {
		args = append(args, "-c:v", "copy")
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
	// When burn-in is active, the subtitle filter chain includes scaling
	// (and hw download/upload for QSV/VAAPI). Otherwise, standalone scaling.
	if !isVideoCopy {
		if opts.SubtitleBurnIn && opts.SubtitleTrackIndex >= 0 {
			args = appendSubtitleBurnInArgs(args, opts)
		} else if opts.HWAccel == "qsv" {
			scale := qsvScaleFilter(opts.TargetResolution)
			args = append(args, "-vf", scale)
		} else if opts.HWAccel == "vaapi" {
			scale := vaapiScaleFilter(opts.TargetResolution)
			args = append(args, "-vf", scale)
		} else if opts.TargetResolution != "" {
			scale := resolutionToScale(opts.TargetResolution)
			if scale != "" {
				args = append(args, "-vf", scale)
			}
		}

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
		"-hls_list_size", "0",
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

func resolveEffectiveTranscodeHWAccel(opts TranscodeOpts) string {
	hwAccel := ResolveHWAccel(opts.HWAccel)
	if hwAccel == "" {
		return ""
	}
	if strings.EqualFold(opts.TargetCodecVideo, "copy") {
		return "none"
	}
	if IsMPEG4Part2VideoCodec(opts.SourceVideoCodec) {
		return "none"
	}
	return hwAccel
}

// appendStreamSelectionArgs limits output to primary video/audio streams.
func appendStreamSelectionArgs(args []string, audioTrackIndex int) []string {
	args = append(args, "-map", "0:v:0")
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

	// Hardware encoders (QSV, VAAPI) may not reliably honor
	// force_key_frames expressions. Set explicit GOP size so segment
	// boundaries always start with an IDR frame. We assume 30 fps as a
	// safe ceiling — the GOP will be at most segmentDuration * 30 frames.
	// Matches Jellyfin's approach for hardware encoders.
	if opts.HWAccel == "qsv" || opts.HWAccel == "vaapi" {
		gopSize := fmt.Sprintf("%d", opts.SegmentDuration*30)
		args = append(args, "-g", gopSize, "-keyint_min", gopSize)
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
			"-init_hw_device", fmt.Sprintf("vaapi=va:%s,driver=iHD,kernel_driver=i915,vendor_id=0x8086", hwDevice),
			"-init_hw_device", "qsv=qs@va",
			"-filter_hw_device", "va",
			"-hwaccel", "vaapi",
			"-hwaccel_output_format", "vaapi",
			"-noautorotate",
		)
	case "vaapi":
		vaapiDevice := PickRenderDevice(opts.HWDevice)
		if vaapiDevice == "" {
			vaapiDevice = "/dev/dri/renderD128" // last-resort fallback
		}
		args = append(args,
			"-init_hw_device", fmt.Sprintf("vaapi=hw:%s", vaapiDevice),
			"-filter_hw_device", "hw",
			"-hwaccel", "vaapi",
			"-hwaccel_output_format", "vaapi",
		)
	}
	return args
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
		codec = "h264"
	}

	if codec == "copy" {
		return append(args, "-c:v", "copy")
	}

	preset := videoPreset(opts, opts.HWAccel)
	hasBitrateCap := opts.TargetBitrateKbps > 0

	switch {
	case opts.HWAccel == "qsv" && codec == "h264":
		if hasBitrateCap {
			// VBR mode with bitrate cap instead of global_quality.
			args = append(args, "-c:v", "h264_qsv", "-preset", preset,
				"-b:v", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-maxrate", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-bufsize", fmt.Sprintf("%dk", opts.TargetBitrateKbps*2))
		} else {
			args = append(args, "-c:v", "h264_qsv", "-preset", preset, "-global_quality", "23")
		}
	case opts.HWAccel == "qsv" && codec == "hevc":
		if hasBitrateCap {
			args = append(args, "-c:v", "hevc_qsv", "-preset", preset,
				"-b:v", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-maxrate", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-bufsize", fmt.Sprintf("%dk", opts.TargetBitrateKbps*2))
		} else {
			args = append(args, "-c:v", "hevc_qsv", "-preset", preset, "-global_quality", "28")
		}
	case opts.HWAccel == "vaapi" && codec == "h264":
		args = append(args, "-c:v", "h264_vaapi", "-qp", "23")
		if hasBitrateCap {
			args = append(args,
				"-maxrate", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-bufsize", fmt.Sprintf("%dk", opts.TargetBitrateKbps*2))
		}
	case opts.HWAccel == "vaapi" && codec == "hevc":
		args = append(args, "-c:v", "hevc_vaapi", "-qp", "28")
		if hasBitrateCap {
			args = append(args,
				"-maxrate", fmt.Sprintf("%dk", opts.TargetBitrateKbps),
				"-bufsize", fmt.Sprintf("%dk", opts.TargetBitrateKbps*2))
		}
	default:
		// CPU fallback — match Jellyfin's proven browser-compatible settings.
		// Force yuv420p to ensure 8-bit output (10-bit sources produce High 10
		// Profile which browsers cannot decode via MSE).
		if codec == "hevc" {
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

	return args
}

// appendAudioArgs adds audio codec arguments. Supports "copy" for passthrough,
// plus opus / aac / eac3 / ac3 as re-encode targets. EAC3 and AC3 are useful
// when we must transcode video but want to preserve surround channels for an
// HDMI receiver — both are legal in HLS fMP4 (not MPEG-TS; ensure the HLS
// packager is fMP4 when emitting these).
func appendAudioArgs(args []string, opts TranscodeOpts) []string {
	codec := opts.TargetCodecAudio
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
		args = append(args, "-c:a", "aac", "-b:a", "192k", "-ac", "2")
	}

	return args
}

// appendSubtitleBurnInArgs adds subtitle burn-in filter arguments.
// For CPU encoding, the filter chain is: [scale,]subtitles.
// For QSV/VAAPI, frames must be downloaded from hardware, processed on CPU,
// then re-uploaded: hwdownload → format=yuv420p → [scale,] subtitles → hwupload → hwmap.
func appendSubtitleBurnInArgs(args []string, opts TranscodeOpts) []string {
	scale := resolutionToScale(opts.TargetResolution)
	subFilter := fmt.Sprintf("subtitles='%s':si=%d",
		escapeFilterPath(opts.InputPath), opts.SubtitleTrackIndex)

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
		// VAAPI→QSV pipeline: download from VAAPI surface to CPU, apply subtitle
		// and scale filters, convert to nv12 (required by hwupload for VAAPI
		// surfaces), upload back to VAAPI, then map to QSV for the encoder.
		vf := "hwdownload,format=yuv420p," + cpuFilters + ",format=nv12,hwupload,hwmap=derive_device=qsv,format=qsv"
		args = append(args, "-vf", vf)
	case "vaapi":
		// VAAPI-only: download, apply CPU filters, convert to nv12, upload back.
		vf := "hwdownload,format=yuv420p," + cpuFilters + ",format=nv12,hwupload"
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
	case "420p":
		return "scale=-2:420"
	case "328p":
		return "scale=-2:328"
	default:
		return ""
	}
}

// qsvScaleFilter returns the VAAPI→QSV filter chain with optional resolution scaling.
func qsvScaleFilter(res string) string {
	switch res {
	case "2160p":
		return "scale_vaapi=w=-2:h=2160:format=nv12,hwmap=derive_device=qsv,format=qsv"
	case "1080p":
		return "scale_vaapi=w=-2:h=1080:format=nv12,hwmap=derive_device=qsv,format=qsv"
	case "720p":
		return "scale_vaapi=w=-2:h=720:format=nv12,hwmap=derive_device=qsv,format=qsv"
	case "480p":
		return "scale_vaapi=w=-2:h=480:format=nv12,hwmap=derive_device=qsv,format=qsv"
	case "420p":
		return "scale_vaapi=w=-2:h=420:format=nv12,hwmap=derive_device=qsv,format=qsv"
	case "328p":
		return "scale_vaapi=w=-2:h=328:format=nv12,hwmap=derive_device=qsv,format=qsv"
	default:
		return "scale_vaapi=format=nv12,hwmap=derive_device=qsv,format=qsv"
	}
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
	case "420p":
		return "scale_vaapi=w=-2:h=420:format=nv12"
	case "328p":
		return "scale_vaapi=w=-2:h=328:format=nv12"
	default:
		return "scale_vaapi=format=nv12"
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

// minManifestSegments is the standard startup lead for actively encoded HLS.
// True video transcodes benefit from a larger cushion so playback does not
// outrun the encoder immediately after the first frame appears.
const minManifestSegments = 3

// minCopyManifestSegments is the startup lead for codec-copy sessions.
// Copying video while transcoding only audio can produce startup files far
// faster than real-time encoding, so waiting for 3 full segments adds
// unnecessary latency at playback start.
const minCopyManifestSegments = 2

func startupSegmentRequirement(opts TranscodeOpts) int {
	if strings.EqualFold(opts.TargetCodecVideo, "copy") {
		return minCopyManifestSegments
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
				if s.restarting {
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
// still use the synthetic full VOD manifest when duration is known because
// forced keyframes make that timeline stable and seek-anywhere friendly.
func (s *TranscodeSession) BuildPlaybackManifest(segPrefix, rawQuery string) ([]byte, error) {
	opts := s.Opts()
	if strings.EqualFold(opts.TargetCodecVideo, "copy") || opts.TotalDuration <= 0 {
		// Copy-video or unknown-duration sessions must use FFmpeg's real manifest.
		manifest, err := s.WaitForManifest(30 * time.Second)
		if err != nil {
			return nil, err
		}
		return RewriteManifestPaths(manifest, segPrefix, rawQuery)
	}

	return s.GenerateFullManifest(segPrefix, rawQuery), nil
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
		Restarting:           s.restarting,
		StartSegmentNumber:   opts.StartSegmentNumber,
		SegmentDuration:      opts.SegmentDuration,
		LastRequestedSegment: s.lastRequestedSegment,
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
	case !progress.Running:
		decision.Reason = "transcode_not_running"
	case progress.Restarting:
		decision.Reason = "transcode_restarting"
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

	var suffix string
	if rawQuery != "" {
		suffix = "?" + rawQuery
	}

	segExt := hlsSegmentExtension(opts)
	hlsVersion := 3
	if segExt == ".m4s" {
		hlsVersion = 7
	}

	var buf bytes.Buffer
	buf.WriteString("#EXTM3U\n")
	buf.WriteString(fmt.Sprintf("#EXT-X-VERSION:%d\n", hlsVersion))
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
	if s.outputDir != "" {
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

// Restart kills the current ffmpeg process and starts a new one seeking to
// the given position. startSegment sets -hls_segment_start_number so that
// output filenames align with the expected segment numbering. Existing
// segment files are preserved so backward seeks can reuse them; for
// copy-mode sessions, stale segments at or after the restart point are
// cleaned to prevent serving data from the wrong timeline position.
func (s *TranscodeSession) Restart(ctx context.Context, seekSeconds float64, startSegment int) error {
	s.StopThrottler()
	s.mu.Lock()
	s.restarting = true
	cancelCurrent := s.cancel
	done := s.done
	s.mu.Unlock()

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
	opts := s.opts
	s.mu.Unlock()

	// Copy-mode restarts must clean stale segments so ffmpeg writes fresh
	// output. Encoded transcodes keep old segments for backward seek reuse.
	if strings.EqualFold(opts.TargetCodecVideo, "copy") {
		s.cleanStaleSegments(startSegment)
	}

	opts.SeekSeconds = seekSeconds
	opts.StartSegmentNumber = startSegment
	opts.FastStart = false // seek-restarts use veryfast for better quality

	args := buildFFmpegArgs(opts)
	bin := opts.FFmpegPath
	if bin == "" {
		bin = ffmpegBinary()
	}

	log.Printf("playback: ffmpeg restart cmd: %s %s", bin, strings.Join(args, " "))
	s.logFFmpegEvent(ctx, "ffmpeg process restart", "")

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, bin, args...)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		s.mu.Lock()
		s.restarting = false
		s.waitErr = err
		s.mu.Unlock()
		return fmt.Errorf("create stdin pipe: %w", err)
	}
	cmd.Dir = opts.OutputDir
	cmd.Stderr = s.newStderrWriter(ctx)
	cmd.WaitDelay = 3 * time.Second

	if err := cmd.Start(); err != nil {
		cancel()
		s.mu.Lock()
		s.restarting = false
		s.waitErr = err
		s.mu.Unlock()
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
	s.restarting = false
	s.stdinPipe = stdinPipe
	s.lastRequestedSegment = startSegment
	s.done = make(chan struct{})
	s.mu.Unlock()

	go func() {
		waitErr := cmd.Wait()
		s.flushStderr(ctx)
		s.mu.Lock()
		s.running = false
		s.waitErr = waitErr
		s.mu.Unlock()
		s.logWaitResult(ctx, waitErr)
		close(s.done)
	}()

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
		restarting := s.restarting
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
