package scanner

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
)

func TestParseAudiobookFolderSingleM4B(t *testing.T) {
	ffprobePath := FFprobePathFromFFmpeg("ffmpeg")
	if _, err := exec.LookPath(ffprobePath); err != nil {
		ffprobePath = "ffprobe"
		if _, err := exec.LookPath(ffprobePath); err != nil {
			t.Skip("ffprobe not available")
		}
	}

	ctx := context.Background()
	got, err := parseAudiobookFolder(ctx, ffprobePath, "testdata/audiobook_fixtures/single_book")
	if err != nil {
		t.Fatalf("parseAudiobookFolder: %v", err)
	}
	if got.Title != "Test Audiobook" {
		t.Errorf("Title = %q, want %q", got.Title, "Test Audiobook")
	}
	if got.Author != "Test Author" {
		t.Errorf("Author = %q, want %q", got.Author, "Test Author")
	}
	if got.Series != "Test Series" {
		t.Errorf("Series = %q, want %q", got.Series, "Test Series")
	}
	if got.Year != 2024 {
		t.Errorf("Year = %d, want 2024", got.Year)
	}
	if len(got.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(got.Files))
	}
	if len(got.Files[0].Chapters) != 2 {
		t.Errorf("file 0 chapters = %d, want 2", len(got.Files[0].Chapters))
	}
}

func TestParseAudiobookFolderMultiFile(t *testing.T) {
	ffprobePath := FFprobePathFromFFmpeg("ffmpeg")
	if _, err := exec.LookPath(ffprobePath); err != nil {
		ffprobePath = "ffprobe"
		if _, err := exec.LookPath(ffprobePath); err != nil {
			t.Skip("ffprobe not available")
		}
	}

	ctx := context.Background()
	got, err := parseAudiobookFolder(ctx, ffprobePath, "testdata/audiobook_fixtures/multi_file")
	if err != nil {
		t.Fatalf("parseAudiobookFolder: %v", err)
	}
	if got.Title != "Multi File Test" {
		t.Errorf("Title = %q, want %q", got.Title, "Multi File Test")
	}
	if got.Author != "Multi Author" {
		t.Errorf("Author = %q, want %q", got.Author, "Multi Author")
	}
	if got.Year != 2023 {
		t.Errorf("Year = %d, want 2023", got.Year)
	}
	if len(got.Files) != 3 {
		t.Fatalf("got %d files, want 3", len(got.Files))
	}
	for i, f := range got.Files {
		if len(f.Chapters) != 1 {
			t.Errorf("file %d: %d synthesized chapters, want 1", i, len(f.Chapters))
			continue
		}
		wantStem := fmt.Sprintf("part%d", i+1)
		if f.Chapters[0].Title != wantStem {
			t.Errorf("file %d: chapter title = %q, want %q", i, f.Chapters[0].Title, wantStem)
		}
	}
}

func TestStripNarratorSuffix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Solitaire Read By Holly Gibbs", "Solitaire"},
		{"The Skyward Series 3 - Cytonic (UK Version: Read by Sophie Aldred)", "The Skyward Series 3 - Cytonic"},
		{"Mick Stranahan (unabridged) 1 - Skin Tight", "Mick Stranahan 1 - Skin Tight"},
		{"An Evening with Alan Titchmarsh (Unabridged)", "An Evening with Alan Titchmarsh"},
		// "Read by Celebrities" is a series name here, not a narrator credit
		{"Classic Stories and Tales Read by Celebrities 1 - Classics of Childhood, Volume One",
			"Classic Stories and Tales Read by Celebrities 1 - Classics of Childhood, Volume One"},
		{"Old Harry's Game", "Old Harry's Game"}, // unchanged
	}
	for _, c := range cases {
		got := stripNarratorSuffix(c.in)
		if got != c.want {
			t.Errorf("stripNarratorSuffix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
