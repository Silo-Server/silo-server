package nodeconfig

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

// An administrator's drain request is read from the node's own row once the
// row is matched, and the node hears about each change once.
func TestRefreshNodeDrainReadsTheRow(t *testing.T) {
	requested := false
	var readErr error
	reads := 0
	w := NewWatcher(nil, nil, nil, BootstrapOverrides{NodeURL: "http://node-1"})
	w.loadDrain = func(_ context.Context, rowID int) (bool, error) {
		reads++
		if rowID != 7 {
			t.Fatalf("read row %d, want the matched row 7", rowID)
		}
		return requested, readErr
	}
	var changes []bool
	w.OnNodeDrainChange(func(r bool) { changes = append(changes, r) })
	ctx := context.Background()

	w.refreshNodeDrain(ctx)
	if reads != 0 {
		t.Fatal("read a drain request before the row was matched")
	}
	w.rememberNodeRegistration(7, "http://node-1")

	w.refreshNodeDrain(ctx)
	if w.NodeDrainRequested() || len(changes) != 0 {
		t.Fatalf("undrained row: requested=%v changes=%v", w.NodeDrainRequested(), changes)
	}
	requested = true
	w.refreshNodeDrain(ctx)
	w.refreshNodeDrain(ctx)
	if !w.NodeDrainRequested() || len(changes) != 1 || !changes[0] {
		t.Fatalf("drained row: requested=%v changes=%v, want one change to true", w.NodeDrainRequested(), changes)
	}

	// A failed read, such as a node started before the API migrated the
	// column, is not an operator lifting the drain.
	readErr = errors.New("column drain_requested does not exist")
	requested = false
	w.refreshNodeDrain(ctx)
	if !w.NodeDrainRequested() || len(changes) != 1 {
		t.Fatalf("after a failed read: requested=%v changes=%v, want the request kept", w.NodeDrainRequested(), changes)
	}

	readErr = nil
	w.refreshNodeDrain(ctx)
	if w.NodeDrainRequested() || len(changes) != 2 || changes[1] {
		t.Fatalf("resumed row: requested=%v changes=%v", w.NodeDrainRequested(), changes)
	}
}

// A node pool change makes a node reread its drain request, which is how a
// drain arrives promptly; it does not reload the config, and an API host,
// which has no row, ignores it.
func TestNodePoolChangeRefreshesOnlyANodesDrain(t *testing.T) {
	node := NewWatcher(nil, nil, nil, BootstrapOverrides{NodeURL: "http://node-1"})
	apiHost := NewWatcher(nil, nil, nil, BootstrapOverrides{})
	poolChanged := cache.Event{Type: cache.EventNodePoolChanged}
	if !node.refreshesDrainOn(poolChanged) {
		t.Fatal("a node must reread its drain request when the pool changes")
	}
	if apiHost.refreshesDrainOn(poolChanged) {
		t.Fatal("an API host has no drain request to read")
	}
	if node.refreshesDrainOn(cache.Event{Type: cache.EventSettingsChanged}) {
		t.Fatal("a settings change reloads the config, which reads the drain request anyway")
	}
}

func TestQueryNodeDrainRequested(t *testing.T) {
	pool := newOverrideTestPool(t)
	ctx := context.Background()
	var columns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'stream_nodes' AND column_name = 'drain_requested'`).Scan(&columns); err != nil || columns < 1 {
		t.Skip("test database has not applied the stream_nodes drain migration")
	}
	id := insertOverrideNode(t, pool, fmt.Sprintf("http://drain-node-%d:8082", time.Now().UnixNano()), nil)

	w := &Watcher{pool: pool}
	requested, err := w.queryNodeDrainRequested(ctx, id)
	if err != nil || requested {
		t.Fatalf("undrained row: %v, %v", requested, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE stream_nodes SET drain_requested = true WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if requested, err = w.queryNodeDrainRequested(ctx, id); err != nil || !requested {
		t.Fatalf("drained row: %v, %v", requested, err)
	}
	if _, err := w.queryNodeDrainRequested(ctx, -1); err == nil {
		t.Fatal("a missing row read as a drain request")
	}
}
