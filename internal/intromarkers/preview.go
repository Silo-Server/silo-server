package intromarkers

import "math"

// Next-episode previews.
//
// A preview carries new footage every time, so audio and picture matching
// cannot find it: there is nothing to match. What recurs is its shape. A
// release ends the file the same way each episode — a card, a jingle, then the
// same length of preview — so a stretch from the end of the credits to the end
// of the file is a preview when its length agrees across the season and the
// season carries shared evidence at its very end.
//
// This is groundwork: the detector reports what it finds in the run summary so
// a library can be measured before preview markers get a setting, a marker
// kind with a skip semantic of its own, and a web control. A preview is never
// an automatic skip — the reference engine reports one as a button — so
// nothing here writes a marker.
const (
	// previewMinimumSeconds and previewMaximumSeconds bound a preview.
	previewMinimumSeconds = 10.0
	previewMaximumSeconds = 75.0
	// previewLengthToleranceSeconds is how far two previews' lengths may
	// differ and still agree; previewSpreadSeconds bounds the whole group.
	previewLengthToleranceSeconds = 1.5
	previewSpreadSeconds          = 3.0
	// previewEndEvidenceSeconds is how much of the end of the file carries
	// the card or jingle that identifies the preview.
	previewEndEvidenceSeconds = 5.0
	// previewMinimumShares is how many files of the group must carry it.
	previewMinimumShares = 2
	// previewConfidence rates a detected preview. It is below the credits
	// floor on purpose: a preview is a button, never a skip.
	previewConfidence = 0.55
)

// previewInput is one file's tail evidence for preview detection.
type previewInput struct {
	Candidate Candidate
	// CreditsEnd is where the file's credits end; HasCredits says whether it
	// has any.
	CreditsEnd float64
	HasCredits bool
	// Keyframes are the file's classified tail keyframes.
	Keyframes []creditsKeyframe
}

// detectPreviews returns the preview after each file's credits, keyed by file
// ID, for the files of a season whose previews agree.
func detectPreviews(inputs []previewInput) map[int]Segment {
	type candidatePreview struct {
		fileID    int
		candidate Candidate
		start     float64
		length    float64
		// endCard reports shared evidence at the end of the file.
		endCard bool
	}
	var candidates []candidatePreview
	for _, input := range inputs {
		if !input.HasCredits || input.Candidate.DurationSeconds <= 0 {
			continue
		}
		length := input.Candidate.DurationSeconds - input.CreditsEnd
		if length < previewMinimumSeconds || length > previewMaximumSeconds {
			continue
		}
		candidates = append(candidates, candidatePreview{
			fileID:    input.Candidate.FileID,
			candidate: input.Candidate,
			start:     input.CreditsEnd,
			length:    length,
			endCard:   hasEndEvidence(input),
		})
	}

	previews := map[int]Segment{}
	for _, anchor := range candidates {
		group := make([]candidatePreview, 0, len(candidates))
		for _, other := range candidates {
			if math.Abs(other.length-anchor.length) <= previewLengthToleranceSeconds {
				group = append(group, other)
			}
		}
		if len(group) < previewMinimumShares+1 {
			continue
		}
		shares := 0
		shortest := math.Inf(1)
		for _, member := range group {
			if member.endCard {
				shares++
			}
			shortest = math.Min(shortest, member.length)
		}
		if shares < previewMinimumShares {
			continue
		}
		for _, member := range group {
			if _, done := previews[member.fileID]; done {
				continue
			}
			end := member.start + shortest
			if member.candidate.DurationSeconds > 0 && end > member.candidate.DurationSeconds {
				end = member.candidate.DurationSeconds
			}
			start := end - shortest
			if start < 0 || (member.candidate.IntroEnd != nil && start < *member.candidate.IntroEnd) {
				continue
			}
			previews[member.fileID] = Segment{
				Start:      start,
				End:        end,
				Confidence: previewConfidence,
				Algorithm:  PreviewAlgorithm,
			}
		}
	}
	if len(previews) == 0 {
		return nil
	}
	return previews
}

// hasEndEvidence reports whether the last seconds of the file carry a card:
// text on black, or a card, which every preview of a release opens or closes
// with.
func hasEndEvidence(input previewInput) bool {
	if len(input.Keyframes) == 0 {
		return false
	}
	end := input.Candidate.DurationSeconds
	for i := len(input.Keyframes) - 1; i >= 0; i-- {
		frame := input.Keyframes[i]
		if end-frame.Seconds > previewEndEvidenceSeconds {
			break
		}
		if frame.Class.text() || frame.Class == keyframeMostlyBlack {
			return true
		}
	}
	return false
}
