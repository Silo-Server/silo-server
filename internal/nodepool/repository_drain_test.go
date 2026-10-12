package nodepool

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// A drain request is stored, read back by every row read, and lifted again,
// without moving the node's admin revision: it is not configuration, so it must
// not invalidate an administrator's pending edit.
func TestRepositoryDrainRequestRoundTrip(t *testing.T) {
	pool := newNodeTestPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("drain-test-%d", time.Now().UnixNano()),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://drain-test-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })
	if node.DrainRequested || node.DrainState != DrainStateNone {
		t.Fatalf("new node: drain_requested=%v drain_state=%q", node.DrainRequested, node.DrainState)
	}
	revision := func() int64 {
		t.Helper()
		var r int64
		if err := pool.QueryRow(ctx, `SELECT admin_revision FROM stream_nodes WHERE id = $1`, node.ID).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	before := revision()

	drained, err := repo.SetDrainRequested(ctx, node.ID, true)
	if err != nil || !drained.DrainRequested {
		t.Fatalf("SetDrainRequested(true) = %+v, %v", drained, err)
	}
	listed, err := repo.ListEnabled(ctx, NodeTypeTranscode)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, n := range listed {
		if n.ID == node.ID {
			found = n.DrainRequested && n.Draining()
		}
	}
	if !found {
		t.Fatal("ListEnabled does not carry the drain request")
	}
	if after := revision(); after != before {
		t.Fatalf("admin_revision moved from %d to %d on a drain request", before, after)
	}

	// A resume also drops the state the node reported for the lifted request,
	// so the node is placeable again before its next health check.
	if err := repo.UpdateHealthReport(ctx, node.ID, node.URL, HealthReport{Healthy: true, DrainState: DrainStateDrained}); err != nil {
		t.Fatal(err)
	}
	resumed, err := repo.SetDrainRequested(ctx, node.ID, false)
	if err != nil || resumed.DrainRequested || resumed.DrainState != DrainStateNone || resumed.Draining() {
		t.Fatalf("SetDrainRequested(false) = %+v, %v", resumed, err)
	}
	if err := repo.UpdateHealthReport(ctx, node.ID, node.URL, HealthReport{Healthy: true, DrainState: DrainStateDraining}); err != nil {
		t.Fatal(err)
	}
	if again, err := repo.SetDrainRequested(ctx, node.ID, true); err != nil || again.DrainState != DrainStateDraining {
		t.Fatalf("a drain request must keep the reported state: %+v, %v", again, err)
	}
	if _, err := repo.SetDrainRequested(ctx, node.ID, false); err != nil {
		t.Fatal(err)
	}
	// A node draining on its own (SIGTERM) keeps its reported state when a
	// request that was never set is lifted: it would announce no change, so
	// nothing would put the state back before the next sweep.
	if err := repo.UpdateHealthReport(ctx, node.ID, node.URL, HealthReport{Healthy: true, DrainState: DrainStateDraining}); err != nil {
		t.Fatal(err)
	}
	if kept, err := repo.SetDrainRequested(ctx, node.ID, false); err != nil || kept.DrainState != DrainStateDraining || !kept.Draining() {
		t.Fatalf("lifting an unset request must keep the reported state: %+v, %v", kept, err)
	}
	if _, err := repo.SetDrainRequested(ctx, -1, true); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("unknown node = %v, want ErrNodeNotFound", err)
	}
}

// The health report writes the node's drain state; the positional UpdateHealth
// leaves it alone.
func TestRepositoryHealthReportWritesDrainState(t *testing.T) {
	pool := newNodeTestPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("drain-health-%d", time.Now().UnixNano()),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://drain-health-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })

	if err := repo.UpdateHealthReport(ctx, node.ID, node.URL, HealthReport{Healthy: true, ActiveJobs: 2, DrainState: DrainStateDraining}); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetByID(ctx, node.ID)
	if err != nil || stored.DrainState != DrainStateDraining || stored.ActiveJobs != 2 {
		t.Fatalf("after a draining report: %+v, %v", stored, err)
	}
	if err := repo.UpdateHealth(ctx, node.ID, node.URL, true, 0, 0, nil, nil); err != nil {
		t.Fatal(err)
	}
	if stored, err = repo.GetByID(ctx, node.ID); err != nil || stored.DrainState != DrainStateDraining {
		t.Fatalf("UpdateHealth changed the drain state: %+v, %v", stored, err)
	}
	// The zero value an in-memory report carries is stored as none.
	if err := repo.UpdateHealthReport(ctx, node.ID, node.URL, HealthReport{Healthy: true}); err != nil {
		t.Fatal(err)
	}
	if stored, err = repo.GetByID(ctx, node.ID); err != nil || stored.DrainState != DrainStateNone {
		t.Fatalf("after a report without a drain state: %+v, %v", stored, err)
	}
}

// Repointing a row at another machine drops the drain state its old worker
// reported, like every other observation of that worker.
func TestRepositoryRepointClearsDrainState(t *testing.T) {
	pool := newNodeTestPool(t)
	ctx := context.Background()
	repo := NewRepository(pool)
	node, err := repo.Create(ctx, CreateNodeInput{
		Name: fmt.Sprintf("drain-repoint-%d", time.Now().UnixNano()),
		Type: NodeTypeTranscode,
		URL:  fmt.Sprintf("http://drain-repoint-%d", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, node.ID) })
	if err := repo.UpdateHealthReport(ctx, node.ID, node.URL, HealthReport{Healthy: true, DrainState: DrainStateDrained}); err != nil {
		t.Fatal(err)
	}
	name := node.Name + "-renamed"
	if renamed, err := repo.Update(ctx, node.ID, UpdateNodeInput{Name: &name}); err != nil || renamed.DrainState != DrainStateDrained {
		t.Fatalf("a rename must keep the drain state: %+v, %v", renamed, err)
	}
	url := node.URL + "-replacement"
	repointed, err := repo.Update(ctx, node.ID, UpdateNodeInput{URL: &url})
	if err != nil || repointed.DrainState != DrainStateNone {
		t.Fatalf("repointed row: %+v, %v", repointed, err)
	}
}
