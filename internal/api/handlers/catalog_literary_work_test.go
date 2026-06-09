package handlers

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeCatalogWorkSummaryProvider struct{}

func (fakeCatalogWorkSummaryProvider) GetSummaryForContentID(ctx context.Context, contentID string, filter catalog.AccessFilter) (*catalog.WorkSummary, error) {
	if contentID != "ebook-1" && contentID != "audio-1" {
		return nil, nil
	}
	return &catalog.WorkSummary{
		WorkID: "work-1",
		Title:  "Project Hail Mary",
		Formats: []catalog.WorkFormatSummary{
			{Type: "ebook", ContentID: "ebook-1", LibraryID: 1},
			{Type: "audiobook", ContentID: "audio-1", LibraryID: 2},
		},
	}, nil
}

func TestGroupCatalogItemsByWorkDeduplicatesLinkedFormats(t *testing.T) {
	handler := &CatalogHandler{
		itemsH:      &ItemsHandler{},
		workSummary: fakeCatalogWorkSummaryProvider{},
	}
	req := httptest.NewRequest("GET", "/api/v1/catalog?group=work", nil)
	items := []itemListResponse{
		{ContentID: "ebook-1", Type: "ebook", Title: "Project Hail Mary"},
		{ContentID: "audio-1", Type: "audiobook", Title: "Project Hail Mary"},
		{ContentID: "movie-1", Type: "movie", Title: "Unrelated"},
	}

	grouped := handler.groupCatalogItemsByWork(req, items)

	if len(grouped) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(grouped), grouped)
	}
	if grouped[0].Type != "work" || grouped[0].WorkID != "work-1" || len(grouped[0].WorkFormats) != 2 {
		t.Fatalf("grouped work = %#v", grouped[0])
	}
	if grouped[1].ContentID != "movie-1" {
		t.Fatalf("second item = %#v, want unrelated movie preserved", grouped[1])
	}
}
