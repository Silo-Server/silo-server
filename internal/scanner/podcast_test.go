package scanner

import (
	"context"
	"os/exec"
	"testing"
)

func TestParsePodcastShow(t *testing.T) {
	ffprobePath := FFprobePathFromFFmpeg("ffmpeg")
	if _, err := exec.LookPath(ffprobePath); err != nil {
		ffprobePath = "ffprobe"
		if _, err := exec.LookPath(ffprobePath); err != nil {
			t.Skip("ffprobe not available")
		}
	}

	ctx := context.Background()
	got, err := parsePodcastShow(ctx, ffprobePath, "testdata/podcast_fixtures/show_a")
	if err != nil {
		t.Fatalf("parsePodcastShow: %v", err)
	}
	if got.Title != "Show A" {
		t.Errorf("Title = %q, want %q", got.Title, "Show A")
	}
	if got.Author != "Show A Host" {
		t.Errorf("Author = %q, want %q", got.Author, "Show A Host")
	}
	if got.Year != 2024 {
		t.Errorf("Year = %d, want 2024", got.Year)
	}
	if len(got.Episodes) != 3 {
		t.Fatalf("got %d episodes, want 3", len(got.Episodes))
	}
	for i, ep := range got.Episodes {
		wantTrack := i + 1
		if ep.Track != wantTrack {
			t.Errorf("episode %d Track = %d, want %d", i, ep.Track, wantTrack)
		}
	}
}
