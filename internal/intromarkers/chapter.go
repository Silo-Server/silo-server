package intromarkers

import (
	"regexp"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

var introChapterPattern = regexp.MustCompile(`(?i)(^|\s)(intro|introduction|opening)(\s|:|$)`)
var generatedChapterPattern = regexp.MustCompile(`(?i)^chapter\s*\d+$`)

// introWordChapterPattern matches the titles introChapterPattern takes by the
// word intro or introduction, which many releases also give a cold open.
var introWordChapterPattern = regexp.MustCompile(`(?i)(^|\s)(intro|introduction)(\s|:|$)`)

// openingSongChapterPattern matches an Opening chapter as anime releases name
// the opening song: Opening, Opening 2, Opening: <song>. Opening Credits and
// other two-word titles western releases give their intro do not match.
var openingSongChapterPattern = regexp.MustCompile(`(?i)^opening\s*\d*(\s*[:\-].*)?$`)

// DetectChapterIntro returns the intro a file's chapters mark.
// releaseNamesOpening reports that another file of the file's season group
// has an opening song chapter (see chaptersNameOpening). A release that names
// its opening OP or Opening and also has Intro chapters uses Intro for the
// cold open, so such a chapter is not the intro there, nor in a file that has
// its own opening song chapter.
func DetectChapterIntro(chapters []models.MediaChapter, releaseNamesOpening bool) (Segment, bool) {
	coldOpenIntros := releaseNamesOpening || chaptersNameOpening(chapters)
	for i, chapter := range chapters {
		title := strings.TrimSpace(chapter.Title)
		if !isIntroChapterTitle(title) {
			continue
		}
		if coldOpenIntros && isColdOpenChapterTitle(title) {
			continue
		}
		if i > 0 && isIntroChapterTitle(strings.TrimSpace(chapters[i-1].Title)) {
			continue
		}
		if i+1 < len(chapters) && isIntroChapterTitle(strings.TrimSpace(chapters[i+1].Title)) {
			continue
		}

		end := chapter.EndSeconds
		if i+1 < len(chapters) && chapters[i+1].StartSeconds > chapter.StartSeconds {
			end = chapters[i+1].StartSeconds
		}
		duration := end - chapter.StartSeconds
		if duration < 10 || duration > 180 {
			continue
		}
		return Segment{
			Start:      chapter.StartSeconds,
			End:        end,
			Confidence: 0.95,
			Algorithm:  ChapterAlgorithm,
		}, true
	}
	return Segment{}, false
}

func isIntroChapterTitle(title string) bool {
	if title == "" || generatedChapterPattern.MatchString(title) {
		return false
	}
	if introChapterPattern.MatchString(title) {
		return true
	}
	return isExplicitOPChapterTitle(title)
}

// chaptersNameOpening reports whether chapters include an opening song
// chapter: an explicit OP title or an Opening title as
// openingSongChapterPattern defines it.
func chaptersNameOpening(chapters []models.MediaChapter) bool {
	for _, chapter := range chapters {
		if isOpeningSongChapterTitle(strings.TrimSpace(chapter.Title)) {
			return true
		}
	}
	return false
}

func isOpeningSongChapterTitle(title string) bool {
	return isExplicitOPChapterTitle(title) || openingSongChapterPattern.MatchString(title)
}

// isColdOpenChapterTitle reports whether an intro-like title is one a release
// that names its opening song may give its cold open: Intro or Introduction,
// and not an opening song title.
func isColdOpenChapterTitle(title string) bool {
	return introWordChapterPattern.MatchString(title) && !isOpeningSongChapterTitle(title)
}

// hasColdOpenChapter reports whether chapters include one whose title
// isColdOpenChapterTitle accepts.
func hasColdOpenChapter(chapters []models.MediaChapter) bool {
	for _, chapter := range chapters {
		title := strings.TrimSpace(chapter.Title)
		if isIntroChapterTitle(title) && isColdOpenChapterTitle(title) {
			return true
		}
	}
	return false
}

// openingNamedGroups returns the season group keys (seasonGroupKey) of
// candidates in which some file has an opening song chapter. candidates
// must hold whole season groups for a missing key to mean the group has none.
func openingNamedGroups(candidates []Candidate) map[string]bool {
	named := map[string]bool{}
	for _, candidate := range candidates {
		if chaptersNameOpening(candidate.Chapters) {
			named[seasonGroupKey(candidate)] = true
		}
	}
	return named
}

func isExplicitOPChapterTitle(title string) bool {
	if !strings.HasPrefix(title, "OP") {
		return false
	}
	if len(title) == len("OP") {
		return true
	}
	switch next := title[len("OP")]; {
	case next >= '0' && next <= '9':
		return true
	case next == ' ' || next == ':' || next == '-':
		return strings.TrimSpace(title[len("OP")+1:]) != ""
	default:
		return false
	}
}
