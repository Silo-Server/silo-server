package catalog

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Scheduled syncs can refresh collages before the router supplies the
// generator, so setting it must not race with refreshes. Run with -race.
func TestPersonalCollectionCollagesGeneratorArrivesWhileRefreshing(t *testing.T) {
	// The pool never connects: refreshes wait out RefreshDelay first.
	pool, err := pgxpool.New(t.Context(), "postgres://silo@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	p := NewPersonalCollectionCollages(pool, nil)
	p.RefreshDelay = time.Hour
	if p.enabled() {
		t.Fatal("enabled without a generator")
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 200 {
			p.Refresh(1, PersonalCollectionDefinition{ID: fmt.Sprint(i)}, AccessFilter{})
		}
	})
	p.SetCollageGenerator(&fakeCollageGenerator{})
	wg.Wait()
	if !p.enabled() {
		t.Fatal("not enabled after SetCollageGenerator")
	}
	p.SetCollageGenerator(nil)
	if p.enabled() {
		t.Fatal("enabled after clearing the generator")
	}
}
