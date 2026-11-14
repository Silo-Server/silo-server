package playback

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// StreamExtractOpts configures a single streaming subtitle extract.
type StreamExtractOpts struct {
	// InputPath is the path to the source media file.
	InputPath    string
	Packets      *SubtitlePacketInput
	Embedded     *EmbeddedSubtitleRef
	KeepPath     string
	KeepFormat   string
	KeepOffset   time.Duration
	KeepMaxBytes int64
	// CacheIdentity is a credential-free, stable identity for a transient
	// remote input. When set, subtitle caching uses a bounded generation
	// instead of requiring os.Stat on InputPath.
	CacheIdentity string
	// SourceSizeBytes is the source container's size when the caller knows it
	// (a catalog row's FileSize). It is 0 when unknown, which a virtual row's
	// size commonly is. It decides only whether a bounded bitmap page is worth
	// extracting directly: a container large enough that the prefix read costs
	// a page its whole deadline is served from the track's progressive artifact
	// instead (see subtitle_progressive_sup.go).
	SourceSizeBytes int64
	// SourceDurationSeconds is the source's runtime in seconds, when the caller
	// knows it (a catalog row's Duration). Together with SourceSizeBytes it
	// prices the container's average byte rate, which is what a bounded bitmap
	// page's discarded prefix costs (see subtitle_page_cost.go). Zero is an
	// unknown runtime and simply leaves the prefix to the declared bitrate.
	SourceDurationSeconds float64
	// SourceBitrateKbps is the container's average bitrate in kbps as a probe
	// recorded it (a catalog row's Bitrate). It is the fallback byte rate for
	// pricing a bitmap page's prefix when the size and runtime are not both
	// known. Zero is unknown.
	SourceBitrateKbps int
	// NoteSourceReadRate, when set, receives the byte rate this extract read its
	// input at, measured from the process's own I/O counters. The subtitle cache
	// records it per source identity so a later bitmap page's prefix can be
	// priced in wall-clock terms, not only in bytes. It is called at most once,
	// after the process is reaped, and only when a rate could be measured.
	NoteSourceReadRate func(bytesPerSecond float64)
	// TrackIndex is the subtitle stream ordinal within the container
	// (matches ffmpeg's `0:s:N` specifier). Callers pass the same index
	// they would to ExtractSubtitle.
	TrackIndex int
	// SourceCodec is the codec name reported during probe (e.g. "subrip",
	// "ass"). Controls whether we copy the stream (for ASS, which carries
	// styling) or remux to WebVTT (for everything else).
	SourceCodec string
	// TargetFormat optionally forces a compatible converted artifact (currently
	// WebVTT). Empty preserves the legacy source-driven behavior.
	TargetFormat string
	// SeekSeconds asks ffmpeg to start demuxing at this position. For
	// text-event codecs this is the key win — ffmpeg skips the prefix of
	// the container instead of scanning from byte 0 to produce earlier
	// cues the client will never display. ASS participates when a window is
	// requested (see WindowRequested) or a nonzero position is supplied:
	// each window re-emits a self-contained script header, so a windowed
	// extraction is safe. A zero position is a valid window start when the
	// caller asked for a bounded window; without a window request it
	// preserves the whole-script behavior native ASS renderers rely on.
	SeekSeconds float64
	// WindowRequested marks that the caller explicitly asked for a bounded
	// window (from ?position and/or ?duration) rather than a whole-track
	// fetch. It is deliberately separate from SeekSeconds because
	// position=0 is a valid window start: a client that sends
	// position=0&duration=600 wants bounded output on the source timeline,
	// not the whole script. Callers set this whenever the request carried an
	// explicit position or duration parameter, even when the value is zero.
	// ASS windows iff this is set or SeekSeconds > 0; legacy duration-only
	// direct callers that leave it unset keep whole-track behavior, and PGS
	// still gates windowing on AllowWindow.
	WindowRequested bool
	// DurationSeconds bounds the extract to a window of this length,
	// using an absolute ffmpeg output endpoint. Zero means "until end of file".
	// A bounded window lets the client consume one fetch to completion
	// while keeping memory and in-flight state finite; the client
	// requests subsequent windows as playback approaches the tail.
	DurationSeconds float64
	// AllowWindow lets SeekSeconds/DurationSeconds apply to PGS extracts.
	// By default PGS is never windowed because clients fetch the .sup
	// stream exactly once and consume it whole; a client that explicitly
	// opts in (via ?windowed=1) re-requests fresh windows itself as
	// playback moves outside coverage. ASS does not consult this flag: it
	// windows when WindowRequested is set (or on a nonzero position), so
	// its renderer either receives the complete script or a self-contained
	// slice it asked for.
	AllowWindow bool
	// DisableBackgroundWarm prevents a windowed text or PGS miss from
	// starting a detached full-track extract. Remote relay inputs use
	// request-scoped registrations, so a detached warm must not outlive that
	// registration; the stream handler resolves and holds its own
	// registration for a virtual window miss instead
	// (StreamHandler.warmVirtualSubtitleAfterWindowMiss). Local files leave
	// this false and retain the normal cache-warm behavior.
	DisableBackgroundWarm bool

	// DetachedRelayDemux marks this extract as a whole-track upgrade probe
	// against a track that is already committed or has a full-track fill
	// warming. Its output is the committed full-track artifact the next window
	// (or probe) reads, not a window a client is waiting on, so its relay read
	// draws from the detached demux budget and can never take a slot a
	// foreground window needs. When the detached budget is taken the probe is
	// skipped and answered retryable; the client's probe cadence re-issues it.
	// A cold param-less fetch (a native client's one-shot whole-track request)
	// has no such cadence and stays on the request-path budget. Explicit
	// windowed requests leave this false.
	DetachedRelayDemux bool
	// FreshSourceRetry marks a caller's bounded retry after it replaced
	// InputPath with a newly resolved URL for the same cache identity. The
	// per-identity extraction backoff exists to stop repeated client retries
	// from re-opening a dead provider URL; a deliberate fresh-source retry
	// must not be suppressed by the failure it is recovering from. Only the
	// backoff check is skipped; a failure here still advances the streak.
	FreshSourceRetry bool
	// InputIsExtractedSup marks InputPath as a cached full-track .sup
	// elementary stream (a previous unseeked full extract, so its
	// timestamps are on the playback timeline) rather than the original
	// media container. The input format is forced with `-f sup` — the
	// headerless stream is probeable via its "PG" magic, but an explicit
	// format is robust against probe-size edge cases — and the stream
	// mapping is forced to `0:s:0`: a .sup holds exactly one stream, so
	// TrackIndex (which names the ordinal in the *original* container) no
	// longer applies. Seeking such an input with -copyts re-emits its
	// stored timestamps.
	InputIsExtractedSup bool
	// InputIsExtractedText marks InputPath as a cached full-track text
	// artifact (vtt/ass) produced by a previous non-windowed extract rather
	// than the original media container. The demuxer is forced to the
	// artifact's format and the mapping to `0:s:0` (same single-stream
	// reasoning as InputIsExtractedSup), and a windowed re-extract from it
	// carries absolute timestamps exactly like the .sup path, so a window
	// cut from the cache is byte-compatible with one cut from the source.
	// Empty means InputPath is the original container.
	InputIsExtractedText string
	// PinnedTextArtifact, when non-nil, is a committed full-track text cache
	// entry resolved by ResolveCommittedTextEntry. ServeExtract uses its
	// exact path as the windowed extract input instead of re-resolving, so a
	// caller that skipped track-identity validation based on that artifact
	// is guaranteed to serve the same validated bytes (a generation-bucket
	// rollover between check and read cannot turn it into a miss). If the
	// artifact has been evicted by serve time, ServeExtractWithResult fails
	// before writing a response with ErrCommittedTextArtifactGone so the
	// caller can revalidate and retry.
	PinnedTextArtifact *CommittedTextArtifact
	// FFmpegPath overrides the ffmpeg binary lookup.
	FFmpegPath string
	// Writer receives ffmpeg's stdout bytes as they arrive. When it
	// implements http.Flusher, each chunk is flushed so cues reach the
	// browser in real time.
	Writer io.Writer
}

