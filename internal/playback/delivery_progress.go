package playback

import (
	"math"
	"strings"
	"sync"
	"time"
)

// Delivery progress tells an administrator how far a stream has been sent
// ahead of its viewer. Direct play records which media time the file bytes it
// served cover; HLS reads the segments the client finished downloading and the
// segments ffmpeg produced. Both are observations for the admin dashboard, not
// inputs to any playback decision.

// deliveredSpanSlots bounds the delivered intervals a session keeps. A player
// reads a handful of disjoint ranges (the file head, an index at the tail,
// one run per seek), so a few slots hold every range that matters.
const deliveredSpanSlots = 8

// deliveredPositionTolerance is how far outside an interval the position may
// be and still count as inside it: a position report and a delivery report are
// sampled at different moments, and the byte-to-time mapping is an estimate.
const deliveredPositionTolerance = 2.0

// MediaSpan is a range of media time in seconds.
type MediaSpan struct {
	Start float64
	End   float64
}

// deliveredSpans is the delivered media time of a session's current file. It
// is a fixed-size value so a Session copy never shares it.
type deliveredSpans struct {
	fileID int
	count  int
	spans  [deliveredSpanSlots]MediaSpan
}

// note merges span into the set. When the set is full, the span farthest from
// position is dropped, so the ranges around the viewer survive tail reads and
// seeks.
func (d *deliveredSpans) note(span MediaSpan, position float64) {
	merged := span
	kept := d.spans[:0]
	for _, existing := range d.spans[:d.count] {
		if existing.Start <= merged.End && merged.Start <= existing.End {
			merged = MediaSpan{Start: math.Min(existing.Start, merged.Start), End: math.Max(existing.End, merged.End)}
			continue
		}
		kept = append(kept, existing)
	}
	if len(kept) == deliveredSpanSlots {
		farthest := 0
		for i := range kept {
			if spanDistance(kept[i], position) > spanDistance(kept[farthest], position) {
				farthest = i
			}
		}
		if spanDistance(merged, position) >= spanDistance(kept[farthest], position) {
			d.count = len(kept)
			return
		}
		kept = append(kept[:farthest], kept[farthest+1:]...)
	}
	kept = append(kept, merged)
	d.count = len(kept)
}

func (d *deliveredSpans) list() []MediaSpan {
	return d.spans[:d.count]
}

func spanDistance(span MediaSpan, position float64) float64 {
	switch {
	case position < span.Start:
		return span.Start - position
	case position > span.End:
		return position - span.End
	default:
		return 0
	}
}

// DeliveredThroughAt returns the end of the span that contains position,
// within deliveredPositionTolerance. A plain maximum would be wrong: players
// read an index at the end of the file and keep ranges from before a seek, and
// neither says anything about the media ahead of the viewer.
func DeliveredThroughAt(spans []MediaSpan, position float64) (float64, bool) {
	through, found := 0.0, false
	for _, span := range spans {
		if position < span.Start-deliveredPositionTolerance || position > span.End+deliveredPositionTolerance {
			continue
		}
		if !found || span.End > through {
			through, found = span.End, true
		}
	}
	return through, found
}

// DeliveredMediaSpan maps the byte range [start, end) of a file of size bytes
// to media time, assuming a constant bitrate across durationSeconds. It
// reports false when the size or duration is unknown.
func DeliveredMediaSpan(start, end, size int64, durationSeconds float64) (MediaSpan, bool) {
	if size <= 0 || !(durationSeconds > 0) || end <= start || start < 0 {
		return MediaSpan{}, false
	}
	end = min(end, size)
	if end <= start {
		return MediaSpan{}, false
	}
	return MediaSpan{
		Start: float64(start) / float64(size) * durationSeconds,
		End:   float64(end) / float64(size) * durationSeconds,
	}, true
}

// NoteDelivered records that the session's file fileID has been delivered
// across span. Spans recorded for another file are discarded first.
func (m *SessionManager) NoteDelivered(sessionID string, fileID int, span MediaSpan) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if s.delivered.fileID != fileID {
		s.delivered = deliveredSpans{fileID: fileID}
	}
	s.delivered.note(span, s.Position)
	return nil
}

// DeliveredThroughSeconds returns how far the session's current file has been
// delivered past the playback position, in media seconds, when a direct-play
// transport on this server recorded it.
func (s *Session) DeliveredThroughSeconds() (float64, bool) {
	if s == nil || s.delivered.count == 0 || s.delivered.fileID != s.MediaFileID {
		return 0, false
	}
	return DeliveredThroughAt(s.delivered.list(), s.Position)
}

// DeliveryNoter records delivered media time on a playback session.
type DeliveryNoter interface {
	NoteDelivered(sessionID string, fileID int, span MediaSpan) error
}

// DirectPlayDeliveryRecorder returns a DirectPlayServeOptions.OnDelivered
// callback that records the delivered bytes of file fileID, whose duration is
// durationSeconds, on session sessionID. It returns nil when nothing can be
// recorded: no noter, no session, or an unknown duration.
func DirectPlayDeliveryRecorder(noter DeliveryNoter, sessionID string, fileID int, durationSeconds float64) func(start, end, size int64) {
	if noter == nil || sessionID == "" || !(durationSeconds > 0) {
		return nil
	}
	return func(start, end, size int64) {
		if span, ok := DeliveredMediaSpan(start, end, size, durationSeconds); ok {
			_ = noter.NoteDelivered(sessionID, fileID, span)
		}
	}
}

