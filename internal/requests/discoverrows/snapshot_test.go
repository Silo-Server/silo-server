package discoverrows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func snapshotTestRepository(t *testing.T) *SnapshotRepository {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("discover_lists_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(t.Context(), `CREATE SCHEMA `+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+quoted+` CASCADE`) })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(t.Context(), `CREATE TABLE request_discover_list_snapshots (LIKE public.request_discover_list_snapshots INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	return NewSnapshotRepository(pool)
}

func TestDiscoverListSnapshotLeaseDatabase(t *testing.T) {
	repo := snapshotTestRepository(t)
	ctx := t.Context()
	const key = "tmdb_list:7"
	if statuses, err := repo.Statuses(ctx, []string{key}); err != nil || statuses[key].State != StatePending {
		t.Fatalf("before any fetch = %+v %v, want pending", statuses, err)
	}
	claimAt, claimed, err := repo.TryClaimRefresh(ctx, key, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("first claim = %v %v", claimed, err)
	}
	if _, again, err := repo.TryClaimRefresh(ctx, key, time.Minute); err != nil || again {
		t.Fatalf("second claim inside the lease = %v %v, want refused", again, err)
	}
	cards := []Card{{ID: 1, MediaType: "movie", Title: "One", ReleaseDate: "1999-03-31"}, {ID: 2, MediaType: "series", Title: "Two"}}
	if err := repo.SaveSuccess(ctx, key, cards, snapshotOK, claimAt, time.Now()); err != nil {
		t.Fatal(err)
	}

	// A worker whose lease was taken over cannot overwrite the newer claim.
	stale := claimAt
	newer, claimed, err := repo.TryClaimRefresh(ctx, key, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim after save = %v %v", claimed, err)
	}
	if err := repo.SaveSuccess(ctx, key, nil, snapshotOK, stale, time.Now()); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("save with a lost lease = %v, want ErrLeaseLost", err)
	}
	if err := repo.RecordFailure(ctx, key, "tmdb down", newer); err != nil {
		t.Fatal(err)
	}
	got, found, err := repo.Get(ctx, key)
	if err != nil || !found || len(got.Cards) != 2 || got.Cards[0].mediaResult().Year != 1999 {
		t.Fatalf("after a failure = %+v %v %v, want the last good cards", got, found, err)
	}
	statuses, err := repo.Statuses(ctx, []string{key})
	if err != nil || statuses[key].State != StateError || statuses[key].Entries != 2 || statuses[key].Error != "tmdb down" {
		t.Fatalf("status = %+v %v", statuses[key], err)
	}

	if n, err := repo.DeleteUnreferenced(ctx, nil, time.Hour); err != nil || n != 0 {
		t.Fatalf("delete of a recently fetched list = %d %v, want kept", n, err)
	}
	if n, err := repo.DeleteUnreferenced(ctx, []string{key}, 0); err != nil || n != 0 {
		t.Fatalf("delete of a named list = %d %v, want kept", n, err)
	}
	if n, err := repo.DeleteUnreferenced(ctx, []string{"tmdb_list:8"}, 0); err != nil || n != 1 {
		t.Fatalf("delete of an unnamed old list = %d %v, want removed", n, err)
	}
}
