package jellycompat

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// resumeFilteringUserData serves a fixed in-progress list and hides a chosen
// set of media item IDs from FilterResumeProgress, recording which statuses
// were listed and how often the filter ran.
type resumeFilteringUserData struct {
	mockUserDataService
	entries           []upstreamProgress
	hidden            map[string]bool
	listedStatuses    []string
	filteredStatuses  []string
	lastFilteredTypes []string
	filterCalls       int
	filteredBatchSize []int
}

func (s *resumeFilteringUserData) ListProgress(_ context.Context, _ *Session, status string, limit, offset int) ([]upstreamProgress, error) {
	s.listedStatuses = append(s.listedStatuses, status)
	if offset >= len(s.entries) {
		return nil, nil
	}
	end := min(offset+limit, len(s.entries))
	return s.entries[offset:end], nil
}

func (s *resumeFilteringUserData) ListProgressFiltered(_ context.Context, _ *Session, status string, types []string, _ *int, limit, offset int) ([]upstreamProgress, error) {
	s.filteredStatuses = append(s.filteredStatuses, status)
	s.lastFilteredTypes = types
	if offset >= len(s.entries) {
		return nil, nil
	}
	end := min(offset+limit, len(s.entries))
	return s.entries[offset:end], nil
}

func (s *resumeFilteringUserData) FilterResumeProgress(_ context.Context, _ *Session, entries []upstreamProgress) ([]upstreamProgress, error) {
	s.filterCalls++
	s.filteredBatchSize = append(s.filteredBatchSize, len(entries))
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

// TestLoadProgressPage_CompletedTypeFilterUsesFilteredFetch pins that the
// watched-items path (completed status with an IncludeItemTypes filter) routes
// through ListProgressFiltered — the SQL pre-filter — instead of the full-set
// ListProgress scan it replaced.
func TestLoadProgressPage_CompletedTypeFilterUsesFilteredFetch(t *testing.T) {
	items := map[string]*models.MediaItem{
		"movie-1": {ContentID: "movie-1", Type: "movie", Title: "Movie One"},
		"movie-2": {ContentID: "movie-2", Type: "movie", Title: "Movie Two"},
	}
	userData := &resumeFilteringUserData{
		entries: []upstreamProgress{
			{MediaItemID: "movie-1", PositionSeconds: 100, DurationSeconds: 100, Completed: true},
			{MediaItemID: "movie-2", PositionSeconds: 100, DurationSeconds: 100, Completed: true},
		},
	}
	h := resumeTestHandler(userData, items)

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	typeSet := map[string]bool{"movie": true}
	dtos, _, err := h.loadProgressPage(context.Background(), session, "completed", itemsQuery{limit: 10}, typeSet, nil)
	if err != nil {
		t.Fatalf("loadProgressPage: %v", err)
	}

	if len(userData.filteredStatuses) == 0 {
		t.Fatal("expected ListProgressFiltered to serve completed + type filter")
	}
	if len(userData.listedStatuses) != 0 {
		t.Fatalf("expected ListProgress not to be called; got %v", userData.listedStatuses)
	}
	if len(userData.lastFilteredTypes) != 1 || userData.lastFilteredTypes[0] != "movie" {
		t.Fatalf("filtered types = %v, want [movie]", userData.lastFilteredTypes)
	}
	if got := dtoNames(dtos); len(got) != 2 {
		t.Fatalf("completed names = %v, want 2 movies", got)
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