const MaxDemuxCopyBytes int64 = 128 << 20

func (opts StreamExtractOpts) demuxCopyLimit() int64 {
	if opts.KeepMaxBytes > 0 && opts.KeepMaxBytes < MaxDemuxCopyBytes {
		return opts.KeepMaxBytes
	}
	return MaxDemuxCopyBytes
}

// StreamExtractSubtitle runs ffmpeg to extract a single subtitle track,
// seeked to SeekSeconds, and pipes its stdout to opts.Writer. The process
// exits when ffmpeg finishes; the function returns nil on clean exit or
// an error that includes truncated ffmpeg stderr on failure.
//
// Unlike ExtractSubtitle this does not buffer the full output — the
// writer sees cues as ffmpeg emits them. The first cue typically lands
// within a second even on network storage because the `-ss` input seek
// lets ffmpeg skip most of the container.
func StreamExtractSubtitle(ctx context.Context, opts StreamExtractOpts) error {
	if opts.Writer == nil {
		return errors.New("StreamExtractSubtitle: Writer is required")
	}
	if opts.InputPath == "" {
		return errors.New("StreamExtractSubtitle: InputPath is required")
	}

	bin := opts.FFmpegPath
	if bin == "" {
		bin = "ffmpeg"
	}

	extractCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := fspath.CommandContext(extractCtx, bin, streamExtractArgs(opts)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderrBuf := &strings.Builder{}
	cmd.Stderr = stderrBuf

	start := time.Now()
	wait, err := startSubtitleCommand(extractCtx, cmd, opts.Packets)
	if err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	// A windowed bitmap page that cannot seek its source reads the container
	// from byte 0 to reach the window. Watch the child's own read count so a
	// page that would read hundreds of megabytes to emit a few segments is
	// killed early instead of running to the extraction timeout.
	budget, stopBudget := opts.startPageReadBudget(extractCtx, cmd.Process.Pid, cancel)
	defer stopBudget()

	// Copy stdout → writer with per-chunk flush so the browser receives
	// cues as they're produced rather than at ffmpeg exit. Count the bytes on
	// the way through so the trace reports what actually moved.
	counter := &subtitleCountingWriter{w: opts.Writer}
	copyErr := copyAndFlush(counter, stdout)
	if copyErr != nil {
		// A failed response writer need not cancel the request context (for
		// example, a write deadline). Stop the producer before waiting: it
		// otherwise blocks forever once its unread stdout pipe fills.
		cancel()
	}

	waitErr, feedErr := wait()
	var extractErr error
	switch {
	case waitErr != nil:
		extractErr = waitErr
	case copyErr != nil:
		extractErr = copyErr
	case feedErr != nil:
		extractErr = fmt.Errorf("feed subtitle packets: %w", feedErr)
	}
	outcome := subtitleTraceOutcome(ctx, extractErr)
	level := slog.LevelInfo
	if outcome != SubtitleTraceOutcomeSuccess {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "subtitle stream extract finished",
		subtitleTraceAttrs(ctx, opts.SourceCodec, subtitlePhaseForOutput(opts.SourceCodec, opts.TargetFormat),
			counter.bytes, outcome, time.Since(start),
			"track", opts.TrackIndex, "seek", opts.SeekSeconds, "ffmpeg_err", waitErr)...)

	// Publish the rate this page read the source at, so the next page's prefix
	// can be priced in time as well as in bytes. A page killed by the cap still
	// measured a real rate, and that is exactly the sample worth keeping.
	if rate, ok := budget.observedBytesPerSecond(time.Since(start)); ok && opts.NoteSourceReadRate != nil {
		opts.NoteSourceReadRate(rate)
	}
	// A killed extract reports a signal, which says nothing about why it was
	// killed; the budget is the reason and replaces it.
	if budgetErr := budget.pageReadBudgetError(); budgetErr != nil {
		return budgetErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if copyErr != nil {
		return copyErr
	}
	if waitErr != nil {
		// ExitError with non-zero status is ffmpeg reporting a real
		// problem. Client disconnect (copy failed) manifests as the
		// context being canceled, which surfaces here as ffmpeg being
		// killed — propagate it as a regular cancellation error.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg subtitle stream failed: %w (stderr: %s)",
			waitErr, truncateStderr(stderrBuf.String()))
	}
	return nil
}

// cachedSupSeekSlack is how far before a window's start an extract from a
// cached .sup seeks, so a display set at the start is read whole. Its
// segments are separate packets, and in a cache made from a Blu-ray transport
// stream they keep their own timestamps, decoded ahead of the time the set
// shows (a full-plane object decodes in about 0.13 s). The extra lead-in only
// adds earlier screens, or part of one, which a PGS decoder merges or drops.
const cachedSupSeekSlack = 2.0

// windowFromCachedSup adapts a windowed extract whose input is a cached .sup
// whose first cue is at firstCue seconds. ffmpeg cannot seek to a point
// before a file's first packet: it reports the failed seek, carries on, and
// loses the first display set's presentation segment. A window that starts
// at or before the first cue reads from the start instead and ends where it
// would have; nothing before the first cue exists to leave out.
func windowFromCachedSup(opts StreamExtractOpts, firstCue float64) StreamExtractOpts {
	if opts.SeekSeconds > 0 && opts.SeekSeconds-cachedSupSeekSlack <= firstCue {
		if opts.DurationSeconds > 0 {
			opts.DurationSeconds += opts.SeekSeconds
		}
		opts.SeekSeconds = 0
	}
	return opts
}

// readSupFirstCue returns the time, in seconds, of the first segment in a
// .sup file.
func readSupFirstCue(path string) (float64, error) {
	f, err := fspath.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, 13)
	if _, err := io.ReadFull(f, header); err != nil {
		return 0, err
	}
	if header[0] != 'P' || header[1] != 'G' {
		return 0, errors.New("not a .sup stream")
	}
	return float64(binary.BigEndian.Uint32(header[2:6])) / 90000, nil
}

