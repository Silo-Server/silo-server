package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestGroupedCatalogEntryKeyDeduplicatesLinkedFormats(t *testing.T) {
	summary := &catalog.WorkSummary{
		WorkID: "work-1",
		Title:  "Project Hail Mary",
		Formats: []catalog.WorkFormatSummary{
			{Type: "ebook", ContentID: "ebook-1", LibraryID: 1},
			{Type: "audiobook", ContentID: "audio-1", LibraryID: 2},
		},
	}

	ebookKey := groupedCatalogEntryKey(&models.MediaItem{ContentID: "ebook-1", Type: "ebook"}, summary)
	audioKey := groupedCatalogEntryKey(&models.MediaItem{ContentID: "audio-1", Type: "audiobook"}, summary)

	if ebookKey != audioKey {
		t.Fatalf("linked format keys differ: %q vs %q", ebookKey, audioKey)
	}
	movieKey := groupedCatalogEntryKey(&models.MediaItem{ContentID: "movie-1", Type: "movie"}, nil)
	if movieKey != "item:movie-1" {
		t.Fatalf("movie key = %q, want item key", movieKey)
	}

	resp := itemListResponse{ContentID: "ebook-1", Type: "ebook", Title: "Project Hail Mary"}
	applyWorkSummaryToCatalogItem(&resp, summary)
	if resp.Type != "work" || resp.WorkID != "work-1" || len(resp.WorkFormats) != 2 {
		t.Fatalf("work response = %#v", resp)
	}
}
