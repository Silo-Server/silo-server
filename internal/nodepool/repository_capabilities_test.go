package nodepool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newNodeTestPool follows the repository-wide convention for tests that need a
// real database: skip unless one is configured, and skip again if it predates
// the migration under test rather than failing on a missing column.
func newNodeTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	// Every read here selects the full column list, so the newest column is the
	// one worth probing: a database missing it fails every test in the package.
	var columns int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_name = 'stream_nodes'
		   AND column_name IN ('capabilities_hash', 'hw_accel_override', 'capability_drift')`).Scan(&columns); err != nil || columns < 3 {
		t.Skip("test database has not applied the stream_nodes capability/override migrations")
	}
	return pool
}

// Stored capabilities are what makes GPU inventory survive an API restart, so
// the payload, its hash, and its age must all come back through an ordinary
// List — the same read the pools and the admin API use.
func TestRepositoryUpdateCapabilitiesRoundTrip(t *testing.T) {
	pool := newNodeTestPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)

	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("capability-test-%d", time.Now().UnixNano()),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://capability-test-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })

	if node.Capabilities != nil || node.CapabilitiesHash != nil || node.CapabilitiesRefreshedAt != nil {
		t.Fatalf("new node already carries capabilities: %+v", node)
	}

	payload := json.RawMessage(`{"resolved":"nvenc","render_devices":["/dev/dri/renderD128"]}`)
	refreshedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := repo.UpdateCapabilities(ctx, node.ID, node.URL, payload, "sha256:abc", refreshedAt, nil, nil, nil); err != nil {
		t.Fatalf("update capabilities: %v", err)
	}

	reloaded, err := repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	var stored, want map[string]any
	if err := json.Unmarshal(reloaded.Capabilities, &stored); err != nil {
		t.Fatalf("stored capabilities are not json: %v", err)
	}
	if err := json.Unmarshal(payload, &want); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(stored) != fmt.Sprint(want) {
		t.Fatalf("stored capabilities = %v, want %v", stored, want)
	}
	if reloaded.CapabilitiesHash == nil || *reloaded.CapabilitiesHash != "sha256:abc" {
		t.Fatalf("stored hash = %v", reloaded.CapabilitiesHash)
	}
	if reloaded.CapabilitiesRefreshedAt == nil || !reloaded.CapabilitiesRefreshedAt.UTC().Equal(refreshedAt) {
		t.Fatalf("stored refresh time = %v, want %v", reloaded.CapabilitiesRefreshedAt, refreshedAt)
	}
}

// The drift note is what puts a silent hardware regression on the node list, so
// it has to survive a write and come back through an ordinary read — and a later
// clean refetch has to clear it, or a repaired node stays flagged forever.
func TestRepositoryUpdateCapabilitiesDriftRoundTripAndClear(t *testing.T) {
	pool := newNodeTestPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)

	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("drift-test-%d", time.Now().UnixNano()),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://drift-test-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })
	if node.CapabilityDrift != nil {
		t.Fatalf("new node already carries drift: %q", *node.CapabilityDrift)
	}

	note := "verified hardware backends lost: nvenc; resolved backend nvenc -> none"
	payload := json.RawMessage(`{"resolved":"none"}`)
	if err := repo.UpdateCapabilities(ctx, node.ID, node.URL, payload, "sha256:degraded", time.Now(), &note, nil, nil); err != nil {
		t.Fatalf("update capabilities with drift: %v", err)
	}
	reloaded, err := repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	if reloaded.CapabilityDrift == nil || *reloaded.CapabilityDrift != note {
		t.Fatalf("stored drift = %v, want %q", reloaded.CapabilityDrift, note)
	}

	recovered := json.RawMessage(`{"resolved":"nvenc"}`)
	if err := repo.UpdateCapabilities(ctx, node.ID, node.URL, recovered, "sha256:recovered", time.Now(), nil, nil, ptrString("sha256:degraded")); err != nil {
		t.Fatalf("update capabilities without drift: %v", err)
	}
	reloaded, err = repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	if reloaded.CapabilityDrift != nil {
		t.Fatalf("drift = %q after recovery, want NULL", *reloaded.CapabilityDrift)
	}
}

func TestRepositoryUpdateCapabilitiesUnknownNode(t *testing.T) {
	repo := NewRepository(newNodeTestPool(t))
	err := repo.UpdateCapabilities(context.Background(), -1, "http://gone", []byte(`{}`), "sha256:abc", time.Now(), nil, nil, nil)
	if !errors.Is(err, ErrNodeMoved) {
		t.Fatalf("err = %v, want ErrNodeMoved", err)
	}
}

// A capability fetch is detached from the sweep and bounded at two minutes,
// which is ample time for an administrator to repoint the row at a different
// machine. Writing by id alone would store one worker's GPU identities against
// another's URL, and the planner would place shared-GPU work on that reading
// until a later sweep corrected it.
func TestRepositoryUpdateCapabilitiesRefusesAfterAURLEdit(t *testing.T) {
	ctx := context.Background()
	repo := NewRepository(newNodeTestPool(t))
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("moved-test-%d", time.Now().UnixNano()),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://moved-test-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })

	moved := node.URL + "-elsewhere"
	if _, err := repo.Update(ctx, node.ID, UpdateNodeInput{URL: &moved}); err != nil {
		t.Fatalf("repoint node: %v", err)
	}

	err = repo.UpdateCapabilities(ctx, node.ID, node.URL, []byte(`{"resolved":"qsv"}`), "sha256:stale", time.Now(), nil, nil, nil)
	if !errors.Is(err, ErrNodeMoved) {
		t.Fatalf("err = %v, want ErrNodeMoved after the row was repointed", err)
	}
	reloaded, err := repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	if len(reloaded.Capabilities) != 0 {
		t.Fatalf("capabilities = %s, want the stale payload discarded", reloaded.Capabilities)
	}
}

// The trailing slash a pool trims is not a different node: the pools normalize
// URLs and the column does not, so an exact match would fence out every
// legitimate write for a row registered with one.
func TestRepositoryUpdateCapabilitiesIgnoresATrailingSlash(t *testing.T) {
	ctx := context.Background()
	repo := NewRepository(newNodeTestPool(t))
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("slash-test-%d", time.Now().UnixNano()),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://slash-test-%d/", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })

	normalized := strings.TrimSuffix(node.URL, "/")
	if err := repo.UpdateCapabilities(ctx, node.ID, normalized, []byte(`{"resolved":"qsv"}`), "sha256:ok", time.Now(), nil, nil, nil); err != nil {
		t.Fatalf("UpdateCapabilities with a normalized URL: %v", err)
	}
}

// Everything a capability report and a health sample describe belongs to the
// worker the URL addressed. Repointing the row at a different machine has to
// drop it: the caller publishes the returned row to the pools immediately, and
// these are the fields placement reads — the GPU identities behind
// physical_gpu_keys and the scratch fill behind admission.
func TestRepositoryUpdateClearsWorkerStateWhenTheURLMoves(t *testing.T) {
	ctx := context.Background()
	repo := NewRepository(newNodeTestPool(t))
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("repoint-%d", time.Now().UnixNano()),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://repoint-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })

	note := "verified hardware backends lost: qsv"
	payload := []byte(`{"resolved":"qsv","render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],"boot_id":"boot-1"}`)
	if err := repo.UpdateCapabilities(ctx, node.ID, node.URL, payload, "sha256:old", time.Now(), &note, []byte(`{"backends":["qsv"]}`), nil); err != nil {
		t.Fatalf("store capabilities: %v", err)
	}
	if err := repo.UpdateHealth(ctx, node.ID, node.URL, true, 2, 0, []byte(`{"system":{"cpu_pct":41}}`)); err != nil {
		t.Fatalf("store health: %v", err)
	}

	moved := node.URL + "-elsewhere"
	updated, err := repo.Update(ctx, node.ID, UpdateNodeInput{URL: &moved})
	if err != nil {
		t.Fatalf("repoint node: %v", err)
	}

	if len(updated.Capabilities) != 0 || updated.CapabilitiesHash != nil || updated.CapabilitiesRefreshedAt != nil {
		t.Fatalf("capabilities survived a repoint: %+v", updated)
	}
	if len(updated.LastStats) != 0 {
		t.Fatalf("last_stats survived a repoint: %s", updated.LastStats)
	}
	if updated.CapabilityDrift != nil || len(updated.CapabilityDriftBaseline) != 0 {
		t.Fatalf("drift survived a repoint: %v / %s", updated.CapabilityDrift, updated.CapabilityDriftBaseline)
	}
	if len(updated.PhysicalGPUKeys) != 0 {
		t.Fatalf("GPU identities survived a repoint: %v", updated.PhysicalGPUKeys)
	}
}

