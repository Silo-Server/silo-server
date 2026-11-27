package intromarkers

import "math"

// Where the credits start, seen through their picture.
//
// End credits usually cut from the last shot to black, and the first name
// appears some time later: a title card on black, a few seconds of black
// before the roll begins, or a dense column of names at once. The audio match
// cannot see any of that. The credits' music may start a minute in, and the
// matcher starts where the music starts, so the placed segment began late on
// exactly the files TheIntroDB reports it on: the screen is black and text is
// on it, and the marker starts after that.
//
// The rules below read the keyframes around the first text the tail pass saw.
// They walk back over black and over the dense, mostly black keyframes that
// credit columns produce — the old rule stopped at the first mostly black
// keyframe, which is what a title card with bold text looks like — and they
// allow a long thin stretch of black, because a cut to black followed by
// silence or a quiet music bed is normal. What they will not do is walk into
// the story: a keyframe that shows a scene stops the walk, and so does a gap
// wider than the tail pass's own keyframe spacing, which would be a guess.
const (
	// creditsLeadSeconds bounds how far back over black a credits start may
	// move. A black card much longer than this is a scene that fades, not the
	// lead-in to credits.
	creditsLeadSeconds = 45.0
	// creditsLeadGapSeconds is the widest gap between two keyframes the walk
	// crosses. Beyond it the tail pass saw nothing, and the black before it
	// may belong to the story.
	creditsLeadGapSeconds = 12.0
)

// creditsLeadIn returns where a credits run that starts at its first text
// keyframe actually starts: the beginning of the black or mostly black
// keyframes directly before it, bounded by creditsLeadSeconds and by the last
// story keyframe.
func creditsLeadIn(keyframes []creditsKeyframe, firstText int, windowStart float64) float64 {
	if firstText <= 0 || firstText >= len(keyframes) {
		return keyframes[firstText].Seconds
	}
	start := keyframes[firstText].Seconds
	limit := math.Max(windowStart, start-creditsLeadSeconds)
	for i := firstText - 1; i >= 0; i-- {
		frame := keyframes[i]
		if !creditsLeadClass(frame.Class) {
			break
		}
		if start-frame.Seconds > creditsLeadGapSeconds {
			break
		}
		if frame.Seconds < limit {
			break
		}
		start = frame.Seconds
	}
	return start
}

// creditsLeadClass reports whether a keyframe may belong to the lead-in before
// a credits sequence: black, or mostly black, which is what a dense column of
// names on black measures as.
func creditsLeadClass(class keyframeClass) bool {
	return class == keyframeBlack || class == keyframeMostlyBlack
}

// creditsStartsBefore reports whether a run's lead-in start may replace an
// audio match's start. The match's start is where its recording begins; the
// picture may show credits before that, but only if nothing but black and text
// lies between, since a story keyframe in between means the match starts on
// music over the last shot and the run is a different stretch.
func creditsStartsBefore(keyframes []creditsKeyframe, leadIn, matchStart, matchEnd float64) bool {
	if leadIn >= matchStart {
		return false
	}
	// The run has to reach the match, or the two describe different spans.
	if leadIn > matchEnd {
		return false
	}
	for _, frame := range keyframes {
		if frame.Seconds < leadIn || frame.Seconds >= matchStart {
			continue
		}
		if frame.Class == keyframeBlack || frame.Class == keyframeMostlyBlack || frame.Class.text() {
			continue
		}
		return false
	}
	return true
}
