package metadata

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
)

// posterLargeRung is the widest poster rung, the one the latest ladder
// version added.
var posterLargeRung = imagesize.Variant(artworkkey.ImagePoster, imagesize.Large)

// A ladder change leaves existing revisions without the new widest rung until
// the backfill regenerates them. The reader answers from the publication
// manifest alone, so a client asking for that rung gets the widest one the
// revision published.
func TestArtworkPublicationSelectsWidestPublishedRungDB(t *testing.T) {
	pool := artworkPublicationTestPool(t)
	ctx := t.Context()
	original := fmt.Sprintf("tmdb/movies/publication-%d/poster/original.rev.webp", time.Now().UnixNano())
	large, previousLarge := variantKey(original, posterLargeRung), variantKey(original, "w780")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path=$1`, original)
	})
	// The revision predates the current ladder: it has the previous ladder's
	// widest rung, not the current one.
	if err := catalog.NewArtworkRevisionTracker(pool).TrackArtworkRevision(ctx, original, "poster",
		[]string{original, previousLarge, variantKey(original, "w500"), variantKey(original, "w300")}); err != nil {
		t.Fatal(err)
	}

	resolver := NewPluginImageResolver()
	t.Cleanup(resolver.Close)
	resolver.SetArtworkResolver(noProbeStore{t})
	resolver.SetArtworkAvailabilityReader(NewArtworkPublicationStore(pool))
	if got := resolver.ResolveImageURL(ctx, large, "large"); got != "https://images.example/"+previousLarge {
		t.Fatalf("resolved %q, want the widest published rung %s", got, previousLarge)
	}
}

// Only a completed publication advertises artwork: a legacy GC-only record is
// unknown, an upload in progress is a placeholder, a retry keeps the keys the
// last publication made, and a collected revision is a placeholder again.
func TestArtworkPublicationLifecycleDB(t *testing.T) {
	pool := artworkPublicationTestPool(t)
	ctx := t.Context()
	original := fmt.Sprintf("tmdb/movies/publication-life-%d/poster/original.rev.webp", time.Now().UnixNano())
	large, medium := variantKey(original, posterLargeRung), variantKey(original, "w500")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path=$1`, original)
	})
	tracker := catalog.NewArtworkRevisionTracker(pool)
	store := NewArtworkPublicationStore(pool)
	read := func() (ArtworkAvailability, bool) {
		t.Helper()
		states, err := store.ArtworkAvailability(ctx, []string{original})
		if err != nil {
			t.Fatal(err)
		}
		state, known := states[original]
		return state, known
	}
	track := func(keys []string) {
		t.Helper()
		if err := tracker.TrackArtworkRevision(ctx, original, "poster", keys); err != nil {
			t.Fatal(err)
		}
	}
	// GC registration alone is not evidence of an incomplete publication.
	if _, err := pool.Exec(ctx, `INSERT INTO artwork_revision_gc_candidates(original_path,image_type,not_before)
        VALUES($1,'poster',NOW())`, original); err != nil {
		t.Fatal(err)
	}
	if state, known := read(); known || selectPublishedVariant(large, state, known) != medium {
		t.Fatal("legacy GC-only row was not left to the established rung")
	}
	track(nil)
	if state, known := read(); selectPublishedVariant(large, state, known) != "" {
		t.Fatal("upload in progress was advertised")
	}
	track([]string{original, large, medium})
	if state, known := read(); selectPublishedVariant(large, state, known) != large {
		t.Fatal("published rung was not advertised")
	}
	track(nil)
	if state, _ := read(); !slices.Contains(state.Published, large) {
		t.Fatal("retry discarded publication")
	}
	if _, err := pool.Exec(ctx, `UPDATE artwork_revision_gc_candidates SET deleted_at = NOW() WHERE original_path=$1`, original); err != nil {
		t.Fatal(err)
	}
	if state, known := read(); !known || selectPublishedVariant(large, state, known) != "" {
		t.Fatal("collected revision was advertised")
	}
}

func artworkPublicationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