// An edit that leaves the row on the same worker keeps its state — including
// when only a trailing slash differs, which the pools normalize away and the
// column does not.
func TestRepositoryUpdateKeepsWorkerStateWithoutAMove(t *testing.T) {
	ctx := context.Background()
	repo := NewRepository(newNodeTestPool(t))
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("stay-%d", time.Now().UnixNano()),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://stay-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })

	payload := []byte(`{"resolved":"qsv","render_device_details":[{"path":"/dev/dri/renderD128","pci_address":"0000:03:00.0"}],"boot_id":"boot-1"}`)
	if err := repo.UpdateCapabilities(ctx, node.ID, node.URL, payload, "sha256:keep", time.Now(), nil, nil, nil); err != nil {
		t.Fatalf("store capabilities: %v", err)
	}

	renamed := "renamed-" + node.Name
	if updated, err := repo.Update(ctx, node.ID, UpdateNodeInput{Name: &renamed}); err != nil {
		t.Fatalf("rename node: %v", err)
	} else if len(updated.Capabilities) == 0 || updated.CapabilitiesHash == nil {
		t.Fatalf("a rename dropped the worker's capabilities: %+v", updated)
	}

	slashed := node.URL + "/"
	if updated, err := repo.Update(ctx, node.ID, UpdateNodeInput{URL: &slashed}); err != nil {
		t.Fatalf("re-save url: %v", err)
	} else if len(updated.Capabilities) == 0 {
		t.Fatal("a trailing-slash change was treated as a different worker")
	}
}

