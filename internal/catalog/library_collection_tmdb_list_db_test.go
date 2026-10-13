package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"
)

type fakeTMDBListFetcher struct {
	entries []TMDBCollectionEntry
	gotID   int
}

func (f *fakeTMDBListFetcher) GetList(_ context.Context, id, _ int) ([]TMDBCollectionEntry, error) {
	f.gotID = id
	return f.entries, nil
}

// A TMDB list mixes movies and shows; sync keeps list order and drops
// entries the library does not own.
func TestSyncTMDBListCollectionMatchesMixedEntriesInListOrderDB(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	suffix := time.Now().UnixNano()

	var libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("tmdb-list-%d", suffix)).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, libraryID) })

	movieID := fmt.Sprintf("tmdb-list-movie-%d", suffix)
	showID := fmt.Sprintf("tmdb-list-show-%d", suffix)
	movieTMDB := 900000000 + int(suffix%100000000)
	showTMDB := 800000000 + int(suffix%100000000)
	for _, seed := range []struct {
		id, kind, title string
		tmdb            int
	}{
		{movieID, "movie", "List Movie", movieTMDB},
		{showID, "series", "List Show", showTMDB},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, sort_title, tmdb_id, genres) VALUES ($1,$2,$3,$3,$4,'{}'::text[])`, seed.id, seed.kind, seed.title, strconv.Itoa(seed.tmdb)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1,$2)`, seed.id, libraryID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movieID, showID})
	})

	repo := NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, CreateLibraryCollectionInput{
		LibraryID:      libraryID,
		Title:          "my list",
		Slug:           fmt.Sprintf("my-list-%d", suffix),
		CollectionType: "tmdb",
		SourceURL:      "https://www.themoviedb.org/list/310",
		SourceConfig:   json.RawMessage(`{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310-my-movie-list"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), collection.ID) })

	svc := NewLibraryCollectionService(repo, NewItemRepository(pool), NewLibraryItemRepository(pool), nil)
	fetcher := &fakeTMDBListFetcher{entries: []TMDBCollectionEntry{
		{ID: showTMDB, MediaType: "tv", Title: "List Show"},
		{ID: 1, MediaType: "movie", Title: "Not In Library"},
		{ID: movieTMDB, MediaType: "movie", Title: "List Movie"},
	}}
	svc.TMDBLists = fetcher

	run, err := svc.SyncCollectionWithOptions(ctx, collection.ID, SyncCollectionOptions{SkipCollage: true})
	if err != nil {
		t.Fatalf("SyncCollection: %v", err)
	}
	if fetcher.gotID != 310 {
		t.Fatalf("fetched list id = %d, want 310", fetcher.gotID)
	}
	if run.Status != "success" || run.ItemsMatched != 2 || run.ItemsUnmatched != 1 {
		t.Fatalf("sync run = %+v, want success with 2 matched and 1 unmatched", run)
	}
	items, err := repo.ListItems(ctx, collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].MediaItemID != showID || items[1].MediaItemID != movieID {
		t.Fatalf("collection items = %+v, want [%s %s] in list order", items, showID, movieID)
	}
}

func TestSyncTMDBListCollectionRecordsFailedRunForBadURLDB(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	var libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("tmdb-list-bad-%d", time.Now().UnixNano())).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, libraryID) })

	repo := NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, CreateLibraryCollectionInput{
		LibraryID:      libraryID,
		Title:          "bad list",
		Slug:           fmt.Sprintf("bad-list-%d", libraryID),
		CollectionType: "tmdb",
		SourceConfig:   json.RawMessage(`{"mode":"tmdb_list","url":"https://www.themoviedb.org/collection/10"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), collection.ID) })

	svc := NewLibraryCollectionService(repo, nil, nil, nil)
	fetcher := &fakeTMDBListFetcher{}
	svc.TMDBLists = fetcher

	run, err := svc.SyncCollection(ctx, collection.ID)
	if err != nil {
		t.Fatalf("SyncCollection: %v", err)
	}
	if run.Status != "failed" {
		t.Fatalf("sync run status = %q, want failed", run.Status)
	}
	if fetcher.gotID != 0 {
		t.Fatalf("fetcher was called with list %d for an invalid URL", fetcher.gotID)
	}
}
