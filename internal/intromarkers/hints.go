package intromarkers

import (
	"regexp"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Chapters as evidence for the markers a file carries no chapter for.
//
// A release that names its credits chapter usually names the scenes around
// them too: the recap it replays before the opening, and the next-episode
// preview after the credits. Those names give the bounds of the markers the
// detectors place, to the frame and without decoding anything: credits stop
// where a preview chapter begins, and an intro does not start inside a recap.
//
// Subtitle tracks are the other source of the same kind of hint, and they are
// not used yet: a stored track carries its language and codec, not its cue
// times, so a hint from dialogue needs a cues artifact first. See
// docs/architecture/intro-detection.md.

var (
	// recapChapterPattern matches the titles a release gives its replay of
	// earlier episodes.
	recapChapterPattern = regexp.MustCompile(`(?i)^(previously( on)?|recap|recapitulation|last time( on)?|the story so far)\b`)
	// previewChapterPattern matches the titles a release gives its
	// next-episode preview.
	previewChapterPattern = regexp.MustCompile(`(?i)^(next( episode| time)?( on)?|preview|coming (up|next)|on the next)\b`)
)

// titleRun returns the run of adjacent chapters whose titles match, as one
// segment. A run is adjacent when each chapter starts where the one before it
// ended, so "Previously On" followed by "Recap" is one recap.
func titleRun(chapters []models.MediaChapter, matches func(string) bool, duration float64) (Segment, bool) {
	for i := range chapters {
		if !matches(strings.TrimSpace(chapters[i].Title)) {
			continue
		}
		start, end := chapters[i].StartSeconds, chapters[i].EndSeconds
		for j := i + 1; j < len(chapters); j++ {
			next := chapters[j]
			if !nearlyEqual(next.StartSeconds, end) || !matches(strings.TrimSpace(next.Title)) {
				break
			}
			end = next.EndSeconds
		}
		if duration > 0 && end > duration {
			end = duration
		}
		if end > start {
			return Segment{Start: start, End: end}, true
		}
	}
	return Segment{}, false
}

// recapHeadSeconds is how far into a file a recap chapter may start.
const recapHeadSeconds = 300.0

// nearlyEqual reports whether two chapter bounds are the same mark.
func nearlyEqual(a, b float64) bool {
	delta := a - b
	return delta >= -0.5 && delta <= 0.5
}

// isRecapChapterTitle reports whether a chapter title names a recap.
func isRecapChapterTitle(title string) bool {
	return recapChapterPattern.MatchString(title)
}

// isPreviewChapterTitle reports whether a chapter title names a
// next-episode preview.
func isPreviewChapterTitle(title string) bool {
	return previewChapterPattern.MatchString(title)
}

// DetectChapterRecap returns the recap a file's chapters mark: a run of
// chapters titled like one in the opening of the file, at least five seconds
// long. It is not a marker by itself in this version; it bounds the intro.
func DetectChapterRecap(chapters []models.MediaChapter, duration float64) (Segment, bool) {
	segment, ok := titleRun(chapters, isRecapChapterTitle, duration)
	if !ok {
		return Segment{}, false
	}
	// A recap replays the last episode before the opening, so it sits in the
	// first minutes; a "Recap" chapter later is a scene or a documentary
	// insert.
	if segment.Start > recapHeadSeconds {
		return Segment{}, false
	}
	if segment.End-segment.Start < 5 {
		return Segment{}, false
	}
	return segment, true
}

// DetectChapterPreview returns the next-episode preview a file's chapters
// mark: a run of chapters titled like one in the last part of the file, five
// to 240 seconds long. It is not a marker by itself in this version; it ends
// the credits.
func DetectChapterPreview(chapters []models.MediaChapter, duration float64) (Segment, bool) {
	segment, ok := titleRun(chapters, isPreviewChapterTitle, duration)
	if !ok {
		return Segment{}, false
	}
	length := segment.End - segment.Start
	if length < 5 || length > 240 {
		return Segment{}, false
	}
	if duration > 0 && segment.Start < duration*0.5 {
		return Segment{}, false
	}
	return segment, true
}

// recapEndBefore returns where the recap a file's chapters mark ends, when that
// is at or before bound: an intro cannot start inside the recap.
func recapEndBefore(chapters []models.MediaChapter, duration, bound float64) (float64, bool) {
	recap, ok := DetectChapterRecap(chapters, duration)
	if !ok || recap.End > bound+0.5 {
		return 0, false
	}
	return recap.End, true
}

// previewStartInside returns where a preview starting inside the segment
// begins: the earliest of the stored preview marker and the chapter preview.
//
// The stored marker comes from an online provider or an earlier scan; the
// chapter is authored for this file. Credits never run past the preview
// either way.
func previewStartInside(candidate Candidate, segment Segment) (float64, bool) {
	best, found := segment.End, false
	if candidate.PreviewStart != nil && *candidate.PreviewStart > segment.Start && *candidate.PreviewStart < best {
		best, found = *candidate.PreviewStart, true
	}
	if preview, ok := DetectChapterPreview(candidate.Chapters, candidate.DurationSeconds); ok &&
		preview.Start > segment.Start && preview.Start < best {
		best, found = preview.Start, true
	}
	return best, found
}
