package playback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// StreamExtractOpts configures a single streaming subtitle extract.
type StreamExtractOpts struct {
	// InputPath is the path to the source media file.
	InputPath string
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
	// cues the client will never display. Ignored for ASS because ASS
	// output needs the script header that only appears at offset 0.
	SeekSeconds float64
	// DurationSeconds bounds the extract to a window of this length
	// (passed as ffmpeg's `-t`). Zero means "until end of file". A
	// bounded window lets the client consume one fetch to completion
	// while keeping memory and in-flight state finite; the client
	// requests subsequent windows as playback approaches the tail.
	DurationSeconds float64
	// AllowWindow lets SeekSeconds/DurationSeconds apply to PGS extracts.
	// By default PGS is never windowed because clients fetch the .sup
	// stream exactly once and consume it whole; a client that explicitly
	// opts in (via ?windowed=1) re-requests fresh windows itself as
	// playback moves outside coverage. ASS ignores this flag — its
	// [Script Info] header exists only at stream offset 0, so a seeked
	// extract would be structurally broken.
	AllowWindow bool
	// InputIsExtractedSup marks InputPath as a cached full-track .sup
	// elementary stream (a previous full extract, produced with -copyts so
	// its timestamps are absolute source PTS) rather than the original
	// media container. The input format is forced with `-f sup` — the
	// headerless stream is probeable via its "PG" magic, but an explicit
	// format is robust against probe-size edge cases — and the stream
	// mapping is forced to `0:s:0`: a .sup holds exactly one stream, so
	// TrackIndex (which names the ordinal in the *original* container) no
	// longer applies. Seeking such an input with -copyts re-emits the same
	// absolute timestamps, so windowed output is byte-compatible with a
	// window cut from the original file.
	InputIsExtractedSup bool
	// FFmpegPath overrides the ffmpeg binary lookup.
	FFmpegPath string
	// Writer receives ffmpeg's stdout bytes as they arrive. When it
	// implements http.Flusher, each chunk is flushed so cues reach the
	// browser in real time.
	Writer io.Writer
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

	cmd := exec.CommandContext(ctx, bin, streamExtractArgs(opts)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderrBuf := &strings.Builder{}
	cmd.Stderr = stderrBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	// Copy stdout → writer with per-chunk flush so the browser receives
	// cues as they're produced rather than at ffmpeg exit.
	copyErr := copyAndFlush(opts.Writer, stdout)

	waitErr := cmd.Wait()
	slog.DebugContext(ctx, "subtitle stream extract finished", "component", "playback",
		"track", opts.TrackIndex,
		"seek", opts.SeekSeconds,
		"elapsed_ms", time.Since(start).Milliseconds(),
		"ffmpeg_err", waitErr,
	)

	if waitErr != nil {
		// ExitError with non-zero status is ffmpeg reporting a real
		// problem. Client disconnect (copy failed) manifests as the
		// context being cancelled, which surfaces here as ffmpeg being
		// killed — propagate it as a regular cancellation error.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffmpeg subtitle stream failed: %w (stderr: %s)",
			waitErr, truncateStderr(stderrBuf.String()))
	}
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return copyErr
	}
	return nil
}

// streamExtractArgs builds the ffmpeg argument list for a streaming
// subtitle extract.
func streamExtractArgs(opts StreamExtractOpts) []string {
	outCodec, outFormat := streamExtractOutput(opts.SourceCodec, opts.TargetFormat)

	args := []string{
		"-hide_banner", "-nostats", "-loglevel", "error",
	}

	// Input seek (before -i) is the fast variant: ffmpeg jumps near the
	// requested position before demuxing. ASS can't use it because the
	// output needs the [Script Info] header which only sits at offset 0.
	// PGS defaults to non-windowed too: a client that fetches the .sup
	// stream exactly once and consumes it whole needs the complete track
	// from offset 0 — windowing would silently drop every cue outside
	// the window. Clients that manage their own sliding window opt in
	// via AllowWindow; -copyts below keeps the windowed output on
	// absolute source timestamps so cues stay in sync. The same logic
	// governs the -t duration cap below.
	windowable := !IsASS(opts.SourceCodec) && (!IsPGS(opts.SourceCodec) || opts.AllowWindow)
	seekApplied := opts.SeekSeconds > 0 && windowable
	if seekApplied {
		args = append(args, "-ss", strconv.FormatFloat(opts.SeekSeconds, 'f', 3, 64))
	}

	// Duration limit must be an *input* option (placed before -i) so it
	// caps how much of the file we read. Placed as an output option, -t
	// combined with -copyts stops output when PTS reaches the given
	// value — which with a non-zero seek is already in the past, so
	// ffmpeg would emit only the WEBVTT header and zero cues.
	if opts.DurationSeconds > 0 && windowable {
		args = append(args, "-t", strconv.FormatFloat(opts.DurationSeconds, 'f', 3, 64))
	}

	// A cached .sup input has no container magic worth probing and exactly
	// one stream: force the demuxer and remap to the sole stream ordinal.
	trackIndex := opts.TrackIndex
	if opts.InputIsExtractedSup {
		args = append(args, "-f", "sup")
		trackIndex = 0
	}
	args = append(args,
		"-i", opts.InputPath,
		"-map", fmt.Sprintf("0:s:%d", trackIndex),
		"-c:s", outCodec,
	)

	// When we seek the input, preserve the absolute source timestamps
	// in the output. Without this ffmpeg rebases cues to start at 0,
	// which makes every cue play `opts.SeekSeconds` earlier than it
	// should — the symptom is subtitles that look "out of sync" with
	// the video the player is showing at the same media time.
	if seekApplied {
		args = append(args, "-copyts", "-avoid_negative_ts", "disabled")
	}

	return append(args,
		"-f", outFormat,
		"pipe:1",
	)
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
