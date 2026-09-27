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
)

// DetectChapterCredits finds the credits chapter of a file of the given
// duration: the last chapter titled like credits whose neighbors are not.
// The chapter must start in the file's credits tail window and last as long
// as credits can. Its end is the next chapter's start, which an authored
// chapter after the credits keeps; the last chapter's end moves to the end of
// the file when it lies within the EOF snap.
func DetectChapterCredits(chapters []models.MediaChapter, duration float64, isMovie bool) (Segment, bool) {
	if duration <= 0 || len(chapters) == 0 {
		return Segment{}, false
	}
	limits := creditsLimitsFor(isMovie)
	sorted := append([]models.MediaChapter(nil), chapters...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].StartSeconds < sorted[j].StartSeconds })
	isCredits := func(i int) bool { return isCreditsChapterTitle(sorted[i].Title, isMovie) }

	for i := len(sorted) - 1; i >= 0; i-- {
		if !isCredits(i) {
			continue
		}
		// Two credits-like chapters in a row leave it unclear which one the
		// credits are.
		if (i > 0 && isCredits(i-1)) || (i+1 < len(sorted) && isCredits(i+1)) {
			continue
		}
		chapter := sorted[i]
		end := chapter.EndSeconds
		bounded := i+1 < len(sorted) && sorted[i+1].StartSeconds > chapter.StartSeconds
		if bounded {
			end = sorted[i+1].StartSeconds
		}
		if end <= 0 || end > duration {
			end = duration
		}
		// A following chapter, such as a post-credits scene, is not credits
		// however short it is.
		if !bounded {
			end = snapCreditsEnd(end, duration)
		}
		length := end - chapter.StartSeconds
		if chapter.StartSeconds < limits.windowStart(duration) || length < limits.minSeconds || length > limits.maxSeconds {
			return Segment{}, false
		}
		return Segment{
			Start:      chapter.StartSeconds,
			End:        end,
			Confidence: creditsChapterConfidence,
			Algorithm:  CreditsChapterAlgorithm,
		}, true
	}
	return Segment{}, false
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
