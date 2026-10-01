package intromarkers

import (
	"math"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// Combining audio and video credits evidence.
const (
	// creditsRefineWindowSeconds is how far after an audio match's start a
	// text run may begin and still move the start to it. The match often
	// starts on the music over the story's last shot.
	creditsRefineWindowSeconds = 60.0
	// creditsRefineMinimumSeconds keeps a run starting with the audio match
	// from counting as a later start.
	creditsRefineMinimumSeconds = 0.5
	// creditsVideoBlackLeadSeconds is the largest gap a video-only start
	// may cross, keyframe by keyframe, moving back over true black before
	// the first text.
	creditsVideoBlackLeadSeconds = 10.0
	// creditsAudioVideoBonus raises a strong audio match that video
	// corroborated, up to creditsAudioVideoMaximumConfidence.
	creditsAudioVideoBonus             = 0.05
	creditsAudioVideoMaximumConfidence = 0.95
	// creditsWeakAudioConfidence rates a match only one partner episode
	// confirmed, which counts only when a text run overlaps it.
	creditsWeakAudioConfidence = 0.65
	// Video-only credits rate by how much of their text is on black: text
	// on light cards is more often a title card inside the story.
	creditsVideoLetteredConfidence = 0.60
	creditsVideoCardConfidence     = 0.55
)

// creditsAudio is a file's season audio match in the tail, before it is
// combined with video.
type creditsAudio struct {
	Start, End float64
	// Confirmations is how many partner episodes agreed with the match.
	Confirmations int
	// Confidence rates a strong match; zero means the match cannot stand on
	// its own.
	Confidence float64
}

// strong reports whether the match may place credits without video.
func (a creditsAudio) strong() bool {
	return a.Confirmations >= creditsMinimumConfirmations && a.Confidence > 0
}

// weak reports whether the match counts only with a text run over it.
func (a creditsAudio) weak() bool {
	return a.Confirmations == 1
}

// creditsAudioFor rates a file's season match. A match confirmed by at least
// two partner episodes is strong unless it is short and the season does not
// share its duration; a match one partner confirmed is weak.
func creditsAudioFor(match seasonMatch, profile matchProfile) creditsAudio {
	audio := creditsAudio{Start: match.Segment.Start, End: match.Segment.End, Confirmations: match.Confirmations}
	short := match.Segment.End-match.Segment.Start < profile.ShortSeconds
	switch {
	case match.Confirmations < creditsMinimumConfirmations || (short && !match.SeasonConsistent):
	case match.SeasonConsistent:
		audio.Confidence = profile.ConsistentConfidence
	default:
		audio.Confidence = profile.InconsistentConfidence
	}
	return audio
}

// creditsEvidence is what credits detection knows about one file's tail.
type creditsEvidence struct {
	// Audio is the file's season audio match, if any.
	Audio *creditsAudio
	// Keyframes are the tail's classified keyframes, empty without video.
	Keyframes []creditsKeyframe
	// Silences are the tail's silences in absolute media seconds.
	Silences []mediasample.Interval
}

// combineCredits places a file's credits from its tail evidence and checks
// them with applyCreditsGuards. Chapter credits are placed before this and
// win over everything here.
//
//   - A strong audio match grows over text runs that overlap it or lie
//     within creditsMergeGapSeconds, and its start moves to the first such
//     run when that run begins up to creditsRefineWindowSeconds later and
//     only story keyframes lie between (credits-audio:video:v1).
//   - A qualifying text run reaching the end of the file that starts more
//     than creditsMergeGapSeconds after an audio match which does not reach
//     it wins: that match is most likely a recurring cue.
//   - A weak audio match counts only with a text run near it, and an audio
//     match no run corroborates must reach the end of the file.
//   - Without usable audio, the last cluster of runs is the credits
//     (credits-video:v1) when it ends near the end of the file. Its start
//     moves back over the true black just before it, then to the end of a
//     silence between the story and the credits.
func combineCredits(candidate Candidate, evidence creditsEvidence, limits creditsLimits) (Segment, bool) {
	duration := candidate.DurationSeconds
	keyframes := evidence.Keyframes
	runs := buildRuns(keyframes)
	segment, ok := combineAudio(evidence.Audio, keyframes, &runs, duration)
	if !ok {
		segment, ok = placeVideoCredits(runs, keyframes, evidence.Silences, limits.windowStart(duration), duration, limits)
	}
	if !ok {
		return Segment{}, false
	}
	segment.End = snapCreditsEnd(segment.End, duration)
	return applyCreditsGuards(segment, candidate, limits)
}

// combineAudio places credits from an audio match and the runs near it. When
// a later run reaching the end of the file overrides the match, it narrows
// runs to the runs after the match for the video-only path.
func combineAudio(audio *creditsAudio, keyframes []creditsKeyframe, runs *[]creditsRun, duration float64) (Segment, bool) {
	if audio == nil || (!audio.strong() && !audio.weak()) {
		return Segment{}, false
	}
	var near []creditsRun
	for _, run := range *runs {
		if run.End >= audio.Start-creditsMergeGapSeconds && run.Start <= audio.End+creditsMergeGapSeconds {
			near = append(near, run)
		}
	}
	if audio.weak() && len(near) == 0 {
		return Segment{}, false
	}

	start, end := audio.Start, audio.End
	refined := false
	for _, run := range near {
		if run.Start < start {
			start, refined = run.Start, true
		}
		if run.End > end {
			end, refined = run.End, true
		}
	}
	if len(near) > 0 {
		first := near[0]
		for _, run := range near[1:] {
			if run.Start < first.Start {
				first = run
			}
		}
		if first.Start >= audio.Start+creditsRefineMinimumSeconds && first.Start <= audio.Start+creditsRefineWindowSeconds &&
			onlyContentBetween(keyframes, audio.Start, first.Start) {
			start, refined = first.Start, true
		}
	}

	var later []creditsRun
	for _, run := range *runs {
		if run.Start > end+creditsMergeGapSeconds {
			later = append(later, run)
		}
	}
	if len(later) > 0 && reachesEOF(later[len(later)-1].End, duration) && !reachesEOF(end, duration) {
		*runs = later
		return Segment{}, false
	}
	if len(near) == 0 && !reachesEOF(end, duration) {
		return Segment{}, false
	}

	segment := Segment{Start: start, End: end, Algorithm: CreditsAudioAlgorithm}
	switch {
	case audio.strong() && refined:
		segment.Confidence = math.Min(creditsAudioVideoMaximumConfidence, audio.Confidence+creditsAudioVideoBonus)
	case audio.strong():
		segment.Confidence = audio.Confidence
	default:
		segment.Confidence = creditsWeakAudioConfidence
	}
	if refined {
		segment.Algorithm = CreditsAudioVideoAlgorithm
	}
	return segment, true
}

// onlyContentBetween reports whether keyframes in [from, to) exist and all
// show story. A mostly black keyframe counts as story here: without text
// around it, it is as likely a dark scene as credits.
func onlyContentBetween(keyframes []creditsKeyframe, from, to float64) bool {
	found := false
	for _, keyframe := range keyframes {
		if keyframe.Seconds < from || keyframe.Seconds >= to {
			continue
		}
		if keyframe.Class != keyframeContent && keyframe.Class != keyframeMostlyBlack {
			return false
		}
		found = true
	}
	return found
}

// placeVideoCredits places credits from text runs alone: the last cluster of
// runs, which must end within limits.videoEOFSeconds of the end of the file.
func placeVideoCredits(runs []creditsRun, keyframes []creditsKeyframe, silences []mediasample.Interval, windowStart, duration float64, limits creditsLimits) (Segment, bool) {
	clusters := clusterRuns(runs)
	if len(clusters) == 0 {
		return Segment{}, false
	}
	cluster := clusters[len(clusters)-1]
	if duration-cluster.End > limits.videoEOFSeconds {
		return Segment{}, false
	}
	start := cluster.Start
	i := cluster.First - 1
	for ; i >= 0 && keyframes[i].Class == keyframeBlack && start-keyframes[i].Seconds <= creditsVideoBlackLeadSeconds; i-- {
		start = keyframes[i].Seconds
	}
	// A silence entirely between the last keyframe before the credits and
	// their start marks the cut; one reaching into either side does not.
	previous := windowStart
	if i >= 0 {
		previous = keyframes[i].Seconds
	}
	for _, silence := range silences {
		if silence.End > 0 && previous <= silence.Start && silence.End <= start && silence.End > previous {
			start = silence.End
		}
	}
	confidence := creditsVideoCardConfidence
	if 2*cluster.Lettered >= cluster.Text {
		confidence = creditsVideoLetteredConfidence
	}
	return Segment{Start: start, End: cluster.End, Confidence: confidence, Algorithm: CreditsVideoAlgorithm}, true
}
