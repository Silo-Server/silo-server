package usercollections

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type recordingTMDBListFetcher struct {
	gotID int
	err   error
}

func (f *recordingTMDBListFetcher) GetList(_ context.Context, id, _ int) ([]catalog.TMDBCollectionEntry, error) {
	f.gotID = id
	return nil, f.err
}

func TestSyncTMDBListFallsBackToSourceURLAndRedactsFetchErrors(t *testing.T) {
	fetcher := &recordingTMDBListFetcher{err: &url.Error{
		Op:  "Get",
		URL: "https://api.themoviedb.org/3/list/310?page=1&api_key=secret-tmdb-key",
		Err: context.DeadlineExceeded,
	}}
	svc := NewService(nil, nil, nil, &staticOwners{}, nil, slog.New(slog.DiscardHandler))
	svc.TMDBLists = fetcher
	collection := &userstore.Collection{
		ID:               "c",
		CreatorProfileID: "owner",
		SourceURL:        "https://www.themoviedb.org/list/310",
		SourceConfig:     `{"mode":"tmdb_list"}`,
	}

	_, _, err := svc.RunSync(t.Context(), 7, nil, collection)
	if fetcher.gotID != 310 {
		t.Fatalf("fetched list = %d, want 310 from source_url", fetcher.gotID)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the fetch cause preserved", err)
	}
	if strings.Contains(err.Error(), "secret-tmdb-key") {
		t.Fatalf("error leaks the API key: %q", err)
	}
}

func TestSyncTMDBListRejectsNonListURL(t *testing.T) {
	fetcher := &recordingTMDBListFetcher{}
	svc := NewService(nil, nil, nil, &staticOwners{}, nil, slog.New(slog.DiscardHandler))
	svc.TMDBLists = fetcher
	collection := &userstore.Collection{ID: "c", CreatorProfileID: "owner", SourceConfig: `{"mode":"tmdb_list","url":"https://www.themoviedb.org/movie/550"}`}

	if _, _, err := svc.RunSync(t.Context(), 7, nil, collection); err == nil {
		t.Fatal("RunSync succeeded for a non-list URL")
	}
	if fetcher.gotID != 0 {
		t.Fatalf("fetcher was called with list %d", fetcher.gotID)
	}
}
