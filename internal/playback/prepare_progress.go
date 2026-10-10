package playback

import (
	"bytes"
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
)

// PrepareProgress is one reading of a prepared-download encode, taken from
// FFmpeg's machine-readable -progress stream.
type PrepareProgress struct {
	// EncodedSeconds is how much of the output timeline FFmpeg has written.
	EncodedSeconds float64
	// DurationSeconds is the source duration progress is measured against.
	// Zero means unknown, so no percentage can be derived.
	DurationSeconds float64
	// Speed is the encode rate as a multiple of realtime; zero when FFmpeg
	// has not reported one yet.
	Speed float64
}

// PrepareProgressSink receives readings while PrepareFile runs. Calls arrive
// from the goroutine copying FFmpeg's stdout, one encode at a time.
type PrepareProgressSink interface {
	PrepareProgress(PrepareProgress)
}

// DownloadPrepareLogSessionID is the operational-log session key for one
// prepared-download artifact. Every attempt, local or on a node, logs under
// it, so an admin can read a job's FFmpeg output as one stream.
func DownloadPrepareLogSessionID(artifactID string) string {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return ""
	}
	return "download-prepare-" + artifactID
}

// prepareProgressWriter parses FFmpeg's key=value -progress stream. Each block
// ends with a progress= line, which is when a reading is emitted.
type prepareProgressWriter struct {
	sink     PrepareProgressSink
	duration float64
	pending  []byte
	current  PrepareProgress
}

func newPrepareProgressWriter(sink PrepareProgressSink, duration float64) *prepareProgressWriter {
	if duration < 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		duration = 0
	}
	return &prepareProgressWriter{sink: sink, duration: duration, current: PrepareProgress{DurationSeconds: duration}}
}

func (w *prepareProgressWriter) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		w.line(string(bytes.TrimSpace(w.pending[:i])))
		w.pending = w.pending[i+1:]
	}
	// FFmpeg never writes a progress line anywhere near this long; a stream
	// that does is not the progress protocol, so stop buffering it.
	if len(w.pending) > 4096 {
		w.pending = w.pending[:0]
	}
	return len(p), nil
}

func (w *prepareProgressWriter) line(line string) {
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return
	}
	switch strings.TrimSpace(key) {
	case "out_time_us", "out_time_ms":
		// Both keys carry microseconds; out_time_ms is a historical misnomer.
		if micros, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && micros >= 0 {
			w.current.EncodedSeconds = float64(micros) / 1e6
		}
	case "speed":
		raw := strings.TrimSuffix(strings.TrimSpace(value), "x")
		if speed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil && speed > 0 && !math.IsInf(speed, 0) {
			w.current.Speed = speed
		}
	case "progress":
		if w.sink == nil {
			return
		}
		reading := w.current
		if strings.TrimSpace(value) == "end" && reading.DurationSeconds > 0 {
			reading.EncodedSeconds = reading.DurationSeconds
		}
		w.sink.PrepareProgress(reading)
	}
}

// prepareLogWriter forwards FFmpeg's stderr to the operational log sink, one
// line per record, under the same caps as streaming transcodes.
type prepareLogWriter struct {
	ctx       context.Context
	sink      FFmpegLogSink
	sessionID string
	attrs     FFmpegLogAttrs

	mu      sync.Mutex
	pending []byte
	lines   int
	bytes   int
	dropped int
	capped  bool
}

func newPrepareLogWriter(ctx context.Context, opts TranscodeOpts) *prepareLogWriter {
	if opts.FFmpegLogSink == nil || opts.SessionID == "" {
		return nil
	}
	return &prepareLogWriter{
		ctx:       ctx,
		sink:      opts.FFmpegLogSink,
		sessionID: opts.SessionID,
		attrs:     prepareLogAttrs(opts),
	}
}

func prepareLogAttrs(opts TranscodeOpts) FFmpegLogAttrs {
	executionMode := opts.ExecutionMode
	if executionMode == "" {
		executionMode = "download_prepare"
	}
	return FFmpegLogAttrs{
		NodeType:         opts.NodeType,
		ExecutionMode:    executionMode,
		InputPath:        opts.InputPath,
		TargetResolution: opts.TargetResolution,
		TargetVideoCodec: opts.TargetCodecVideo,
		TargetAudioCodec: opts.TargetCodecAudio,
		HWAccel:          opts.HWAccel,
	}
}

func (w *prepareLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		w.lineLocked(string(w.pending[:i]))
		w.pending = w.pending[i+1:]
	}
	if len(w.pending) > maxPersistedFFmpegChars {
		w.lineLocked(string(w.pending))
		w.pending = w.pending[:0]
	}
	return len(p), nil
}

func (w *prepareLogWriter) flush() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 {
		w.lineLocked(string(w.pending))
		w.pending = w.pending[:0]
	}
}

func (w *prepareLogWriter) lineLocked(line string) {
	line = strings.ToValidUTF8(strings.TrimRight(line, "\r"), "�")
	if strings.TrimSpace(line) == "" {
		return
	}
	trimmed, truncated := truncateUTF8String(line, maxPersistedFFmpegChars)
	if truncated {
		trimmed += "...[truncated]"
	}
	if w.lines >= maxPersistedFFmpegLines || w.bytes+len(trimmed) > maxPersistedFFmpegBytes {
		w.dropped++
		if !w.capped {
			w.capped = true
			attrs := w.attrs
			attrs.DroppedLines = w.dropped
			w.sink.WriteEvent(w.ctx, w.sessionID, attrs, "ffmpeg stderr logging capped")
		}
		return
	}
	w.lines++
	w.bytes += len(trimmed)
	attrs := w.attrs
	attrs.LineIndex = w.lines
	w.sink.WriteLine(w.ctx, w.sessionID, attrs, trimmed)
}

func (w *prepareLogWriter) event(message, exitError string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	attrs := w.attrs
	w.mu.Unlock()
	attrs.ExitError = exitError
	w.sink.WriteEvent(w.ctx, w.sessionID, attrs, message)
}

func (w *prepareLogWriter) setHWAccel(hwAccel string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.attrs.HWAccel = hwAccel
	w.mu.Unlock()
}
