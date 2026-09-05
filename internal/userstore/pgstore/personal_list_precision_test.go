package pgstore

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestPostgresPersonalListPagePreservesTimestampPrecision(t *testing.T) {
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
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("list-precision-%d", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, userID)
	})
	store := newStore(pool, userID)
	const profileID = "precision-profile"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Precision"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.UTC)
	// All three rows share the same visible millisecond. Their item IDs
	// deliberately disagree with timestamp order; two also share an exact
	// timestamp so the secondary key is exercised.
	ids := []string{"m-a", "m-b", "m-z"}
	times := []time.Time{base.Add(900 * time.Microsecond), base.Add(900 * time.Microsecond), base.Add(100 * time.Microsecond)}
	for i, id := range ids {
		if _, err := store.AddFavoriteAt(ctx, profileID, id, times[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AddToWatchlistAt(ctx, profileID, id, times[i]); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"favorites", "watchlist"} {
		t.Run(kind, func(t *testing.T) {
			var after *userstore.ListKey
			var got []string
			for range 4 {
				var keys []userstore.ListKey
				if kind == "favorites" {
					rows, err := store.ListFavoritesPage(ctx, profileID, after, 1)
					if err != nil {
						t.Fatal(err)
					}
					for _, row := range rows {
						keys = append(keys, userstore.ListKey{AddedAt: row.AddedAt, MediaItemID: row.MediaItemID})
					}
				} else {
					rows, err := store.ListWatchlistPage(ctx, profileID, after, 1)
					if err != nil {
						t.Fatal(err)
					}
					for _, row := range rows {
						keys = append(keys, userstore.ListKey{AddedAt: row.AddedAt, MediaItemID: row.MediaItemID})
					}
				}
				if len(keys) == 0 {
					break
				}
				key := keys[0]
				at, err := time.Parse(time.RFC3339Nano, key.AddedAt)
				if err != nil || at.Nanosecond()%int(time.Millisecond) == 0 {
					t.Fatalf("cursor timestamp lost stored precision: %q (%v)", key.AddedAt, err)
				}
				got = append(got, key.MediaItemID)
				after = &key
			}
			if !slices.Equal(got, []string{"m-b", "m-a", "m-z"}) {
				t.Fatalf("page walk = %v; want timestamp order, then ID, without gaps or repeats", got)
			}
		})
	}
}