// streamExtractArgs builds the ffmpeg argument list for a streaming
// subtitle extract.
func streamExtractArgs(opts StreamExtractOpts) []string {
	outCodec, outFormat := streamExtractOutput(opts.SourceCodec, opts.TargetFormat)

	args := []string{
		"-hide_banner", "-nostats", "-loglevel", "error",
	}

	// Input seek (before -i) is the fast variant: ffmpeg jumps near the
	// requested position before demuxing. Text codecs always use it; ASS
	// uses it when a window was requested (WindowRequested) or the caller
	// supplies a nonzero position. PGS defaults to non-windowed: a client
	// that fetches the .sup stream exactly once and consumes it whole needs
	// the complete track from offset 0 — windowing would silently drop every
	// cue outside the window. Clients that manage their own sliding window
	// opt in via AllowWindow; -copyts below keeps the windowed output on
	// absolute source timestamps so cues stay in sync. The same logic
	// governs the -t duration cap below.
	//
	// A window request is not the same as a nonzero position: a client that
	// sends position=0&duration=600 wants the first bounded slice, and
	// treating SeekSeconds==0 as "whole track" made every startup fetch
	// demux the entire file. seekApplied therefore follows the window
	// intent, so explicit-zero emits `-ss 0`.
	//
	// Windowed ASS is safe because the ASS muxer writes the script header
	// (styles and font references) from the container extradata on every
	// extraction, so each window is a self-contained script; -copyts keeps
	// its events on the source timeline, which the client's JASSUB
	// `timeOffset` relies on. Without a window request the whole script is
	// emitted, so native clients that fetch .ass once are unaffected.
	windowable := opts.windowable()
	seekApplied := opts.windowSeekApplied()
	if seekApplied {
		seek := opts.SeekSeconds
		if opts.InputIsExtractedSup {
			seek = max(0, seek-cachedSupSeekSlack)
		}
		args = append(args, "-ss", strconv.FormatFloat(seek, 'f', 3, 64))
		// ffmpeg offsets an input seek by the input's start time. A cached
		// .sup's start time is its first cue, so the seek has to be taken
		// as an absolute timestamp or the window starts that much late.
		if opts.InputIsExtractedSup {
			args = append(args, "-seek_timestamp", "1")
		}
	}

	// A cached .sup input has no container magic worth probing and exactly
	// one stream: force the demuxer and remap to the sole stream ordinal.
	// A cached text artifact (vtt/ass) is the same shape: force the demuxer
	// and remap to its single stream. ffmpeg's WebVTT demuxer is registered
	// as "webvtt" (the cache key spells it "vtt", the muxer name), and the
	// ass demuxer accepts the artifact directly.
	trackIndex := opts.TrackIndex
	switch {
	case opts.InputIsExtractedSup:
		args = append(args, "-f", "sup")
		trackIndex = 0
	case opts.InputIsExtractedText != "":
		inputFormat := opts.InputIsExtractedText
		if inputFormat == SubtitleFormatVTTV3 {
			inputFormat = subtitleMuxerWebVTT
		}
		args = append(args, "-f", inputFormat)
		trackIndex = 0
	}
	args = append(args,
		"-i", opts.InputPath,
		"-map", fmt.Sprintf("0:s:%d", trackIndex),
		"-c:s", outCodec,
	)

	// When we seek the input, keep the cues on the source timeline.
	// Without -copyts ffmpeg rebases cues to start at 0, which makes
	// every cue play `opts.SeekSeconds` earlier than it should — the
	// symptom is subtitles that look "out of sync" with the video the
	// player is showing at the same media time.
	//
	// The playback timeline starts at the container start, which is how
	// an unseeked extract comes out (ffmpeg subtracts the input start
	// time). -start_at_zero makes a seeked extract subtract it too, so
	// windows taken after a seek stay on the same clock as the first one
	// instead of running late by the container's start time (seconds
	// for TS remuxes). A cached .sup was already rebased when it was
	// extracted, and its own start time is its first cue, so it keeps its
	// timestamps as they are, seeked or not; rebasing it would pull every
	// cue early by the first cue's time.
	switch {
	case opts.InputIsExtractedSup:
		args = append(args, "-copyts", "-avoid_negative_ts", "disabled")
	case seekApplied:
		args = append(args, "-copyts", "-start_at_zero", "-avoid_negative_ts", "disabled")
	}
	// Input -t does not bound subtitle-only output: ffmpeg keeps demuxing
	// and emitting cues to EOF. Limit the output instead, using the window's
	// end on the playback timeline, which -copyts keeps after a seek.
	// Using only the duration here would end resumed windows too early (or
	// emit no cues when the seek already exceeds that duration).
	if opts.DurationSeconds > 0 && windowable {
		end := opts.DurationSeconds
		if seekApplied {
			end += opts.SeekSeconds
		}
		args = append(args, "-to", strconv.FormatFloat(end, 'f', 3, 64))
	}

	args = append(args, "-f", outFormat, "pipe:1")
	if opts.KeepPath != "" && !seekApplied {
		args = append(args, "-map", fmt.Sprintf("0:s:%d", trackIndex), "-c:s", "copy", "-output_ts_offset", strconv.FormatFloat(opts.KeepOffset.Seconds(), 'f', 6, 64), "-flush_packets", "1", "-fs", strconv.FormatInt(opts.demuxCopyLimit(), 10))
		if opts.KeepFormat == "mp4" { //nolint:goconst // Container token shared with capability fixtures; names stay inline
			args = append(args, "-movflags", "+faststart")
		}
		args = append(args, "-f", opts.KeepFormat, "file:"+opts.KeepPath)
		args = append([]string{"-hide_banner", "-nostats", "-loglevel", "error", "-y"}, append(args[4:5], args[5:]...)...)
	}
	return args
}

