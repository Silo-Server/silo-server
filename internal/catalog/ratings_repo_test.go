package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRatingsRepoSupportsSQLiteProfilesAndProfilePurge(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	var sqliteUserID, targetUserID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("ratings-sqlite-%d", suffix),
	).Scan(&sqliteUserID); err != nil {
		t.Fatalf("seed SQLite-profile user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("ratings-target-%d", suffix),
	).Scan(&targetUserID); err != nil {
		t.Fatalf("seed target user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, []int{sqliteUserID, targetUserID})
	})

	profileID := fmt.Sprintf("sqlite-profile-%d", suffix)
	targetProfileID := fmt.Sprintf("target-profile-%d", suffix)
	ownedItemID := fmt.Sprintf("ratings-owned-%d", suffix)
	targetItemID := fmt.Sprintf("ratings-target-item-%d", suffix)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_ratings (user_id, profile_id, media_item_id, rating, rated_at)
		VALUES ($1, $2, $3, 5, now()),
		       ($4, $5, $6, 4, now())`,
		sqliteUserID, profileID, ownedItemID,
		targetUserID, targetProfileID, targetItemID,
	); err != nil {
		t.Fatalf("seed ratings: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO household_rating_reactions (
			media_item_id,
			target_user_id,
			target_profile_id,
			reactor_user_id,
			reactor_profile_id,
			reaction
		) VALUES ($1, $2, $3, $4, $5, 1)`,
		targetItemID, targetUserID, targetProfileID, sqliteUserID, profileID,
	); err != nil {
		t.Fatalf("seed SQLite-profile reaction: %v", err)
	}

	repo := NewRatingsRepo(pool)
	ratings, err := repo.ListCommunity(ctx, sqliteUserID, profileID, ownedItemID, 100, 0)
	if err != nil {
		t.Fatalf("list community ratings: %v", err)
	}
	if len(ratings) != 1 || ratings[0].ProfileResolved {
		t.Fatalf("community ratings = %+v, want one unresolved SQLite-profile card", ratings)
	}

	if err := repo.PurgeProfile(ctx, sqliteUserID, profileID); err != nil {
		t.Fatalf("purge profile ratings: %v", err)
	}
	var ownedCount, reactionCount, targetCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM user_ratings WHERE user_id = $1 AND profile_id = $2),
			(SELECT count(*) FROM household_rating_reactions WHERE reactor_user_id = $1 AND reactor_profile_id = $2),
			(SELECT count(*) FROM user_ratings WHERE user_id = $3 AND profile_id = $4)`,
		sqliteUserID, profileID, targetUserID, targetProfileID,
	).Scan(&ownedCount, &reactionCount, &targetCount); err != nil {
		t.Fatalf("count purged rating data: %v", err)
	}
	if ownedCount != 0 || reactionCount != 0 || targetCount != 1 {
		t.Fatalf("post-purge counts = owned %d reactions %d target %d, want 0, 0, 1", ownedCount, reactionCount, targetCount)
	}
}