// completedRunGap is how many segments past a run's end a completed download
// may land and still extend the run, so a client that fetches two segments at
// once and finishes them out of order keeps one run.
const completedRunGap = 1

// segmentRun is a contiguous run of segment numbers. The zero value is empty.
type segmentRun struct {
	first, last int
	ok          bool
}

// add extends the run with a completed segment, or starts a new run at it
// when it is not next to the run: the client sought elsewhere.
func (r segmentRun) add(segNum int) segmentRun {
	if r.ok && segNum >= r.first && segNum <= r.last+1+completedRunGap {
		r.last = max(r.last, segNum)
		return r
	}
	return segmentRun{first: segNum, last: segNum, ok: true}
}

// HLSDeliveryProgress is how far an HLS transcode on this server has been
// fetched by its client and produced by ffmpeg, in media seconds.
type HLSDeliveryProgress struct {
	// Fetched spans the run of segments the client most recently finished
	// downloading, when FetchedOK.
	Fetched   MediaSpan
	FetchedOK bool
	// ProducedThrough is the end of the last segment the current ffmpeg
	// generation wrote, when ProducedOK.
	ProducedThrough float64
	ProducedOK      bool
}

// FetchedThroughAt returns the end of the fetched run when it contains
// position. A run elsewhere (the client sought away from it) says nothing
// about the media ahead of the viewer.
func (p HLSDeliveryProgress) FetchedThroughAt(position float64) (float64, bool) {
	if !p.FetchedOK {
		return 0, false
	}
	return DeliveredThroughAt([]MediaSpan{p.Fetched}, position)
}

// DeliveryProgress reports how far the client has fetched and ffmpeg has
// produced this session's segments.
func (s *TranscodeSession) DeliveryProgress(now time.Time) HLSDeliveryProgress {
	s.mu.Lock()
	run := s.completedRun
	s.mu.Unlock()

	progress := s.SegmentProgress(now)
	spanOf := s.segmentMediaSpans()
	var out HLSDeliveryProgress
	if run.ok {
		first, firstOK := spanOf(run.first)
		last, lastOK := spanOf(run.last)
		if firstOK && lastOK {
			out.Fetched, out.FetchedOK = MediaSpan{Start: first.Start, End: last.End}, true
		}
	}
	if !progress.Restarting && progress.ProducedHead >= progress.StartSegmentNumber && !progressPredatesGeneration(progress) {
		if head, ok := spanOf(progress.ProducedHead); ok {
			out.ProducedThrough, out.ProducedOK = head.End, true
		}
	}
	return out
}

// segmentMediaSpans returns a lookup of the media time each segment number
// covers on the timeline the session serves: a planned copy stream's keyframe
// plan, a copy stream's real playlist, or fixed-duration segments for an
// encoded stream, whose numbers count from the start of the media (see
// RestartSeekTarget).
func (s *TranscodeSession) segmentMediaSpans() func(segNum int) (MediaSpan, bool) {
	opts := s.Opts()
	if plan := s.copyPlan; plan != nil {
		return func(segNum int) (MediaSpan, bool) {
			if segNum < 0 || segNum >= len(plan.durations) {
				return MediaSpan{}, false
			}
			start := 0.0
			for _, d := range plan.durations[:segNum] {
				start += d
			}
			return MediaSpan{Start: start, End: start + plan.durations[segNum]}, true
		}
	}
	if strings.EqualFold(opts.TargetCodecVideo, "copy") {
		base, timeline, err := s.manifestTimelineSnapshot()
		return func(segNum int) (MediaSpan, bool) {
			if err != nil {
				return MediaSpan{}, false
			}
			start := base
			for _, entry := range timeline.entries {
				if entry.number == segNum {
					return MediaSpan{Start: start, End: start + entry.duration}, true
				}
				start += entry.duration
			}
			return MediaSpan{}, false
		}
	}
	segmentSeconds := float64(opts.SegmentDuration)
	if segmentSeconds <= 0 {
		segmentSeconds = defaultSegmentDuration
	}
	return func(segNum int) (MediaSpan, bool) {
		if segNum < 0 {
			return MediaSpan{}, false
		}
		span := MediaSpan{Start: float64(segNum) * segmentSeconds, End: float64(segNum+1) * segmentSeconds}
		if opts.TotalDuration > 0 {
			span.End = math.Min(span.End, opts.TotalDuration)
		}
		return span, span.End > span.Start
	}
}

// LocalTranscodes finds the transcodes this process runs by native playback
// session ID. The native API and the Jellyfin compatibility server each own a
// TranscodeManager, and both register theirs here when they are built, which
// can be after the session sync that reads it.
type LocalTranscodes struct {
	mu       sync.RWMutex
	managers []*TranscodeManager
}

// Add registers a transcode manager. It is safe on a nil receiver.
func (l *LocalTranscodes) Add(m *TranscodeManager) {
	if l == nil || m == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.managers = append(l.managers, m)
}

// DeliveryProgress reports the delivery progress of the session's transcode
// when this process runs it.
func (l *LocalTranscodes) DeliveryProgress(sessionID string, now time.Time) (HLSDeliveryProgress, bool) {
	if l == nil || sessionID == "" {
		return HLSDeliveryProgress{}, false
	}
	l.mu.RLock()
	managers := l.managers
	l.mu.RUnlock()
	for _, m := range managers {
		if ts := m.GetTranscodeSession(sessionID); ts != nil {
			return ts.DeliveryProgress(now), true
		}
	}
	return HLSDeliveryProgress{}, false
}
