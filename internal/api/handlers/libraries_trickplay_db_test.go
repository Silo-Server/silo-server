package handlers

import (
	"context"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLibraryExplicitTrickplayUpdateAlwaysReconcilesPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var folder int
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders(name,type,trickplay_enabled) VALUES('explicit trickplay update','movies',true) RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM media_folders WHERE id=$1`, folder)
	})
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reconciler := &countingReconciler{}
	h := NewLibraryHandler(catalog.NewFolderRepository(pool), nil, nil, pool, nil)
	h.ArtworkStore = store
	h.Trickplay = reconciler
	for i, enabled := range []bool{true, false, false, true} {
		if _, err := h.UpdateLibrary(t.Context(), folder, 0, LibraryUpdateRequest{TrickplayEnabled: new(enabled)}); err != nil {
			t.Fatal(err)
		}
		if reconciler.calls != i+1 {
			t.Fatalf("explicit %v reconciled %d times, want %d", enabled, reconciler.calls, i+1)
		}
	}
	for i, kind := range []string{"audiobooks", "movies"} {
		if _, err := h.UpdateLibrary(t.Context(), folder, 0, LibraryUpdateRequest{Type: new(kind)}); err != nil {
			t.Fatal(err)
		}
		if reconciler.calls != i+5 {
			t.Errorf("type-only %s reconciled %d times, want %d", kind, reconciler.calls, i+5)
		}
	}
}
