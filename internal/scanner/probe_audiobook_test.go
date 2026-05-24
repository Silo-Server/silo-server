package scanner

import (
	"context"
	"os/exec"
	"testing"
)

func TestProbeFileAudiobookFixture(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available; skipping fixture-based test")
	}

	ffprobePath := FFprobePathFromFFmpeg("ffmpeg")
	if _, err := exec.LookPath(ffprobePath); err != nil {
		// Fall back to plain "ffprobe" on PATH
		ffprobePath = "ffprobe"
		if _, err := exec.LookPath(ffprobePath); err != nil {
			t.Skip("ffprobe not available; skipping fixture-based test")
		}
	}

	ctx := context.Background()
	got, err := ProbeFile(ctx, ffprobePath, "testdata/audiobook_fixtures/single_book/book.m4b")
	if err != nil {
		t.Fatalf("ProbeFile: %v", err)
	}

	// Chapters
	if len(got.Chapters) != 2 {
		t.Errorf("Chapters = %d, want 2", len(got.Chapters))
	} else {
		if got.Chapters[0].Title != "Intro" {
			t.Errorf("Chapters[0].Title = %q, want %q", got.Chapters[0].Title, "Intro")
		}
		if got.Chapters[1].Title != "Outro" {
			t.Errorf("Chapters[1].Title = %q, want %q", got.Chapters[1].Title, "Outro")
		}
	}

	// Format tags
	if got.FormatTags == nil {
		t.Fatal("FormatTags is nil")
	}
	if title := got.FormatTags["title"]; title != "Test Audiobook" {
		t.Errorf("FormatTags[\"title\"] = %q, want %q", title, "Test Audiobook")
	}
	if artist := got.FormatTags["artist"]; artist != "Test Author" {
		t.Errorf("FormatTags[\"artist\"] = %q, want %q", artist, "Test Author")
	}
	if album := got.FormatTags["album"]; album != "Test Series" {
		t.Errorf("FormatTags[\"album\"] = %q, want %q", album, "Test Series")
	}
}
