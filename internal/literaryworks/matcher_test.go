package literaryworks

import (
	"maps"
	"testing"
)

func TestMatcherSameTitleDifferentAuthorDoesNotLink(t *testing.T) {
	ebook := MatchItem{ContentID: "e1", Type: FormatEbook, Title: "The Last Adventure", Authors: []string{"A. Lee"}}
	audio := MatchItem{ContentID: "a1", Type: FormatAudiobook, Title: "The Last Adventure", Authors: []string{"B. Lee"}}
	candidate := ScoreCandidate(ebook, audio)
	if candidate.Score >= AutoLinkThreshold {
		t.Fatalf("score = %v, want below threshold", candidate.Score)
	}
}

// ScoreCandidate's checks run in descending score, so AutoLinkContent's
// anchor is the strongest evidence: a shared edition id, then series and
// author, then a shared work id, which also outranks an author conflict.
func TestMatcherScoresSharedWorkIDBelowEditionAndSeries(t *testing.T) {
	index := 2.0
	work := map[string]WorkID{"openlibrary_work": {ID: "OL1W", Evidence: "isbn"}}
	targetWork := map[string]WorkID{"openlibrary_work": {ID: "OL1W", Evidence: "title_author"}}
	base := func(id, format, author string) MatchItem {
		return MatchItem{ContentID: id, Type: format, Title: "The Last Adventure", Authors: []string{author}}
	}
	for _, tc := range []struct {
		name       string
		edit       func(source, target *MatchItem)
		score      float64
		linkSource string
		evidence   map[string]string
	}{
		{"shared edition id beats a shared work id", func(s, tg *MatchItem) {
			s.ExternalIDs, tg.ExternalIDs = map[string]string{"isbn": "9780306406157"}, map[string]string{"isbn": "9780306406157"}
			s.WorkIDs, tg.WorkIDs = work, targetWork
		}, 0.98, LinkExternalID, map[string]string{"external_id": "isbn:9780306406157"}},
		{"series and author beat a shared work id", func(s, tg *MatchItem) {
			s.SeriesName, s.SeriesIndex, tg.SeriesName, tg.SeriesIndex = "Saga", &index, "Saga", &index
			s.WorkIDs, tg.WorkIDs = work, targetWork
		}, 0.92, LinkSeriesMatch, map[string]string{"series": "Saga", "author": "lee"}},
		{"shared work id beats an author conflict", func(s, tg *MatchItem) {
			tg.Authors = []string{"Bo Chen"}
			s.WorkIDs, tg.WorkIDs = work, targetWork
		}, 0.9, LinkExternalID, map[string]string{
			"work_id": "openlibrary_work:OL1W", "source_work_evidence": "isbn", "target_work_evidence": "title_author",
		}},
		{"title and author without a work id", func(*MatchItem, *MatchItem) {}, 0.9, LinkMetadataMatch,
			map[string]string{"title": "The Last Adventure", "author": "lee"}},
		{"another work id is no match", func(s, tg *MatchItem) {
			tg.Authors = []string{"Bo Chen"}
			s.WorkIDs, tg.WorkIDs = work, map[string]WorkID{"openlibrary_work": {ID: "OL2W"}}
		}, 0.2, LinkMetadataMatch, map[string]string{"conflict": "author"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, target := base("e1", FormatEbook, "A. Lee"), base("a1", FormatAudiobook, "A. Lee")
			tc.edit(&source, &target)
			got := ScoreCandidate(source, target)
			if got.Score != tc.score || got.LinkSource != tc.linkSource || !maps.Equal(got.Evidence, tc.evidence) {
				t.Fatalf("candidate = %v %s %v, want %v %s %v", got.Score, got.LinkSource, got.Evidence, tc.score, tc.linkSource, tc.evidence)
			}
		})
	}
}
