package intromarkers

// applyCreditsGuards checks a detected credits segment against the file
// before it is written, and returns the segment to write. Credits must start
// in the tail window and last as long as credits can; they cannot start
// before the file's intro ends; a preview that starts inside them ends them;
// and they cannot run past the file or rate below creditsMinimumConfidence.
func applyCreditsGuards(segment Segment, candidate Candidate, limits creditsLimits) (Segment, bool) {
	duration := candidate.DurationSeconds
	if duration <= 0 || segment.Start < limits.windowStart(duration) {
		return Segment{}, false
	}
	if candidate.IntroEnd != nil && segment.Start < *candidate.IntroEnd {
		return Segment{}, false
	}
	if candidate.PreviewStart != nil && *candidate.PreviewStart > segment.Start && *candidate.PreviewStart < segment.End {
		segment.End = *candidate.PreviewStart
	}
	length := segment.End - segment.Start
	if length < limits.minSeconds || length > limits.maxSeconds {
		return Segment{}, false
	}
	if segment.End > duration || segment.Confidence < creditsMinimumConfidence {
		return Segment{}, false
	}
	return segment, true
}
