package api

import (
	"context"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/routeinventory"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// TestRouteInventoryCoversRuntimeRouter is the runtime backstop for the
// inventory gate. `make verify-route-inventory` proves the committed artifact
// matches the source analysis and is the guarantee; this proves the source
// analysis matches a router that actually runs, so an analyzer bug cannot
// quietly drop a live route.
//
// Its reach is exactly the routes these fixtures construct, and no further. The
// comparison is one-directional for the same reason: the inventory is expected
// to hold more routes than any single wiring registers, and the fixtures below
// cannot see routes behind dependencies they do not construct. The proxy and
// transcode node listeners, whose rows are all unconditional, compare for
// equality instead.
func TestRouteInventoryCoversRuntimeRouter(t *testing.T) {
	inventory, err := routeinventory.LoadArtifact(".")
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// NewRouter seals the router so no production caller can register on it;
	// a test walks the tree through the unexported constructor instead.
	fixtures := map[string]chi.Routes{
		"minimal": newChiRouter(Dependencies{Config: cfg}),
		"maximal": newChiRouter(Dependencies{
			DB:         pool,
			Config:     cfg,
			FileRepo:   scanner.NewFileRepository(pool),
			FolderRepo: catalog.NewFolderRepository(pool),
			SessionMgr: playback.NewSessionManager(0, 0),
		}),
	}

	total := 0
	for name, router := range fixtures {
		observed, err := routeinventory.Observed(router)
		if err != nil {
			t.Fatalf("%s: walk router: %v", name, err)
		}
		total += len(observed)
		if missing := inventory.Reconcile(routeinventory.ListenerAPI, observed); len(missing) > 0 {
			t.Errorf("%s fixture registers %d route(s) with no inventory row; run `make route-inventory`:\n  %v",
				name, len(missing), missing)
		}
	}
	if total == 0 {
		t.Fatal("no routes observed; the fixtures no longer build a real router")
	}
}
