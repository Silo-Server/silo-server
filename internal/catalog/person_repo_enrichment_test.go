package catalog

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// The generated SQL is asserted clause by clause because the enrichment rule
// only exists as SQL: there is no Go decision to unit test. Matching whole
// generated clauses (not loose fragments) is what makes a mis-wired column —
// the photo_source_path assignment gated on photo_thumbhash, say — fail here.
// Behavior is covered by TestPersonCreditEnrichmentPhotoRules, which needs a
// Postgres instance and therefore does not run in the default gate.
func TestPersonPhotoEnrichmentSQLGuardsCachedArtwork(t *testing.T) {
	single := personPhotoReplacePredicate("photo_path", "$1")
	for _, fragment := range []string{
		"COALESCE(photo_path, '') = '' AND $1 <> ''",
		"photo_path = '-' OR photo_path LIKE '%://%'",
		"$1 NOT IN ('', '-')",
		"$1 IS DISTINCT FROM photo_path",
	} {
		if !strings.Contains(single, fragment) {
			t.Fatalf("photo replace predicate %q is missing %q", single, fragment)
		}
	}

	batch := buildBatchPersonEnrichmentQuery()
	gate := personPhotoReplacePredicate("people.photo_path", "t.photo_path")
	if !strings.Contains(batch, gate) {
		t.Fatalf("batch enrichment SQL does not gate on the photo replace predicate %q", gate)
	}
	// Every photo column moves under the same photo_path decision, so the
	// served path can never be bound to the previous image's source or hash.
	for _, column := range []string{"photo_path", "photo_source_path", "photo_thumbhash"} {
		clause := fmt.Sprintf("%s = CASE WHEN %s THEN t.%s ELSE people.%s END", column, gate, column, column)
		if !strings.Contains(batch, clause) {
			t.Fatalf("batch enrichment SQL is missing the guarded assignment %q", clause)
		}
		destructive := fmt.Sprintf("WHEN t.%s NOT IN ('', '-') THEN t.%s", column, column)
		if strings.Contains(batch, destructive) {
			t.Fatalf("batch enrichment SQL still unconditionally overwrites %s", column)
		}
	}
	if !strings.Contains(batch, "WHERE people.id = t.id") || !strings.Contains(batch, "updated_at = NOW()") {
		t.Fatal("batch enrichment SQL lost its guarded update or timestamp assignment")
	}
}

type seededPerson struct {
	id        int64
	tmdbID    string
	photoPath string
	updatedAt time.Time
}

type personPhotoState struct {
	photoPath string
	source    string
	thumbhash string
	updatedAt time.Time
}

