package literaryworks

import "testing"

func TestFilesToResponseIncludesOriginalFormatAndDuration(t *testing.T) {
	files := []WorkFile{
		{FileID: 10, FilePath: "/books/Project Hail Mary.epub", Size: 12345},
		{FileID: 20, FilePath: "/books/Project Hail Mary.m4b", Size: 98765, DurationSeconds: 58200},
	}

	resp := filesToResponse(files)

	if len(resp) != 2 {
		t.Fatalf("len = %d, want 2", len(resp))
	}
	if resp[0].OriginalName != "Project Hail Mary.epub" || resp[0].Format != "epub" || resp[0].MIMEType == "" {
		t.Fatalf("ebook file response = %#v", resp[0])
	}
	if resp[1].Format != "m4b" || resp[1].DurationSeconds != 58200 {
		t.Fatalf("audio file response = %#v", resp[1])
	}
}

func TestGeneratedWorkIDUsesTitleAndAuthorNotFormat(t *testing.T) {
	ebook := MatchItem{Title: "Project Hail Mary", Type: FormatEbook, Authors: []string{"Andy Weir"}}
	audio := MatchItem{Title: " Project  Hail Mary ", Type: FormatAudiobook, Authors: []string{"Andy Weir"}}

	if generatedWorkID(ebook) != generatedWorkID(audio) {
		t.Fatalf("generated IDs differ for same title/author across formats")
	}
}

func TestCompactContentIDsTrimsAndDeduplicates(t *testing.T) {
	got := compactContentIDs([]string{" ebook-1 ", "", "audio-1", "ebook-1"})

	if len(got) != 2 || got[0] != "ebook-1" || got[1] != "audio-1" {
		t.Fatalf("compactContentIDs = %#v", got)
	}
}