// windowable reports whether the extract's codec/muxer can be sliced by a
// window at all. Text always can; ASS carries self-contained script headers per
// window and requires a window request; PGS is a bitmap elementary stream that
// only windows when the caller explicitly allowed it (AllowWindow). This is the
// codec half of the seek decision, shared by streamExtractArgs and
// WindowCoverage so an advertised range can never disagree with the ffmpeg
// command.
func (o StreamExtractOpts) windowable() bool {
	windowRequested := o.WindowRequested || o.SeekSeconds > 0
	return (!IsPGS(o.SourceCodec) || o.AllowWindow) &&
		(!IsASS(o.SourceCodec) || windowRequested)
}

// ClampOpenEndedWindow bounds an explicitly-started window that carries no
// duration so it cannot extract to end-of-file. A request that supplies a
// position but no duration is authoritative window intent (position=0 included,
// see WindowRequested), but ffmpeg would otherwise emit `-ss <position>` with
// no `-to`, demuxing the rest of the container — the unbounded whole-container
// extract the implicit window exists to prevent. maxDuration fills in the same
// implicit window a whole-track request gets.
//
// It is a no-op for a whole-track request (no position intent), an explicit
// position+duration, a codec that cannot window at all (PGS without
// AllowWindow), and a non-positive cap. Returns true when it set a duration.
func (o *StreamExtractOpts) ClampOpenEndedWindow(maxDuration float64) bool {
	if o == nil || maxDuration <= 0 || o.DurationSeconds > 0 {
		return false
	}
	if !(o.WindowRequested || o.SeekSeconds > 0) {
		return false
	}
	if !o.windowable() {
		return false
	}
	o.DurationSeconds = maxDuration
	return true
}

