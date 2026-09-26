package catalog

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRatingsRepoCompareAndSetDB covers the writes watch-provider rating sync
// uses: they apply only while the rating still has the value the sync read.
func TestRatingsRepoCompareAndSetDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var userID int
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id",
		fmt.Sprintf("ratings-cas-%d", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	repo := NewRatingsRepo(pool)
	const profile, item = "cas-profile", "cas-movie"
	ratedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	applied, err := repo.SetIfUnchanged(ctx, userID, profile, item, ObservedRating{}, 4, ratedAt)
	if err != nil || !applied {
		t.Fatalf("insert when unrated: applied=%v err=%v", applied, err)
	}
	got, err := repo.Get(ctx, userID, profile, item)
	if err != nil || got == nil || got.Rating != 4 || !got.RatedAt.Equal(ratedAt) {
		t.Fatalf("stored rating = %#v err=%v, want 4 at the provider time", got, err)
	}
	if applied, _ := repo.SetIfUnchanged(ctx, userID, profile, item, ObservedRating{}, 5, ratedAt); applied {
		t.Fatal("expecting unrated must not overwrite an existing rating")
	}
	if applied, _ := repo.SetIfUnchanged(ctx, userID, profile, item, ObservedRating{Rating: 3, RatedAt: ratedAt}, 5, ratedAt); applied {
		t.Fatal("a stale expected value must not apply")
	}
	if applied, _ := repo.DeleteIfUnchanged(ctx, userID, profile, item, ObservedRating{Rating: 3, RatedAt: ratedAt}); applied {
		t.Fatal("a stale expected value must not delete")
	}

	// A re-save of the same stars after the observation moves rated_at, so
	// the stale observation no longer matches.
	if err := repo.Set(ctx, userID, profile, item, 4); err != nil {
		t.Fatal(err)
	}
	if applied, _ := repo.SetIfUnchanged(ctx, userID, profile, item, ObservedRating{Rating: 4, RatedAt: ratedAt}, 2, ratedAt); applied {
		t.Fatal("a same-value re-save after the observation must win")
	}
	if applied, _ := repo.DeleteIfUnchanged(ctx, userID, profile, item, ObservedRating{Rating: 4, RatedAt: ratedAt}); applied {
		t.Fatal("a same-value re-save after the observation must survive a stale delete")
	}
	resaved, err := repo.Get(ctx, userID, profile, item)
	if err != nil || resaved == nil {
		t.Fatalf("resaved = %#v err=%v", resaved, err)
	}
	ratedAt = resaved.RatedAt

	// Two writers expecting the same value: exactly one wins.
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for _, rating := range []int{1, 2} {
		wg.Go(func() {
			ok, err := repo.SetIfUnchanged(ctx, userID, profile, item, ObservedRating{Rating: 4, RatedAt: ratedAt}, rating, ratedAt)
			if err != nil {
				t.Error(err)
			}
			results <- ok
		})
	}
	wg.Wait()
	close(results)
	wins := 0
	for ok := range results {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent compare-and-set winners = %d, want 1", wins)
	}

	current, err := repo.Get(ctx, userID, profile, item)
	if err != nil || current == nil {
		t.Fatalf("current = %#v err=%v", current, err)
	}
	if applied, err := repo.DeleteIfUnchanged(ctx, userID, profile, item, ObservedRating{Rating: current.Rating, RatedAt: current.RatedAt}); err != nil || !applied {
		t.Fatalf("delete with the current value: applied=%v err=%v", applied, err)
	}
	if gone, _ := repo.Get(ctx, userID, profile, item); gone != nil {
		t.Fatalf("rating survived delete: %#v", gone)
	}
}
