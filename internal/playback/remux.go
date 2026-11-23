package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/processmetrics"

	"github.com/Silo-Server/silo-server/internal/procgroup"
)

const (
	jellyfinFFmpegPath             = "/usr/lib/jellyfin-ffmpeg/ffmpeg"
	homebrewFFmpegFullAppleSilicon = "/opt/homebrew/opt/ffmpeg-full/bin/ffmpeg"
	homebrewFFmpegFullIntel        = "/usr/local/opt/ffmpeg-full/bin/ffmpeg"
	ffmpegExecutable               = ffmpegComponent
)

var (
	resolvedFFmpegPath string
	ffmpegOnce         sync.Once

	doviRPUMu    sync.Mutex
	doviRPUCache map[string]bool

	leadingPictureDropMu    sync.Mutex
	leadingPictureDropCache map[string]bool
)

// ResumeLeadingPictureDropBitstreamFilter drops the non-key packets whose
// presentation time precedes the first packet of a seeked stream copy: the
// open-GOP leading pictures that reference frames the copy never sent. The
// escaped comma is part of one FFmpeg argument.
const ResumeLeadingPictureDropBitstreamFilter = `noise=drop=lt(pts\,startpts)*not(key)`

// ffmpegBinary returns the path to the ffmpeg binary.
// Resolved once at first call, then cached for the process lifetime.
func ffmpegBinary() string {
	ffmpegOnce.Do(func() {
		resolvedFFmpegPath = discoverFFmpegPath(runtime.GOOS, exec.LookPath)
	})
	return resolvedFFmpegPath
}

func discoverFFmpegPath(goos string, lookPath func(string) (string, error)) string {
	candidates := []string{jellyfinFFmpegPath}
	if goos == darwinGOOS {
		// Homebrew's regular FFmpeg omits libass and other filters Silo uses.
		// Prefer the keg-only full build on both Apple Silicon and Intel Macs
		// when it is installed; PATH remains the final portable fallback.
		candidates = []string{
			homebrewFFmpegFullAppleSilicon,
			homebrewFFmpegFullIntel,
			jellyfinFFmpegPath,
		}
	}
	for _, candidate := range candidates {
		if _, err := lookPath(candidate); err == nil {
			return candidate
		}
	}
	return ffmpegExecutable
}

// HomebrewFFmpegFull reports whether path is Homebrew's keg-only ffmpeg-full,
// the build discovery prefers on a Mac.
func HomebrewFFmpegFull(path string) bool {
	return path == homebrewFFmpegFullAppleSilicon || path == homebrewFFmpegFullIntel
}

// ResolveFFmpegPath returns the ffmpeg binary the playback pipeline executes
// for the given configured path: the configured path when set, otherwise the
// process-global discovery (jellyfin-ffmpeg install, then PATH). Capability
// probes must resolve through this same function so a feature advertised at
// planning time is guaranteed present in the binary that later runs.
func ResolveFFmpegPath(configured string) string {
	return resolveFFmpegPath(configured, exec.LookPath, ffmpegBinary)
}

func resolveFFmpegPath(
	configured string,
	lookPath func(string) (string, error),
	discover func() string,
) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return discover()
	}
	// Older and container-oriented settings use Jellyfin's Linux-only path as
	// the default. Treat that conventional default as discovery only when it is
	// absent; genuinely custom invalid paths must still fail loudly.
	if configured == jellyfinFFmpegPath {
		if _, err := lookPath(configured); err != nil {
			return discover()
		}
	}
	return configured
}

// supportsDoviRPUFilter reports whether the given FFmpeg binary can run
// DV7ToHDR10BitstreamFilter: the dovi_rpu bitstream filter (FFmpeg 7.1+) that
// strips the Dolby Vision metadata, and filter_units, which removes a Profile
// 7 enhancement layer interleaved in the video stream. Probed once per binary
// path.
func supportsDoviRPUFilter(bin string) bool {
	doviRPUMu.Lock()
	defer doviRPUMu.Unlock()
	if available, ok := doviRPUCache[bin]; ok {
		return available
	}
	out, err := procgroup.CommandContext(context.Background(), bin, "-hide_banner", "-bsfs").Output()
	available := err == nil && bytes.Contains(out, []byte("dovi_rpu")) && bytes.Contains(out, []byte("filter_units"))
	if !available {
		slog.Warn("ffmpeg lacks the dovi_rpu or filter_units bitstream filter (dovi_rpu needs FFmpeg 7.1+); validated Profile 7 HDR10 remux is disabled", "ffmpeg", bin)
	}
	if doviRPUCache == nil {
		doviRPUCache = make(map[string]bool)
	}
	doviRPUCache[bin] = available
	return available
}

