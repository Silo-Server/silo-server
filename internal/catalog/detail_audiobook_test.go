package catalog

import (
	"context"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestSortAudiobookMediaFilesPreservesPresentationPartOrder(t *testing.T) {
	files := []*models.MediaFile{
		{ID: 30, FilePath: "/books/book/03.mp3", PresentationPartIndex: 3},
		{ID: 10, FilePath: "/books/book/01.mp3", PresentationPartIndex: 1},
		{ID: 20, FilePath: "/books/book/02.mp3", PresentationPartIndex: 2},
	}

	sortAudiobookMediaFiles(files)

	got := []int{files[0].ID, files[1].ID, files[2].ID}
	want := []int{10, 20, 30}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted IDs = %v, want %v", got, want)
		}
	}
}

func TestSortAudiobookMediaFilesFallsBackToPath(t *testing.T) {
	files := []*models.MediaFile{
		{ID: 20, FilePath: "/books/book/02.mp3"},
		{ID: 10, FilePath: "/books/book/01.mp3"},
	}

	sortAudiobookMediaFiles(files)

	got := []int{files[0].ID, files[1].ID}
	want := []int{10, 20}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted IDs = %v, want %v", got, want)
		}
	}
}

func TestPresignAudiobookPosterURLUsesPosterVariant(t *testing.T) {
	resolver := &recordingCatalogImageResolver{}
	detail := &DetailService{}
	detail.SetImageResolver(resolver)

	got := detail.presignAudiobookPosterURL(context.Background(), "local/audiobooks/book/poster/original.webp")

	if !strings.Contains(got, "/w500.webp") {
		t.Fatalf("resolved URL = %q, want w500 poster variant", got)
	}
	if resolver.variant != "featured" {
		t.Fatalf("resolver variant = %q, want featured", resolver.variant)
	}
}

type recordingCatalogImageResolver struct {
	path    string
	variant string
}

func (r *recordingCatalogImageResolver) ResolveImageURL(_ context.Context, path string, variant string) string {
	r.path = path
	r.variant = variant
	return "resolved://" + path
}

func (r *recordingCatalogImageResolver) ResolveImageURLs(_ context.Context, paths []string, variant string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		out[path] = r.ResolveImageURL(context.Background(), path, variant)
	}
	return out
}
