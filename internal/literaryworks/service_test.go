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
