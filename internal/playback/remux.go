package playback

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

var (
	resolvedFFmpegPath string
	ffmpegOnce         sync.Once

	doviRPUMu    sync.Mutex
	doviRPUCache map[string]bool
)

// ffmpegBinary returns the path to the ffmpeg binary.
// Resolved once at first call, then cached for the process lifetime.
func ffmpegBinary() string {
	ffmpegOnce.Do(func() {
		const jellyfinPath = "/usr/lib/jellyfin-ffmpeg/ffmpeg"
		if _, err := exec.LookPath(jellyfinPath); err == nil {
			resolvedFFmpegPath = jellyfinPath
			return
		}
		resolvedFFmpegPath = "ffmpeg"
	})
	return resolvedFFmpegPath
}

// ResolveFFmpegPath returns the ffmpeg binary the playback pipeline executes
// for the given configured path: the configured path when set, otherwise the
// process-global discovery (jellyfin-ffmpeg install, then PATH). Capability
// probes must resolve through this same function so a feature advertised at
// planning time is guaranteed present in the binary that later runs.
func ResolveFFmpegPath(configured string) string {
	if strings.TrimSpace(configured) != "" {
		return configured
	}
	return ffmpegBinary()
}

// supportsDoviRPUFilter reports whether the given FFmpeg binary can strip
// Dolby Vision RPU metadata via the dovi_rpu bitstream filter (FFmpeg 7.1+).
// The enhancement layer itself is dropped by stream mapping, so stripping the
// RPUs yields a clean HDR10 base layer. Probed once per binary path.
func supportsDoviRPUFilter(bin string) bool {
	doviRPUMu.Lock()
	defer doviRPUMu.Unlock()
	if available, ok := doviRPUCache[bin]; ok {
		return available
	}
	out, err := exec.Command(bin, "-hide_banner", "-bsfs").Output()
	available := err == nil && bytes.Contains(out, []byte("dovi_rpu"))
	if !available {
		slog.Warn("ffmpeg lacks the dovi_rpu bitstream filter (needs FFmpeg 7.1+); validated Profile 7 HDR10 remux is disabled", "ffmpeg", bin)
	}
	if doviRPUCache == nil {
		doviRPUCache = make(map[string]bool)
	}
	doviRPUCache[bin] = available
	return available
}

// remuxDVProfile neutralizes a Dolby Vision profile the local ffmpeg cannot
// handle. Profile 7 is the only profile that triggers an RPU strip in
// buildRemuxArgs; when the dovi_rpu filter is unavailable the remux must
// still start (an unknown bitstream filter aborts ffmpeg immediately), so
// fall back to the pre-strip behavior instead of failing playback.
func remuxDVProfile(dvProfile int, canStripRPU bool) int {
	if dvProfile == 7 && !canStripRPU {
		return 0
	}
	return dvProfile
}

// RemuxSession represents a running ffmpeg remux process that copies
// codecs to a new container format without re-encoding.
type RemuxSession struct {
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	outputPipe io.ReadCloser
}

// RemuxDVMode makes Profile 7 handling an explicit byte-level recipe choice.
// The empty/legacy mode exists only for pre-v3 callers and old stream tokens.
type RemuxDVMode string

const (
	RemuxDVLegacyAutoV3   RemuxDVMode = "legacy_auto"
	RemuxDVPreserveV3     RemuxDVMode = "preserve"
	RemuxDVStripToHDR10V3 RemuxDVMode = "strip_to_hdr10"
	RemuxDVRejectP7V3     RemuxDVMode = "reject_profile_7"
)