// windowSeekApplied reports whether the extract both has a window intent and a
// codec/muxer that supports slicing, i.e. whether streamExtractArgs emits
// `-ss`/`-copyts` and therefore returns only a bounded slice of the track. A
// duration-only request on a codec that does not window (legacy ASS callers)
// reads as false even though windowIntent is true.
func (o StreamExtractOpts) windowSeekApplied() bool {
	windowRequested := o.WindowRequested || o.SeekSeconds > 0
	return windowRequested && o.windowable()
}

// SubtitleCoverageHeader carries the source-time range a windowed subtitle
// response covers, as `<start>-<end>` with three-decimal seconds and `*` for an
// open end. It is present only on a bounded response. A client that requested
// the whole track (present in the API as a request with no position/duration)
// uses it to learn that it received a slice and must request subsequent
// windows; a whole-track or committed-artifact response omits it.
const SubtitleCoverageHeader = "X-Subtitle-Coverage"

// SubtitleWindowedHeader is `true` on a bounded subtitle response and absent on
// a whole-track one. It mirrors the presence of SubtitleCoverageHeader for
// clients that only need the boolean.
const SubtitleWindowedHeader = "X-Subtitle-Windowed"

// WindowCoverage reports whether opts produces a bounded window and, if so, the
// source-time range it covers. openEnded is true when the caller supplied a
// start with no duration cap (the extract runs to EOF from the start).
// windowed mirrors the exact `-ss`/`-to` decision streamExtractArgs makes, so
// the advertised range is the range ffmpeg is told to produce.
func (o StreamExtractOpts) WindowCoverage() (windowed bool, startSeconds, endSeconds float64, openEnded bool) {
	if !o.windowIntent() || !o.windowable() {
		return false, 0, 0, false
	}
	seekApplied := o.windowSeekApplied()
	start := o.SeekSeconds
	if !seekApplied {
		// A duration-only text request is bounded by `-to` without an input
		// seek, so it starts at source zero and ends at the duration.
		start = 0
	}
	if o.DurationSeconds > 0 {
		end := o.DurationSeconds
		if seekApplied {
			end = o.SeekSeconds + o.DurationSeconds
		}
		return true, start, end, false
	}
	return true, start, 0, true
}

