package intromarkers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestIsCreditsChapterTitle(t *testing.T) {
	cases := []struct {
		title       string
		episode     bool
		movie       bool
		description string
	}{
		{"Credits", true, true, "plain credits"},
		{"credit", true, true, "singular"},
		{"End Credits", true, true, "end credits"},
		{"End Titles", true, true, "end titles"},
		{"end title", true, true, "lowercase singular end title"},
		{"Outro", true, true, "outro"},
		{"Credits: Cast", true, true, "credits with a subtitle"},
		{"ED", true, true, "anime ending"},
		{"ED2", true, true, "numbered anime ending"},
		{"ED: Song Name", true, true, "anime ending with song"},
		{"ED - Song Name", true, true, "anime ending with dash"},
		{"Ending", true, false, "ending is credits only in episodes"},
		{"Ending Theme", true, false, "ending theme"},
		{"Opening Credits", false, false, "opening credits are an intro"},
		{"Intro", false, false, "intro"},
		{"OP", false, false, "anime opening"},
		{"Post-Credits Scene", false, false, "post-credits scene"},
		{"Mid Credits", false, false, "mid-credits scene"},
		{"After Credits", false, false, "after-credits scene"},
		{"Pre-credits", false, false, "pre-credits scene"},
		{"Credits End", false, false, "credits end marks where credits stop"},
		{"Credits: End", false, false, "credits end with colon"},
		{"Ed's Story", false, false, "a name, not ED"},
		{"EDGE", false, false, "a word starting with ED"},
		{"ed", false, false, "lowercase ed"},
		{"Chapter 12", false, false, "generated chapter"},
		{"Accreditation", false, false, "credit inside a word"},
		{"", false, false, "empty"},
		{"Part 3", false, false, "story chapter"},
		{"The Happy Ending", true, false, "ending as a word"},
		{"Ending Credits", true, true, "credits keyword wins"},
	}
	for _, tc := range cases {
		if got := isCreditsChapterTitle(tc.title, false); got != tc.episode {
			t.Errorf("%s: episode %q = %v, want %v", tc.description, tc.title, got, tc.episode)
		}
		if got := isCreditsChapterTitle(tc.title, true); got != tc.movie {
			t.Errorf("%s: movie %q = %v, want %v", tc.description, tc.title, got, tc.movie)
		}
	}
}

func chapter(title string, start, end float64) models.MediaChapter {
	return models.MediaChapter{Title: title, StartSeconds: start, EndSeconds: end}
}

func TestDetectChapterCredits(t *testing.T) {
	cases := []struct {
		name      string
		chapters  []models.MediaChapter
		duration  float64
		isMovie   bool
		wantOK    bool
		wantStart float64
		wantEnd   float64
	}{
		{
			name:     "last chapter runs to the end",
			chapters: []models.MediaChapter{chapter("Story", 0, 1320), chapter("Credits", 1320, 1400)},
			duration: 1400, wantOK: true, wantStart: 1320, wantEnd: 1400,
		},
		{
			name:     "end snaps to the end of the file",
			chapters: []models.MediaChapter{chapter("Story", 0, 1320), chapter("End Credits", 1320, 1390)},
			duration: 1400, wantOK: true, wantStart: 1320, wantEnd: 1400,
		},
		{
			name:     "a preview after the ending ends it",
			chapters: []models.MediaChapter{chapter("Part B", 0, 1300), chapter("ED", 1300, 1390), chapter("Preview", 1390, 1420)},
			duration: 1420, wantOK: true, wantStart: 1300, wantEnd: 1390,
		},
		{
			name: "a post-credits scene near the end keeps its chapter",
			chapters: []models.MediaChapter{
				chapter("Story", 0, 1300), chapter("Credits", 1300, 1390), chapter("Post-Credits Scene", 1390, 1400),
			},
			duration: 1400, wantOK: true, wantStart: 1300, wantEnd: 1390,
		},
		{
			name:     "an untitled trailing chapter near the end bounds the credits too",
			chapters: []models.MediaChapter{chapter("Story", 0, 1300), chapter("ED", 1300, 1392), chapter("", 1392, 1400)},
			duration: 1400, wantOK: true, wantStart: 1300, wantEnd: 1392,
		},
		{
			name: "reverse scan takes the last credits chapter",
			chapters: []models.MediaChapter{
				chapter("Previously", 0, 60), chapter("Credits", 60, 100), chapter("Story", 100, 1300),
				chapter("Credits", 1300, 1370), chapter("Stinger", 1370, 1400),
			},
			duration: 1400, wantOK: true, wantStart: 1300, wantEnd: 1370,
		},
		{
			name: "ambiguous neighbors are skipped",
			chapters: []models.MediaChapter{
				chapter("Story", 0, 1250), chapter("End Credits", 1250, 1300), chapter("Credits", 1300, 1400),
			},
			duration: 1400,
		},
		{
			name: "an earlier unambiguous match is used after skipping ambiguous ones",
			chapters: []models.MediaChapter{
				chapter("Story", 0, 1200), chapter("Outro", 1200, 1260), chapter("Story 2", 1260, 1300),
				chapter("Credits", 1300, 1350), chapter("Credits", 1350, 1400),
			},
			duration: 1400, wantOK: true, wantStart: 1200, wantEnd: 1260,
		},
		{
			name:     "credits must start in the tail window",
			chapters: []models.MediaChapter{chapter("Story", 0, 900), chapter("Credits", 900, 1400)},
			duration: 1400,
		},
		{
			name:     "credits shorter than fifteen seconds",
			chapters: []models.MediaChapter{chapter("Story", 0, 1300), chapter("Credits", 1300, 1310), chapter("Stinger", 1310, 1400)},
			duration: 1400,
		},
		{
			name:     "a movie's credits may run fifteen minutes",
			chapters: []models.MediaChapter{chapter("Story", 0, 6600), chapter("End Credits", 6600, 7200)},
			duration: 7200, isMovie: true, wantOK: true, wantStart: 6600, wantEnd: 7200,
		},
		{
			name:     "an episode's tail window is shorter",
			chapters: []models.MediaChapter{chapter("Story", 0, 6600), chapter("End Credits", 6600, 7200)},
			duration: 7200,
		},
		{
			name:     "ending is not movie credits",
			chapters: []models.MediaChapter{chapter("Story", 0, 6800), chapter("Ending", 6800, 7200)},
			duration: 7200, isMovie: true,
		},
		{
			name:     "unsorted chapters",
			chapters: []models.MediaChapter{chapter("Credits", 1320, 1400), chapter("Story", 0, 1320)},
			duration: 1400, wantOK: true, wantStart: 1320, wantEnd: 1400,
		},
		{
			name:     "no duration",
			chapters: []models.MediaChapter{chapter("Credits", 1320, 1400)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DetectChapterCredits(tc.chapters, tc.duration, tc.isMovie)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v (%+v), want %v", ok, got, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.Start != tc.wantStart || got.End != tc.wantEnd {
				t.Fatalf("credits = %.1f-%.1f, want %.1f-%.1f", got.Start, got.End, tc.wantStart, tc.wantEnd)
			}
			if got.Algorithm != CreditsChapterAlgorithm || got.Confidence != 0.95 {
				t.Fatalf("credits = %+v, want %s at 0.95", got, CreditsChapterAlgorithm)
			}
		})
	}
}
