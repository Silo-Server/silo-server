package jellycompat

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// resumeFilteringUserData serves a fixed in-progress list and hides a chosen
// set of media item IDs from FilterResumeProgress, recording how often the
// filter ran.
type resumeFilteringUserData struct {
	mockUserDataService
	entries     []upstreamProgress
	hidden      map[string]bool
	filterCalls int
	maxScanned  int // highest row index read across ListProgress pages
}

func (s *resumeFilteringUserData) ListProgress(_ context.Context, _ *Session, _ string, limit, offset int) ([]upstreamProgress, error) {
	if offset >= len(s.entries) {
		return nil, nil
	}
	end := min(offset+limit, len(s.entries))
	if end > s.maxScanned {
		s.maxScanned = end
	}
	return s.entries[offset:end], nil
}

func (s *resumeFilteringUserData) ListProgressFiltered(_ context.Context, _ *Session, _ string, _ []string, _ *int, limit, offset int) ([]upstreamProgress, error) {
	if offset >= len(s.entries) {
		return nil, nil
	}
	end := min(offset+limit, len(s.entries))
	return s.entries[offset:end], nil
}

func (s *resumeFilteringUserData) FilterResumeProgress(_ context.Context, _ *Session, entries []upstreamProgress) ([]upstreamProgress, error) {
	s.filterCalls++
	kept := make([]upstreamProgress, 0, len(entries))
	for _, entry := range entries {
		if s.hidden[entry.MediaItemID] {
			continue
		}
		kept = append(kept, entry)
	}
	return kept, nil
}

func resumeTestHandler(userData UserDataService, itemsByID map[string]*models.MediaItem) *ItemsHandler {
	codec := NewResourceIDCodec()
	return &ItemsHandler{
		content:  &stubContentService{detail: &upstreamItemDetail{}},
		userData: userData,
		itemRepo: &countingItemRepo{itemsByID: itemsByID},
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
	}
}

func dtoNames(dtos []baseItemDTO) []string {
	names := make([]string, 0, len(dtos))
	for _, dto := range dtos {
		names = append(names, dto.Name)
	}
	return names
}

// TestLoadProgressPage_BoundsScanForSparseVisibleSet pins the resumeScanMaxRows
// cap: a heavy watcher whose in-progress rows are almost all hidden by
// FilterResumeProgress must not page through the entire history looking to fill
// the visible page. Before the cap was made unconditional, the scan only stopped
// once len(items) >= limit, so a page that never fills walked every row.
//
// The general loop and the raw-offset fast path (default Continue Watching
// shape: no type/library filter, EnableTotalRecordCount=false, StartIndex=0)
// have separate scan loops, so each needs its own cap. The fast path is the
// branch the sections fallback hits in production.
func TestLoadProgressPage_BoundsScanForSparseVisibleSet(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query itemsQuery
	}{
		// EnableTotalRecordCount=true is the shape that previously forced the
		// full O(history) scan.
		{name: "general loop", query: itemsQuery{limit: 20, enableTotalRecordCount: true}},
		// Default shape → the raw-offset fast path (loop A), not the general loop.
		{name: "raw-offset fast path", query: itemsQuery{limit: 20}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const total = 5000
			entries := make([]upstreamProgress, 0, total)
			items := make(map[string]*models.MediaItem, total)
			hidden := make(map[string]bool, total)
			for i := 0; i < total; i++ {
				id := fmt.Sprintf("movie-%d", i)
				entries = append(entries, upstreamProgress{MediaItemID: id, PositionSeconds: 10, DurationSeconds: 100})
				items[id] = &models.MediaItem{ContentID: id, Type: "movie", Title: id}
				hidden[id] = true // everything dismissed → visible page never fills
			}
			userData := &resumeFilteringUserData{entries: entries, hidden: hidden}
			h := resumeTestHandler(userData, items)

			session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
			_, _, err := h.loadProgressPage(context.Background(), session, "in_progress", tc.query, nil, nil)
			if err != nil {
				t.Fatalf("loadProgressPage: %v", err)
			}

			// The scan must stop near the cap, not walk all `total` rows. Allow one
			// extra batch of overshoot (the break fires after offset crosses the cap).
			const oneBatchOvershoot = 200
			if userData.maxScanned > resumeScanMaxRows+oneBatchOvershoot {
				t.Fatalf("scan not bounded: read %d rows, want <= %d", userData.maxScanned, resumeScanMaxRows+oneBatchOvershoot)
			}
			if userData.maxScanned >= total {
				t.Fatalf("scanned the entire history (%d rows); cap did not engage", userData.maxScanned)
			}
		})
	}
}

