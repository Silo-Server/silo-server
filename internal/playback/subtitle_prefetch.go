package playback

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// SubtitlePrefetchWindow names one bounded source-time window that
// PrefetchWindowsInBackground may extract and keep as a last-good window.
type SubtitlePrefetchWindow struct {
	StartSeconds    float64
	DurationSeconds float64
}

// subtitlePrefetchMaxWindows bounds the work one prefetch kick may do. A
// prefetch exists to cover the viewer's next page or two; anything wider is
// the full-track warm's job (or the client's own window requests).
const subtitlePrefetchMaxWindows = 2

// PrefetchWindowsInBackground fills the bounded window cache for the windows a
// mid-file playhead is about to need, using the ordinary windowed extract path.
//
// It is the safe alternative to starting a full-track warm mid-file. A
// full-track warm always extracts from source zero and commits under the
// whole-track artifact key; starting one at a playhead would publish a tail
// slice as the complete track, with offsets that do not match the source
// timeline and without the cues before the seek. A bounded prefetch never
// commits an artifact: each window goes through ServeExtractWithResult, which
// stores the slice under windowResultKey (or its covering-window family) and
// lets fix 2's covering lookup serve the next request without another extract.
//
// The prefetch is deliberately opportunistic, like every other detached warm:
//
//   - It skips while the request path is saturated for the source — every slot
//     held, or a client read shed inside the quiet window — so it never adds a
//     demux to a relay pipe that is already refusing client reads. The check
//     runs before the kick and before every window, so capacity that fills
//     after the kick still stops the remaining windows.
//
//   - It draws from the detached admission budget and shares warmSem with the
//     full-track warms, so a burst of kicks and an idle warm cannot stack
//     background demuxes server-wide. No capacity means the kick is dropped; the
//     next window miss re-attempts it.
//
//   - At most subtitlePrefetchMaxWindows windows run per kick, and identical
//     concurrent windows coalesce through the cache's window flight.
//
// windows beyond the cap are ignored. The returned channel closes exactly once
// when the prefetch settles or is skipped.
func (c *SubtitleCache) PrefetchWindowsInBackground(opts StreamExtractOpts, windows []SubtitlePrefetchWindow, extract SUPExtractFunc, requestID string) <-chan struct{} {
	done := make(chan struct{})
	format := subtitleCacheFormat(opts.SourceCodec, opts.TargetFormat)
	if c == nil || extract == nil || c.dir() == "" || format == "" || len(windows) == 0 {
		// Logged rather than returned silently: without it a kick that could
		// not run is indistinguishable from one that is still extracting, and
		// production had no way to tell whether this path was reached at all.
		reason := "cache unavailable"
		switch {
		case len(windows) == 0:
			reason = "no windows"
		case extract == nil:
			reason = "no extractor"
		case format == "":
			reason = "unknown format"
		}
		logSubtitlePrefetchSkip(reason, opts, requestID)
		close(done)
		return done
	}
	if len(windows) > subtitlePrefetchMaxWindows {
		windows = windows[:subtitlePrefetchMaxWindows]
	}
	// A prefetch a refused request is waiting on is not opportunistic: it is the
	// extraction that makes the client's retry a cache hit instead of another
	// full relay read. It therefore skips the opportunistic shedding gates and
	// admits through the reserved recovery budget; the shed it used to take here
	// is what left production retries paying the same 26 s read again.
	recovery := opts.WaitingRelayDemux
	if !recovery && relayDemuxDetachedBlocked(opts.InputPath, subtitleWarmRequestShedQuietWindow) {
		slog.Debug("subtitle window prefetch skipped: request-path admission is shedding",
			"input", opts.InputPath, "track", opts.TrackIndex, "format", format)
		close(done)
		return done
	}
	select {
	case c.warmSem <- struct{}{}:
	default:
		slog.Debug("subtitle window prefetch skipped: all warm slots busy",
			"input", opts.InputPath, "track", opts.TrackIndex, "format", format)
		close(done)
		return done
	}

	// The prefetch is always detached and never starts a full-track fill of its
	// own: the window slice is the only output, and a mid-file fill under the
	// artifact key is exactly what this path must not create.
	opts.DisableBackgroundWarm = true
	opts.DetachedRelayDemux = true
	opts.PinnedTextArtifact = nil
	opts.Writer = nil
	opts.SeekSeconds = 0
	opts.DurationSeconds = 0
	opts.WindowRequested = false
	opts.AllowWindow = false
	opts.InputIsExtractedSup = false
	opts.InputIsExtractedText = ""
	windows = append([]SubtitlePrefetchWindow(nil), windows...)

	logSubtitlePrefetchStart(opts, format, windows, requestID)

	go func() {
		defer close(done)
		defer func() { <-c.warmSem }()
		prefetched := 0
		defer func() { logSubtitlePrefetchSettled(opts, format, len(windows), prefetched, requestID) }()
		for _, window := range windows {
			if window.DurationSeconds <= 0 {
				continue
			}
			if !recovery && relayDemuxDetachedBlocked(opts.InputPath, subtitleWarmRequestShedQuietWindow) {
				slog.Debug("subtitle window prefetch stopped: request-path admission is shedding",
					"input", opts.InputPath, "track", opts.TrackIndex, "format", format)
				return
			}
			windowOpts := opts
			windowOpts.SeekSeconds = window.StartSeconds
			windowOpts.DurationSeconds = window.DurationSeconds
			windowOpts.WindowRequested = true
			// PGS windows only when AllowWindow is set; text and ASS honor
			// WindowRequested. The cache still stores the slice under
			// windowResultKey, never under the whole-track artifact key.
			windowOpts.AllowWindow = true
			key, ok := c.windowResultKey(windowOpts, format)
			if !ok {
				continue
			}
			if _, cached := c.lookupWindowResult(key); cached {
				continue
			}
			if _, covering := c.lookupCoveringWindowResult(windowResultKeyBase(key), windowOpts); covering {
				continue
			}
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
			if err != nil {
				return
			}
			start := time.Now()
			if _, err := c.ServeExtractWithResult(&subtitlePrefetchWriter{header: make(http.Header)}, req, windowOpts, extract); err != nil {
				// One failure ends the kick: a rekey, a shed detached slot, or
				// an upstream failure all mean the next window miss is a better
				// trigger than another demux here. A recovery kick is the one
				// case a viewer is waiting on, so its failure warns instead of
				// staying at debug: the retry will pay the full read again.
				level := slog.LevelDebug
				if recovery {
					level = slog.LevelWarn
				}
				slog.Log(context.Background(), level, "subtitle window prefetch stopped",
					"input", opts.InputPath, "track", opts.TrackIndex, "format", format,
					"start", window.StartSeconds, "duration", window.DurationSeconds,
					"recovery", recovery,
					"request_id", requestID, "error", err)
				return
			}
			slog.Debug("subtitle window prefetched",
				"input", opts.InputPath, "track", opts.TrackIndex, "format", format,
				"start", window.StartSeconds, "duration", window.DurationSeconds,
				"elapsed", time.Since(start), "request_id", requestID)
			prefetched++
		}
	}()
	return done
}

