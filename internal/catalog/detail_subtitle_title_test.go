package catalog

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestVersionSubtitleTracksPreserveSidecarTitleSource(t *testing.T) {
	file := &models.MediaFile{ExternalSubtitles: []models.ExternalSubtitle{
		{Path: "untitled.en.ass", EmbeddedTitle: "Signs"},
		{Path: "explicit.en.ass", Title: "explicit.en.ass", EmbeddedTitle: "Signs"},
	}}
	tracks := buildVersionSubtitleTracks(file)
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
	if tracks[0].Title != "untitled.en.ass" || !tracks[0].TitleIsFallback {
		t.Fatalf("untitled sidecar = %+v", tracks[0])
	}
	if tracks[1].Title != "explicit.en.ass" || tracks[1].TitleIsFallback {
		t.Fatalf("explicitly titled sidecar = %+v", tracks[1])
	}
}
