package intromarkers

import (
	"math"
	"regexp"
)

// What a file's subtitle cues say about its markers.
//
// Two readings are worth the demux. A cue that says "Previously On" marks the
// recap, and an intro cannot start inside it. Dialog stops when the credits
// roll, so the last cue before the credits ends where they begin: the credits
// start no earlier than that, and on a roll whose music begins late — the files
// TheIntroDB reports as starting late — the cue end is often the only exact
// bound there is.
var (
	// cueRecapPattern matches the recap cue a release writes in its own
	// subtitle track.
	cueRecapPattern = regexp.MustCompile(`(?i)\b(previously on|last time on|the story so far|recap)\b`)
	// cueCreditsPattern matches a cue that is itself a credit, such as a
	// translator's card, which says the credits have begun.
	cueCreditsPattern = regexp.MustCompile(`(?i)\b(subtitle[sd]? by|subtitles? (were )?(created|translated)|translated by|synced by)\b`)
	// cueNonDialogPattern matches a cue that is not dialog: a lyric marker, or
	// a line that is nothing but a bracketed sound tag ([door slams],
	// (laughs)). A cue like this carries no line of speech, so it cannot say
	// where the speech stopped.
	cueNonDialogPattern = regexp.MustCompile(`(?i)[♪♫]|^\s*[\[(][^\])\n]{0,40}[\])]\s*$`)
)

// Where a cue bound needs corroboration.
const (
	// cueDialogRunSeconds is how far back from a bound its supporting dialog
	// is counted.
	cueDialogRunSeconds = 60.0
	// cueNonDialogMinimum is how many non-dialog cues a window must hold before
	// the track counts as one carrying the music. A single [door slams] near
	// the last line is a sound effect in a dialog track, not a song.
	cueNonDialogMinimum = 3
	// cueDialogRunMinimum is how many dialog cues must end inside that window
	// before a bound on a track that also carries non-dialog text is taken. A
	// song over the credits is transcribed with its lyrics, and a machine
	// transcript writes them as plain lines, so one line after the music
	// started is not evidence that dialog was still running.
	cueDialogRunMinimum = 3
	// creditCueMaxLeadSeconds is how far past the placed start a cue may begin
	// and still be read as a line the placement cut off.
	//
	// The hint exists for the placement that landed inside the last line of
	// dialog, so the line it reads is one already running when the credits
	// begin, or one within this lead of it. A cue that starts later is inside
	// the credits: on-screen credit text, or the closing song's lyrics. Moving
	// the start to the end of one moves it into the credits picture, which is
	// the fault the hint was added to prevent. On a measured library the
	// misplaced starts came from cues beginning 27 to 30 seconds in, and the
	// whole of the search window they were found in is why.
	creditCueMaxLeadSeconds = 10.0
)

// cueBoundIsDialogue reports whether the cue ending at end is dialog, rather
// than content inside the credits. The caller has already bounded the search to
// cues that begin close enough to the placed start to be a line it cut off.
func cueBoundIsDialogue(cues []subtitleCue, segment Segment, end float64) bool {
	// A track carries the music as well as the dialog when its cues run into
	// the song, and a machine transcript writes a song's lyrics as ordinary
	// lines. On such a track one line ending after the placed start is not
	// dialog that ran late; it needs a run of dialog behind it.
	if cueNonDialogCount(cues, segment.Start, end, creditCueSearchSeconds) >= cueNonDialogMinimum &&
		cueDialogRun(cues, end, cueDialogRunSeconds) < cueDialogRunMinimum {
		return false
	}
	return true
}

// cuesStartingBefore returns the cues that begin at or before limit.
func cuesStartingBefore(cues []subtitleCue, limit float64) []subtitleCue {
	for i, cue := range cues {
		if cue.Start > limit {
			return cues[:i]
		}
	}
	return cues
}

// cueDialogRun counts the dialog cues ending within window before end.
func cueDialogRun(cues []subtitleCue, end, window float64) int {
	run := 0
	for _, cue := range cues {
		if cue.End > end || cue.End < end-window {
			continue
		}
		if cueCreditsPattern.MatchString(cue.Text) || cueNonDialogPattern.MatchString(cue.Text) {
			continue
		}
		run++
	}
	return run
}

// cueNonDialogCount counts the non-dialog cues between from-window and to: the
// lyrics and sound tags of a track that carries the music as well as the
// speech.
func cueNonDialogCount(cues []subtitleCue, from, to, window float64) int {
	count := 0
	for _, cue := range cues {
		if cue.Start < from-window || cue.Start > to {
			continue
		}
		if cueCreditsPattern.MatchString(cue.Text) {
			continue
		}
		if cueNonDialogPattern.MatchString(cue.Text) {
			count++
		}
	}
	return count
}

