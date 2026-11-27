package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// A library's episode order cannot change while a refresh of the library is
// queued or running: that refresh may already have passed some shows, so
// they would keep the old numbering with no refresh left to renumber them.
func TestLibraryEpisodeOrderChangeWaitsForRunningRefreshDB(t *testing.T) {
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
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders(name,type) VALUES('episode order refresh guard','tv') RETURNING id`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	var user int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username, role) VALUES ($1, 'admin') RETURNING id`,
		fmt.Sprintf("episode-order-guard-%d", folder)).Scan(&user); err != nil {
		t.Fatal(err)
	}
	jobs := adminjob.NewRepository(pool)
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		_, _ = pool.Exec(ctx, `DELETE FROM admin_jobs WHERE request_payload->>'library_id' = $1::text`, folder)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id=$1`, folder)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, user)
	})
	h := NewLibraryHandler(catalog.NewFolderRepository(pool), nil, nil, pool, nil)
	h.JobRepo = jobs

	if _, err := jobs.CreateLibraryRefresh(t.Context(), user, adminjob.LibraryRefreshRequest{
		LibraryID: folder, LibraryName: "episode order refresh guard", Mode: adminjob.LibraryRefreshModeQuick,
	}, "test refresh"); err != nil {
		t.Fatal(err)
	}
	_, err = h.UpdateLibrary(t.Context(), folder, user, LibraryUpdateRequest{EpisodeOrderProvider: new("tmdb")})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("episode order change during a refresh = %v, want a 409", err)
	}
	stored, err := catalog.NewFolderRepository(pool).GetByID(t.Context(), folder)
	if err != nil || stored.EpisodeOrderProvider != "" {
		t.Fatalf("stored provider = %+v, %v; want it unchanged", stored, err)
	}
	// Other settings still save while the refresh runs.
	if _, err := h.UpdateLibrary(t.Context(), folder, user, LibraryUpdateRequest{Name: new("renamed during refresh")}); err != nil {
		t.Fatalf("rename during a refresh = %v", err)
	}
}
