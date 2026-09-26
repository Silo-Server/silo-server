package api

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	apiv2 "github.com/Silo-Server/silo-server/contracts/api/v2"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/routeinventory"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/secret"
)

var updateOfflineRoutes = flag.Bool("update-offline-routes", false, "rewrite contracts/api/v2/offline-routes.txt from the real router")

// TestOfflineRouteSet pins which rows the API listener registers under the
// scenario executor's offline wiring. The executor cannot walk the router
// itself: NewRouter returns a sealed handler so nothing outside this package's
// tests recovers a registration surface. This test builds the same wiring
// through the unexported constructor, walks it with the route inventory's
// own enumeration (so the spelling matches inventory and catalog rows), and
// compares the result byte-for-byte with the committed file. It fails when
// the file is stale; `make offline-routes` regenerates it.
//
// The wiring here and the executor's must set the same Dependencies fields.
// The file records that field set on its wiring line and the executor checks
// it against its own construction, so the two cannot drift apart silently.
// Every registration condition the route inventory records is a presence
// test on a Dependencies field or on a local built from one, never a test
// of a field's value, so an equal field set is an equal route set.
func TestOfflineRouteSet(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	// A pool whose target never answers, the way the executor's offline
	// router has one: DB-dependent routes register and every query fails.
	pool, err := pgxpool.New(context.Background(), "postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	cipher, err := secret.New(bytes.Repeat([]byte{0}, secret.MinMasterKeyLen))
	if err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{
		Config:           cfg,
		AppContext:       context.Background(),
		DB:               pool,
		SecretCipher:     cipher,
		ClientIPResolver: clientip.NewResolver(nil),
		NodeID:           "offline-routes",
		PublicURL:        "https://silo.example.test",
	}
	observed, err := routeinventory.Observed(newChiRouter(deps))
	if err != nil {
		t.Fatalf("walk offline router: %v", err)
	}
	if len(observed) == 0 {
		t.Fatal("no routes observed; the wiring no longer builds a real router")
	}
	actual := scenariocatalog.EncodeOfflineRoutes(scenariocatalog.WiringFields(deps), observed)
	// Round-trip through the decoder so a file this test writes is one the
	// executor can read.
	if _, err := scenariocatalog.DecodeOfflineRoutes(actual); err != nil {
		t.Fatalf("encoded offline routes do not decode: %v", err)
	}

	root, err := routeinventory.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "contracts", "api", "v2", apiv2.OfflineRoutesPath)
	if *updateOfflineRoutes {
		if err := os.WriteFile(path, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, actual) {
		committed := "a file that does not decode"
		if set, err := scenariocatalog.DecodeOfflineRoutes(want); err == nil {
			committed = fmt.Sprintf("%d routes", len(set.Routes))
		}
		t.Fatalf("contracts/api/v2/%s is stale (committed: %s; observed: %d routes); inspect the diff and run make offline-routes",
			apiv2.OfflineRoutesPath, committed, len(observed))
	}
}
