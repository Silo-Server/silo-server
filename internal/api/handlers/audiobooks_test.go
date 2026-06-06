package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestAudiobookListItemsDoNotExposeRawPosterKeysWithoutPresigner(t *testing.T) {
	h := &AudiobookHandler{}
	items := h.audiobookListItems(context.Background(), []*models.MediaItem{{
		ContentID:  "book-1",
		Title:      "Book",
		PosterPath: "metadata/audiobooks/book-1/poster/original.webp",
	}})

	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].PosterURL != "" {
		t.Fatalf("PosterURL = %q, want empty without presigner", items[0].PosterURL)
	}
}

func TestAudiobookDurationForProgress(t *testing.T) {
	files := []*models.MediaFile{
		{ID: 10, Duration: 120},
		{ID: 11, Duration: 180},
	}

	if got := audiobookDurationForProgress(files, 10); got != 120 {
		t.Fatalf("selected file duration = %v, want 120", got)
	}
	if got := audiobookDurationForProgress(files, 0); got != 300 {
		t.Fatalf("total duration = %v, want 300", got)
	}
	if got := audiobookDurationForProgress([]*models.MediaFile{{ID: 1}}, 1); got != 0 {
		t.Fatalf("unknown duration = %v, want 0", got)
	}
}