// TestLoadProgressPage_HugeLimitDoesNotPreallocate pins that a client-supplied
// Limit far beyond the profile's progress rows sizes nothing by Limit. Both
// scan loops used Limit as the result slice's capacity, so
// /Items?IsPlayed=true&Limit=100000000 asked for about 120 GB and crashed the
// server. Limit=math.MaxInt, which parseItemsQuery accepts, makes the unfixed
// make() panic at once, so the test catches a revert without measuring memory.
// The page must still hold every row.
func TestLoadProgressPage_HugeLimitDoesNotPreallocate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query itemsQuery
	}{
		{name: "raw-offset fast path", query: itemsQuery{limit: math.MaxInt}},
		{name: "general loop", query: itemsQuery{limit: math.MaxInt, enableTotalRecordCount: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := map[string]*models.MediaItem{
				"movie-1": {ContentID: "movie-1", Type: "movie", Title: "Movie One"},
				"movie-2": {ContentID: "movie-2", Type: "movie", Title: "Movie Two"},
			}
			userData := &resumeFilteringUserData{entries: []upstreamProgress{
				{MediaItemID: "movie-1", PositionSeconds: 100, DurationSeconds: 100, Completed: true},
				{MediaItemID: "movie-2", PositionSeconds: 100, DurationSeconds: 100, Completed: true},
			}}
			h := resumeTestHandler(userData, items)
			session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("loadProgressPage sized an allocation by Limit: %v", r)
				}
			}()
			dtos, _, err := h.loadProgressPage(context.Background(), session, "completed", tc.query, nil, nil)
			if err != nil {
				t.Fatalf("loadProgressPage: %v", err)
			}
			if got := dtoNames(dtos); len(got) != 2 || got[0] != "Movie One" || got[1] != "Movie Two" {
				t.Fatalf("names = %v, want [Movie One Movie Two]", got)
			}
		})
	}
}

// fakeDismissalStore embeds userstore.UserStore so only the methods
// FilterResumeProgress touches need real implementations.
type fakeDismissalStore struct {
	userstore.UserStore
	dismissals []userstore.HomeItemDismissal
}

func (s *fakeDismissalStore) ListHomeDismissals(_ context.Context, _ string, _ string) ([]userstore.HomeItemDismissal, error) {
	return s.dismissals, nil
}

func (s *fakeDismissalStore) ListProgress(_ context.Context, _ string, _ string, _ int, _ int) ([]userstore.WatchProgress, error) {
	return nil, nil
}

type fakeDismissalStoreProvider struct {
	store userstore.UserStore
}

func (p *fakeDismissalStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (p *fakeDismissalStoreProvider) Close() error { return nil }

// A ParentId must scope Resume. Moonfin for Roku resumes a series by asking
// for one resumable item under it; ignoring ParentId answered with whatever
// the profile played last, so Resume started the wrong title.
// resumeSeriesEpisodeRepo lists a fixed set of episodes for each series.
type resumeSeriesEpisodeRepo struct {
	countingEpisodeRepo
	bySeries map[string][]*models.Episode
}

func (r *resumeSeriesEpisodeRepo) ListIDsBySeriesIDs(_ context.Context, seriesIDs []string) (map[string][]string, error) {
	ids := map[string][]string{}
	for _, seriesID := range seriesIDs {
		for _, episode := range r.bySeries[seriesID] {
			ids[seriesID] = append(ids[seriesID], episode.ContentID)
		}
	}
	return ids, nil
}

// resumableUserData answers ListResumableProgress from a fixed set of
// in-progress entries and records the ids it was asked about.
type resumableUserData struct {
	mockUserDataService
	inProgress map[string]upstreamProgress
	asked      []string
}

func (u *resumableUserData) ListResumableProgress(_ context.Context, _ *Session, ids []string) ([]upstreamProgress, error) {
	u.asked = append(u.asked, ids...)
	var result []upstreamProgress
	for _, id := range ids {
		if entry, ok := u.inProgress[id]; ok {
			result = append(result, entry)
		}
	}
	return result, nil
}
