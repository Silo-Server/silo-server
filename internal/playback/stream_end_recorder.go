package playback

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"
)

// StreamEndStore shortens a media file's stored duration to where a
// transcode found its media ends.
type StreamEndStore interface {
	// ShortenDuration sets the file's duration to seconds when the stored
	// duration is longer, and reports whether it did.
	ShortenDuration(ctx context.Context, mediaFileID, seconds int) (bool, error)
}

// maxNotedStreamEnds bounds StreamEndRecorder.noted; past it the record
// starts over.
const maxNotedStreamEnds = 4096

// StreamEndRecorder stores where a transcode that read a file to its end
// found the media ends, when the stored duration runs longer. The synthetic
// playlist of every later transcode then ends with the media instead of
// listing segments no run writes. It only ever shortens a duration, writes in
// the background, and writes each value once per process. A nil recorder
// records nothing.
type StreamEndRecorder struct {
	store StreamEndStore
	mu    sync.Mutex
	// noted is the duration last written per media file.
	noted map[int]int
}

// NewStreamEndRecorder returns a recorder that writes to store, or nil when
// store is nil.
func NewStreamEndRecorder(store StreamEndStore) *StreamEndRecorder {
	if store == nil {
		return nil
	}
	return &StreamEndRecorder{store: store}
}

// Note records that mediaFileID's media ends at endSeconds.
func (r *StreamEndRecorder) Note(ctx context.Context, mediaFileID int, endSeconds float64) {
	if r == nil || mediaFileID <= 0 || !(endSeconds > 0) || math.IsInf(endSeconds, 0) {
		return
	}
	// Rounded up, as the scanner stores a span: the last partial second
	// holds the last segment.
	seconds := int(math.Ceil(endSeconds))
	r.mu.Lock()
	if r.noted[mediaFileID] == seconds {
		r.mu.Unlock()
		return
	}
	if r.noted == nil || len(r.noted) >= maxNotedStreamEnds {
		r.noted = make(map[int]int)
	}
	r.noted[mediaFileID] = seconds
	r.mu.Unlock()

	go func() {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		shortened, err := r.store.ShortenDuration(writeCtx, mediaFileID, seconds)
		switch {
		case err != nil:
			r.mu.Lock()
			delete(r.noted, mediaFileID)
			r.mu.Unlock()
			slog.WarnContext(writeCtx, "store a transcode's stream end", "component", "playback", "media_file_id", mediaFileID, "error", err)
		case shortened:
			slog.InfoContext(writeCtx, "stored duration shortened to where a transcode found the media ends",
				"component", "playback", "media_file_id", mediaFileID, "duration_seconds", seconds)
		}
	}()
}
