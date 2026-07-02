package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata/translation"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeMetadataAIItemAccess struct {
	items     map[string]*models.MediaItem
	ensureErr map[string]error
	checked   []string
}

func (f *fakeMetadataAIItemAccess) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	if item := f.items[contentID]; item != nil {
		return item, nil
	}
	return nil, catalog.ErrItemNotFound
}

func (f *fakeMetadataAIItemAccess) EnsureAccessible(_ context.Context, contentID string, _ catalog.AccessFilter) error {
	f.checked = append(f.checked, contentID)
	return f.ensureErr[contentID]
}

type fakeMetadataAISeasonLookup map[string]*models.Season

func (f fakeMetadataAISeasonLookup) GetByID(_ context.Context, contentID string) (*models.Season, error) {
	if season := f[contentID]; season != nil {
		return season, nil
	}
	return nil, catalog.ErrSeasonNotFound
}

type fakeMetadataAIEpisodeLookup map[string]*models.Episode

func (f fakeMetadataAIEpisodeLookup) GetByID(_ context.Context, contentID string) (*models.Episode, error) {
	if episode := f[contentID]; episode != nil {
		return episode, nil
	}
	return nil, catalog.ErrEpisodeNotFound
}

func TestMetadataAIResolveOnViewTarget(t *testing.T) {
	itemAccess := &fakeMetadataAIItemAccess{
		items: map[string]*models.MediaItem{
			"movie-1": {ContentID: "movie-1", Type: "movie"},
		},
		ensureErr: map[string]error{},
	}
	handler := &MetadataAIHandler{
		ItemAccess: itemAccess,
		SeasonLookup: fakeMetadataAISeasonLookup{
			"season-1": {ContentID: "season-1", SeriesID: "series-1"},
		},
		EpisodeLookup: fakeMetadataAIEpisodeLookup{
			"episode-1": {ContentID: "episode-1", SeriesID: "series-1"},
		},
	}

	tests := []struct {
		name       string
		contentID  string
		wantKind   translation.TargetKind
		wantAccess string
	}{
		{
			name:       "item",
			contentID:  "movie-1",
			wantKind:   translation.TargetItem,
			wantAccess: "movie-1",
		},
		{
			name:       "season",
			contentID:  "season-1",
			wantKind:   translation.TargetSeason,
			wantAccess: "series-1",
		},
		{
			name:       "episode",
			contentID:  "episode-1",
			wantKind:   translation.TargetEpisode,
			wantAccess: "series-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			itemAccess.checked = nil
			got, err := handler.resolveTranslationTarget(context.Background(), tc.contentID)
			if err != nil {
				t.Fatalf("resolveTranslationTarget: %v", err)
			}
			if got.kind != tc.wantKind {
				t.Fatalf("kind = %s, want %s", got.kind, tc.wantKind)
			}
			if got.accessContentID != tc.wantAccess {
				t.Fatalf("accessContentID = %q, want %q", got.accessContentID, tc.wantAccess)
			}
		})
	}
}

func TestMetadataAIResolveTranslationTargetReportsMissingContent(t *testing.T) {
	itemAccess := &fakeMetadataAIItemAccess{
		items:     map[string]*models.MediaItem{},
		ensureErr: map[string]error{},
	}
	handler := &MetadataAIHandler{
		ItemAccess:    itemAccess,
		SeasonLookup:  fakeMetadataAISeasonLookup{},
		EpisodeLookup: fakeMetadataAIEpisodeLookup{},
	}

	_, err := handler.resolveTranslationTarget(context.Background(), "missing")
	if !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatalf("err = %v, want %v", err, catalog.ErrItemNotFound)
	}
}