// buildRemuxArgs constructs the ffmpeg argument list for a remux operation.
// The args perform codec copy (-c copy) into the target container format,
// using fragmented output for streaming (frag_keyframe+delay_moov+default_base_moof) and
// pipe:1 for stdout output.
// When transcodeAudio is true, video is copied but audio is transcoded to
// stereo AAC (handles cases like DTS/TrueHD that browsers cannot decode).
// dvProfile is the file's Dolby Vision profile (0 = none). Profile 7 remuxes
// strip DV RPUs: the enhancement layer is dropped by the video map below, so
// the RPUs would dangle — stripping yields a clean HDR10 base layer (the
// Apple-parity fallback for devices without a P7 decoder). Profile 8 RPUs
// stay: the base layer is self-contained and DV clients can render it.
func buildRemuxArgs(filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, tagDVSampleEntry bool) []string {
	args := []string{
		"-nostdin",
		"-hide_banner",
		"-loglevel", "error",
		// Cap the input probe to speed up startup on large MKV containers
		// (defaults scan up to 5 seconds / several MB). Mirrors the transcode
		// pipeline, which has run these caps in production without issue.
		"-fflags", "+genpts+fastseek",
		"-analyzeduration", "3000000",
		"-probesize", "5000000",
	}

	// Add seek if requested (before input for fast seek).
	if seekSeconds > 0 {
		args = append(args, "-ss", strconv.FormatFloat(seekSeconds, 'f', 3, 64))
		if transcodeAudio {
			// In copy mode (-c:v copy) video must start at a keyframe, but
			// accurate_seek (the default) trims re-encoded audio to the exact
			// seek point. This mismatch causes A/V desync equal to the gap
			// between the keyframe and the seek point. Disabling accurate seek
			// keeps both streams aligned at the keyframe boundary.
			args = append(args, "-noaccurate_seek")
		}
	}

	args = append(args, "-i", filePath)

	// Strip metadata/chapters and skip subtitle/data streams — the remux
	// output is fed to an HTML <video> tag, none of it is consumed.
	args = append(args,
		"-map_metadata", "-1",
		"-map_chapters", "-1",
	)

	// Select specific video and audio streams.
	args = append(args, "-map", "0:v:0")
	if audioTrackIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:a:%d?", audioTrackIndex))
	} else {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args, "-sn", "-dn")

	if dvProfile == 7 {
		args = append(args, "-bsf:v", "dovi_rpu=strip=1")
	} else if (dvProfile == 5 || dvProfile == 8) && tagDVSampleEntry {
		// FFmpeg carries the DOVI configuration record into MP4 but otherwise
		// labels copied HEVC as hev1. Media3 keys decoder selection from the
		// sample entry, so retain an explicit Dolby Vision tag as well.
		// dvhe keeps FFmpeg's dvvC box; forcing dvh1 makes FFmpeg 7.1 omit it.
		// Only the explicit v3 preserve recipe opts in: legacy web/jellycompat
		// consumers keep the pre-v3 hev1 labeling their demuxers accept.
		args = append(args, "-tag:v", "dvhe")
	}

	if transcodeAudio {
		// Video copy + stereo AAC encode is effectively single-threaded work.
		// ffmpeg's default auto-threading spawns one filter thread per CPU
		// core for the implicit downmix/resampler, all idle. Pin to one.
		args = append(args,
			"-threads", "1",
			"-filter_threads", "1",
			"-filter_complex_threads", "1",
			"-c:v", "copy",
			"-c:a", "aac",
			"-ac", "2",
			"-b:a", "192k",
		)
	} else {
		args = append(args, "-c", "copy")
	}

	args = append(args,
		"-avoid_negative_ts", "make_zero",
		"-f", outputFormat,
		// delay_moov lets the MP4 muxer inspect the first audio packet before
		// writing codec configuration. empty_moov fails immediately for copied
		// E-AC-3/Atmos tracks because their frame size is not known at header time.
		"-movflags", "frag_keyframe+delay_moov+default_base_moof",
		"pipe:1",
	)

	return args
}

// StartRemux starts an ffmpeg process that copies codecs to a new container.
// When transcodeAudio is false the command is:
//
//	ffmpeg -i {input} -c copy -f {format} -movflags frag_keyframe+delay_moov+default_base_moof pipe:1
//
// When transcodeAudio is true video is copied but audio is transcoded to AAC.
// The caller must call Close() when done to clean up resources.
func StartRemux(ctx context.Context, filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int) (*RemuxSession, error) {
	return StartRemuxWithDVMode(ctx, filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, RemuxDVLegacyAutoV3, "")
}

