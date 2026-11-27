package intromarkers

import (
	"math"
	"sort"
)

// Boundary refinement from the loudness trace.
//
// An envelope match says where the shared recording starts and stops to within
// its hop, but two encodes of one theme disagree slightly at both ends, and a
// match can start a beat early or late. The trace itself decides the last few
// tenths: an opening starts where the level lifts out of the quiet that
// precedes it, and ends where the music fades under the level it held. This is
// how the reference engine places its edges, and it needs no second decode.
const (
	// introStartSearchBackSeconds is how far before the matched start the
	// quiet point that precedes the opening may lie.
	introStartSearchBackSeconds = 1.5
	// introStartSearchForwardSeconds is how far into the matched opening the
	// quiet point may lie. A match usually starts a frame or two early.
	introStartSearchForwardSeconds = 1.5
	// introStartMinimumLiftDB is how much louder the opening must be than
	// the quiet point before it for that point to mark the start. A cold
	// open's dialog runs into the theme without a break, so without the
	// lift the matched start stands.
	introStartMinimumLiftDB = 4.0
	// introStartLiftHoldSeconds is how long the level may stay low after its
	// quietest frame before the lift is a different feature. It also skips a
	// single quiet frame inside a beat, whose lift arrives at once.
	introStartLiftHoldSeconds = 3.0
	// introEndSearchBackSeconds and introEndSearchForwardSeconds bound where
	// the fade that ends an opening may lie, and
	// introEndMaximumTrimSeconds bounds how far back the trace may move the
	// matched end. An envelope edge is already within a window; a fall
	// further back than this is the music's own shape, not its end.
	introEndSearchBackSeconds    = 2.0
	introEndSearchForwardSeconds = 3.0
	introEndMaximumTrimSeconds   = 1.5
	// introEndDropDB is how far under the story's own level the opening's
	// music has to fall before it counts as over.
	introEndDropDB = 6.0
	// introEndAfterLeadSeconds and introEndAfterSeconds bound the stretch
	// after the match the story's level is measured over: close enough to
	// the cut, far enough to miss the reverb of the last note.
	introEndAfterLeadSeconds = 0.5
	introEndAfterSeconds     = 3.0
	// introEndFadeHoldSeconds is how long that fall must hold, and how far
	// past the matched end it must still hold for the fall to be the end of
	// the opening rather than a dip between phrases.
	introEndFadeHoldSeconds = 0.5
	// introEndMinimumSeconds is the shortest opening a fade may leave; a
	// matched span shorter than this keeps its end.
	introEndMinimumSeconds = 2.0
	// zeroStartLimitSeconds is where a start this close to the beginning of
	// the file becomes the beginning of the file. The reference path used a
	// two-second window applied to a start already corrected by a fixed
	// lead, which turned openings that begin after a four-second studio logo
	// into openings at zero. An envelope edge needs no lead, so only a start
	// at the file's own first frames snaps.
	zeroStartLimitSeconds = 0.35
)

// refineIntroSegment places a matched opening's edges on the loudness trace and
// on the file's chapters, and reports nothing when refinement no longer yields
// a plausible intro.
func refineIntroSegment(input levelsInput, segment Segment, cfg Config) Segment {
	env := envelopeOf(input)
	start, end := refineLevelEdges(env, input.WindowStart, segment.Start, segment.End)
	segment.Start, segment.End = start, end

	// A start at the very beginning of the file is the beginning of the
	// file, and nothing else snaps to zero.
	if segment.Start <= zeroStartLimitSeconds {
		segment.Start = 0
	}
	// An intro cannot start inside the recap a release marks: a match that
	// reaches into it starts where the recap ends.
	if recapEnd, ok := recapEndBefore(input.Candidate.Chapters, input.Candidate.DurationSeconds, segment.End); ok &&
		segment.Start < recapEnd {
		segment.Start = recapEnd
	}
	segment.Start, segment.End = snapIntroToChapters(input.Candidate, segment)
	if segment.End <= segment.Start {
		return Segment{}
	}
	minimum := float64(cfg.MinimumIntroDurationSeconds)
	maximum := math.Min(levelMaximumSeconds, float64(cfg.MaximumIntroDurationSeconds))
	duration := segment.End - segment.Start
	if duration < minimum || duration > maximum {
		return Segment{}
	}
	if segment.Start < 0 {
		segment.Start = 0
	}
	if input.Candidate.DurationSeconds > 0 && segment.End > input.Candidate.DurationSeconds {
		segment.End = input.Candidate.DurationSeconds
	}
	return segment
}

// refineLevelEdges moves both edges of a matched opening onto the loudness
// trace. A boundary the trace cannot place better stays where the match put it.
func refineLevelEdges(env envelope, windowStart, start, end float64) (float64, float64) {
	if env.len() == 0 {
		return start, end
	}
	refinedStart := refineLevelStart(env, windowStart, start)
	refinedEnd := refineLevelEnd(env, windowStart, refinedStart, end)
	if refinedEnd <= refinedStart {
		return start, end
	}
	return refinedStart, refinedEnd
}