func TestPersonCreditEnrichmentPhotoRules(t *testing.T) {
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
	repo := NewPersonRepository(pool)

	const cachedKey = "tmdb/people/%d/profile/original.cached.webp"

	// seed inserts a person whose every enrichable field is already populated,
	// so any row change observed by a test came from the photo rule.
	seed := func(t *testing.T, label, photoPath, sourcePath string) seededPerson {
		t.Helper()
		nowID := time.Now().UnixNano()
		// Cached keys are per-person so the artwork GC assertions cannot pick
		// up a row left by another test; the literal paths pass through.
		if strings.Contains(photoPath, "%d") {
			photoPath = fmt.Sprintf(photoPath, nowID)
		}
		seeded := seededPerson{
			id:        nowID,
			tmdbID:    fmt.Sprintf("credit-enrichment-%s-%d", label, nowID),
			photoPath: photoPath,
			updatedAt: time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond),
		}
		_, err := pool.Exec(ctx, `
			INSERT INTO people (
				id, name, tmdb_id, imdb_id, tvdb_id, plex_guid,
				photo_path, photo_source_path, photo_thumbhash,
				bio, birthplace, homepage, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, $8, 'existing-thumbhash',
				'existing bio', 'existing birthplace', 'https://example.com', $9
			)
		`, seeded.id, "Credit Enrichment "+label, seeded.tmdbID,
			fmt.Sprintf("existing-imdb-%d", nowID), fmt.Sprintf("existing-tvdb-%d", nowID),
			fmt.Sprintf("existing-plex-%d", nowID), seeded.photoPath, sourcePath, seeded.updatedAt)
		if err != nil {
			t.Fatalf("seed person: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM people WHERE id = $1`, seeded.id)
			_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, seeded.photoPath)
		})
		return seeded
	}

	readPhoto := func(t *testing.T, id int64) personPhotoState {
		t.Helper()
		var got personPhotoState
		if err := pool.QueryRow(ctx, `
			SELECT COALESCE(photo_path, ''), COALESCE(photo_source_path, ''),
			       COALESCE(photo_thumbhash, ''), updated_at
			FROM people WHERE id = $1
		`, id).Scan(&got.photoPath, &got.source, &got.thumbhash, &got.updatedAt); err != nil {
			t.Fatalf("read enriched person: %v", err)
		}
		return got
	}

	assertNoGCCandidate := func(t *testing.T, path string) {
		t.Helper()
		var candidates int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM artwork_revision_gc_candidates WHERE original_path = $1
		`, path).Scan(&candidates); err != nil {
			t.Fatalf("count artwork GC candidates: %v", err)
		}
		if candidates != 0 {
			t.Fatalf("enrichment armed %d artwork GC candidates for %q, want 0", candidates, path)
		}
	}

	// incoming is shaped like a real item credit: every scalar field carries a
	// replacement value, and the photo columns carry whatever the caller passes.
	incoming := func(seed seededPerson, photoPath, sourcePath, thumbhash string) models.Person {
		return models.Person{
			Name:            "Credit Enrichment",
			TmdbID:          seed.tmdbID,
			ImdbID:          "replacement-imdb",
			TvdbID:          "replacement-tvdb",
			PlexGUID:        "replacement-plex",
			PhotoPath:       photoPath,
			PhotoSourcePath: sourcePath,
			PhotoThumbhash:  thumbhash,
			Bio:             "replacement bio",
			Birthplace:      "replacement birthplace",
			Homepage:        "https://replacement.example.com",
		}
	}

	batchEnrich := func(t *testing.T, seed seededPerson, p models.Person) {
		t.Helper()
		ids, err := repo.BatchFindOrCreate(ctx, []models.Person{p})
		if err != nil {
			t.Fatalf("BatchFindOrCreate: %v", err)
		}
		if len(ids) != 1 || ids[0] != seed.id {
			t.Fatalf("BatchFindOrCreate ids = %v, want [%d]", ids, seed.id)
		}
	}

	t.Run("cached key survives single find or create", func(t *testing.T) {
		seeded := seed(t, "single", cachedKey, "https://images.example/original.jpg")
		id, err := repo.FindOrCreate(ctx, incoming(seeded,
			"https://images.example/replacement.jpg",
			"https://images.example/replacement-source.jpg",
			"replacement-thumbhash"))
		if err != nil {
			t.Fatalf("FindOrCreate: %v", err)
		}
		if id != seeded.id {
			t.Fatalf("FindOrCreate id = %d, want %d", id, seeded.id)
		}
		got := readPhoto(t, seeded.id)
		if got.photoPath != seeded.photoPath || got.source != "https://images.example/original.jpg" ||
			got.thumbhash != "existing-thumbhash" {
			t.Fatalf("cached artwork was overwritten: %+v", got)
		}
		if !got.updatedAt.Equal(seeded.updatedAt) {
			t.Fatalf("no-op enrichment changed updated_at: got %v, want %v", got.updatedAt, seeded.updatedAt)
		}
		assertNoGCCandidate(t, seeded.photoPath)
	})

	t.Run("cached key survives batch find or create", func(t *testing.T) {
		seeded := seed(t, "batch", cachedKey, "https://images.example/original.jpg")
		batchEnrich(t, seeded, incoming(seeded,
			"https://images.example/replacement.jpg",
			"https://images.example/replacement-source.jpg",
			"replacement-thumbhash"))
		got := readPhoto(t, seeded.id)
		if got.photoPath != seeded.photoPath || got.source != "https://images.example/original.jpg" ||
			got.thumbhash != "existing-thumbhash" {
			t.Fatalf("cached artwork was overwritten: %+v", got)
		}
		if !got.updatedAt.Equal(seeded.updatedAt) {
			t.Fatalf("no-op enrichment changed updated_at: got %v, want %v", got.updatedAt, seeded.updatedAt)
		}
		assertNoGCCandidate(t, seeded.photoPath)
	})

	// A credit carries a photo URL but never a source path, so the whole triple
	// has to move together: leaving the old source behind would make the image
	// cache download it and land the previous image under the new photo.
	t.Run("sentinel replacement clears the previous source binding", func(t *testing.T) {
		seeded := seed(t, "sentinel", "-", "https://images.example/stale-source.jpg")
		batchEnrich(t, seeded, incoming(seeded, "https://images.example/replacement.jpg", "", ""))
		got := readPhoto(t, seeded.id)
		if got.photoPath != "https://images.example/replacement.jpg" || got.source != "" || got.thumbhash != "" {
			t.Fatalf("photo triple did not move as a unit: %+v", got)
		}
		if !got.updatedAt.After(seeded.updatedAt) {
			t.Fatalf("sentinel replacement did not advance updated_at: got %v, previous %v", got.updatedAt, seeded.updatedAt)
		}
	})

	// Nothing refreshes a person without an external id (FindRefreshCandidates
	// skips them), so an uncached URL has to stay replaceable from a credit.
	t.Run("uncached url is replaceable", func(t *testing.T) {
		seeded := seed(t, "url", "https://images.example/dead-%d.jpg", "https://images.example/dead-source.jpg")
		batchEnrich(t, seeded, incoming(seeded,
			"https://images.example/replacement.jpg",
			"https://images.example/replacement-source.jpg",
			"replacement-thumbhash"))
		got := readPhoto(t, seeded.id)
		if got.photoPath != "https://images.example/replacement.jpg" ||
			got.source != "https://images.example/replacement-source.jpg" ||
			got.thumbhash != "replacement-thumbhash" {
			t.Fatalf("uncached url was not replaced: %+v", got)
		}
		// The GC trigger ignores non-cached paths, so displacing a URL must not
		// queue anything for deletion.
		assertNoGCCandidate(t, seeded.photoPath)
	})

	// Re-scanning an unchanged credit must not touch the row: rewriting the
	// same URL would drop the source path a pending cache job is keyed on and
	// re-order the person in the refresh sweep for nothing.
	t.Run("unchanged url is a no-op", func(t *testing.T) {
		seeded := seed(t, "same", "https://images.example/same-%d.jpg", "https://images.example/same-source.jpg")
		batchEnrich(t, seeded, incoming(seeded, seeded.photoPath, "", ""))
		got := readPhoto(t, seeded.id)
		if got.photoPath != seeded.photoPath || got.source != "https://images.example/same-source.jpg" ||
			got.thumbhash != "existing-thumbhash" {
			t.Fatalf("unchanged credit rewrote the photo columns: %+v", got)
		}
		if !got.updatedAt.Equal(seeded.updatedAt) {
			t.Fatalf("unchanged credit changed updated_at: got %v, want %v", got.updatedAt, seeded.updatedAt)
		}
	})

	t.Run("empty photo columns are filled", func(t *testing.T) {
		seeded := seed(t, "empty", "", "")
		batchEnrich(t, seeded, incoming(seeded,
			"https://images.example/replacement.jpg",
			"https://images.example/replacement-source.jpg",
			"replacement-thumbhash"))
		got := readPhoto(t, seeded.id)
		if got.photoPath != "https://images.example/replacement.jpg" ||
			got.source != "https://images.example/replacement-source.jpg" ||
			got.thumbhash != "replacement-thumbhash" {
			t.Fatalf("empty photo columns were not filled: %+v", got)
		}
	})
}
