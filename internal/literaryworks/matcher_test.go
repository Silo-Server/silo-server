package literaryworks

import "testing"

func TestMatcherSameTitleAuthorLinks(t *testing.T) {
	ebook := MatchItem{ContentID: "e1", Type: FormatEbook, Title: "Project Hail Mary", Authors: []string{"Andy Weir"}}
	audio := MatchItem{ContentID: "a1", Type: FormatAudiobook, Title: "Project Hail Mary", Authors: []string{"Andy Weir"}, Narrators: []string{"Ray Porter"}}
	candidate := ScoreCandidate(ebook, audio)
	if candidate.Score < AutoLinkThreshold || candidate.LinkSource != LinkMetadataMatch {
		t.Fatalf("candidate = %#v, want metadata auto-link", candidate)
	}
}

func TestMatcherSameTitleDifferentAuthorDoesNotLink(t *testing.T) {
	ebook := MatchItem{ContentID: "e1", Type: FormatEbook, Title: "The Last Adventure", Authors: []string{"A. Lee"}}
	audio := MatchItem{ContentID: "a1", Type: FormatAudiobook, Title: "The Last Adventure", Authors: []string{"B. Lee"}}
	candidate := ScoreCandidate(ebook, audio)
	if candidate.Score >= AutoLinkThreshold {
		t.Fatalf("score = %v, want below threshold", candidate.Score)
	}
}

func TestMatcherSeriesIndexLinksSubtitleVariants(t *testing.T) {
	ebook := MatchItem{ContentID: "e1", Type: FormatEbook, Title: "The Last Adventure", Authors: []string{"A. Lee"}, SeriesName: "Constance Verity", SeriesIndex: floatPtr(1)}
	audio := MatchItem{ContentID: "a1", Type: FormatAudiobook, Title: "Constance Verity 1 - The Last Adventure of Constance Verity", Authors: []string{"A. Lee"}, SeriesName: "Constance Verity", SeriesIndex: floatPtr(1)}
	candidate := ScoreCandidate(ebook, audio)
	if candidate.Score < AutoLinkThreshold || candidate.LinkSource != LinkSeriesMatch {
		t.Fatalf("candidate = %#v, want series auto-link", candidate)
	}
}

func TestMatcherSharedExternalIDWins(t *testing.T) {
	ebook := MatchItem{ContentID: "e1", Type: FormatEbook, Title: "Different Ebook Title", Authors: []string{"A. Lee"}, ExternalIDs: map[string]string{"isbn": "978123"}}
	audio := MatchItem{ContentID: "a1", Type: FormatAudiobook, Title: "Different Audio Title", Authors: []string{"B. Lee"}, ExternalIDs: map[string]string{"isbn": "978123"}}
	candidate := ScoreCandidate(ebook, audio)
	if candidate.Score < AutoLinkThreshold || candidate.LinkSource != LinkExternalID {
		t.Fatalf("candidate = %#v, want external id auto-link", candidate)
	}
}

func TestMatcherIgnoresASINAsWorkIdentity(t *testing.T) {
	ebook := MatchItem{ContentID: "e1", Type: FormatEbook, Title: "Different Ebook Title", Authors: []string{"A. Lee"}, ExternalIDs: map[string]string{"asin": "B123"}}
	audio := MatchItem{ContentID: "a1", Type: FormatAudiobook, Title: "Different Audio Title", Authors: []string{"B. Lee"}, ExternalIDs: map[string]string{"asin": "B123"}}
	candidate := ScoreCandidate(ebook, audio)
	if candidate.Score >= AutoLinkThreshold {
		t.Fatalf("candidate = %#v, want ASIN below auto-link threshold", candidate)
	}
}

func floatPtr(v float64) *float64 {
	return &v
}
