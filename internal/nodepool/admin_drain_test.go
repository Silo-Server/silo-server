package nodepool

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAdminNodeDrainTransactions(t *testing.T) {
	pool := configurationTestPool(t)
	store := NewAdminConfigurationStore(pool)
	ctx := t.Context()
	node, err := store.Create(ctx, CreateNodeInput{Name: "drain", Type: NodeTypeProxy, URL: "http://drain.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	refused := errors.New("stale validator")
	guard := func(want int64) func(int64) error {
		return func(got int64) error {
			if got != want {
				return refused
			}
			return nil
		}
	}
	if _, err = store.BeginDrain(ctx, node.ID, guard(node.AdminRevision+1)); !errors.Is(err, refused) {
		t.Fatalf("stale fence write: %v", err)
	}
	before, err := store.ReadDrain(ctx, node.ID)
	if err != nil || before.FenceID != "" || !before.Node.Enabled || before.Node.AdminRevision != node.AdminRevision {
		t.Fatalf("guard rollback: %+v %v", before, err)
	}
	drained, err := store.BeginDrain(ctx, node.ID, guard(node.AdminRevision))
	if err != nil || drained.FenceID == "" || drained.Node.Enabled || drained.Node.AdminRevision <= node.AdminRevision {
		t.Fatalf("durable fence: %+v %v", drained, err)
	}
	// A lost HTTP reply is reconciled by a durable read, never by host stats.
	observed, err := store.ReadDrain(ctx, node.ID)
	if err != nil || observed.FenceID != drained.FenceID || observed.Node.AdminRevision != drained.Node.AdminRevision {
		t.Fatalf("response-loss reconciliation: %+v %v", observed, err)
	}
	if _, _, err = store.Update(ctx, node.ID, UpdateNodeInput{Enabled: new(true)}, guard(drained.Node.AdminRevision)); err == nil {
		t.Fatal("reenabled retiring worker")
	}
	for _, input := range []UpdateNodeInput{{URL: new("http://replacement.invalid")}, {Name: new("replacement")}} {
		if _, _, err = store.Update(ctx, node.ID, input, guard(drained.Node.AdminRevision)); err == nil {
			t.Fatal("repointed retiring worker")
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE stream_nodes SET type='transcode' WHERE id=$1`, node.ID); err == nil {
		t.Fatal("direct writer changed retiring worker identity")
	}
	unchanged, err := store.ReadDrain(ctx, node.ID)
	if err != nil || unchanged.Node.URL != node.URL || unchanged.Node.Name != node.Name || unchanged.Node.Type != node.Type || unchanged.Node.AdminRevision != drained.Node.AdminRevision {
		t.Fatalf("rejected repoint changed authority: %+v %v", unchanged, err)
	}
	if _, err = store.CancelDrain(ctx, node.ID, guard(node.AdminRevision)); !errors.Is(err, refused) {
		t.Fatalf("stale cancellation: %v", err)
	}
	canceled, err := store.CancelDrain(ctx, node.ID, guard(drained.Node.AdminRevision))
	if err != nil || canceled.Enabled || canceled.AdminRevision <= drained.Node.AdminRevision {
		t.Fatalf("explicit cancellation: %+v %v", canceled, err)
	}
	if _, err = store.BeginDrain(ctx, node.ID, guard(drained.Node.AdminRevision)); !errors.Is(err, refused) {
		t.Fatalf("old drained receipt survived cancellation: %v", err)
	}
	node, _, err = store.Update(ctx, node.ID, UpdateNodeInput{Enabled: new(true)}, guard(canceled.AdminRevision))
	if err != nil || !node.Enabled {
		t.Fatalf("reenable after cancellation: %+v %v", node, err)
	}
	newDrain, err := store.BeginDrain(ctx, node.ID, guard(node.AdminRevision))
	if err != nil || newDrain.FenceID == drained.FenceID {
		t.Fatalf("new retirement identity: %+v %v", newDrain, err)
	}
	if err = store.Delete(ctx, node.ID, guard(newDrain.Node.AdminRevision)); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM stream_node_drain_fences WHERE node_id=$1`, node.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("fence cleanup: %d %v", remaining, err)
	}
}

func TestAdminNodeDrainPublicationFaultRollsBack(t *testing.T) {
	pool := configurationTestPool(t)
	store := NewAdminConfigurationStore(pool)
	ctx := t.Context()
	node, err := store.Create(ctx, CreateNodeInput{Name: "publication-fault", Type: NodeTypeTranscode, URL: "http://fault.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_drain_revision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 IF NEW.drain_revision<>OLD.drain_revision THEN RAISE EXCEPTION 'synthetic post-fence publication fault'; END IF;
	 RETURN NEW; END $$;
	 CREATE TRIGGER fail_drain_revision BEFORE UPDATE ON stream_nodes FOR EACH ROW EXECUTE FUNCTION fail_drain_revision()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginDrain(ctx, node.ID, func(int64) error { return nil }); err == nil {
		t.Fatal("publication fault disappeared")
	}
	current, err := store.ReadDrain(ctx, node.ID)
	if err != nil || current.FenceID != "" || !current.Node.Enabled || current.Node.AdminRevision != node.AdminRevision {
		t.Fatalf("partial mutation survived rollback: %+v %v", current, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_drain_revision ON stream_nodes; DROP FUNCTION fail_drain_revision()`); err != nil {
		t.Fatal(err)
	}
	green, err := store.BeginDrain(ctx, node.ID, func(int64) error { return nil })
	if err != nil || green.FenceID == "" || green.Node.Enabled {
		t.Fatalf("publication recovery: %+v %v", green, err)
	}
}

func TestAdminNodeDrainConcurrentMigrationRollbackPreservesFence(t *testing.T) {
	pool := configurationTestPool(t)
	store := NewAdminConfigurationStore(pool)
	ctx := t.Context()
	node, err := store.Create(ctx, CreateNodeInput{Name: "rollback-race", Type: NodeTypeProxy, URL: "http://rollback.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	drainResult := make(chan error, 1)
	go func() {
		_, err := store.BeginDrain(ctx, node.ID, func(int64) error {
			close(locked)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		drainResult <- err
	}()
	<-locked
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	migration, err := os.ReadFile("../../migrations/sql/20261008031132_add_worker_drain_fences.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := strings.Split(string(migration), "-- +goose Down")[1]
	rollbackResult := make(chan error, 1)
	go func() {
		tx, err := pool.Begin(ctx)
		if err == nil {
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			_, err = tx.Exec(ctx, down)
		}
		rollbackResult <- err
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	waiting := false
	for !waiting {
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='stream_nodes'::regclass AND mode='AccessExclusiveLock' AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("rollback did not wait on the production node mutation")
		}
	}
	unblock()
	if err = <-drainResult; err != nil {
		t.Fatal(err)
	}
	if err = <-rollbackResult; err == nil || !strings.Contains(err.Error(), "cancel worker drains") {
		t.Fatalf("rollback erased concurrent fence: %v", err)
	}
	current, err := store.ReadDrain(ctx, node.ID)
	if err != nil || current.FenceID == "" || current.Node.Enabled {
		t.Fatalf("rollback lost durable retirement: %+v %v", current, err)
	}
}

func TestAdminNodeDrainMigrationRollbackPreservesConfigurationRevision(t *testing.T) {
	pool := configurationTestPool(t)
	ctx := t.Context()
	store := NewAdminConfigurationStore(pool)
	node, err := store.Create(ctx, CreateNodeInput{Name: "rollback-validator", Type: NodeTypeProxy, URL: "http://rollback-validator.invalid", Enabled: new(false)})
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := store.BeginDrain(ctx, node.ID, func(int64) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := store.CancelDrain(ctx, node.ID, func(int64) error { return nil })
	if err != nil || canceled.AdminRevision <= fenced.Node.AdminRevision {
		t.Fatalf("cancellation did not advance effective revision: %+v %v", canceled, err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20261008031132_add_worker_drain_fences.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := strings.Split(string(migration), "-- +goose Down")[1]
	var originalStoredRevision int64
	if err := pool.QueryRow(ctx, `SELECT admin_revision FROM stream_nodes WHERE id=$1`, node.ID).Scan(&originalStoredRevision); err != nil {
		t.Fatal(err)
	}
	// Inject after revision promotion and before re-enabling the original
	// trigger. Goose's transaction must restore both the schema and pre-image.
	faultTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fault := strings.Replace(down, "ALTER TABLE stream_nodes ENABLE TRIGGER stream_node_configuration_revision;", "SELECT 1/0;\nALTER TABLE stream_nodes ENABLE TRIGGER stream_node_configuration_revision;", 1)
	if _, err := faultTx.Exec(ctx, fault); err == nil {
		_ = faultTx.Rollback(context.WithoutCancel(ctx))
		t.Fatal("post-promotion rollback fault did not fire")
	}
	if err := faultTx.Rollback(context.WithoutCancel(ctx)); err != nil {
		t.Fatal(err)
	}
	var restoredRevision int64
	var enabledTrigger bool
	if err := pool.QueryRow(ctx, `SELECT n.admin_revision, t.tgenabled='O' FROM stream_nodes n JOIN pg_trigger t ON t.tgrelid='stream_nodes'::regclass AND t.tgname='stream_node_configuration_revision' WHERE n.id=$1`, node.ID).Scan(&restoredRevision, &enabledTrigger); err != nil || restoredRevision != originalStoredRevision || !enabledTrigger {
		t.Fatalf("fault rollback lost revision/trigger state: %d/%d enabled=%t %v", restoredRevision, originalStoredRevision, enabledTrigger, err)
	}
	current, err := store.ReadDrain(ctx, node.ID)
	if err != nil || current.Node.AdminRevision != canceled.AdminRevision || current.FenceID != "" {
		t.Fatalf("fault rollback lost effective configuration: %+v %v", current, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT admin_revision FROM stream_nodes WHERE id=$1`, node.ID).Scan(&revision); err != nil || revision != canceled.AdminRevision || revision <= node.AdminRevision {
		t.Fatalf("rollback resurrected original validator: revision=%d want=%d original=%d %v", revision, canceled.AdminRevision, node.AdminRevision, err)
	}
	// The bridge repository and original configuration trigger still operate
	// on the rolled-back schema, and the next ordinary write remains newer.
	if _, err := NewRepository(pool).Update(ctx, node.ID, UpdateNodeInput{Enabled: new(true)}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT admin_revision FROM stream_nodes WHERE id=$1`, node.ID).Scan(&revision); err != nil || revision <= canceled.AdminRevision {
		t.Fatalf("configuration trigger not restored after rollback: %d %v", revision, err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname='stream_node_drain_fences'`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("rollback fence table cleanup: %d %v", remaining, err)
	}
}
