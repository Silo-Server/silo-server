package metadata

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

func TestRetryOnDeadlock(t *testing.T) {
	prevBackoff := artworkReconcileDeadlockBaseBackoff
	artworkReconcileDeadlockBaseBackoff = time.Millisecond
	t.Cleanup(func() { artworkReconcileDeadlockBaseBackoff = prevBackoff })

	deadlock := &pgconn.PgError{Code: "40P01"}
	ctx := context.Background()

	t.Run("retries deadlocks until success", func(t *testing.T) {
		calls := 0
		err := retryOnDeadlock(ctx, func() error {
			calls++
			if calls < 3 {
				return fmt.Errorf("exec: %w", deadlock)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("retryOnDeadlock: %v", err)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3", calls)
		}
	})

	t.Run("gives up after max attempts", func(t *testing.T) {
		calls := 0
		err := retryOnDeadlock(ctx, func() error {
			calls++
			return deadlock
		})
		if !errors.Is(err, deadlock) {
			t.Fatalf("err = %v, want the deadlock error", err)
		}
		if calls != artworkReconcileDeadlockMaxAttempts {
			t.Errorf("calls = %d, want %d", calls, artworkReconcileDeadlockMaxAttempts)
		}
	})

	t.Run("returns other errors immediately", func(t *testing.T) {
		calls := 0
		boom := errors.New("boom")
		if err := retryOnDeadlock(ctx, func() error {
			calls++
			return boom
		}); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})

	t.Run("honors cancellation between attempts", func(t *testing.T) {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		err := retryOnDeadlock(canceled, func() error { return deadlock })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

// presentObjects reports the keys it holds as present and any other as missing.
type presentObjects map[string]bool

func (p presentObjects) Stat(_ context.Context, key string) (blobstore.ObjectInfo, error) {
	if p[key] {
		return blobstore.ObjectInfo{}, nil
	}
	return blobstore.ObjectInfo{}, blobstore.ErrNotFound
}

// An uploaded image's row is the only pointer to its object: a bulk reset
// must keep the row while the object survives and clear it once it is gone.
func TestArtworkReconcileBulkResetVerifiesUploadsDB(t *testing.T) {
	pool := storedArtworkReleaseTestPool(t)
	ctx := t.Context()
	const (
		kept = "local/movies/up-kept/1a2b3c4d/poster/original.r1.webp"
		lost = "local/movies/up-lost/5e6f7a8b/poster/original.r1.webp"
	)
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, locked_fields, poster_path, poster_source_path) VALUES
		('provider', 'movie', '{}', 'tmdb/movies/1/poster/original.r1.webp', 'tmdb://poster/p.jpg'),
		('sidecar', 'movie', '{}', 'local/movies/2/poster/original.r1.webp', 'file:///m/poster.jpg?v=1-1'),
		('up-kept', 'movie', '{10}', $1, 'upload://sha256:aa'),
		('up-lost', 'movie', '{10}', $2, 'upload://sha256:bb')`, kept, lost); err != nil {
		t.Fatal(err)
	}
	var surface artworkSweepSurface
	for _, s := range artworkSweepSurfaces() {
		if s.name == "item posters" {
			surface = s
		}
	}

	r := &ArtworkCacheReconciler{pool: pool, s3: presentObjects{kept: true}}
	var stats ArtworkReconcileStats
	if err := r.bulkResetSurface(ctx, surface, &stats); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"provider": "tmdb://poster/p.jpg",
		"sidecar":  "",
		"up-kept":  kept,
		"up-lost":  "",
	}
	for id, path := range want {
		var got string
		if err := pool.QueryRow(ctx, `SELECT poster_path FROM media_items WHERE content_id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != path {
			t.Errorf("%s poster_path = %q, want %q", id, got, path)
		}
	}
}