// SetSubtitleCoverageHeader records the window an extract covers on the
// response. It is a no-op for a whole-track extract, so a client can rely on
// the header's presence to detect a bounded response.
func SetSubtitleCoverageHeader(h http.Header, opts StreamExtractOpts) {
	if h == nil {
		return
	}
	windowed, start, end, openEnded := opts.WindowCoverage()
	if !windowed {
		return
	}
	if openEnded {
		h.Set(SubtitleCoverageHeader, strconv.FormatFloat(start, 'f', 3, 64)+"-*")
		h.Set(SubtitleWindowedHeader, "true")
		// The subtitle routes already answer cross-origin; expose the two
		// markers so a browser-based client (not just a native one) can read
		// them.
		h.Set("Access-Control-Expose-Headers", SubtitleCoverageHeader+", "+SubtitleWindowedHeader)
		return
	}
	SetSubtitleCoverageRange(h, start, end)
}

// SetSubtitleCoverageRange advertises an explicit covered source-time range,
// `<start>-<end>` with three decimals, as the bounded-window markers. It exists
// for responses that describe coverage rather than a produced slice: a
// retryable answer that reports how far the track's progressive bitmap artifact
// has parsed.
func SetSubtitleCoverageRange(h http.Header, start, end float64) {
	if h == nil {
		return
	}
	h.Set(SubtitleCoverageHeader,
		strconv.FormatFloat(start, 'f', 3, 64)+"-"+strconv.FormatFloat(end, 'f', 3, 64))
	h.Set(SubtitleWindowedHeader, "true")
	h.Set("Access-Control-Expose-Headers", SubtitleCoverageHeader+", "+SubtitleWindowedHeader)
}

// windowIntent reports whether the caller asked for a bounded window rather
// than a whole-track fetch. It drives the cache's choice between the
// full-track fill/serve path and the windowed serve path: an explicit
// position=0 (or a duration-only request) is a window, and routing it to the
// full-track path would re-demux the whole source on normal startup.
func (o StreamExtractOpts) windowIntent() bool {
	return o.WindowRequested || o.SeekSeconds > 0 || o.DurationSeconds > 0
}

