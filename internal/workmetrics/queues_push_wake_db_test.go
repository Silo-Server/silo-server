package workmetrics

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A profile set wake left in flight by a process that stopped mid-send is not
// queued work: no worker can claim it, so the native push queue leaves it out
// while it still counts a delivery waiting for a retry. Regression: such rows
// counted as queued and their age raised SiloQueueAgeHigh for a day, until
// retention marked them failed.
func TestQueueSamplerLeavesProfileSetWakesOutDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL requires an isolated migrated database")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("queue_push_wake_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+quoted+`;
		CREATE TABLE `+quoted+`.push_delivery_attempts (LIKE public.push_delivery_attempts INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+quoted+` CASCADE`) }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	now := time.Now().UTC().Truncate(time.Microsecond)
	retryCreated := now.Add(-3 * time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO push_delivery_attempts
			(id, notification_delivery_id, push_device_id, trigger_type, provider, platform, outcome, next_retry_at, created_at)
		VALUES
			('delivery-retry', 'delivery-1', 'device-1', 'delivery', 'silo_relay', 'apple', 'retrying', $1, $2),
			('wake-in-flight', NULL, 'device-1', 'profile_set', 'silo_relay', 'apple', 'retrying', NULL, $3)`,
		now.Add(time.Minute), retryCreated, now.Add(-5*time.Hour)); err != nil {
		t.Fatal(err)
	}

	s := &QueueSampler{pool: pool, samples: make(map[string]queueSample)}
	s.sample(ctx)
	got := s.samples["native_push_delivery"]
	queued := got.states[stateQueued]
	if !got.available || queued.count != 1 || queued.oldest == nil || !queued.oldest.Equal(retryCreated) {
		t.Fatalf("native push queue = %+v (queued %+v), want 1 queued, oldest %v: the delivery's retry, not the wake", got, queued, retryCreated)
	}
}