// StartRemuxWithDVMode starts a remux with explicit Dolby Vision behavior.
// ffmpegPath selects the binary to execute (empty = process-global discovery);
// v3 callers must pass the configured playback path so the strip capability
// promised by the planner's probe holds for the binary that actually runs.
func StartRemuxWithDVMode(ctx context.Context, filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, mode RemuxDVMode, ffmpegPath string) (*RemuxSession, error) {
	ctx, cancel := context.WithCancel(ctx)

	bin := ResolveFFmpegPath(ffmpegPath)
	effectiveProfile := dvProfile
	tagDVSampleEntry := false
	switch mode {
	case "", RemuxDVLegacyAutoV3:
		effectiveProfile = remuxDVProfile(dvProfile, supportsDoviRPUFilter(bin))
	case RemuxDVStripToHDR10V3:
		if dvProfile != 7 && dvProfile != 8 {
			cancel()
			return nil, fmt.Errorf("Dolby Vision HDR10 strip requires profile 7 or 8")
		}
		if !supportsDoviRPUFilter(bin) {
			cancel()
			return nil, fmt.Errorf("Dolby Vision HDR10 remux requires the dovi_rpu bitstream filter")
		}
		// buildRemuxArgs uses profile 7 as the explicit strip sentinel; the
		// filter is equally required for a compatible profile 8 base layer.
		effectiveProfile = 7
	case RemuxDVPreserveV3:
		if dvProfile == 7 {
			// The remux maps only the base-layer stream, so dual-layer P7
			// cannot be preserved: the EL is dropped and its RPUs would
			// dangle. Callers must strip to HDR10 or transcode instead.
			cancel()
			return nil, fmt.Errorf("Dolby Vision profile 7 cannot be preserved in a progressive remux")
		}
		tagDVSampleEntry = true
	case RemuxDVRejectP7V3:
		if dvProfile == 7 {
			cancel()
			return nil, fmt.Errorf("profile 7 remux is not eligible")
		}
	default:
		cancel()
		return nil, fmt.Errorf("unknown remux Dolby Vision mode %q", mode)
	}
	args := buildRemuxArgs(filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, effectiveProfile, tagDVSampleEntry)
	cmd := exec.CommandContext(ctx, bin, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	return &RemuxSession{
		cmd:        cmd,
		cancel:     cancel,
		outputPipe: stdout,
	}, nil
}

// Read implements io.Reader, piping ffmpeg stdout to the caller.
func (s *RemuxSession) Read(p []byte) (int, error) {
	return s.outputPipe.Read(p)
}

// Close stops the ffmpeg process and cleans up all resources.
// It is safe to call Close multiple times.
func (s *RemuxSession) Close() error {
	s.cancel()
	// Drain the pipe so cmd.Wait does not block.
	_, _ = io.Copy(io.Discard, s.outputPipe)
	return s.cmd.Wait()
}

// containerMIME maps output format names to MIME types for HTTP responses.
func containerMIME(format string) string {
	switch format {
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "matroska":
		return "video/x-matroska"
	case "mpegts":
		return "video/mp2t"
	default:
		return "application/octet-stream"
	}
}

// ServeRemux streams a remuxed file to the HTTP response.
// It starts an ffmpeg remux session and copies the output directly to the
// response writer. The response is streamed (chunked transfer) since the
// total size is not known in advance.
// When transcodeAudio is true, audio is transcoded to AAC while video is copied.
func ServeRemux(w http.ResponseWriter, r *http.Request, filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int) error {
	return ServeRemuxWithDVMode(w, r, filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, RemuxDVLegacyAutoV3, "")
}

// ServeRemuxWithDVMode streams an explicitly declared Dolby Vision recipe.
// ffmpegPath selects the binary to execute (empty = process-global discovery).
func ServeRemuxWithDVMode(w http.ResponseWriter, r *http.Request, filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, mode RemuxDVMode, ffmpegPath string) error {
	// Remux output streams for the length of the title; roll the write
	// deadline with progress instead of the server's absolute WriteTimeout.
	w = httpstream.NewRollingDeadlineWriter(w)
	// Check file exists before starting ffmpeg to return a proper 404.
	// Headers must be written before streaming begins, so we can't detect
	// ffmpeg errors after WriteHeader(200) has been sent.
	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
			return err
		}
		http.Error(w, "failed to access file", http.StatusInternalServerError)
		return err
	}

	session, err := StartRemuxWithDVMode(r.Context(), filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, mode, ffmpegPath)
	if err != nil {
		http.Error(w, "failed to start remux", http.StatusInternalServerError)
		return err
	}
	defer session.Close()

	w.Header().Set("Content-Type", containerMIME(outputFormat))
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(http.StatusOK)

	// Stream ffmpeg output to the HTTP response.
	buf := make([]byte, 32*1024) // 32 KB buffer
	for {
		n, readErr := session.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return nil // Client disconnected.
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if readErr != nil {
			return nil // EOF or error — done streaming.
		}
	}
}
