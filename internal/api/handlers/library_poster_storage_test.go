package handlers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkstore/artworkstoretest"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

type failedPosterStore struct{ artworkstore.Store }

func (failedPosterStore) Put(context.Context, string, []byte) error {
	return errors.New("no space left on device")
}

func TestLibraryPosterReplacementAndFailureLog(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(t.Context(), `CREATE TEMP TABLE media_folders (LIKE public.media_folders INCLUDING DEFAULTS);
CREATE TEMP TABLE media_folder_paths (LIKE public.media_folder_paths INCLUDING DEFAULTS);
INSERT INTO media_folders (id,type,name,enabled,poster_path) VALUES (7,'movies','Poster test',true,'library-posters/7.jpg');`)
	if err != nil {
		t.Fatal(err)
	}
	h := NewLibraryHandler(catalog.NewFolderRepository(pool), nil, nil, nil, nil)
	store := artworkstoretest.New()
	if err := store.Put(t.Context(), "library-posters/7.jpg", []byte("old")); err != nil {
		t.Fatal(err)
	}
	h.ArtworkStore = store
	if _, err := h.UploadLibraryPoster(t.Context(), 7, "image/png", []byte("poster")); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Objects["library-posters/7.jpg"]; ok {
		t.Fatal("replaced poster was not deleted")
	}
	if string(store.Objects["library-posters/7.png"]) != "poster" {
		t.Fatalf("replacement poster missing: %v", store.Calls)
	}
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	h.ArtworkStore = failedPosterStore{}
	if _, err := h.UploadLibraryPoster(t.Context(), 7, "image/png", []byte("replacement")); err == nil {
		t.Fatal("storage failure was ignored")
	}
	if !strings.Contains(logs.String(), "no space left on device") {
		t.Fatalf("storage failure missing from log: %s", logs.String())
	}
}
