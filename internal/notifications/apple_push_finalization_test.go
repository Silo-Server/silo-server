package notifications

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestApplePushFinalizationRollbackAndReplay(t *testing.T) {
	repo, pool := newPushDeviceTestRepo(t)
	cmd := orderedAppleCommand()
	cipher := testPushCipher(t)
	ctx := t.Context()
	denied := errors.New("authority changed during lock wait")
	if _, err := repo.ApplyApplePushAndFinalize(ctx, cmd, cipher, func(ApplePushReceipt) error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_devices`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback rows=%d err=%v", count, err)
	}
	var observed ApplePushReceipt
	first, err := repo.ApplyApplePushAndFinalize(ctx, cmd, cipher, func(r ApplePushReceipt) error { observed = r; return nil })
	if err != nil || observed != first || !first.Enabled {
		t.Fatalf("finalize=%+v err=%v", observed, err)
	}
	if err = repo.RecordPushFailure(ctx, first.RegistrationID, "unregistered", true); err != nil {
		t.Fatal(err)
	}
	replay, err := repo.ApplyApplePushAndFinalize(ctx, cmd, cipher, func(r ApplePushReceipt) error { observed = r; return nil })
	if err != nil || observed != replay || observed.Enabled {
		t.Fatal("finalizer lost disabled outcome", err)
	}
	if err = repo.DeleteAllForProfile(ctx, cmd.ProfileID); err != nil {
		t.Fatal(err)
	}
	_, err = repo.ApplyApplePushAndFinalize(ctx, cmd, cipher, func(r ApplePushReceipt) error { observed = r; return nil })
	if err != nil || !observed.Removed {
		t.Fatal("finalizer lost tombstone", err)
	}
}
func TestApplePushFinalizationHoldsInstallationLock(t *testing.T) {
	_, fixture := newPushDeviceTestRepo(t)
	schema := "apple_finalize_" + strings.ToLower(ulid.Make().String())
	quoted := pgx.Identifier{schema}.Sanitize()
	ctx := t.Context()
	if _, err := fixture.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = fixture.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	for _, table := range []string{"push_devices", "apple_push_installations"} {
		if _, err := fixture.Exec(ctx, "CREATE TABLE "+quoted+"."+table+" (LIKE "+table+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	cfg := fixture.Config()
	cfg.MaxConns = 3
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := NewPushDeviceRepository(pool)
	cmd := orderedAppleCommand()
	cipher := testPushCipher(t)
	entered, release := make(chan struct{}), make(chan struct{})
	firstDone, nextDone := make(chan error, 1), make(chan error, 1)
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	go func() {
		_, e := repo.ApplyApplePushAndFinalize(runCtx, cmd, cipher, func(ApplePushReceipt) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-runCtx.Done():
				return runCtx.Err()
			}
		})
		firstDone <- e
	}()
	select {
	case <-entered:
	case <-runCtx.Done():
		t.Fatal("finalizer not reached")
	}
	next := cmd
	next.Generation = 2
	next.APNsToken = strings.Repeat("b", 64)
	go func() { _, e := repo.ApplyApplePush(runCtx, next, cipher); nextDone <- e }()
	for {
		var blocked bool
		if err = fixture.QueryRow(runCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND cardinality(pg_blocking_pids(pid))>0)`, schema).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case e := <-nextDone:
			t.Fatalf("newer writer overtook finalization: %v", e)
		case <-runCtx.Done():
			t.Fatal("newer writer never blocked")
		default:
			runtime.Gosched()
		}
	}
	close(release)
	for _, done := range []chan error{firstDone, nextDone} {
		select {
		case e := <-done:
			if e != nil {
				t.Fatal(e)
			}
		case <-runCtx.Done():
			t.Fatal("writer did not finish")
		}
	}
	var generation int64
	if err = pool.QueryRow(ctx, `SELECT generation FROM apple_push_installations WHERE device_id=$1`, cmd.DeviceID).Scan(&generation); err != nil || generation != 2 {
		t.Fatalf("generation=%d err=%v", generation, err)
	}
	called := false
	if _, err = repo.ApplyApplePushAndFinalize(ctx, cmd, cipher, func(ApplePushReceipt) error { called = true; return nil }); !errors.Is(err, ErrPushGenerationConflict) || called {
		t.Fatal("stale finalizer invoked", err)
	}
}