func ptrString(value string) *string { return &value }

// Every API replica runs its own health sweep, so two can fetch successive
// reports from one node at once. Without a fence on the report being replaced,
// a slower fetch of the older one lands last and takes the durable GPU
// identities and drift state back with it.
func TestUpdateCapabilitiesRefusesAReportThatFollowsAStaleOne(t *testing.T) {
	ctx := context.Background()
	repo := NewRepository(newNodeTestPool(t))
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("capability-cas-%d", time.Now().UnixNano()),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://capability-cas-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })

	if err := repo.UpdateCapabilities(ctx, node.ID, node.URL,
		[]byte(`{"resolved":"qsv"}`), "sha256:first", time.Now(), nil, nil, nil); err != nil {
		t.Fatalf("first report: %v", err)
	}
	// A second replica stores its newer report.
	if err := repo.UpdateCapabilities(ctx, node.ID, node.URL,
		[]byte(`{"resolved":"nvenc"}`), "sha256:second", time.Now(), nil, nil, ptrString("sha256:first")); err != nil {
		t.Fatalf("second report: %v", err)
	}
	// The first replica's *other* in-flight fetch finally lands. It read the
	// row before either write, so it must not overwrite what is there now.
	overtaken := repo.UpdateCapabilities(ctx, node.ID, node.URL,
		[]byte(`{"resolved":"vaapi"}`), "sha256:overtaken", time.Now(), nil, nil, ptrString("sha256:first"))
	// Superseded, not moved: the row still addresses this worker, and the
	// caller has something to learn from it. The distinction is what stops the
	// losing replica from sweeping against a stale hash forever.
	if !errors.Is(overtaken, ErrCapabilitiesSuperseded) {
		t.Fatalf("overtaken report error = %v, want ErrCapabilitiesSuperseded", overtaken)
	}
	if errors.Is(overtaken, ErrNodeMoved) {
		t.Fatal("a superseded report reported as a moved node; the caller would discard rather than reconcile")
	}

	stored, err := repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	if stored.CapabilitiesHash == nil || *stored.CapabilitiesHash != "sha256:second" {
		t.Fatalf("stored hash = %v, want the newer report kept", stored.CapabilitiesHash)
	}
}