// cueRecap returns the recap a file's cues mark: the cues saying "previously
// on" in the opening, from the first such cue to the end of the run they form.
func cueRecap(cues []subtitleCue, duration float64) (Segment, bool) {
	first, last := -1, -1
	for i, cue := range cues {
		if cue.Start > recapHeadSeconds {
			break
		}
		if !cueRecapPattern.MatchString(cue.Text) {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
		// A "previously on" card is followed by the recap itself: the run
		// continues while the cues keep coming within a few seconds, and
		// stops at the longest recap a cue run may mark.
		recapStart := cue.Start
		for j := i + 1; j < len(cues); j++ {
			if cues[j].Start-cues[j-1].End > recapCueGapSeconds || cues[j].End-recapStart > recapMaximumSeconds {
				break
			}
			last = j
		}
		break
	}
	if first < 0 {
		return Segment{}, false
	}
	start, end := cues[first].Start, cues[last].End
	if end <= start || end-start > recapMaximumSeconds {
		// One card that says it and nothing after is not a recap.
		end = math.Min(end, start+recapMaximumSeconds)
	}
	if duration > 0 && end > duration {
		end = duration
	}
	if end-start < 3 {
		return Segment{}, false
	}
	return Segment{Start: start, End: end}, true
}

// cueDialogEnd returns the end of the last dialog cue that lies at or
// before bound, when it ends within searchSeconds of it: the point where the
// dialog stopped, which is where credits begin.
func cueDialogEnd(cues []subtitleCue, bound, searchSeconds float64) (float64, bool) {
	best, found := 0.0, false
	for _, cue := range cues {
		if cue.Start > bound {
			break
		}
		// A translator's card is part of the credits, not the dialog before
		// them: reading it as dialog would move the start past the first
		// fifteen seconds of credits picture. A cue that is only a lyric or a
		// sound tag holds no line of speech either, so it cannot say where the
		// speech stopped.
		if cueCreditsPattern.MatchString(cue.Text) || cueNonDialogPattern.MatchString(cue.Text) {
			continue
		}
		if cue.End >= bound-searchSeconds && cue.End > best {
			best, found = cue.End, true
		}
	}
	return best, found
}

// cueCreditsMarker returns where a credit cue (a translator's card) begins,
// when one lies inside the segment: it is part of the credits and no marker
// should start after it.
func cueCreditsMarker(cues []subtitleCue, segment Segment) (float64, bool) {
	for _, cue := range cues {
		if cue.Start < segment.Start || cue.Start >= segment.End {
			continue
		}
		if cueCreditsPattern.MatchString(cue.Text) {
			return cue.Start, true
		}
	}
	return 0, false
}

// applyCreditCueHint moves a placed credits start to where the dialog
// stopped, when the cue ends just before or inside the segment. Credits carry
// no dialog, so a start before the last cue is a start inside the story. A
// translator's card is part of the credits, so the start never moves past one.
func applyCreditCueHint(segment Segment, cues []subtitleCue) Segment {
	marker, hasMarker := cueCreditsMarker(cues, segment)
	// A cue that begins well after the placed start is inside the credits —
	// on-screen credit text, or the closing song's lyrics — not a line the
	// placement cut off, so the search stops at the lead.
	within := cuesStartingBefore(cues, segment.Start+creditCueMaxLeadSeconds)
	end, ok := cueDialogEnd(within, segment.Start+creditCueSearchSeconds, creditCueSearchSeconds+creditCueLagSeconds)
	if ok && end > segment.Start && cueBoundIsDialogue(cues, segment, end) {
		segment.Start = end + creditCueLeadSeconds
	}
	if hasMarker && marker < segment.Start {
		segment.Start = marker
	}
	return segment
}

// applyIntroCueHints moves an intro start that fell inside the recap a cue
// announces to the recap's end, and leaves everything else alone.
func applyIntroCueHints(candidate Candidate, segment Segment, cues []subtitleCue) Segment {
	// Only a start that fell inside the recap moves: an opening that begins
	// before the recap and runs through it keeps its opening.
	if recap, ok := cueRecap(cues, candidate.DurationSeconds); ok &&
		segment.Start >= recap.Start && segment.Start < recap.End && recap.End < segment.End {
		segment.Start = recap.End
	}
	return segment
}

// creditsCueHintBounded reports whether the hint left a plausible segment.
func creditsCueHintBounded(segment Segment, duration float64) bool {
	return segment.End > segment.Start && segment.Start >= 0 && (duration <= 0 || segment.End <= duration)
}

const (
	// recapCueGapSeconds is the longest silence inside a recap: a "previously
	// on" card, then the montage's cues with pauses between them.
	recapCueGapSeconds = 6.0
	// recapMaximumSeconds bounds a recap a cue run may mark.
	recapMaximumSeconds = 240.0
	// creditCueSearchSeconds is how far past the credits start the last
	// dialog cue is looked for, and creditCueLagSeconds how far before it:
	// the hint only acts when the dialog stopped near the start.
	creditCueSearchSeconds = 30.0
	creditCueLagSeconds    = 60.0
	// creditCueLeadSeconds is the silence between the last cue and the
	// credits' first frame.
	creditCueLeadSeconds = 0.2
)