// PGSWindowRequest reports whether a subtitle request explicitly opts in
// to windowed PGS extraction (?windowed=1) and, if so, the seek position
// and window duration to use. Only explicit query params count — there is
// deliberately no session-position fallback, because a client that did
// not ask for a window expects the complete track from offset 0 and would
// silently lose every cue outside an implicit window. Absent or invalid
// params leave the existing (non-windowed) behavior byte-identical.
//
// Shared by the API stream handler and the standalone proxy so both
// endpoints gate the window identically.
func PGSWindowRequest(q url.Values) (allow bool, seekSeconds, durationSeconds float64) {
	if q.Get("windowed") != "1" {
		return false, 0, 0
	}
	const maxDuration = 3600.0
	if raw := q.Get("position"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
			seekSeconds = v
		}
	}
	if raw := q.Get("duration"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 && v <= maxDuration {
			durationSeconds = v
		}
	}
	return true, seekSeconds, durationSeconds
}

// copyAndFlush streams from src to dst in 32KB chunks, calling Flush on
// dst after each successful write when dst implements http.Flusher.
func copyAndFlush(dst io.Writer, src io.Reader) error {
	flusher, _ := dst.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// streamExtractOutput picks the ffmpeg output codec and muxer format for
// a given source codec. ASS/SSA is copied so styling survives; PGS is
// copied into a .sup elementary stream for client-side bitmap rendering
// (libpgs); everything else is transmuxed to WebVTT for direct `<track>`
// consumption.
func streamExtractOutput(codec string, targetFormat ...string) (outCodec, outFormat string) {
	// A forced WebVTT target only applies to text sources: bitmap codecs
	// carry no text for ffmpeg's webvtt encoder, so honoring the override
	// would build a command that always fails mid-response. Fall through to
	// the source-driven mapping instead (handlers reject bitmap-to-vtt
	// requests before headers are written).
	if len(targetFormat) > 0 && strings.EqualFold(targetFormat[0], "vtt") && !NeedsBurnIn(codec) {
		return "webvtt", "webvtt"
	}
	switch {
	case IsASS(codec):
		return "copy", "ass"
	case IsPGS(codec):
		return "copy", "sup"
	}
	return "webvtt", "webvtt"
}

// IsSubtitleStreamMapError reports whether an ffmpeg subtitle-extract failure
// came from a stream map that named a subtitle ordinal the input does not have
// ("matches no streams" / "for option 'map'"), or from an ordinal whose codec
// family is not the one the plan promised ("sup muxer supports only codec
// hdmv_pgs_subtitle" / "Could not write header"). A virtual release can rotate
// its subtitle layout between planning and extraction, so either failure against
// a relay input means the source rotated rather than that ffmpeg is broken: the
// plan ordinal now names a stream of another class, and the .sup muxer refuses it
// before writing a segment. Conservative by design: only ffmpeg's own diagnostics
// match, so genuine ffmpeg failures stay loud.
func IsSubtitleStreamMapError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "matches no streams") ||
		strings.Contains(message, "for option 'map'") ||
		strings.Contains(message, "muxer supports only codec")
}

// IsSubtitleUpstreamError reports whether an ffmpeg subtitle-extract failure
// came from the input server answering 5xx ("Server returned 5xx") rather
// than from the extraction itself. A virtual relay input 5xxes when its
// upstream flaps; the extraction command is fine and a retry may succeed, so
// callers answer retryable instead of failed. Conservative by design: only
// ffmpeg's upstream-5xx diagnostic matches, so genuine ffmpeg failures stay
// loud. Checked after IsSubtitleStreamMapError: a rotated layout that also
// 5xxes is still a rotation first.
func IsSubtitleUpstreamError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "server returned 5")
}

// LogSubtitleStreamError writes a non-fatal warning for subtitle stream
// failures. Handlers that already committed HTTP headers call this so
// the user sees a truncated subtitle instead of an error response, and
// operators still have a log trail to debug from.
func LogSubtitleStreamError(ctx context.Context, err error, fileID, trackIndex int) {
	if err == nil {
		return
	}
	if ctx.Err() != nil {
		// Normal client disconnect mid-stream — don't warn.
		return
	}
	slog.WarnContext(ctx, "subtitle stream extract failed", "component", "playback",
		"file_id", fileID,
		"track", trackIndex,
		"error", err,
	)
}
