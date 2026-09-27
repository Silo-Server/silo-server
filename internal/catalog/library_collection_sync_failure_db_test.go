package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

type failingTMDBPresetFetcher struct{ err error }

func (f failingTMDBPresetFetcher) GetCollectionPreset(context.Context, string, string, string, int) ([]TMDBCollectionEntry, error) {
	return nil, f.err
}

// A source error that returns before RecordSyncRun must still leave a failed
// run, or last_sync_status keeps reporting the previous success.
func TestSyncCollectionRecordsFailedRunOnSourceErrorDB(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	var libraryID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, fmt.Sprintf("sync-failure-%d", time.Now().UnixNano())).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, libraryID) })

	repo := NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, CreateLibraryCollectionInput{
		LibraryID:      libraryID,
		Title:          "trending",
		Slug:           fmt.Sprintf("trending-%d", libraryID),
		CollectionType: "tmdb",
		SourceConfig:   json.RawMessage(`{"mode":"tmdb_preset","preset":"trending"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), collection.ID) })

	svc := NewLibraryCollectionService(repo, nil, nil, nil)
	// http.Client reports transport failures as *url.Error carrying the full
	// request URL; TMDB authenticates with an api_key query parameter.
	svc.TMDBCollections = failingTMDBPresetFetcher{err: &url.Error{
		Op:  "Get",
		URL: "https://api.themoviedb.org/3/trending/all/day?api_key=secret-tmdb-key",
		Err: context.DeadlineExceeded,
	}}

	run, syncErr := svc.SyncCollection(ctx, collection.ID)
	if syncErr == nil || !errors.Is(syncErr, context.DeadlineExceeded) {
		t.Fatalf("SyncCollection error = %v, want the source error", syncErr)
	}
	if run != nil {
		t.Fatalf("SyncCollection run = %+v, want nil alongside the error", run)
	}

	runs, err := repo.ListSyncRuns(ctx, collection.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("sync runs = %+v, want one failed run", runs)
	}
	if strings.Contains(runs[0].Message, "secret-tmdb-key") {
		t.Fatalf("failed run message leaks the API key: %q", runs[0].Message)
	}
	if !strings.Contains(runs[0].Message, "api.themoviedb.org") {
		t.Fatalf("failed run message = %q, want the sanitized request host", runs[0].Message)
	}
	got, err := repo.GetByID(ctx, collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSyncStatus != "failed" {
		t.Fatalf("last_sync_status = %v, want failed", got.LastSyncStatus)
	}
}