// supportsLeadingPictureDropFilter reports whether the given FFmpeg binary's
// noise bitstream filter takes the drop expression the resume recipe uses.
// Older builds expose only the integer dropamount option. Probed once per
// binary path, like the dovi_rpu check.
func supportsLeadingPictureDropFilter(bin string) bool {
	leadingPictureDropMu.Lock()
	defer leadingPictureDropMu.Unlock()
	if available, ok := leadingPictureDropCache[bin]; ok {
		return available
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := procgroup.CommandContext(ctx, bin, "-hide_banner", "-h", "bsf=noise").CombinedOutput()
	available := err == nil && slices.Contains(strings.Fields(string(out)), "-drop")
	if !available {
		slog.Warn("ffmpeg's noise bitstream filter has no drop expression; HEVC resume remuxes keep their leading pictures", "ffmpeg", bin)
	}
	if leadingPictureDropCache == nil {
		leadingPictureDropCache = make(map[string]bool)
	}
	leadingPictureDropCache[bin] = available
	return available
}

// remuxDVProfile neutralizes a Dolby Vision profile the local ffmpeg cannot
// handle. Profile 7 is the only profile that triggers an RPU strip in
// buildRemuxArgs; when the strip is unavailable — the dovi_rpu filter is
// missing, or this source's RPU cannot be parsed — the remux must still start
// (an unknown bitstream filter aborts ffmpeg immediately, and a filter that
// rejects every packet hangs the session), so fall back to the pre-strip
// behavior instead of failing playback. Only the legacy/auto mode takes this
// route; the explicit v3 strip recipe fails loudly instead, because it has
// promised the client an HDR10 output it could not then produce.
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
	ctx        context.Context
	cancel     context.CancelFunc
	outputPipe io.ReadCloser
	closeOnce  sync.Once
	closeErr   error
	stderr     *remuxStderrTail
}

// remuxStderrLimit bounds the captured FFmpeg stderr. A remux logs at
// -loglevel error, so a start failure fits in a few hundred bytes; the cap
// only keeps a pathological process from growing the buffer.
const remuxStderrLimit = 4 << 10

// remuxStderrTail keeps the last remuxStderrLimit bytes FFmpeg wrote to
// stderr. Without it a remux that produced no output reports only EOF: the
// transport sees a closed pipe, never the reason FFmpeg refused the input.
type remuxStderrTail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *remuxStderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > remuxStderrLimit {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-remuxStderrLimit:]...)
	}
	return len(p), nil
}

func (t *remuxStderrTail) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
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
// run DV7ToHDR10BitstreamFilter: the video map drops a separate
// enhancement-layer track, but a single-track source interleaves it as NAL
// unit type 63, and its RPUs would dangle either way. The result is a clean
// HDR10 base layer (the Apple-parity fallback for devices without a P7
// decoder). Profile 8 RPUs stay: the base layer is self-contained and DV
// clients can render it.
func buildRemuxArgs(filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, tagSampleEntry, audioOnly bool) []string {
	return buildRemuxArgsWithAudioV3(filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, tagSampleEntry, audioOnly, 0, 0, 0)
}

func buildRemuxArgsWithAudioV3(filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, tagSampleEntry, audioOnly bool, sourceAudioChannels, targetAudioChannels, targetAudioBitrateKbps int) []string {
	return buildRemuxArgsWithLeadingPictureDropV3(filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, tagSampleEntry, audioOnly, sourceAudioChannels, targetAudioChannels, targetAudioBitrateKbps, false, "")
}

