package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type ladderFenceFixture struct {
	contentID string
	widePath  string
	oldPath   string
}

func seedLadderFenceFixture(t *testing.T, pool *pgxpool.Pool) ladderFenceFixture {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	fixture := ladderFenceFixture{
		contentID: fmt.Sprintf("ladder-fence-%d", suffix),
		widePath:  fmt.Sprintf("tmdb/movies/ladder-fence-%d/poster/original.wide.webp", suffix),
		oldPath:   fmt.Sprintf("tmdb/movies/ladder-fence-%d/poster/original.old.webp", suffix),
	}

	var previousVersion int
	var previousAttempt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT backfilled_version, last_attempt_at
		FROM image_ladder_backfill_state
		WHERE id = 1
	`).Scan(&previousVersion, &previousAttempt); err != nil {
		t.Fatalf("read ladder state: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE image_ladder_backfill_state
		SET backfilled_version = 1, last_attempt_at = NULL, updated_at = NOW()
		WHERE id = 1
	`); err != nil {
		t.Fatalf("reset ladder fence state: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO artwork_revision_gc_candidates (
			original_path, image_type, object_keys, not_before, next_attempt_at
		) VALUES
			($1, 'poster', ARRAY[$1, $2], NOW() + interval '1 day', NULL),
			($3, 'poster', ARRAY[$3, $4], NOW() + interval '1 day', NULL)
	`, fixture.widePath, fmt.Sprintf("tmdb/movies/ladder-fence-%d/poster/w780.wide.webp", suffix),
		fixture.oldPath, fmt.Sprintf("tmdb/movies/ladder-fence-%d/poster/w500.old.webp", suffix)); err != nil {
		t.Fatalf("seed ladder fence manifests: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (
			content_id, type, title, status, genres, poster_path, poster_source_path
		) VALUES ($1, 'movie', 'Ladder fence fixture', 'matched', '{}', $2, $3)
	`, fixture.contentID, fixture.widePath, fmt.Sprintf("https://example.invalid/ladder-fence-%d.jpg", suffix)); err != nil {
		t.Fatalf("seed ladder fence fixture: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, fixture.contentID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = ANY($1)`, []string{fixture.widePath, fixture.oldPath})
		_, _ = pool.Exec(context.Background(), `
			UPDATE image_ladder_backfill_state
			SET backfilled_version = $1, last_attempt_at = $2, updated_at = NOW()
			WHERE id = 1
		`, previousVersion, previousAttempt)
	})
	return fixture
}

func TestImageLadderBackfillFinalConfirmationRejectsLateOldArtwork(t *testing.T) {
	pool := imageCacheQueueTestPool(t)
	fixture := seedLadderFenceFixture(t, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `UPDATE media_items SET poster_path = $1 WHERE content_id = $2`, fixture.oldPath, fixture.contentID); err != nil {
		t.Fatalf("publish old-ladder artwork: %v", err)
	}
	confirmed, err := NewImageLadderBackfillStateRepository(pool).ConfirmBackfilled(ctx, 2)
	if err != nil {
		t.Fatalf("ConfirmBackfilled: %v", err)
	}
	if confirmed {
		t.Fatal("confirmed ladder v2 while the published artwork lacks its new rung")
	}

	state, err := NewImageLadderBackfillStateRepository(pool).Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.LastAttemptAt.IsZero() {
		t.Fatalf("last attempt = %v, want immediate retry after rejected confirmation", state.LastAttemptAt)
	}
}

func TestImageLadderBackfillLateOldArtworkReopensCompletedVersion(t *testing.T) {
	pool := imageCacheQueueTestPool(t)
	fixture := seedLadderFenceFixture(t, pool)
	ctx := context.Background()

	// This test exercises the trigger after completion, so establish that state
	// directly. ConfirmBackfilled intentionally scans the whole catalog; using it
	// here would make an unrelated fixture in a shared integration database able
	// to block this test before it reaches the behavior under test.
	if _, err := pool.Exec(ctx, `
		UPDATE image_ladder_backfill_state
		SET backfilled_version = 2, updated_at = NOW()
		WHERE id = 1
	`); err != nil {
		t.Fatalf("establish completed ladder state: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_items SET poster_path = $1 WHERE content_id = $2`, fixture.oldPath, fixture.contentID); err != nil {
		t.Fatalf("publish late old-ladder artwork: %v", err)
	}

	state, err := NewImageLadderBackfillStateRepository(pool).Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if state.BackfilledVersion != 1 {
		t.Fatalf("backfilled version = %d, want reopened v1", state.BackfilledVersion)
	}
	if !state.LastAttemptAt.IsZero() {
		t.Fatalf("last attempt = %v, want late publication to clear pacing", state.LastAttemptAt)
	}
}
