package jellycompat

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// panicOnGetItemDetailContent is a ContentService double that panics when
// GetItemDetail is called. Used to assert that hydrateProgressItems no longer
// performs per-item detail fetches.
type panicOnGetItemDetailContent struct {
	stubContentService
}

func (s *panicOnGetItemDetailContent) GetItemDetail(_ context.Context, _ *Session, contentID string, _ *int) (*upstreamItemDetail, error) {
	panic("hydrateProgressItems must not call GetItemDetail; got contentID=" + contentID)
}

// TestHydrateProgressItems_DoesNotCallGetItemDetail pins the perf fix from
// error-report-2026-05-08.md §6: the Resume hydration path used to issue one
// h.content.GetItemDetail call per progress entry whenever the client requested
// Fields=People|Chapters|MediaStreams|MediaSources, producing the 38s timeout
// observed in production for users with large in-progress lists.
//
// hydrateProgressItems is the SCAN phase and must stay detail-free: it runs
// over up to 200 entries per loop iteration purely for type filtering. The
// detail data clients need (real MediaSources — load-bearing for Infuse and
// SenPlayer Continue Watching rows) is served by upgradeProgressPageToDetail,
// which runs only on the returned page, bounded by the request limit.
func TestHydrateProgressItems_DoesNotCallGetItemDetail(t *testing.T) {
	codec := NewResourceIDCodec()

	movieID := "movie-1"
	episodeID := "episode-1"
	seriesID := "series-1"

	itemRepo := &countingItemRepo{
		itemsByID: map[string]*models.MediaItem{
			movieID:  {ContentID: movieID, Type: "movie", Title: "Test Movie"},
			seriesID: {ContentID: seriesID, Type: "series", Title: "Test Series"},
		},
	}
	episodeRepo := &countingEpisodeRepo{
		episodesByID: map[string]*models.Episode{
			episodeID: {
				ContentID:     episodeID,
				SeriesID:      seriesID,
				Title:         "Test Episode",
				SeasonNumber:  1,
				EpisodeNumber: 1,
			},
		},
	}

	h := &ItemsHandler{
		content:     &panicOnGetItemDetailContent{},
		userData:    &mockUserDataService{},
		itemRepo:    itemRepo,
		episodeRepo: episodeRepo,
		codec:       codec,
		mapper:      newMapper(codec, &config.Config{}),
	}

	entries := []upstreamProgress{
		{MediaItemID: movieID, PositionSeconds: 100, DurationSeconds: 6000},
		{MediaItemID: episodeID, PositionSeconds: 200, DurationSeconds: 1800},
	}

	// Every detail-triggering field set — historically this would have caused
	// one GetItemDetail call per entry. With the fix, GetItemDetail must not
	// be called at all (the panicOnGetItemDetailContent fake panics if it is).
	fields := map[string]bool{
		"people":       true,
		"chapters":     true,
		"mediastreams": true,
		"mediasources": true,
	}

	ctx := context.Background()
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	got, err := h.hydrateProgressItems(ctx, session, entries, fields, nil)
	if err != nil {
		t.Fatalf("hydrateProgressItems returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 hydrated items; got %d (%v)", len(got), got)
	}

	// Every result must carry single-element placeholder slices so the JSON
	// keys are present regardless of omitempty. See stubDetailListFields for
	// rationale.
	for i, item := range got {
		if len(item.dto.People) != 1 {
			t.Errorf("item[%d].People = %v; expected single-element stub", i, item.dto.People)
		}
		if len(item.dto.Chapters) != 1 {
			t.Errorf("item[%d].Chapters = %v; expected single-element stub", i, item.dto.Chapters)
		}
		if len(item.dto.MediaStreams) != 1 {
			t.Errorf("item[%d].MediaStreams = %v; expected single-element stub", i, item.dto.MediaStreams)
		}
		if len(item.dto.MediaSources) != 1 {
			t.Errorf("item[%d].MediaSources = %v; expected single-element stub", i, item.dto.MediaSources)
		}
	}
}

// TestHydrateProgressItems_NoFieldsLeavesDetailFieldsNil verifies that the stub
// only fires when the client explicitly requested the detail-only fields.
// Without those fields, the four arrays remain nil — same as the list path
// behavior for any other endpoint.
func TestHydrateProgressItems_NoFieldsLeavesDetailFieldsNil(t *testing.T) {
	codec := NewResourceIDCodec()
	movieID := "movie-1"

	itemRepo := &countingItemRepo{
		itemsByID: map[string]*models.MediaItem{
			movieID: {ContentID: movieID, Type: "movie", Title: "Test Movie"},
		},
	}

	h := &ItemsHandler{
		content:  &panicOnGetItemDetailContent{},
		userData: &mockUserDataService{},
		itemRepo: itemRepo,
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
	}

	entries := []upstreamProgress{{MediaItemID: movieID, PositionSeconds: 100, DurationSeconds: 6000}}

	got, err := h.hydrateProgressItems(context.Background(), &Session{ProfileID: "profile-1"}, entries, nil, nil)
	if err != nil {
		t.Fatalf("hydrateProgressItems returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 hydrated item; got %d", len(got))
	}
	if got[0].dto.People != nil {
		t.Errorf("People should be nil with no Fields; got %v", got[0].dto.People)
	}
	if got[0].dto.Chapters != nil {
		t.Errorf("Chapters should be nil with no Fields; got %v", got[0].dto.Chapters)
	}
	if got[0].dto.MediaStreams != nil {
		t.Errorf("MediaStreams should be nil with no Fields; got %v", got[0].dto.MediaStreams)
	}
	if got[0].dto.MediaSources != nil {
		t.Errorf("MediaSources should be nil with no Fields; got %v", got[0].dto.MediaSources)
	}
}
