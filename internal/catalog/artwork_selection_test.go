package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestQueueAndParkArtworkRevisionUpserts(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	path := fmt.Sprintf("tmdb/movies/%d/poster/original.rev.webp", suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, path)
	})

	// Arming registers the revision for verification after the grace period.
	if err := queueArtworkRevisionGC(ctx, pool, path, "poster", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("queueArtworkRevisionGC: %v", err)
	}
	var imageType string
	var nextAttempt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT image_type, next_attempt_at FROM artwork_revision_gc_candidates
		WHERE original_path = $1`, path).Scan(&imageType, &nextAttempt); err != nil {
		t.Fatalf("load armed candidate: %v", err)
	}
	if imageType != "poster" {
		t.Fatalf("image_type = %q, want poster", imageType)
	}
	if nextAttempt == nil {
		t.Fatal("armed candidate has NULL next_attempt_at")
	}

	// Publication parks the selected revision: referenced by construction.
	if err := parkArtworkRevision(ctx, pool, path, "poster", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("parkArtworkRevision: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT next_attempt_at FROM artwork_revision_gc_candidates
		WHERE original_path = $1`, path).Scan(&nextAttempt); err != nil {
		t.Fatalf("load parked candidate: %v", err)
	}
	if nextAttempt != nil {
		t.Fatalf("parked candidate next_attempt_at = %v, want NULL", *nextAttempt)
	}
}