// refineLevelStart puts the start where the opening's sound begins: the frame
// at which the level lifts out of the quiet stretch before it. A boundary the
// trace does not show an onset for stays where the match put it, as on a cold
// open whose dialog runs into the theme.
func refineLevelStart(env envelope, windowStart, start float64) float64 {
	if env.len() == 0 {
		return start
	}
	from := windowStart + start - introStartSearchBackSeconds
	to := windowStart + start + introStartSearchForwardSeconds
	index, ok := env.minIndex(from, to)
	if !ok {
		return start
	}
	quiet := env.db[index]
	onset := -1
	for i := index; i < len(env.db); i++ {
		if env.db[i] >= quiet+introStartMinimumLiftDB {
			onset = i
			break
		}
		if float64(i-index)*env.hop > introStartLiftHoldSeconds {
			break
		}
	}
	if onset < 0 {
		return start
	}
	// A single quiet frame inside a beat is not an onset; the level has to
	// stay down for a moment before it lifts.
	if float64(onset-index)*env.hop < 0.15 {
		return start
	}
	return windowStart + float64(onset)*env.hop
}

// refineLevelEnd puts the end where the opening's music stops, measured
// against the story that follows it. A dip between phrases inside the music is
// not the end — the music comes back above the story's level — and a fade is
// taken to its bottom, since the music is audible until then.
func refineLevelEnd(env envelope, windowStart, start, end float64) float64 {
	afterLevel, ok := env.medianBetween(windowStart+end+introEndAfterLeadSeconds, windowStart+end+introEndAfterSeconds)
	if !ok {
		return end
	}
	threshold := afterLevel + introEndDropDB
	first, last, ok := env.indexRange(windowStart+end-introEndSearchBackSeconds, windowStart+end+introEndSearchForwardSeconds)
	if !ok {
		return end
	}
	for i := first; i <= last; i++ {
		if env.db[i] >= threshold {
			continue
		}
		candidate := windowStart + float64(i)*env.hop
		// The music must not come back: the level has to stay under the
		// threshold through the matched end and a little past it.
		if !env.sustainedBelow(candidate, windowStart+end+introEndFadeHoldSeconds, threshold) {
			continue
		}
		if candidate-start < introEndMinimumSeconds {
			return end
		}
		if end-candidate > introEndMaximumTrimSeconds {
			continue
		}
		return candidate
	}
	return end
}

// minIndex returns the frame of the lowest level in a time range.
func (e envelope) minIndex(from, to float64) (int, bool) {
	first, last, ok := e.indexRange(from, to)
	if !ok {
		return 0, false
	}
	best := first
	for i := first; i <= last; i++ {
		if e.db[i] < e.db[best] {
			best = i
		}
	}
	return best, true
}

// indexRange returns the frames covering a time range, both ends inside the
// envelope.
func (e envelope) indexRange(from, to float64) (int, int, bool) {
	if len(e.db) == 0 {
		return 0, 0, false
	}
	first := int(math.Floor(from / e.hop))
	last := int(math.Ceil(to / e.hop))
	first = max(first, 0)
	last = min(last, len(e.db)-1)
	if last < first {
		return 0, 0, false
	}
	return first, last, true
}

// medianBetween returns the median level in a time range.
func (e envelope) medianBetween(from, to float64) (float64, bool) {
	first, last, ok := e.indexRange(from, to)
	if !ok || last-first < 2 {
		return 0, false
	}
	values := make([]float64, 0, last-first+1)
	values = append(values, e.db[first:last+1]...)
	sort.Float64s(values)
	return values[len(values)/2], true
}

// staysAbove reports whether every frame in a time range is at or above level.
func (e envelope) staysAbove(from, to float64, level float64) bool {
	first, last, ok := e.indexRange(from, to)
	if !ok {
		return false
	}
	for i := first; i <= last; i++ {
		if e.db[i] < level {
			return false
		}
	}
	return true
}

// staysBelow reports whether the frames from a time onwards, for hold frames,
// stay under level.
func (e envelope) staysBelow(from float64, hold int, level float64) bool {
	index := int(math.Round(from / e.hop))
	if index < 0 || index >= len(e.db) {
		return false
	}
	for i := index; i < len(e.db) && i < index+hold; i++ {
		if e.db[i] >= level {
			return false
		}
	}
	return true
}

// sustainedBelow reports whether every frame from a time to another stays
// under level. A range that runs past the envelope ends at its last frame.
func (e envelope) sustainedBelow(from, to, level float64) bool {
	first, last, ok := e.indexRange(from, to)
	if !ok {
		return false
	}
	for i := first; i <= last; i++ {
		if e.db[i] >= level {
			return false
		}
	}
	return true
}

// snapIntroToChapters moves an edge onto a nearby authored chapter boundary,
// as the Chromaprint path does, and never past the other edge.
func snapIntroToChapters(candidate Candidate, segment Segment) (float64, float64) {
	start, end := segment.Start, segment.End
	for _, chapter := range candidate.Chapters {
		start = snapBoundary(start, chapter.StartSeconds)
		end = snapBoundary(end, chapter.StartSeconds)
		end = snapBoundary(end, chapter.EndSeconds)
	}
	if end <= start {
		return segment.Start, segment.End
	}
	return start, end
}
