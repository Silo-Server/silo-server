package playback

import (
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
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

var (
	resolvedFFmpegPath string
	ffmpegOnce         sync.Once

	dv7HDR10FilterSupportMu    sync.Mutex
	dv7HDR10FilterSupportCache map[string]bool
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
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	return ffmpegBinary()
}

// supportsDV7HDR10FilterChain reports whether the given FFmpeg binary can
// produce a clean HDR10 base layer from Profile 7: dovi_rpu strips DV metadata
// and filter_units removes the interleaved enhancement-layer NAL units. Probed
// once per binary path.
func supportsDV7HDR10FilterChain(bin string) bool {
	dv7HDR10FilterSupportMu.Lock()
	defer dv7HDR10FilterSupportMu.Unlock()
	if available, ok := dv7HDR10FilterSupportCache[bin]; ok {
		return available
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, bin, "-hide_banner", "-bsfs").Output()
	if err != nil {
		slog.Warn("failed to probe ffmpeg dovi_rpu/filter_units bitstream filters; validated Profile 7 HDR10 remux is disabled for this request", "ffmpeg", bin, "error", err)
		return false
	}
	available := hasDV7HDR10FilterChain(out)
	if !available {
		slog.Warn("ffmpeg lacks the dovi_rpu/filter_units bitstream filters; validated Profile 7 HDR10 remux is disabled", "ffmpeg", bin)
	}
	if dv7HDR10FilterSupportCache == nil {
		dv7HDR10FilterSupportCache = make(map[string]bool)
	}
	dv7HDR10FilterSupportCache[bin] = available
	return available
}

// remuxDVProfile neutralizes a Dolby Vision profile the local ffmpeg cannot
// handle. Profile 7 is the only profile that triggers the HDR10 base-layer
// filter in buildRemuxArgs; when the filter is unavailable — either required
// BSF is missing, or this source's RPU cannot be parsed — the remux must still
// start (an unknown bitstream filter aborts ffmpeg immediately, and a filter
// that rejects every packet hangs the session), so fall back to the pre-strip
// behavior instead of failing playback. Only the legacy/auto mode takes this
// route; the explicit v3 strip recipe fails loudly instead, because it has
// promised the client an HDR10 output it could not then produce.
func remuxDVProfile(dvProfile int, canProduceHDR10BaseLayer bool) int {
	if dvProfile == 7 && !canProduceHDR10BaseLayer {
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
// strip DV metadata and the interleaved enhancement-layer NAL units, yielding
// a clean HDR10 base layer (the Apple fallback for devices without a P7
// decoder). Profile 8 RPUs stay: the base layer is self-contained and DV
// clients can render it.
func buildRemuxArgs(filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, tagSampleEntry, audioOnly bool) []string {
	return buildRemuxArgsWithAudioV3(filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, tagSampleEntry, audioOnly, 0, 0)
}

func buildRemuxArgsWithAudioV3(filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, tagSampleEntry, audioOnly bool, targetAudioChannels, targetAudioBitrateKbps int) []string {
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

	// A planned video remux must fail if the promised video stream disappeared
	// or became unreadable. Only positively identified audio-only media may
	// make the video map optional.
	videoMap := "0:V:0"
	if audioOnly {
		videoMap += "?"
	}
	args = append(args, "-map", videoMap)
	if audioTrackIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:a:%d?", audioTrackIndex))
	} else {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args, "-sn", "-dn")

	if dvProfile == 7 {
		args = append(args, "-bsf:v", DV7ToHDR10BitstreamFilter)
		if tagSampleEntry {
			// The explicit v3 strip recipe promised the client plain HDR10.
			// Safari's media element only answers "probably" for hvc1 — the
			// sample entry Apple's HLS authoring spec requires — so relabel
			// FFmpeg's default hev1 to match the evidence the web probe
			// collects. Stripped output carries no DOVI record, so no -strict
			// relaxation is needed. Legacy/auto strips keep hev1.
			args = append(args, "-tag:v", "hvc1")
		}
	} else if (dvProfile == 5 || dvProfile == 8) && tagSampleEntry {
		// FFmpeg carries the DOVI configuration record into MP4 but otherwise
		// labels copied HEVC as hev1. Media3 keys decoder selection from the
		// sample entry, and Safari's media element only answers "probably" for
		// dvh1 — the sample entry Apple's HLS authoring spec calls for — so tag
		// dvh1. FFmpeg refuses to write the dvvC configuration record box under
		// either tag without -strict unofficial; dvh1 plus -strict unofficial is
		// verified (7.1.4) to retain the full record. Media3 accepts both sample
		// entries, so Android preserve consumers are unaffected. Only the
		// explicit v3 preserve recipe opts in: legacy web/jellycompat consumers
		// keep the pre-v3 hev1 labeling their demuxers accept.
		args = append(args, "-tag:v", "dvh1", "-strict", "unofficial")
	}

	if transcodeAudio {
		channels, bitrateKbps := resolvedAACOutputV3(targetAudioChannels, targetAudioBitrateKbps)
		// Video copy + AAC encode is effectively single-threaded work.
		// ffmpeg's default auto-threading spawns one filter thread per CPU
		// core for the implicit downmix/resampler, all idle. Pin to one.
		args = append(args,
			"-threads", "1",
			"-filter_threads", "1",
			"-filter_complex_threads", "1",
			"-c:v", "copy",
			"-c:a", "aac",
			"-ac", strconv.Itoa(channels),
			"-b:a", strconv.Itoa(bitrateKbps)+"k",
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
	return startRemuxWithOptions(ctx, filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, mode, ffmpegPath, false, 0, 0)
}

func startRemuxWithOptions(ctx context.Context, filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, mode RemuxDVMode, ffmpegPath string, audioOnly bool, targetAudioChannels, targetAudioBitrateKbps int) (*RemuxSession, error) {
	ctx, cancel := context.WithCancel(ctx)

	bin := ResolveFFmpegPath(ffmpegPath)
	effectiveProfile := dvProfile
	tagSampleEntry := false
	switch mode {
	case "", RemuxDVLegacyAutoV3:
		effectiveProfile = remuxDVProfile(dvProfile, supportsDV7HDR10FilterChain(bin) &&
			(dvProfile != 7 || sharedDVRPUProbe.CanStrip(ctx, bin, filePath)))
	case RemuxDVStripToHDR10V3:
		if dvProfile != 7 && dvProfile != 8 {
			cancel()
			return nil, fmt.Errorf("Dolby Vision HDR10 strip requires profile 7 or 8")
		}
		if !supportsDV7HDR10FilterChain(bin) {
			cancel()
			return nil, fmt.Errorf("dolby vision HDR10 remux requires the dovi_rpu and filter_units bitstream filters")
		}
		// The planner refuses this recipe for a source that fails the probe,
		// so reaching here means a session or stream token minted before the
		// verdict was known. Fail definitively: an unfiltered copy would retain
		// Dolby Vision metadata and enhancement-layer NAL units while still
		// claiming HDR10, and attempting the strip anyway is the per-packet
		// rejection that hangs the session. The
		// next start re-plans against the now-cached verdict.
		if !sharedDVRPUProbe.CanStrip(ctx, bin, filePath) {
			cancel()
			return nil, fmt.Errorf("this source's Dolby Vision RPU cannot be stripped to HDR10")
		}
		// buildRemuxArgs uses profile 7 as the explicit strip sentinel; the
		// filter is equally required for a compatible profile 8 base layer.
		// The explicit recipe also labels the output hvc1: the web HDR10
		// probe collects evidence for that sample entry, and the plan must
		// deliver the shape it validated. Legacy/auto strips keep hev1.
		effectiveProfile = 7
		tagSampleEntry = true
	case RemuxDVPreserveV3:
		if dvProfile == 7 {
			// Profile 7 preservation is not a validated remux recipe for this
			// delivery. Callers must use the original bytes, strip to HDR10, or
			// transcode instead.
			cancel()
			return nil, fmt.Errorf("Dolby Vision profile 7 cannot be preserved in a progressive remux")
		}
		tagSampleEntry = true
	case RemuxDVRejectP7V3:
		if dvProfile == 7 {
			cancel()
			return nil, fmt.Errorf("profile 7 remux is not eligible")
		}
	default:
		cancel()
		return nil, fmt.Errorf("unknown remux Dolby Vision mode %q", mode)
	}
	args := buildRemuxArgsWithAudioV3(filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, effectiveProfile, tagSampleEntry, audioOnly, targetAudioChannels, targetAudioBitrateKbps)
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

// RemuxServeOptions carries the optional serving concerns that not every
// caller sets, keeping the positional argument list from growing further.
type RemuxServeOptions struct {
	// DVMode is the explicitly declared Dolby Vision recipe. The zero value
	// decodes as the legacy auto behavior, matching old stream tokens.
	DVMode RemuxDVMode
	// DVRecipeVersion pins byte-level strip behavior across reconstruction and
	// rolling upgrades. It is required whenever DVMode is strip_to_hdr10.
	DVRecipeVersion string
	// FFmpegPath selects the binary to execute (empty = global discovery).
	FFmpegPath string
	// ContentType overrides the container-derived response type. Audio-only
	// sources mux an audio-only fMP4, which must not be announced as video.
	ContentType string
	// AudioOnly permits the otherwise-mandatory video map to be absent.
	AudioOnly bool
	// TargetAudioChannels and TargetAudioBitrateKbps freeze the planned AAC
	// output. Zero values retain the historical stereo 192 kbps behavior.
	TargetAudioChannels    int
	TargetAudioBitrateKbps int
}

// RemuxContentType returns the override required for an audio-only fMP4.
func RemuxContentType(audioOnly bool) string {
	if audioOnly {
		return AudioOnlyRemuxMIMEV3
	}
	return ""
}

// ServeRemux streams a remuxed file to the HTTP response.
// It starts an ffmpeg remux session and copies the output directly to the
// response writer. The response is streamed (chunked transfer) since the
// total size is not known in advance.
// When transcodeAudio is true, audio is transcoded to AAC while video is copied.
func ServeRemux(w http.ResponseWriter, r *http.Request, filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int) error {
	return ServeRemuxWithOptions(w, r, filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, RemuxServeOptions{})
}

// ServeRemuxWithDVMode streams an explicitly declared Dolby Vision recipe.
// ffmpegPath selects the binary to execute (empty = process-global discovery).
func ServeRemuxWithDVMode(w http.ResponseWriter, r *http.Request, filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, mode RemuxDVMode, ffmpegPath string) error {
	version := ""
	if mode == RemuxDVStripToHDR10V3 {
		version = TransformationServerDV7HDR10RecipeVersionV3
	}
	return ServeRemuxWithOptions(w, r, filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, RemuxServeOptions{DVMode: mode, DVRecipeVersion: version, FFmpegPath: ffmpegPath})
}

// ServeRemuxWithOptions is the full remux transport, taking its optional
// serving concerns as a struct.
func ServeRemuxWithOptions(w http.ResponseWriter, r *http.Request, filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, opts RemuxServeOptions) error {
	mode, ffmpegPath := opts.DVMode, opts.FFmpegPath
	if mode == RemuxDVStripToHDR10V3 && opts.DVRecipeVersion != TransformationServerDV7HDR10RecipeVersionV3 {
		err := fmt.Errorf("unsupported Dolby Vision HDR10 remux recipe version %q", opts.DVRecipeVersion)
		http.Error(w, "stale remux recipe", http.StatusConflict)
		return err
	}
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

	session, err := startRemuxWithOptions(r.Context(), filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, mode, ffmpegPath, opts.AudioOnly, opts.TargetAudioChannels, opts.TargetAudioBitrateKbps)
	if err != nil {
		http.Error(w, "failed to start remux", http.StatusInternalServerError)
		return err
	}
	defer session.Close()

	contentType := opts.ContentType
	if contentType == "" {
		contentType = containerMIME(outputFormat)
	}
	w.Header().Set("Content-Type", contentType)
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
