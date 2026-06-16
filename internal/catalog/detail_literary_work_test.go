package catalog

import (
	"context"
	"testing"
)

type fakeWorkSummaryProvider struct{}

func (fakeWorkSummaryProvider) GetSummaryForContentID(ctx context.Context, contentID string, filter AccessFilter) (*WorkSummary, error) {
	return &WorkSummary{
		WorkID: "work-1",
		Title:  "Project Hail Mary",
		Formats: []WorkFormatSummary{
			{Type: "ebook", ContentID: contentID, LibraryID: 1},
			{Type: "audiobook", ContentID: "audio-1", LibraryID: 2},
		},
	}, nil
}

func TestItemDetailIncludesWorkSummaryWhenProviderConfigured(t *testing.T) {
	detail := &ItemDetail{ContentID: "ebook-1", Type: "ebook", Title: "Project Hail Mary"}
	applyWorkSummary(context.Background(), detail, fakeWorkSummaryProvider{}, AccessFilter{})
	if detail.WorkID != "work-1" || len(detail.WorkFormats) != 2 {
		t.Fatalf("detail work fields = %#v", detail)
	}
}