// buildRemuxArgsWithLeadingPictureDropV3 adds the resume leading-picture drop
// to the video bitstream filters. It applies only to a seeked copy with video:
// a start at zero begins on the stream's first keyframe and has no leading
// pictures to drop. copyAudioCodec is the copied audio codec that needs ADTS
// framing converted for MP4, as answered by CopiedAudioADTSCodec; empty means
// this copy needs no audio bitstream filter.
func buildRemuxArgsWithLeadingPictureDropV3(filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, tagSampleEntry, audioOnly bool, sourceAudioChannels, targetAudioChannels, targetAudioBitrateKbps int, dropLeadingPictures bool, copyAudioCodec string) []string {
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

	videoBitstreamFilters := make([]string, 0, 2)
	if dropLeadingPictures && seekSeconds > 0 && !audioOnly {
		videoBitstreamFilters = append(videoBitstreamFilters, ResumeLeadingPictureDropBitstreamFilter)
	}
	if dvProfile == 7 {
		videoBitstreamFilters = append(videoBitstreamFilters, DV7ToHDR10BitstreamFilter)
	}
	if len(videoBitstreamFilters) > 0 {
		args = append(args, "-bsf:v", strings.Join(videoBitstreamFilters, ","))
	}
	if dvProfile == 7 {
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
		// verified (7.1.4, 8.1.3) to retain the full record. Media3 accepts both
		// sample entries, so Android preserve consumers are unaffected. Only the
		// explicit v3 preserve recipe opts in: legacy web/jellycompat consumers
		// keep the pre-v3 hev1 labeling their demuxers accept.
		args = append(args, "-tag:v", "dvh1", "-strict", "unofficial")
	}

	if transcodeAudio {
		channels, bitrateKbps := ResolveAACOutputV3(targetAudioChannels, targetAudioBitrateKbps)
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
		args = appendAACEncodeFilterArgs(args, sourceAudioChannels, "aac", targetAudioChannels, channels)
	} else {
		args = append(args, "-c", "copy")
		if strings.EqualFold(strings.TrimSpace(copyAudioCodec), "aac") {
			// AAC in MPEG-TS (and raw .aac) is ADTS-framed; MP4 needs the
			// ADTS headers stripped into a config record. FFmpeg normally
			// inserts this filter on its own, but not under delay_moov: the
			// muxer rejects the first ADTS packet ("Malformed AAC bitstream")
			// and the response ends after the header. The filter passes
			// already-raw AAC from MKV/MP4 through unchanged, but it refuses
			// any other codec, so the caller passes a codec only when the
			// source container really frames that track as ADTS (see
			// CopiedAudioADTSCodec).
			args = append(args, "-bsf:a", "aac_adtstoasc")
		}
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
	return startRemuxWithOptions(ctx, filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, mode, ffmpegPath, false, 0, 0, 0, false, "")
}

func startRemuxWithOptions(ctx context.Context, filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, mode RemuxDVMode, ffmpegPath string, audioOnly bool, sourceAudioChannels, targetAudioChannels, targetAudioBitrateKbps int, dropLeadingPictures bool, copyAudioCodec string) (*RemuxSession, error) {
	ctx, cancel := context.WithCancel(ctx)

	bin := ResolveFFmpegPath(ffmpegPath)
	effectiveProfile := dvProfile
	tagSampleEntry := false
	switch mode {
	case "", RemuxDVLegacyAutoV3:
		effectiveProfile = remuxDVProfile(dvProfile, supportsDoviRPUFilter(bin) &&
			(dvProfile != 7 || sharedDVRPUProbe.CanStrip(ctx, bin, filePath)))
	case RemuxDVStripToHDR10V3:
		if dvProfile != 7 && dvProfile != 8 {
			cancel()
			return nil, fmt.Errorf("Dolby Vision HDR10 strip requires profile 7 or 8") //nolint:staticcheck // Dolby Vision is a proper product name.
		}
		if !supportsDoviRPUFilter(bin) {
			cancel()
			return nil, fmt.Errorf("the Dolby Vision HDR10 remux requires the dovi_rpu and filter_units bitstream filters")
		}
		// The planner refuses this recipe for a source that fails the probe,
		// so reaching here means a session or stream token minted before the
		// verdict was known. Fail definitively: copying the base layer without
		// the strip would leave dangling RPUs (the decoder stall this recipe
		// exists to prevent) while still claiming HDR10, and attempting the
		// strip anyway is the per-packet rejection that hangs the session. The
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
			// The remux maps only the base-layer stream, so dual-layer P7
			// cannot be preserved: the EL is dropped and its RPUs would
			// dangle. Callers must strip to HDR10 or transcode instead.
			cancel()
			return nil, fmt.Errorf("Dolby Vision profile 7 cannot be preserved in a progressive remux") //nolint:staticcheck // Dolby Vision is a proper product name.
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
	// The leading-picture drop is best effort: a binary without the drop
	// expression serves the plain copy rather than failing the route. Only
	// probe when the filter would actually run.
	dropLeadingPictures = dropLeadingPictures && seekSeconds > 0 && !audioOnly && supportsLeadingPictureDropFilter(bin)
	args := buildRemuxArgsWithLeadingPictureDropV3(filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, effectiveProfile, tagSampleEntry, audioOnly, sourceAudioChannels, targetAudioChannels, targetAudioBitrateKbps, dropLeadingPictures, copyAudioCodec)
	cmd := fspath.CommandContext(ctx, bin, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrTail := &remuxStderrTail{}
	cmd.Stderr = stderrTail

	if err := cmd.Start(); err != nil {
		processmetrics.Record(processmetrics.Remux, nil, err, ctx.Err())
		cancel()
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	return &RemuxSession{
		cmd:        cmd,
		ctx:        ctx,
		cancel:     cancel,
		outputPipe: stdout,
		stderr:     stderrTail,
	}, nil
}

// Read implements io.Reader, piping ffmpeg stdout to the caller.
func (s *RemuxSession) Read(p []byte) (int, error) {
	return s.outputPipe.Read(p)
}

func (s *RemuxSession) Abort() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
}

// Close stops the ffmpeg process and cleans up all resources.
// It is safe to call Close multiple times.
func (s *RemuxSession) Close() error {
	s.closeOnce.Do(func() {
		contextErr := s.ctx.Err()
		s.cancel()
		// The owner drains and waits exactly once, including repeated Close.
		_, _ = io.Copy(io.Discard, s.outputPipe)
		s.closeErr = s.cmd.Wait()
		// Cleanup cancellation must not relabel an already completed FFmpeg
		// failure. A process killed by this Close has a signal exit instead.
		if contextErr == nil && s.closeErr != nil && s.cmd.ProcessState != nil && !s.cmd.ProcessState.Exited() {
			contextErr = context.Canceled
		}
		processmetrics.Record(processmetrics.Remux, s.cmd.ProcessState, s.closeErr, contextErr)
	})
	return s.closeErr
}

// errRemuxNoOutput marks an ffmpeg remux that exited, or whose output pipe
// failed, before producing a single media byte. The concrete read error is
// wrapped alongside it so the cause stays diagnosable while callers get a
// stable identity to match.
var errRemuxNoOutput = errors.New("remux produced no output")

// ErrRemuxRecipeFault reports a remux that FFmpeg refused because of an argument
// this server built — a bitstream filter that does not accept the mapped stream
// ("Codec 'eac3' ... is not supported by the bitstream filter 'aac_adtstoasc'").
// It says nothing about the source bytes, so a caller must not treat it as the
// release failing: indicting the candidate rotates away from a healthy release
// and the verdict the client ends up with is about a file it never faulted.
var ErrRemuxRecipeFault = errors.New("remux recipe fault")

// remuxByteRange is the byte range a progressive remux answer honors.
//
// A generated stream has no seekable file behind it: its body starts at the
// media position the plan anchored, and there is no byte offset that maps back
// to a media position. So the only range the answer can honor is a prefix,
// `bytes=0-N` (the shape a probe or a ranged download of a live-like body
// uses), which is answered by ending the response after N+1 bytes.
//
// A range that starts past zero is not an error: it is a client resuming its
// own read of a body whose shape it believes is byte-addressable — Chromium
// does exactly that on a seek, and on an interrupted read. The answer is the
// whole stream from the generation's start (the pre-range behavior), so the
// client re-reads from the origin and keeps playing. It must never be a fatal
// status: a 416 here closes the player instead of costing it a seek.
//
// The remux output length is unknown until it has been generated, so no range
// is unsatisfiable against a known total and 416 is never this transport's
// answer; a seekable file keeps it through http.ServeContent (see
// ServeDirectPlay and the v2 delivery contract).
type remuxByteRange struct {
	// prefix is true when the answer must stop after length bytes.
	prefix bool
	length int64
}

// parseRemuxByteRange reads the single byte range a progressive remux can
// honor. Everything it cannot answer as a prefix — a start offset, a suffix
// range, a multi-range set, an unparseable or absent header — is answered with
// the whole stream from the generation's start.
func parseRemuxByteRange(header string) remuxByteRange {
	spec, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !ok {
		return remuxByteRange{}
	}
	if strings.Contains(spec, ",") {
		return remuxByteRange{}
	}
	firstText, lastText, found := strings.Cut(strings.TrimSpace(spec), "-")
	if !found {
		return remuxByteRange{}
	}
	firstText, lastText = strings.TrimSpace(firstText), strings.TrimSpace(lastText)
	// A suffix range ("-N") addresses the end of a body whose length is not
	// known until it is generated, so it has no prefix to answer.
	if firstText == "" {
		return remuxByteRange{}
	}
	first, err := strconv.ParseInt(firstText, 10, 64)
	if err != nil || first != 0 {
		// A start offset names a byte position the generation has no media
		// position for; the stream restarts from the plan's anchor instead.
		return remuxByteRange{}
	}
	if lastText == "" {
		// `bytes=0-` is the whole stream.
		return remuxByteRange{}
	}
	last, err := strconv.ParseInt(lastText, 10, 64)
	if err != nil || last < first {
		return remuxByteRange{}
	}
	return remuxByteRange{prefix: true, length: last - first + 1}
}

// remuxRecipeFault classifies FFmpeg's own rejection of the arguments we built.
// An empty answer means the failure is not attributable to the recipe.
func remuxRecipeFault(stderr string) error {
	if !strings.Contains(stderr, "is not supported by the bitstream filter") {
		return nil
	}
	return fmt.Errorf("%w: ffmpeg rejected a bitstream filter: %s", ErrRemuxRecipeFault, strings.TrimSpace(stderr))
}

// remuxInputRefusalCause names one permanent input refusal FFmpeg prints when it
// cannot open or identify the source at all, with the words a viewer-facing
// sentence uses for it.
type remuxInputRefusalCause struct {
	needle   string
	sentence string
}

// remuxInputRefusalCauses are the stderr fragments that mean the input will
// never produce a byte, however long the transport waits: the scheme is not
// buildable, the provider answered an error status, the file is absent or
// unreadable, or the bytes are not media at all.
//
// Only permanent refusals belong here. A transient read error, a slow connect or
// an aborted read is exactly the case the slow-source extension exists for, so
// listing one would turn a play that would have delivered into an abort.
var remuxInputRefusalCauses = []remuxInputRefusalCause{
	{needle: "Protocol not found", sentence: "the input protocol is not available"},
	{needle: "Invalid data found when processing input", sentence: "the source is not a readable media file"},
	{needle: "Server returned 4", sentence: "the source server refused the request"},
	{needle: "Server returned 5", sentence: "the source server failed the request"},
	{needle: "HTTP error 4", sentence: "the source server refused the request"},
	{needle: "HTTP error 5", sentence: "the source server failed the request"},
	{needle: "No such file or directory", sentence: "the source file is missing"},
	{needle: "Permission denied", sentence: "the source file is not readable"},
	{needle: "moov atom not found", sentence: "the source container is truncated"},
	{needle: "could not find codec parameters", sentence: "the source has no readable stream parameters"},
	{needle: "does not contain any stream", sentence: "the source contains no playable stream"},
	{needle: "matches no streams", sentence: "the source contains none of the requested streams"},
}

// remuxInputRefusal reports whether FFmpeg's own stderr names a permanent reason
// the input cannot be read, so the first-output watchdog can indict the delivery
// from evidence instead of from elapsed time. A nil result means "no cause named
// yet", which is what buys a silent process its one extension.
func remuxInputRefusal(stderr string) error {
	for _, cause := range remuxInputRefusalCauses {
		if strings.Contains(stderr, cause.needle) {
			return errors.New(cause.sentence)
		}
	}
	return nil
}

// Kind values for remuxInputKind.
const (
	remuxInputVirtual = "virtual"
	remuxInputRemote  = "remote"
	remuxInputLocal   = "local"
)

// remuxInputKind names the shape of the input a remux was handed, which is the
// first thing to check when ffmpeg produced no output: an unresolved
// "virtual://" identity is not a protocol ffmpeg can open.
func remuxInputKind(filePath string) string {
	lower := strings.ToLower(strings.TrimSpace(filePath))
	switch {
	case strings.HasPrefix(lower, "virtual://"):
		return remuxInputVirtual
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return remuxInputRemote
	default:
		return remuxInputLocal
	}
}

// logRemuxNoOutput records why a remux produced no media byte. FFmpeg's own
// stderr is the only place the reason appears; without it the failure reads as
// a bare EOF and a broken argument, an unsupported input scheme, and a dead
// provider are indistinguishable.
//
// A client that walked away ends the request context, and the owner answers it
// by aborting the remux; the closed pipe that produces is our own teardown, not
// the encoder's or the source's verdict. Those are recorded at debug so a
// normal cancel does not read as an encoder failure (every production instance
// of this warning was a client cancel that had already delivered media) while a
// remux that produced nothing with the request still live keeps the warning.
func logRemuxNoOutput(ctx context.Context, filePath string, session *RemuxSession) {
	attrs := []any{logKeyComponent, logValuePlayback, "input_kind", remuxInputKind(filePath)}
	if session != nil {
		if tail := session.stderr.String(); tail != "" {
			attrs = append(attrs, "ffmpeg_stderr", tail)
		}
	}
	if ctx != nil && ctx.Err() != nil {
		attrs = append(attrs, "reason", "client_canceled")
		slog.DebugContext(ctx, "remux produced no output before the request ended", attrs...)
		return
	}
	slog.WarnContext(ctx, "remux produced no output", attrs...)
}

// writeStartError records a remux failure that happened before any media byte
// reached the client. When deferToCaller is set the failure is left
// uncommitted so a byte handler that can still fail over to a sibling candidate
// owns the final status: committing a 4xx/5xx here locks a v2 response writer
// into its rejected state and discards the retry's body. Callers that cannot
// fail over keep the historical text error.
func writeStartError(w http.ResponseWriter, deferToCaller bool, message string, status int) {
	if deferToCaller {
		return
	}
	http.Error(w, message, status)
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

// appendCopiedAACToMP4Args adds aac_adtstoasc when a copied audio track of
// codec copyAudioCodec is muxed into MP4 (the progressive fMP4 remux and fMP4
// HLS segments). AAC in MPEG-TS (and raw .aac) is ADTS-framed; MP4 needs the
// ADTS headers stripped into a config record. FFmpeg normally inserts this
// filter on its own, but neither under delay_moov nor inside the HLS muxer's
// fMP4 segments: the muxer rejects the first ADTS packet ("Malformed AAC
// bitstream") and the output ends there. The filter passes already-raw AAC
// from MKV/MP4 through unchanged, but it refuses any other codec, so it is only
// added for a copied AAC track. An empty codec (older tokens and recipes)
// keeps the plain copy.
func appendCopiedAACToMP4Args(args []string, copyAudioCodec string) []string {
	if !copiedAudioIsAAC(copyAudioCodec) {
		return args
	}
	return append(args, "-bsf:a", "aac_adtstoasc")
}

func copiedAudioIsAAC(copyAudioCodec string) bool {
	return strings.EqualFold(strings.TrimSpace(copyAudioCodec), "aac")
}

// CopiedAudioADTSCodec answers which copied audio codec a remux must move from
// ADTS framing into MP4, or "" when this copy needs no audio bitstream filter.
//
// Only AAC is ever ADTS-framed, and only in containers that carry it that way:
// MPEG-TS and raw ADTS (and a container the row does not name, which keeps the
// historical answer for older tokens). Matroska, MP4 and WebM store the
// AudioSpecificConfig, so their AAC frames need no filter and are copied
// unmoved. A codec string alone is not enough evidence: a copied E-AC-3,
// TrueHD or DTS track is refused by aac_adtstoasc ("Codec 'eac3' ... is not
// supported by the bitstream filter"), and production reached that with a
// row whose copy codec was mislabeled AAC. Any other codec therefore never
// gets the filter, and neither does an AAC copy whose container cannot frame
// it as ADTS.
func CopiedAudioADTSCodec(codec, container string) string {
	if !copiedAudioIsAAC(codec) {
		return ""
	}
	switch normalizeContainerV3(container) {
	case "", containerMPEGTSV3, containerM2TSV3, containerRawAACV3, containerADTSV3:
		return audioCodecAACV3
	}
	return ""
}

// Container spellings that carry AAC as ADTS. normalizeContainerV3 answers "ts"
// for mpegts; the others are the raw-ADTS names a catalog row or an older token
// can still carry.
const (
	containerMPEGTSV3 = "ts"
	containerM2TSV3   = "m2ts"
	containerRawAACV3 = "aac"
	containerADTSV3   = "adts"
)

// RemuxServeOptions carries the optional serving concerns that not every
// caller sets, keeping the positional argument list from growing further.
// RemuxStreamOriginQueryParam is the query parameter a seeked progressive
// remux URL carries when its plan asked for the resume leading-picture drop:
// the plan's stream origin in seconds (RemuxServeOptions.StreamOriginSeconds).
const RemuxStreamOriginQueryParam = "origin"

// RemuxStreamOrigin returns the stream origin a progressive remux URL
// carries, or nil when it has none or it isn't a finite, non-negative number.
func RemuxStreamOrigin(query url.Values) *float64 {
	origin, err := strconv.ParseFloat(query.Get(RemuxStreamOriginQueryParam), 64)
	if err != nil || origin < 0 || math.IsInf(origin, 0) || math.IsNaN(origin) {
		return nil
	}
	return &origin
}

type RemuxServeOptions struct {
	// StreamOriginSeconds is the plan's stream origin from the URL; see
	// RemuxStreamOrigin. Nil serves the plain copy.
	StreamOriginSeconds *float64
	// DVMode is the explicitly declared Dolby Vision recipe. The zero value
	// decodes as the legacy auto behavior, matching old stream tokens.
	DVMode RemuxDVMode
	// FFmpegPath selects the binary to execute (empty = global discovery).
	FFmpegPath string
	// ContentType overrides the container-derived response type. Audio-only
	// sources mux an audio-only fMP4, which must not be announced as video.
	ContentType string
	// AudioOnly permits the otherwise-mandatory video map to be absent.
	AudioOnly bool
	// SourceAudioChannels identifies a real surround-to-stereo conversion so an
	// already-stereo source keeps its authored level. TargetAudioChannels and
	// TargetAudioBitrateKbps freeze the planned AAC output. Zero target values
	// retain the historical stereo 192 kbps behavior.
	SourceAudioChannels    int
	TargetAudioChannels    int
	TargetAudioBitrateKbps int
	// CopyAudioCodec is the copied audio codec that needs ADTS framing moved
	// into MP4, as answered by CopiedAudioADTSCodec. A copied AAC track gets
	// aac_adtstoasc; every other codec, and AAC that the container already
	// frames with a config record, keeps the plain copy. Empty (older tokens)
	// keeps the plain copy. It is ignored when audio is transcoded.
	CopyAudioCodec string
	// DropResumeLeadingPictures removes open-GOP leading pictures from a
	// seeked video copy when this executor's FFmpeg supports it. It is a
	// best-effort client workaround frozen into the session, so an executor
	// without the filter, or one that predates the field, serves the plain copy.
	DropResumeLeadingPictures bool
	// Abort ends the response early when it is closed. A progressive remux is
	// one long response, so without it the only thing that can stop the stream
	// is the client itself — a server-initiated session stop cannot withdraw a
	// route the client is still being fed. Callers that serve a session pass
	// SessionManager.WatchTransportStop's channel.
	Abort <-chan struct{}
	// DeferStartError returns a failure that happened before FFmpeg produced
	// any media byte to the caller without writing a response, so a byte
	// handler that can still fail over to a sibling candidate owns the final
	// status. Committing a 4xx/5xx here is what locked a v2 response writer
	// into its rejected state and discarded the retry's successful body. The
	// zero value preserves the historical behavior for callers that cannot
	// fail over, such as the proxy and transcode node relays. It has no effect
	// once streaming has begun: a mid-stream failure never reaches this path
	// and still ends the response normally.
	DeferStartError bool
	// TimingStart is when the serving request arrived, used only for the
	// per-seek timing log. Zero omits the handler-setup segment.
	TimingStart time.Time
	// FirstOutputTimeout bounds how long the transport waits for FFmpeg's first
	// media byte. Zero selects remuxFirstOutputTimeout. A relay or provider that
	// accepts the connection but never produces bytes (a dead candidate) would
	// otherwise park the response until the client stops the session (39s was
	// observed in production). On timeout the process is closed and the call
	// returns errRemuxNoOutput, so the caller can rotate or fail fast.
	FirstOutputTimeout time.Duration
}

// remuxFirstOutputTimeout is the default first-media-byte budget for a remux.
// A healthy remote source produces a byte within a few hundred milliseconds;
// this is deliberately loose enough for a slow provider open yet far below the
// time a client will wait before giving up.
var remuxFirstOutputTimeout = 15 * time.Second

// remuxFirstOutputSlowSourceGrace is the one extension a silent but still-live
// remux earns before it is indicted, so the whole first-output window is
// remuxFirstOutputTimeout + this grace (25 s by default).
//
// The evidence for it is the production measurement: cold AltMount providers
// produced their first byte at 15-20 s, and the 15 s watchdog aborted ten of
// those plays in one window — plays that then delivered on the retry. Waiting is
// only ever spent on a process that has named no failure (see
// remuxInputRefusal) and is still running, and the total stays under the client's
// 60 s transport startup guard and its 45 s per-attempt replan budget, so a
// genuinely dead candidate still answers a retryable failure well before the
// viewer gives up.
var remuxFirstOutputSlowSourceGrace = 10 * time.Second

// RemuxContentType returns the override required for an audio-only fMP4.
func RemuxContentType(audioOnly bool) string {
	if audioOnly {
		return AudioOnlyRemuxMIMEV3
	}
	return ""
}

// logRemuxSeekTiming emits one structured line per progressive remux seek so a
// client-side time-to-first-byte can be attributed to handler setup, FFmpeg
// spawn, and FFmpeg's own first output. It is deliberately silent for seek 0
// (an ordinary start) to keep the log to the user-visible seek path.
// logKeyComponent names the slog attribute every playback log line carries.
const logKeyComponent = "component"

// logValuePlayback is the component value for playback log lines, shared so
// repeated slog attribute lists do not duplicate the string literal.
const logValuePlayback = "playback"

func logRemuxSeekTiming(ctx context.Context, seekSeconds float64, filePath, outputFormat string, timingStart, spawnStart, spawnDone, firstByte time.Time) {
	if seekSeconds <= 0 {
		return
	}
	attrs := []any{
		logKeyComponent, "playback",
		"seek_seconds", seekSeconds,
		"output_format", outputFormat,
		"remote_input", isRemoteRemuxInput(filePath),
		"spawn_ms", spawnDone.Sub(spawnStart).Milliseconds(),
	}
	if !timingStart.IsZero() {
		attrs = append(attrs, "handler_ms", spawnStart.Sub(timingStart).Milliseconds())
	}
	if !firstByte.IsZero() {
		attrs = append(attrs,
			"first_byte_ms", firstByte.Sub(spawnDone).Milliseconds(),
			"total_ms", firstByte.Sub(spawnStart).Milliseconds())
		if !timingStart.IsZero() {
			attrs = append(attrs, "request_to_first_byte_ms", firstByte.Sub(timingStart).Milliseconds())
		}
	}
	slog.InfoContext(ctx, "progressive remux seek started", attrs...)
}

func isRemoteRemuxInput(filePath string) bool {
	lower := strings.ToLower(strings.TrimSpace(filePath))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "virtual://")
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
	return ServeRemuxWithOptions(w, r, filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, RemuxServeOptions{DVMode: mode, FFmpegPath: ffmpegPath})
}

// ServeRemuxWithOptions is the full remux transport, taking its optional
// serving concerns as a struct.
func ServeRemuxWithOptions(w http.ResponseWriter, r *http.Request, filePath, outputFormat string, seekSeconds float64, transcodeAudio bool, audioTrackIndex int, dvProfile int, opts RemuxServeOptions) error {
	mode, ffmpegPath := opts.DVMode, opts.FFmpegPath
	// Remux output streams for the length of the title; roll the write
	// deadline with progress instead of the server's absolute WriteTimeout.
	streamWriter := httpstream.NewRollingDeadlineWriter(w)
	w = streamWriter
	// Check file exists before starting ffmpeg to return a proper 404.
	// Headers must be written before streaming begins, so we can't detect
	// ffmpeg errors after WriteHeader(200) has been sent. Remote inputs
	// (resolved provider URLs and the loopback relay) are opened by FFmpeg
	// directly; os.Stat on an HTTP URL would incorrectly return ENOENT and
	// break every virtual remux.
	if !isRemoteRemuxInput(filePath) {
		if _, err := fspath.Stat(filePath); err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "file not found", http.StatusNotFound)
				return err
			}
		}
	}

	contentType := opts.ContentType
	if contentType == "" {
		contentType = containerMIME(outputFormat)
	}
	setStreamHeaders := func() {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Transfer-Encoding", "chunked")
	}
	// A generated stream can answer a prefix range and nothing else: `bytes=0-N`
	// ends the response after N+1 bytes, and every other range (a start offset,
	// a suffix, a multi-range set) gets the whole stream from the generation's
	// start. It is never a fatal status: a browser resumes its own read of the
	// body with a byte offset, and refusing that closes the player instead of
	// costing it a seek (see remuxByteRange).
	byteRange := parseRemuxByteRange(r.Header.Get("Range"))
	// net/http discards a HEAD response's body, so a remux started for one
	// would run ffmpeg over the whole file for nothing. Answer with the
	// headers a GET sends instead.
	if r.Method == http.MethodHead {
		setStreamHeaders()
		w.WriteHeader(http.StatusOK)
		return nil
	}

	session, err := startRemuxWithOptions(r.Context(), filePath, outputFormat, seekSeconds, transcodeAudio, audioTrackIndex, dvProfile, mode, ffmpegPath, opts.AudioOnly, opts.SourceAudioChannels, opts.TargetAudioChannels, opts.TargetAudioBitrateKbps, opts.DropResumeLeadingPictures, opts.CopyAudioCodec)
	if err != nil {
		writeStartError(w, opts.DeferStartError, "failed to start remux", http.StatusBadGateway)
		return err
	}
	defer func() { _ = session.Close() }()

	if opts.Abort != nil {
		// Deferred after session.Close, so it runs before it: the watcher is
		// gone by the time the owner drains and reaps the process.
		served := make(chan struct{})
		watcherDone := make(chan struct{})
		defer func() {
			close(served)
			<-watcherDone
		}()
		go func() {
			defer close(watcherDone)
			select {
			case <-opts.Abort:
				_ = streamWriter.Abort()
				session.Abort()
			case <-served:
			}
		}()
	}

	buf := make([]byte, 32*1024)
	first := make([]byte, 0)
	firstTimeout := opts.FirstOutputTimeout
	if firstTimeout <= 0 {
		firstTimeout = remuxFirstOutputTimeout
	}
	readResult := make(chan struct {
		n   int
		err error
	}, 1)
	go func() {
		n, err := session.Read(buf)
		readResult <- struct {
			n   int
			err error
		}{n, err}
	}()
	var n int
	var readErr error
	// The first-output window has two phases so the watchdog can tell a slow
	// provider from a dead one. Phase one is the ordinary budget: a healthy
	// remote source answers in a few hundred milliseconds, so nothing that is
	// still silent at the budget and has not named a cause is being fast. Phase
	// two is one bounded extension for that case, because a cold provider that
	// takes 15-20 s to open is slowness, not a verdict — aborting it killed plays
	// that would have delivered (ten occurrences in one production window, one
	// of which the client then reported as a non-retryable audio-codec failure).
	// A process that already named a permanent input refusal is indicted at the
	// ordinary budget: waiting cannot change "Protocol not found".
	window := firstTimeout
	extended := false
	for {
		timer := time.NewTimer(window)
		select {
		case result := <-readResult:
			timer.Stop()
			n, readErr = result.n, result.err
		case <-timer.C:
			if !extended && remuxInputRefusal(session.stderr.String()) == nil {
				extended = true
				window = remuxFirstOutputSlowSourceGrace
				slog.InfoContext(r.Context(), "remux first output still pending; extending the window for a live source",
					logKeyComponent, logValuePlayback,
					"input_kind", remuxInputKind(filePath),
					"first_output_timeout", firstTimeout,
					"extension", remuxFirstOutputSlowSourceGrace,
				)
				continue
			}
			session.Abort()
			// Reap before logging: Close is what waits for FFmpeg and for its
			// stderr to be copied, and the deferred Close would otherwise run
			// after the diagnostic line was written.
			_ = session.Close()
			logRemuxNoOutput(r.Context(), filePath, session)
			if fault := remuxRecipeFault(session.stderr.String()); fault != nil {
				writeStartError(w, opts.DeferStartError, "failed to remux", http.StatusBadGateway)
				return fault
			}
			if refusal := remuxInputRefusal(session.stderr.String()); refusal != nil {
				writeStartError(w, opts.DeferStartError, "failed to remux", http.StatusBadGateway)
				return fmt.Errorf("%w: %s: %w", errRemuxNoOutput, refusal.Error(), io.EOF)
			}
			budget := firstTimeout
			if extended {
				budget = firstTimeout + remuxFirstOutputSlowSourceGrace
			}
			err := fmt.Errorf("%w: first output timeout after %s: %w", errRemuxNoOutput, budget, io.EOF)
			writeStartError(w, opts.DeferStartError, "failed to remux", http.StatusBadGateway)
			return err
		}
		break
	}
	if n > 0 {
		first = append(first, buf[:n]...)
	} else if readErr != nil {
		_ = session.Close()
		logRemuxNoOutput(r.Context(), filePath, session)
		if fault := remuxRecipeFault(session.stderr.String()); fault != nil {
			writeStartError(w, opts.DeferStartError, "failed to remux", http.StatusBadGateway)
			return fault
		}
		err := fmt.Errorf("%w: %w", errRemuxNoOutput, readErr)
		writeStartError(w, opts.DeferStartError, "failed to remux", http.StatusBadGateway)
		return err
	}
	setStreamHeaders()
	// A prefix range is answered as a partial response of exactly the bytes it
	// asked for. The complete length is unknown (the body is generated), so the
	// Content-Range carries `*`, and the response ends when the byte count is
	// met — a client that asked for a prefix must not receive the whole title.
	rangeRemaining := int64(-1)
	if byteRange.prefix {
		rangeRemaining = byteRange.length
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/*", byteRange.length-1))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if rangeRemaining >= 0 && int64(len(first)) > rangeRemaining {
		first = first[:rangeRemaining]
	}
	if rangeRemaining >= 0 {
		rangeRemaining -= int64(len(first))
	}
	if _, writeErr := w.Write(first); writeErr != nil {
		return nil
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if rangeRemaining == 0 {
		return nil
	}

	// Stream subsequent ffmpeg output to the HTTP response.
	for {
		n, readErr := session.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if rangeRemaining >= 0 && int64(len(chunk)) > rangeRemaining {
				chunk = chunk[:rangeRemaining]
			}
			if _, writeErr := w.Write(chunk); writeErr != nil {
				return nil // Client disconnected.
			}
			if rangeRemaining >= 0 {
				rangeRemaining -= int64(len(chunk))
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if rangeRemaining == 0 {
				return nil // The requested prefix is complete.
			}
		}
		if readErr != nil {
			return nil // EOF or error — done streaming.
		}
	}
}

//nolint:unused // Retained for compatibility with dormant integration paths.
func isLoopbackRelayInput(filePath string) bool {
	parsed, err := url.Parse(filePath)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil {
		return false
	}
	address := net.ParseIP(parsed.Hostname())
	return address != nil && address.IsLoopback()
}
