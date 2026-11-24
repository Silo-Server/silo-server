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
	// ShortenDuration sets the file's duration to seconds when it is still
	// from, the duration the transcode was planned with, is longer than
	// seconds, and was last probed before probedBefore, and reports whether
	// it did.
	ShortenDuration(ctx context.Context, mediaFileID, from, seconds int, probedBefore time.Time) (bool, error)
}

// StreamEnd is where a transcode found a media file's media ends.
type StreamEnd struct {
	// Seconds is where the media ends.
	Seconds float64
	// PlannedSeconds is the stored duration the transcode was planned with.
	PlannedSeconds float64
	// ReadSince is no later than when the run that measured the end started
	// reading the file. A file probed after it may have been replaced since,
	// even under the same stored duration.
	ReadSince time.Time
}

// maxNotedStreamEnds bounds StreamEndRecorder.noted; past it the record
// starts over.
const maxNotedStreamEnds = 4096

// notedStreamEndTTL is how long a written end suppresses the same write. A
// node reports a session's end every second; after this long, a duration a
// later probe stored again can be corrected again.
const notedStreamEndTTL = 10 * time.Minute

type notedStreamEnd struct {
	from, seconds int
	at            time.Time
}

// StreamEndRecorder stores where a transcode that read a file to its end
// found the media ends, when the stored duration runs longer. The synthetic
// playlist of every later transcode then ends with the media instead of
// listing segments no run writes. It only ever shortens a duration, writes in
// the background, and writes each value once per process. A nil recorder
// records nothing.
type StreamEndRecorder struct {
	store StreamEndStore
	mu    sync.Mutex
	// noted is the write last made per media file.
	noted map[int]notedStreamEnd
}

// NewStreamEndRecorder returns a recorder that writes to store, or nil when
// store is nil.
func NewStreamEndRecorder(store StreamEndStore) *StreamEndRecorder {
	if store == nil {
		return nil
	}
	return &StreamEndRecorder{store: store}
}

// Note records where mediaFileID's media ends. The write applies only while
// the file still has the duration the transcode was planned with and was not
// probed since the measuring run started reading it, so an end measured on a
// file since replaced or probed again changes nothing.
func (r *StreamEndRecorder) Note(ctx context.Context, mediaFileID int, end StreamEnd) {
	if r == nil || mediaFileID <= 0 || !(end.Seconds > 0) || math.IsInf(end.Seconds, 0) || !(end.PlannedSeconds > end.Seconds) || end.ReadSince.IsZero() {
		return
	}
	// Rounded up, as the scanner stores a span: the last partial second
	// holds the last segment. The planned duration is a stored whole number.
	seconds, from := int(math.Ceil(end.Seconds)), int(math.Round(end.PlannedSeconds))
	if seconds >= from {
		// The end rounds up to the stored duration: nothing to shorten, as
		// for most transcodes a node reports once they finish.
		return
	}
	now := time.Now()
	r.mu.Lock()
	if last, ok := r.noted[mediaFileID]; ok && last.from == from && last.seconds == seconds && now.Sub(last.at) < notedStreamEndTTL {
		r.mu.Unlock()
		return
	}
	if r.noted == nil || len(r.noted) >= maxNotedStreamEnds {
		r.noted = make(map[int]notedStreamEnd)
	}
	r.noted[mediaFileID] = notedStreamEnd{from: from, seconds: seconds, at: now}
	r.mu.Unlock()

	go func() {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		shortened, err := r.store.ShortenDuration(writeCtx, mediaFileID, from, seconds, end.ReadSince)
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