// logSubtitlePrefetchStart marks a kick that is about to extract. A prefetch
// runs for as long as its windows take (tens of seconds to the window timeout)
// and every per-window line is written only once that window settles, so
// without this line a running prefetch is invisible in production — which is
// indistinguishable from a kick that never ran.
func logSubtitlePrefetchStart(opts StreamExtractOpts, format string, windows []SubtitlePrefetchWindow, requestID string) {
	slog.Debug("subtitle window prefetch starting",
		"input", opts.InputPath, "track", opts.TrackIndex, "format", format,
		"windows", len(windows),
		"start", windows[0].StartSeconds, "duration", windows[0].DurationSeconds,
		"request_id", requestID)
}

// logSubtitlePrefetchSettled closes a kick's story: how many windows it
// produced and how many it skipped as already covered (the difference), so a
// prefetch that always skips is visible as such.
func logSubtitlePrefetchSettled(opts StreamExtractOpts, format string, windows, prefetched int, requestID string) {
	slog.Debug("subtitle window prefetch settled",
		"input", opts.InputPath, "track", opts.TrackIndex, "format", format,
		"windows", windows, "prefetched", prefetched, "request_id", requestID)
}

// logSubtitlePrefetchSkip reports a kick that could not run at all. It is a
// separate function so the kick itself stays under the package's function-length
// limit.
func logSubtitlePrefetchSkip(reason string, opts StreamExtractOpts, requestID string) {
	slog.Debug("subtitle window prefetch skipped",
		"reason", reason, "input", opts.InputPath, "track", opts.TrackIndex, "request_id", requestID)
}

// subtitlePrefetchWriter is a sink for a prefetched window. The window capture
// in ServeExtractWithResult keeps the bytes; nothing is delivered to a client,
// so Write only reports success.
type subtitlePrefetchWriter struct {
	header http.Header
}

func (w *subtitlePrefetchWriter) Header() http.Header { return w.header }

func (w *subtitlePrefetchWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *subtitlePrefetchWriter) WriteHeader(int) {}
