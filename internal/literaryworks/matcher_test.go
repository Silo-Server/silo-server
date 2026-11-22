package literaryworks

import "testing"

func TestMatcherSameTitleDifferentAuthorDoesNotLink(t *testing.T) {
	ebook := MatchItem{ContentID: "e1", Type: FormatEbook, Title: "The Last Adventure", Authors: []string{"A. Lee"}}
	audio := MatchItem{ContentID: "a1", Type: FormatAudiobook, Title: "The Last Adventure", Authors: []string{"B. Lee"}}
	candidate := ScoreCandidate(ebook, audio)
	if candidate.Score >= AutoLinkThreshold {
		t.Fatalf("score = %v, want below threshold", candidate.Score)
	}
}
