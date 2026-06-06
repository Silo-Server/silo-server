package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPresignAudiobookPosterDoesNotExposeRawKeysWithoutPresigner(t *testing.T) {
	h := &AudiobookHandler{}
	got := h.presignAudiobookPoster(context.Background(), "metadata/audiobooks/book-1/poster/original.webp")
	if got != "" {
		t.Fatalf("PosterURL = %q, want empty without presigner", got)
	}
}

func TestAudiobookListConditionsIncludeAccessPredicates(t *testing.T) {
	conditions, args, _, empty := audiobookListConditions(catalog.AccessFilter{
		AllowedLibraryIDs:  []int{1, 2},
		DisabledLibraryIDs: []int{9},
		MaxContentRating:   "PG-13",
	}, "Fantasy")
	if empty {
		t.Fatal("empty = true, want false")
	}
	sql := strings.Join(conditions, " AND ")
	for _, want := range []string{
		"mi.type = 'audiobook'",
		"ANY(mi.genres)",
		"EXISTS",
		"NOT EXISTS",
		"mi.content_rating = ANY",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("conditions missing %q in:\n%s", want, sql)
		}
	}
	if len(args) != 4 {
		t.Fatalf("len(args) = %d, want 4", len(args))
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
