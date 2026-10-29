package jellycompat

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
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

// TestLoadProgressPage_ResumeHidesFilteredEntries pins that the Resume path
// runs every raw batch through FilterResumeProgress so dismissed and
// superseded entries never reach Jellyfin clients, mirroring the first-party
// Continue Watching row.
func TestLoadProgressPage_ResumeHidesFilteredEntries(t *testing.T) {
	items := map[string]*models.MediaItem{
		"movie-1": {ContentID: "movie-1", Type: "movie", Title: "Movie One"},
		"movie-2": {ContentID: "movie-2", Type: "movie", Title: "Movie Two"},
		"movie-3": {ContentID: "movie-3", Type: "movie", Title: "Movie Three"},
	}
	userData := &resumeFilteringUserData{
		entries: []upstreamProgress{
			{MediaItemID: "movie-1", PositionSeconds: 10, DurationSeconds: 100},
			{MediaItemID: "movie-2", PositionSeconds: 20, DurationSeconds: 100},
			{MediaItemID: "movie-3", PositionSeconds: 30, DurationSeconds: 100},
		},
		hidden: map[string]bool{"movie-2": true},
	}
	h := resumeTestHandler(userData, items)

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	dtos, _, err := h.loadProgressPage(context.Background(), session, "in_progress", itemsQuery{limit: 10}, nil, nil)
	if err != nil {
		t.Fatalf("loadProgressPage: %v", err)
	}

	got := dtoNames(dtos)
	want := []string{"Movie One", "Movie Three"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("resume names = %v, want %v", got, want)
	}
	if userData.filterCalls == 0 {
		t.Fatal("expected FilterResumeProgress to be called for in_progress status")
	}
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

// TestLoadProgressPage_CompletedScanNotCapped pins that the completed
// (watched-items) path is exempt from resumeScanMaxRows: it paginates on an exact
// TotalRecordCount, so it must keep scanning past the cap. Mirrors the Codex
// review concern that the cap not leak into watched-items pagination.
func TestLoadProgressPage_CompletedScanNotCapped(t *testing.T) {
	const total = resumeScanMaxRows * 3
	entries := make([]upstreamProgress, 0, total)
	items := make(map[string]*models.MediaItem, total)
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("movie-%d", i)
		entries = append(entries, upstreamProgress{MediaItemID: id, PositionSeconds: 100, DurationSeconds: 100, Completed: true})
		items[id] = &models.MediaItem{ContentID: id, Type: "movie", Title: id}
	}
	// No hidden entries: completed does not run FilterResumeProgress.
	userData := &resumeFilteringUserData{entries: entries}
	h := resumeTestHandler(userData, items)

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	query := itemsQuery{limit: 20, enableTotalRecordCount: true}
	_, gotTotal, err := h.loadProgressPage(context.Background(), session, "completed", query, nil, nil)
	if err != nil {
		t.Fatalf("loadProgressPage: %v", err)
	}
	// The exact watched-items total must be reported, i.e. the scan must not stop
	// at the resume cap.
	if gotTotal != total {
		t.Fatalf("completed total = %d, want exact %d (cap must not apply to watched items)", gotTotal, total)
	}
}

// TestLoadProgressPage_ResumeFilterAppliesWithTypeFilter covers the
// scan-from-zero branch (IncludeItemTypes present): filtering must apply
// there too, and visible pagination must skip filtered entries.
func TestLoadProgressPage_ResumeFilterAppliesWithTypeFilter(t *testing.T) {
	items := map[string]*models.MediaItem{
		"movie-1": {ContentID: "movie-1", Type: "movie", Title: "Movie One"},
		"movie-2": {ContentID: "movie-2", Type: "movie", Title: "Movie Two"},
		"movie-3": {ContentID: "movie-3", Type: "movie", Title: "Movie Three"},
	}
	userData := &resumeFilteringUserData{
		entries: []upstreamProgress{
			{MediaItemID: "movie-1", PositionSeconds: 10, DurationSeconds: 100},
			{MediaItemID: "movie-2", PositionSeconds: 20, DurationSeconds: 100},
			{MediaItemID: "movie-3", PositionSeconds: 30, DurationSeconds: 100},
		},
		hidden: map[string]bool{"movie-1": true},
	}
	h := resumeTestHandler(userData, items)

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	query := itemsQuery{limit: 10, enableTotalRecordCount: true}
	typeSet := map[string]bool{"movie": true}
	dtos, total, err := h.loadProgressPage(context.Background(), session, "in_progress", query, typeSet, nil)
	if err != nil {
		t.Fatalf("loadProgressPage: %v", err)
	}

	got := dtoNames(dtos)
	want := []string{"Movie Two", "Movie Three"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("resume names = %v, want %v", got, want)
	}
	if total != 2 {
		t.Fatalf("TotalRecordCount = %d, want 2 (hidden entries excluded)", total)
	}
}

