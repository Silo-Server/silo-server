package intromarkers

// applyCreditsGuards checks a detected credits segment against the file
// before it is written, and returns the segment to write. A preview that
// starts inside the credits ends them; the result must then pass the
// placement every marker source shares (markers.CheckCreditsPlacement: tail
// window, after the intro, credits length), and the detector's own bounds:
// it cannot run past the file or rate below creditsMinimumConfidence.
func applyCreditsGuards(segment Segment, candidate Candidate, limits creditsLimits) (Segment, bool) {
	duration := candidate.DurationSeconds
	if duration <= 0 {
		return Segment{}, false
	}
	if candidate.PreviewStart != nil && *candidate.PreviewStart > segment.Start && *candidate.PreviewStart < segment.End {
		segment.End = *candidate.PreviewStart
	}
	if limits.CheckCreditsPlacement(segment.Start, segment.End, duration, candidate.IntroEnd) != nil {
		return Segment{}, false
	}
	if segment.End > duration || segment.Confidence < creditsMinimumConfidence {
		return Segment{}, false
	}
	return segment, true
}
