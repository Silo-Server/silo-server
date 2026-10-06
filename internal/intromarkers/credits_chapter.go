package intromarkers

import (
	"regexp"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

var (
	creditsChapterPattern = regexp.MustCompile(`(?i)(^|\s)(credits?|end\s+titles?|outro)(\s|:|$)`)
	// creditsEndPattern matches what follows the keyword in titles such as
	// "Credits End", which mark where credits stop. RE2 has no lookahead, so
	// the remainder is checked separately.
	creditsEndPattern = regexp.MustCompile(`(?i)^[\s:]+end\b`)
	// aroundCreditsPattern matches scenes placed around the credits, such as
	// "Post-Credits Scene", which are story rather than credits.
	aroundCreditsPattern = regexp.MustCompile(`(?i)\b(post|mid|after|pre)[\s-]*credits?\b`)
	// creditsScenePattern matches a credits or ending title that names a
	// scene, such as "Credits Scene", "End Credits Stinger", or "Ending
	// Scene", which is story that plays with or after the credits. Anime
	// "ED: …" titles are exempt, since their song names can hold these words.
	creditsScenePattern = regexp.MustCompile(`(?i)\b(scenes?|stingers?|tags?|bonus(es)?)\b`)
	// explicitEDChapterPattern matches anime ending chapters: "ED", "ED2",
	// "ED: Title". It is case-sensitive so "Ed's Story" is not an ending.
	explicitEDChapterPattern = regexp.MustCompile(`^ED(\d+)?([ :-].*)?$`)
	// endingChapterPattern matches "Ending", which in a movie is as likely
	// the story's ending as its credits.
	endingChapterPattern = regexp.MustCompile(`(?i)(^|\s)ending(\s|:|$)`)
	// namedCreditsPattern matches titles that name the credits outright,
	// unlike "Ending" or "Outro", which can also name the story's final scene.
	namedCreditsPattern = regexp.MustCompile(`(?i)(^|\s)(credits?|end\s+titles?)(\s|:|$)`)
)

// maximumAmbiguousCreditsChapterSeconds bounds a chapter that counts as
// credits only by the word "Ending" or "Outro". An anime ending runs about 90
// seconds; a longer chapter with such a title is usually the story's final
// scene.
const maximumAmbiguousCreditsChapterSeconds = 180.0

// creditsChapterGapSeconds is the most time between two credits chapters that
// still makes them adjacent. A wider gap is an unchaptered interval that the
// credits run does not cross.
const creditsChapterGapSeconds = 1.0

// DetectChapterCredits finds the credits chapters of a file of the given
// duration: the last run of adjacent chapters titled like credits, which
// together form one segment, as when "Ending" is followed by "End Credits".
// The run must start in the file's credits tail window and last as long as
// credits can. Its end is the next chapter's start, which an authored
// chapter after the credits keeps; the last chapter's end moves to the end of
// the file when it lies within the EOF snap.
func DetectChapterCredits(chapters []models.MediaChapter, duration float64, isMovie bool) (Segment, bool) {
	if duration <= 0 || len(chapters) == 0 {
		return Segment{}, false
	}
	limits := creditsLimitsFor(isMovie)
	sorted := append([]models.MediaChapter(nil), chapters...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].StartSeconds < sorted[j].StartSeconds })
	isCredits := func(i int) bool {
		title := sorted[i].Title
		if !isCreditsChapterTitle(title, isMovie) {
			return false
		}
		return !ambiguousCreditsTitle(title) || chapterSpan(sorted, i, duration) <= maximumAmbiguousCreditsChapterSeconds
	}

	last := len(sorted) - 1
	for last >= 0 && !isCredits(last) {
		last--
	}
	if last < 0 {
		return Segment{}, false
	}
	first := last
	for first > 0 && isCredits(first-1) &&
		sorted[first-1].EndSeconds >= sorted[first].StartSeconds-creditsChapterGapSeconds {
		first--
	}

	start := sorted[first].StartSeconds
	end := sorted[last].EndSeconds
	bounded := last+1 < len(sorted) && sorted[last+1].StartSeconds > sorted[last].StartSeconds
	if bounded {
		end = sorted[last+1].StartSeconds
	}
	if end <= 0 || end > duration {
		end = duration
	}
	// A following chapter, such as a post-credits scene, is not credits
	// however short it is.
	if !bounded {
		end = snapCreditsEnd(end, duration)
	}
	length := end - start
	if start < limits.windowStart(duration) || length < limits.minSeconds || length > limits.maxSeconds {
		return Segment{}, false
	}
	return Segment{
		Start:      start,
		End:        end,
		Confidence: creditsChapterConfidence,
		Algorithm:  CreditsChapterAlgorithm,
	}, true
}

// chapterSpan is how long chapter i of sorted plays: until the next chapter
// starts, or until its own end, which falls back to the end of a file of the
// given duration.
func chapterSpan(sorted []models.MediaChapter, i int, duration float64) float64 {
	chapter := sorted[i]
	end := chapter.EndSeconds
	if i+1 < len(sorted) && sorted[i+1].StartSeconds > chapter.StartSeconds {
		end = sorted[i+1].StartSeconds
	}
	if end <= 0 || end > duration {
		end = duration
	}
	return end - chapter.StartSeconds
}

// ambiguousCreditsTitle reports whether a credits chapter title names the
// credits only by "Ending" or "Outro". Anime "ED" tags are not ambiguous.
func ambiguousCreditsTitle(title string) bool {
	title = strings.TrimSpace(title)
	return !explicitEDChapterPattern.MatchString(title) && !namedCreditsPattern.MatchString(title)
}

// isCreditsChapterTitle reports whether a chapter title names end credits.
func isCreditsChapterTitle(title string, isMovie bool) bool {
	title = strings.TrimSpace(title)
	if title == "" || generatedChapterPattern.MatchString(title) || isIntroChapterTitle(title) ||
		aroundCreditsPattern.MatchString(title) {
		return false
	}
	explicitED := explicitEDChapterPattern.MatchString(title)
	if match := creditsChapterPattern.FindStringSubmatchIndex(title); match != nil {
		return !creditsEndPattern.MatchString(title[match[5]:]) && (explicitED || !creditsScenePattern.MatchString(title))
	}
	if explicitED {
		return true
	}
	return !isMovie && endingChapterPattern.MatchString(title) && !creditsScenePattern.MatchString(title)
}