// TestLoadProgressPage_CompletedSkipsResumeFilter pins that the watched-items
// view ("completed" status) is served unfiltered: dismissals and superseded
// hiding are Continue Watching semantics only.
func TestLoadProgressPage_CompletedSkipsResumeFilter(t *testing.T) {
	items := map[string]*models.MediaItem{
		"movie-1": {ContentID: "movie-1", Type: "movie", Title: "Movie One"},
	}
	userData := &resumeFilteringUserData{
		entries: []upstreamProgress{
			{MediaItemID: "movie-1", PositionSeconds: 100, DurationSeconds: 100, Completed: true},
		},
		hidden: map[string]bool{"movie-1": true},
	}
	h := resumeTestHandler(userData, items)

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	dtos, _, err := h.loadProgressPage(context.Background(), session, "completed", itemsQuery{limit: 10}, nil, nil)
	if err != nil {
		t.Fatalf("loadProgressPage: %v", err)
	}

	if userData.filterCalls != 0 {
		t.Fatalf("FilterResumeProgress calls = %d, want 0 for completed status", userData.filterCalls)
	}
	if len(dtos) != 1 || dtos[0].Name != "Movie One" {
		t.Fatalf("completed names = %v, want [Movie One]", dtoNames(dtos))
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

// TestDirectUserDataServiceFilterResumeProgressDropsDismissedEntries covers
// the glue in directUserDataService: dismissals from the store hide matching
// entries (same timestamp rule as the first-party row), everything else
// passes through. The superseded check is disabled via a nil pool — its
// behavior is pinned by the catalog package tests.
func TestDirectUserDataServiceFilterResumeProgressDropsDismissedEntries(t *testing.T) {
	dismissedAt := "2025-01-01T00:00:00Z"
	svc := &directUserDataService{
		storeProvider: &fakeDismissalStoreProvider{store: &fakeDismissalStore{
			dismissals: []userstore.HomeItemDismissal{
				{MediaItemID: "movie-1", ProgressUpdatedAt: &dismissedAt},
			},
		}},
		resumeFilter: catalog.NewContinueWatchingProgressFilter(nil),
	}

	entries := []upstreamProgress{
		{MediaItemID: "movie-1", PositionSeconds: 10, DurationSeconds: 100, UpdatedAt: dismissedAt},
		{MediaItemID: "movie-2", PositionSeconds: 20, DurationSeconds: 100, UpdatedAt: dismissedAt},
	}

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	got, err := svc.FilterResumeProgress(context.Background(), session, entries)
	if err != nil {
		t.Fatalf("FilterResumeProgress: %v", err)
	}
	if len(got) != 1 || got[0].MediaItemID != "movie-2" {
		t.Fatalf("filtered = %+v, want only movie-2", got)
	}
}

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

// Resume under a series reads that series' episodes the viewer can see,
// however much else the profile has watched since, and a movie ParentId lists
// nothing.
func TestParentResumeReadsTheSeriesEpisodes(t *testing.T) {
	episodes := []*models.Episode{
		{ContentID: "ep-1", SeriesID: "series-1", Title: "Episode One", SeasonNumber: 1, EpisodeNumber: 1},
		{ContentID: "ep-2", SeriesID: "series-1", Title: "Episode Two", SeasonNumber: 1, EpisodeNumber: 2},
		// Without a pool, visibleEpisodeIDs falls back to live files, so an
		// episode with none stands in for one the viewer may not see.
		{ContentID: "ep-hidden", SeriesID: "series-1", Title: "Hidden Episode", SeasonNumber: 1, EpisodeNumber: 3},
	}
	episodeRepo := &resumeSeriesEpisodeRepo{
		countingEpisodeRepo: countingEpisodeRepo{
			episodesByID: map[string]*models.Episode{"ep-1": episodes[0], "ep-2": episodes[1], "ep-hidden": episodes[2]},
			hasFilesByID: map[string]bool{"ep-1": true, "ep-2": true},
		},
		bySeries: map[string][]*models.Episode{"series-1": episodes},
	}
	userData := &resumableUserData{inProgress: map[string]upstreamProgress{
		"ep-hidden": {MediaItemID: "ep-hidden", PositionSeconds: 30, DurationSeconds: 100},
		"ep-2":      {MediaItemID: "ep-2", PositionSeconds: 20, DurationSeconds: 100},
		"movie-1":   {MediaItemID: "movie-1", PositionSeconds: 10, DurationSeconds: 100},
	}}
	h := resumeTestHandler(userData, map[string]*models.MediaItem{
		"series-1": {ContentID: "series-1", Type: "series", Title: "Series One"},
		"movie-1":  {ContentID: "movie-1", Type: "movie", Title: "Movie One"},
	})
	h.episodeRepo = episodeRepo
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}

	dtos, total, err := h.loadParentResume(context.Background(), session, itemsQuery{limit: 1, parentItemID: "series-1"}, nil)
	if err != nil {
		t.Fatalf("loadParentResume: %v", err)
	}
	if got := dtoNames(dtos); len(got) != 1 || got[0] != "Episode Two" || total != 1 {
		t.Fatalf("resume under series-1 = %v (total %d), want [Episode Two]", got, total)
	}
	if fmt.Sprint(userData.asked) != "[ep-1 ep-2]" {
		t.Fatalf("progress read for %v, want only the series' visible episodes", userData.asked)
	}

	dtos, _, err = h.loadParentResume(context.Background(), session, itemsQuery{limit: 1, parentItemID: "movie-1"}, nil)
	if err != nil {
		t.Fatalf("loadParentResume: %v", err)
	}
	if got := dtoNames(dtos); len(got) != 0 {
		t.Fatalf("resume under movie-1 = %v, want none", got)
	}
}
