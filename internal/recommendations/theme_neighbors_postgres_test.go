package recommendations

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// ThemeNeighbors returns the movies and series at least as close to the
// seeds' shared theme as a typical seed, nearest first: not the seeds, not
// farther titles, not other media types, and not titles the viewer cannot see.
func TestThemeNeighborsStopsAtTheMedianSeedPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "tthemenb-"
	const axis = 2900
	cleanupRecoMediaItems(t, pool, prefix)
	repo := NewRepo(pool)
	seed := func(id, mediaType, rating string, age int, nudges map[int]float32) string {
		id = prefix + id
		seedRecoMediaItem(t, pool, id, mediaType, "matched")
		if _, err := pool.Exec(ctx, `UPDATE media_items SET content_rating = $2, content_rating_age = $3 WHERE content_id = $1`, id, rating, age); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpsertEmbedding(ctx, id, axisVector(axis, nudges), "test-model", id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Four seeds, each pulled off the theme axis in its own direction, so
	// their average sits on the axis and each lies the same distance from it.
	var seeds []string
	for i := range 4 {
		seeds = append(seeds, seed("seed-"+string(rune('a'+i)), "movie", "PG", 8, map[int]float32{axis + 1 + i: 0.3}))
	}
	nearest := seed("nearest", "movie", "PG", 8, map[int]float32{axis + 10: 0.05})
	nearShow := seed("near-show", "series", "TV-PG", 8, map[int]float32{axis + 10: 0.15})
	nearR := seed("near-r", "movie", "R", 17, map[int]float32{axis + 10: 0.1})
	seed("near-audiobook", "audiobook", "", 0, map[int]float32{axis + 10: 0.02})
	seed("far", "movie", "PG", 8, map[int]float32{axis + 11: 0.6})

	got, err := repo.ThemeNeighbors(ctx, seeds, 50, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if ids := prefixed(got, prefix); !slices.Equal(ids, []string{nearest, nearR, nearShow}) {
		t.Fatalf("neighbors = %v, want %v", ids, []string{nearest, nearR, nearShow})
	}

	restricted, err := repo.ThemeNeighbors(ctx, seeds, 50, catalog.AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}})
	if err != nil {
		t.Fatal(err)
	}
	if ids := prefixed(restricted, prefix); !slices.Equal(ids, []string{nearest, nearShow}) {
		t.Fatalf("PG neighbors = %v, want %v", ids, []string{nearest, nearShow})
	}

	tooFew, err := repo.ThemeNeighbors(ctx, seeds[:ThemeMinSeeds-1], 50, catalog.AccessFilter{})
	if err != nil || len(tooFew) != 0 {
		t.Fatalf("with %d seeds: %v, %v; want nothing", ThemeMinSeeds-1, tooFew, err)
	}
}
