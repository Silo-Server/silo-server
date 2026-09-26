package handlers

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

type concurrentSectionLookups struct {
	playableStarted chan struct{}
	episodeStarted  chan struct{}
	query           catalog.PlayableTargetQuery
	episodeAccess   catalog.AccessFilter
}

func (s *concurrentSectionLookups) ResolvePlayableTargets(ctx context.Context, query catalog.PlayableTargetQuery) (map[string]string, error) {
	s.query = query
	close(s.playableStarted)
	select {
	case <-s.episodeStarted:
		return map[string]string{query.Items[0].Key(): "playable-episode"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *concurrentSectionLookups) FetchEpisodesByContentIDs(ctx context.Context, _ []string, filter catalog.AccessFilter) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error) {
	s.episodeAccess = filter
	close(s.episodeStarted)
	select {
	case <-s.playableStarted:
		return nil, map[string]sections.SectionItemMeta{"episode": {SeriesTitle: "Series"}}, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func TestBuildSectionsEnrichesConcurrentlyForViewer(t *testing.T) {
	// Both lookups must enter before either returns. The deadline only bounds a
	// broken serial implementation; no elapsed-time assertion determines success.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	lookups := &concurrentSectionLookups{playableStarted: make(chan struct{}), episodeStarted: make(chan struct{})}
	handler := &SectionHandler{playableTargets: lookups, episodeFetcher: lookups}
	access := catalog.AccessFilter{AllowedLibraryIDs: []int{3}, MaxPlaybackQuality: "1080p"}
	response := handler.buildSections(ctx, []sections.SectionWithItems{{
		ResolvedSection: sections.ResolvedSection{ID: "row", SectionType: sections.SectionRecentlyAdded},
		Items:           []*models.MediaItem{{ContentID: "episode", Type: "episode", Title: "Episode"}},
	}}, new(3), access, imagesize.Unset)
	if err := ctx.Err(); err != nil {
		t.Fatalf("enrichment did not finish concurrently: %v", err)
	}
	if !reflect.DeepEqual(lookups.query.Access, access) || !reflect.DeepEqual(lookups.episodeAccess, access) {
		t.Fatalf("viewer access lost: playable %+v, episode %+v", lookups.query.Access, lookups.episodeAccess)
	}
	if !reflect.DeepEqual(lookups.query.LibraryIDs, []int{3}) {
		t.Fatalf("library scope lost: %+v", lookups.query.LibraryIDs)
	}
	if len(response.Sections) != 1 || len(response.Sections[0].Items) != 1 {
		t.Fatalf("unexpected response: %+v", response)
	}
	item := response.Sections[0].Items[0]
	if item.PlayContentID != "playable-episode" || item.SeriesTitle != "Series" {
		t.Fatalf("missing enrichment: %+v", item)
	}
}
