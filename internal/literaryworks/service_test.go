package literaryworks

import "testing"

func TestGeneratedWorkIDUsesTitleAndAuthorNotFormat(t *testing.T) {
	ebook := MatchItem{Title: "Project Hail Mary", Type: FormatEbook, Authors: []string{"Andy Weir"}}
	audio := MatchItem{Title: " Project  Hail Mary ", Type: FormatAudiobook, Authors: []string{"Andy Weir"}}

	if generatedWorkID(ebook) != generatedWorkID(audio) {
		t.Fatalf("generated IDs differ for same title/author across formats")
	}
}
