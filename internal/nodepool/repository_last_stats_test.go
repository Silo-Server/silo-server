package nodepool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// newLastStatsTestPool follows the repository-wide convention: skip without a
// database, and skip again on a database that predates the migration under test
// rather than failing on a missing column.
func newLastStatsTestPool(t *testing.T) *Repository {
	t.Helper()
	pool := newNodeTestPool(t)
	var column *string
	if err := pool.QueryRow(context.Background(),
		`SELECT column_name FROM information_schema.columns
		 WHERE table_name = 'stream_nodes' AND column_name = 'last_stats'`).Scan(&column); err != nil {
		t.Skip("test database has not applied the node last_stats migration")
	}
	return NewRepository(pool)
}

func createLastStatsNode(t *testing.T, repo *Repository) *Node {
	t.Helper()
	ctx := context.Background()
	unique := time.Now().UnixNano()
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("last-stats-test-%d", unique),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://last-stats-test-%d", unique),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })
	return node
}

// The health sweep's existing 30s write is the only write path for resource
// stats. It has to round-trip through an ordinary List — the same read the
// pools and the admin API use.
func TestRepositoryUpdateHealthPersistsLastStats(t *testing.T) {
	repo := newLastStatsTestPool(t)
	ctx := context.Background()
	node := createLastStatsNode(t, repo)

	if node.LastStats != nil {
		t.Fatalf("new node already carries stats: %s", node.LastStats)
	}

	stats := []byte(`{"system":{"cpu_pct":41,"mem_used_mb":9011},"gpu":[{"device":"/dev/dri/renderD128","source":"fdinfo"}]}`)
	if err := repo.UpdateHealth(ctx, node.ID, node.URL, true, 3, 17, stats); err != nil {
		t.Fatalf("update health: %v", err)
	}

	reloaded, err := repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	if reloaded.ActiveJobs != 3 || reloaded.EgressKbps != 17 || !reloaded.Healthy {
		t.Fatalf("existing health fields = %+v, want them unchanged by the new column", reloaded)
	}
	var decoded struct {
		System struct {
			CPUPct int `json:"cpu_pct"`
		} `json:"system"`
		GPU []struct {
			Device string `json:"device"`
		} `json:"gpu"`
	}
	if err := json.Unmarshal(reloaded.LastStats, &decoded); err != nil {
		t.Fatalf("stored stats are not json: %v (%s)", err, reloaded.LastStats)
	}
	if decoded.System.CPUPct != 41 {
		t.Fatalf("stored system.cpu_pct = %d, want 41", decoded.System.CPUPct)
	}
	if len(decoded.GPU) != 1 || decoded.GPU[0].Device != "/dev/dri/renderD128" {
		t.Fatalf("stored gpu = %+v", decoded.GPU)
	}
}

// A node that reports no sample writes NULL, and a node that stops reporting
// clears what it had. Keeping the previous row would leave a dead node's
// numbers on the Nodes page looking current.
func TestRepositoryUpdateHealthWritesNullForNodesWithoutStats(t *testing.T) {
	repo := newLastStatsTestPool(t)
	ctx := context.Background()
	node := createLastStatsNode(t, repo)

	if err := repo.UpdateHealth(ctx, node.ID, node.URL, true, 1, 0, []byte(`{"system":{"cpu_pct":41}}`)); err != nil {
		t.Fatalf("update health: %v", err)
	}
	if err := repo.UpdateHealth(ctx, node.ID, node.URL, false, 0, 0, nil); err != nil {
		t.Fatalf("update health without stats: %v", err)
	}

	reloaded, err := repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	if reloaded.LastStats != nil {
		t.Fatalf("LastStats = %s, want NULL after a check that carried none", reloaded.LastStats)
	}

	// The absent column must also be absent from the admin API's JSON, so a
	// client can tell "no sample" from "a sample of zeroes".
	encoded, err := json.Marshal(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["last_stats"]; ok {
		t.Fatalf("last_stats emitted for a node with none: %s", encoded)
	}
}

// last_stats carries the scratch fill transcode admission reads, so one worker's
// disk reading must never land on a row an administrator has repointed at
// another during the health request.
func TestRepositoryUpdateHealthRefusesAfterAURLEdit(t *testing.T) {
	ctx := context.Background()
	repo := NewRepository(newNodeTestPool(t))
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("health-moved-%d", time.Now().UnixNano()),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://health-moved-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })

	moved := node.URL + "-elsewhere"
	if _, err := repo.Update(ctx, node.ID, UpdateNodeInput{URL: &moved}); err != nil {
		t.Fatalf("repoint node: %v", err)
	}

	err = repo.UpdateHealth(ctx, node.ID, node.URL, true, 3, 17, []byte(`{"system":{"cpu_pct":41}}`))
	if !errors.Is(err, ErrNodeMoved) {
		t.Fatalf("err = %v, want ErrNodeMoved after the row was repointed", err)
	}
	reloaded, err := repo.GetByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	if len(reloaded.LastStats) != 0 {
		t.Fatalf("last_stats = %s, want the stale sample discarded", reloaded.LastStats)
	}
}
