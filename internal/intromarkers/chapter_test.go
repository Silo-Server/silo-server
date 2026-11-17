package intromarkers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestDetectChapterIntroRejectsGeneratedAndAdjacentMatches(t *testing.T) {
	if _, ok := DetectChapterIntro([]models.MediaChapter{
		{Index: 0, Title: "Chapter 01", StartSeconds: 0, EndSeconds: 30},
		{Index: 1, Title: "Part 1", StartSeconds: 30, EndSeconds: 600},
	}, false); ok {
		t.Fatal("generated chapter title should not match")
	}
	if _, ok := DetectChapterIntro([]models.MediaChapter{
		{Index: 0, Title: "Intro", StartSeconds: 0, EndSeconds: 30},
		{Index: 1, Title: "Opening", StartSeconds: 30, EndSeconds: 90},
		{Index: 2, Title: "Part 1", StartSeconds: 90, EndSeconds: 600},
	}, false); ok {
		t.Fatal("adjacent intro-like chapters should not match")
	}
}

func TestDetectChapterIntroRejectsAmbiguousOpusTitles(t *testing.T) {
	for _, title := range []string{"op", "Op", "Op. 5", "Op 5"} {
		t.Run(title, func(t *testing.T) {
			if _, ok := DetectChapterIntro([]models.MediaChapter{
				{Index: 0, Title: title, StartSeconds: 0, EndSeconds: 45},
				{Index: 1, Title: "Movement I", StartSeconds: 45, EndSeconds: 600},
			}, false); ok {
				t.Fatalf("ambiguous title %q should not match", title)
			}
		})
	}
}

func TestDetectChapterIntroAcceptsExplicitOPTitles(t *testing.T) {
	for _, title := range []string{"OP", "OP1", "OP 1", "OP: Opening", "OP - Opening"} {
		t.Run(title, func(t *testing.T) {
			segment, ok := DetectChapterIntro([]models.MediaChapter{
				{Index: 0, Title: title, StartSeconds: 0, EndSeconds: 45},
				{Index: 1, Title: "Part 1", StartSeconds: 45, EndSeconds: 600},
			}, false)
			if !ok {
				t.Fatalf("explicit title %q should match", title)
			}
			if segment.Start != 0 || segment.End != 45 {
				t.Fatalf("unexpected segment %.1f-%.1f", segment.Start, segment.End)
			}
		})
	}
}

// Cold opens: many anime releases title the cold open Intro and the opening
// song OP. The episode layouts are those of issue #4382.
func TestDetectChapterIntroColdOpens(t *testing.T) {
	coldOpenThenOP := []models.MediaChapter{
		{Index: 0, Title: "Intro", StartSeconds: 0, EndSeconds: 133},
		{Index: 1, Title: "OP", StartSeconds: 133, EndSeconds: 223},
		{Index: 2, Title: "Part A", StartSeconds: 223, EndSeconds: 800},
		{Index: 3, Title: "Part B", StartSeconds: 800, EndSeconds: 1300},
		{Index: 4, Title: "ED", StartSeconds: 1300, EndSeconds: 1390},
		{Index: 5, Title: "Next Episode", StartSeconds: 1390, EndSeconds: 1422},
	}
	opOnly := []models.MediaChapter{
		{Index: 0, Title: "OP", StartSeconds: 0, EndSeconds: 90},
		{Index: 1, Title: "Part A", StartSeconds: 90, EndSeconds: 700},
		{Index: 2, Title: "Part B", StartSeconds: 700, EndSeconds: 1300},
		{Index: 3, Title: "ED", StartSeconds: 1300, EndSeconds: 1390},
	}
	loneIntro := []models.MediaChapter{
		{Index: 0, Title: "Intro", StartSeconds: 0, EndSeconds: 31},
		{Index: 1, Title: "Part A", StartSeconds: 31, EndSeconds: 700},
		{Index: 2, Title: "Part B", StartSeconds: 700, EndSeconds: 1300},
		{Index: 3, Title: "ED", StartSeconds: 1300, EndSeconds: 1390},
	}
	introPartOP := []models.MediaChapter{
		{Index: 0, Title: "Introduction", StartSeconds: 0, EndSeconds: 40},
		{Index: 1, Title: "Part A", StartSeconds: 40, EndSeconds: 100},
		{Index: 2, Title: "Opening: Song", StartSeconds: 100, EndSeconds: 190},
		{Index: 3, Title: "Part B", StartSeconds: 190, EndSeconds: 1300},
	}
	westernIntro := []models.MediaChapter{
		{Index: 0, Title: "Prologue", StartSeconds: 0, EndSeconds: 120},
		{Index: 1, Title: "Intro", StartSeconds: 120, EndSeconds: 160},
		{Index: 2, Title: "Act 1", StartSeconds: 160, EndSeconds: 1300},
	}
	openingCredits := []models.MediaChapter{
		{Index: 0, Title: "Opening Credits", StartSeconds: 0, EndSeconds: 60},
		{Index: 1, Title: "Act 1", StartSeconds: 60, EndSeconds: 1300},
	}

	for _, tc := range []struct {
		name                string
		chapters            []models.MediaChapter
		releaseNamesOpening bool
		wantStart, wantEnd  float64
		wantOK              bool
	}{
		// Adjacent intro-like chapters stay ambiguous, so Chromaprint finds
		// the opening, as before.
		{name: "cold open then OP", chapters: coldOpenThenOP, releaseNamesOpening: true},
		{name: "OP only", chapters: opOnly, releaseNamesOpening: true, wantOK: true, wantStart: 0, wantEnd: 90},
		{name: "lone Intro in a release that names OP", chapters: loneIntro, releaseNamesOpening: true},
		{name: "lone Intro in a release that never names OP", chapters: loneIntro, wantOK: true, wantStart: 0, wantEnd: 31},
		{name: "Introduction before a later Opening in the file", chapters: introPartOP, wantOK: true, wantStart: 100, wantEnd: 190},
		{name: "western Intro", chapters: westernIntro, wantOK: true, wantStart: 120, wantEnd: 160},
		{name: "Opening Credits is not a cold open", chapters: openingCredits, releaseNamesOpening: true, wantOK: true, wantStart: 0, wantEnd: 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			segment, ok := DetectChapterIntro(tc.chapters, tc.releaseNamesOpening)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (segment %+v)", ok, tc.wantOK, segment)
			}
			if ok && (segment.Start != tc.wantStart || segment.End != tc.wantEnd) {
				t.Fatalf("segment %.1f-%.1f, want %.1f-%.1f", segment.Start, segment.End, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

func TestChaptersNameOpening(t *testing.T) {
	for title, want := range map[string]bool{
		"OP":              true,
		"OP2":             true,
		"Opening":         true,
		"Opening 2":       true,
		"Opening: Song":   true,
		"Opening Credits": false,
		"Intro":           false,
		"Op. 5":           false,
	} {
		if got := chaptersNameOpening([]models.MediaChapter{{Title: title}}); got != want {
			t.Errorf("chaptersNameOpening(%q) = %v, want %v", title, got, want)
		}
	}
}
