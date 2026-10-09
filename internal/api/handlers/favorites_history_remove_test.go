package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeHistoryItemRepo struct {
	items        map[string]*models.MediaItem
	inaccessible map[string]bool
}

func (r *fakeHistoryItemRepo) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	item, ok := r.items[contentID]
	if !ok {
		return nil, catalog.ErrItemNotFound
	}
	return item, nil
}

func (r *fakeHistoryItemRepo) GetByIDs(_ context.Context, contentIDs []string) ([]*models.MediaItem, error) {
	result := make([]*models.MediaItem, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		if item, ok := r.items[contentID]; ok {
			result = append(result, item)
		}
	}
	return result, nil
}

func (r *fakeHistoryItemRepo) EnsureAccessible(_ context.Context, contentID string, _ catalog.AccessFilter) error {
	if r.inaccessible[contentID] {
		return catalog.ErrItemNotFound
	}
	return nil
}

func TestHandleRemoveHistoryAcceptsEbookTargets(t *testing.T) {
	ctx := context.Background()
	store := newProfileTestStore(t)
	handler := NewPersonalDataHandler(testUserStoreProvider{store: store}, &fakeHistoryItemRepo{
		items: map[string]*models.MediaItem{
			"ebook-1": {ContentID: "ebook-1", Type: "ebook", Title: "Book"},
		},
	})

	// Seed a history entry keyed by the ebook content ID so the removal has
	// something to hide. RemoveHistoryItems only touches watch history,
	// watch progress, and the hidden-items gate — PersonalDataHandler has no
	// write access to ebook_reader_progress (its store interface is read-only),
	// so the reader position survives: hidden is not the same as unread.
	if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{
		ProfileID:   "profile-1",
		MediaItemID: "ebook-1",
		WatchedAt:   "2026-06-01T10:00:00Z",
		Completed:   true,
		Source:      userstore.WatchHistorySourceManual,
	}); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPost,
		"/history/remove",
		`{"targets":[{"content_id":"ebook-1"}]}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()
	handler.HandleRemoveHistory(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	entries, err := store.ListHistory(ctx, "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("history entries = %+v, want hidden after removal", entries)
	}
}

func TestHandleRemoveHistoryRejectsInaccessibleEbook(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewPersonalDataHandler(testUserStoreProvider{store: store}, &fakeHistoryItemRepo{
		items: map[string]*models.MediaItem{
			"ebook-1": {ContentID: "ebook-1", Type: "ebook", Title: "Book"},
		},
		inaccessible: map[string]bool{"ebook-1": true},
	})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPost,
		"/history/remove",
		`{"targets":[{"content_id":"ebook-1"}]}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()
	handler.HandleRemoveHistory(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

// An imported watch can match an episode the catalog knows from metadata but
// that has no file in any library. The history page still shows it on its
// series card, so removing the show, a season, or the episode by show scope
// must hide it too, or the series card can never be cleared. File-less
// episodes nobody watched stay out of the removal.
func TestRemoveHistoryHidesEpisodesWithoutLibraryFilePostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}

	prefix := fmt.Sprintf("history-remove-%d", time.Now().UnixNano())
	series := prefix + "-series"
	season := prefix + "-season-1"
	onDisk, providerOnly, neverWatched := prefix+"-s01e01", prefix+"-s01e02", prefix+"-s01e03"
	var folder, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('series',$1,true) RETURNING id`, prefix).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, series)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder)
	})
	exec(`INSERT INTO media_items (content_id,type,title) VALUES ($1,'series','History Series')`, series)
	exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, series, folder)
	if err := catalog.NewSeasonRepository(pool).Upsert(ctx, &models.Season{ContentID: season, SeriesID: series, SeasonNumber: 1, Title: "Season 1", DefaultMetadataLanguage: "en"}); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO episodes (content_id,series_id,season_id,season_number,episode_number,title) VALUES ($1,$3,$4,1,1,'On disk'), ($2,$3,$4,1,2,'Provider only'), ($5,$3,$4,1,3,'Never watched')`,
		onDisk, providerOnly, series, season, neverWatched)
	exec(`INSERT INTO episode_libraries (episode_id,media_folder_id) VALUES ($1,$2)`, onDisk, folder)
	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
	exec(`INSERT INTO user_profiles (id,user_id,name) VALUES ($1,$2,'History fixture')`, profileID, userID)

	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	episodes := catalog.NewEpisodeRepository(pool)
	handler := NewPersonalDataHandler(testUserStoreProvider{store: store}, &fakeHistoryItemRepo{
		items: map[string]*models.MediaItem{series: {ContentID: series, Type: "series", Title: "History Series"}},
	})
	handler.SetEpisodeRepo(episodes)
	handler.SetSeasonRepo(catalog.NewSeasonRepository(pool))

	// Removal hides every watch up to the removal time, so each round records
	// its watches after it.
	watchedAt := time.Now().UTC().Add(time.Hour)
	for _, target := range []HistoryRemovalTarget{
		{ContentID: series, Scope: historyRemovalScopeShow},
		{ContentID: season, Scope: historyRemovalScopeShow},
		{ContentID: season, Scope: historyRemovalScopeItem},
		{ContentID: onDisk, Scope: historyRemovalScopeShow},
	} {
		watchedAt = watchedAt.Add(24 * time.Hour)
		for _, id := range []string{onDisk, providerOnly} {
			if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{ProfileID: profileID, MediaItemID: id, WatchedAt: watchedAt.Format(time.RFC3339), Completed: true, Source: userstore.WatchHistorySourceImport}); err != nil {
				t.Fatal(err)
			}
		}
		entries, err := store.ListHistory(ctx, profileID, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		display, err := catalog.ResolveHistoryDisplayEntries(ctx, entries, episodes)
		if err != nil {
			t.Fatal(err)
		}
		if len(display) != 1 || display[0].DisplayID != series {
			t.Fatalf("%+v: history display = %+v, want one card for the series", target, display)
		}

		if err := handler.RemoveHistory(ctx, userID, profileID, catalog.AccessFilter{}, []HistoryRemovalTarget{target}); err != nil {
			t.Fatalf("%+v: RemoveHistory: %v", target, err)
		}
		entries, err = store.ListHistory(ctx, profileID, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("%+v: history after removal = %+v, want every episode of the series card hidden", target, entries)
		}
	}
	var hiddenUnwatched int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_history_hidden_items WHERE user_id=$1 AND media_item_id=$2`, userID, neverWatched).Scan(&hiddenUnwatched); err != nil {
		t.Fatal(err)
	}
	if hiddenUnwatched != 0 {
		t.Fatalf("removal hid the unwatched file-less episode; want it left out")
	}
}

// batchRecordingStore answers the two reads watchedHistoryItemIDs makes and
// records each batch size.
type batchRecordingStore struct {
	userstore.UserStore
	history, progress map[string]bool
	batches           []int
}

func (s *batchRecordingStore) LatestHistoryIDs(_ context.Context, _ string, groups map[string][]string) (map[string]string, error) {
	s.batches = append(s.batches, len(groups))
	result := map[string]string{}
	for id := range groups {
		if s.history[id] {
			result[id] = "history-" + id
		}
	}
	return result, nil
}

func (s *batchRecordingStore) ListProgressByMediaItems(_ context.Context, _ string, ids []string) (map[string]userstore.WatchProgress, error) {
	s.batches = append(s.batches, len(ids))
	result := map[string]userstore.WatchProgress{}
	for _, id := range ids {
		if s.progress[id] {
			result[id] = userstore.WatchProgress{MediaItemID: id}
		}
	}
	return result, nil
}

func TestWatchedHistoryItemIDsBatchesAndDeduplicates(t *testing.T) {
	ids := make([]string, 0, 2600)
	for i := range 2500 {
		ids = append(ids, fmt.Sprintf("ep-%d", i))
	}
	ids = append(ids, ids[:100]...) // overlapping targets repeat candidates
	store := &batchRecordingStore{
		history:  map[string]bool{"ep-5": true},
		progress: map[string]bool{"ep-2400": true},
	}

	watched, err := watchedHistoryItemIDs(context.Background(), store, "profile-1", ids)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ep-5", "ep-2400"}; !slices.Equal(watched, want) {
		t.Fatalf("watched = %v, want %v", watched, want)
	}
	total := 0
	for _, n := range store.batches {
		if n > watchedHistoryItemIDBatch {
			t.Fatalf("batch of %d IDs, want at most %d", n, watchedHistoryItemIDBatch)
		}
		total += n
	}
	if total != 2*2500 {
		t.Fatalf("read %d IDs across both reads, want each of the 2500 unique IDs once per read", total)
	}
}
