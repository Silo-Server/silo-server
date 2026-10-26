package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestPersonNameFallbackKeepsNamesakesApartPostgres covers the name fallback
// that FindOrCreate and BatchFindOrCreate use when no stored TMDB or IMDb ID
// matches. Two people who share a name but carry different provider IDs are
// different people; a shared provider ID, or no IDs on either side, still
// resolves to the stored person.
func TestPersonNameFallbackKeepsNamesakesApartPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	repo := NewPersonRepository(pool)
	suffix := uuid.NewString()
	name := "Namesake " + suffix
	placeholderName := "Placeholder " + suffix
	var ids []int64
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM people WHERE id = ANY($1)`, ids)
	})
	findOrCreate := func(p models.Person) int64 {
		t.Helper()
		id, err := repo.FindOrCreate(ctx, p)
		if err != nil {
			t.Fatalf("FindOrCreate(%+v): %v", p, err)
		}
		ids = append(ids, id)
		return id
	}

	stored := findOrCreate(models.Person{Name: name, TmdbID: "tmdb-a-" + suffix, TvdbID: "tvdb-a-" + suffix})

	if got := findOrCreate(models.Person{Name: name, TmdbID: "tmdb-b-" + suffix}); got == stored {
		t.Fatalf("FindOrCreate merged a namesake with another TMDB ID into person %d", stored)
	}
	batch, err := repo.BatchFindOrCreate(ctx, []models.Person{{Name: name, ImdbID: "nm-c-" + suffix}})
	if err != nil {
		t.Fatalf("BatchFindOrCreate: %v", err)
	}
	ids = append(ids, batch...)
	if len(batch) != 1 || batch[0] == 0 || batch[0] == stored {
		t.Fatalf("BatchFindOrCreate = %v, want a new person apart from %d", batch, stored)
	}

	if got := findOrCreate(models.Person{Name: name, TvdbID: "tvdb-a-" + suffix}); got != stored {
		t.Fatalf("FindOrCreate with the stored TVDB ID = %d, want person %d", got, stored)
	}

	placeholder := findOrCreate(models.Person{Name: placeholderName})
	if got := findOrCreate(models.Person{Name: placeholderName}); got != placeholder {
		t.Fatalf("a second name-only credit = %d, want the name-only person %d", got, placeholder)
	}
}
