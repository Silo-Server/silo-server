package catalog

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ClaimPhotoSources points a person's photo source at an NFO's photo only
// while the stored photo is the one the caller read, and leaves the served
// photo alone until the image cache replaces it.
func TestPersonClaimPhotoSourcesPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	repo := NewPersonRepository(pool)
	suffix := uuid.NewString()
	ids, err := repo.BatchFindOrCreate(ctx, []models.Person{
		{Name: "Photo Empty " + suffix},
		{Name: "Photo Raced " + suffix, PhotoPath: "https://image.example.org/raced.jpg", PhotoSourcePath: "https://image.example.org/raced.jpg"},
		{Name: "Photo Same " + suffix},
		{Name: "Photo Enriched " + suffix},
	})
	if err != nil {
		t.Fatalf("BatchFindOrCreate: %v", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM people WHERE id = ANY($1)`, ids)
	})
	empty, raced, same, enriched := ids[0], ids[1], ids[2], ids[3]
	local := "file:///media/Movies/Film/.actors/" + suffix + ".jpg"
	if _, err := pool.Exec(ctx, `UPDATE people SET photo_source_path = $2 WHERE id = $1`, same, local); err != nil {
		t.Fatalf("seed same source: %v", err)
	}

	states, err := repo.PhotoStates(ctx, append(slices.Clone(ids), -1))
	if err != nil {
		t.Fatalf("PhotoStates: %v", err)
	}
	if len(states) != 4 || states[raced] != (PersonPhotoState{Path: "https://image.example.org/raced.jpg", Source: "https://image.example.org/raced.jpg"}) ||
		states[empty] != (PersonPhotoState{}) || states[same].Source != local {
		t.Fatalf("PhotoStates = %#v", states)
	}

	// Provider enrichment gives a person read without a photo a provider
	// photo, with no source, before the claim lands.
	if _, err := pool.Exec(ctx, `UPDATE people SET photo_path = 'https://image.example.org/enriched.jpg' WHERE id = $1`, enriched); err != nil {
		t.Fatalf("seed enrichment: %v", err)
	}

	claimed, err := repo.ClaimPhotoSources(ctx, []PersonPhotoSourceClaim{
		{ID: empty, Source: local},
		// Another writer moved the source since it was read.
		{ID: raced, Source: local},
		// Already the source: nothing to write.
		{ID: same, Expected: PersonPhotoState{Source: local}, Source: local},
		{ID: enriched, Source: local},
	})
	if err != nil {
		t.Fatalf("ClaimPhotoSources: %v", err)
	}
	if !slices.Equal(claimed, []int64{empty}) {
		t.Fatalf("claimed = %v, want only %d", claimed, empty)
	}
	after, err := repo.PhotoStates(ctx, ids)
	if err != nil {
		t.Fatalf("PhotoStates after: %v", err)
	}
	if after[empty] != (PersonPhotoState{Source: local}) {
		t.Errorf("claimed person = %#v, want the local source and no served photo yet", after[empty])
	}
	if after[raced] != states[raced] {
		t.Errorf("raced person = %#v, want unchanged %#v", after[raced], states[raced])
	}
	if after[enriched] != (PersonPhotoState{Path: "https://image.example.org/enriched.jpg"}) {
		t.Errorf("enriched person = %#v, want the provider photo kept and no NFO source", after[enriched])
	}

	// The image cache's copy then lands on the claimed source.
	updated, err := repo.UpdatePhotoIfSourceMatches(ctx, empty, local, "local/people/1/abcd1234/profile/original.webp", "hash")
	if err != nil || !updated {
		t.Fatalf("UpdatePhotoIfSourceMatches = %v, %v", updated, err)
	}
}
