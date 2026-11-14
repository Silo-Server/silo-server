package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

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

// TestPersonFindOrCreateConcurrentNameOnlyPostgres covers parallel scan
// workers (or replicas sharing one database) crediting the same name-only
// person at once: every call resolves to one person instead of each worker
// that missed the lookup inserting its own namesake row.
func TestPersonFindOrCreateConcurrentNameOnlyPostgres(t *testing.T) {
	config := collectionSortTestPool(t).Config()
	// The lookup must see the preceding lock holder's insert even when the
	// connection default would keep a snapshot taken before the lock wait.
	config.ConnConfig.RuntimeParams["default_transaction_isolation"] = "repeatable read"
	ctx := t.Context()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	repo := NewPersonRepository(pool)
	prefix := "Concurrent Author " + uuid.NewString()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM people WHERE LOWER(name) LIKE LOWER($1) || '%'`, prefix)
	})

	const rounds, workers = 20, 8
	for round := range rounds {
		name := fmt.Sprintf("%s %d", prefix, round)
		start := make(chan struct{})
		ids := make([]int64, workers)
		errs := make([]error, workers)
		var wg sync.WaitGroup
		for w := range workers {
			wg.Go(func() {
				<-start
				// Mixed case: the name lookup is case-insensitive.
				credit := name
				if w%2 == 1 {
					credit = strings.ToUpper(name)
				}
				ids[w], errs[w] = repo.FindOrCreate(ctx, models.Person{Name: credit})
			})
		}
		close(start)
		wg.Wait()
		for w, err := range errs {
			if err != nil {
				t.Fatalf("round %d worker %d: FindOrCreate: %v", round, w, err)
			}
		}
		var rows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM people WHERE LOWER(name) = LOWER($1)`, name).Scan(&rows); err != nil {
			t.Fatalf("count people: %v", err)
		}
		if rows != 1 || slices.ContainsFunc(ids, func(id int64) bool { return id != ids[0] }) {
			t.Fatalf("round %d: %d people rows named %q, ids %v; want one shared person", round, rows, name, ids)
		}
	}
}
