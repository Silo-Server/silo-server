package streamtelemetry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type publishLeaseTestKey struct{}
type publishLeaseTestHook struct {
	entered, resume chan struct{}
}

func (h publishLeaseTestHook) DialHook(next redis.DialHook) redis.DialHook          { return next }
func (h publishLeaseTestHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }
func (h publishLeaseTestHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		if ctx.Value(publishLeaseTestKey{}) != nil && len(commands) > 0 && commands[0].Name() == "multi" {
			close(h.entered)
			select {
			case <-h.resume:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return next(ctx, commands)
	}
}

func TestRedisStorePublishLeaseReplacementBeforeCommit(t *testing.T) {
	client := redisTestClient(t)
	other := redisTestClient(t)
	cfg := testRedisStoreConfig("publisher")
	cfg.FullResyncEvery = 1
	store := NewRedisStore(client, cfg, nil)
	successor := NewRedisStore(other, cfg, nil)
	lease := "test:telemetry-lease:" + uuid.NewString()
	t.Cleanup(func() {
		_ = client.Del(context.Background(), lease, store.snapshotKey(cfg.PublisherID), store.rosterKey()).Err()
	})
	if err := client.Set(t.Context(), lease, "old-owner", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	first := Snapshot{PublisherID: cfg.PublisherID, Sequence: 1, CapturedAt: time.Now(), Sessions: []SessionView{{SessionID: "first"}}}
	if err := store.PublishWithLease(t.Context(), first, lease, "old-owner"); err != nil {
		t.Fatal(err)
	}
	hook := publishLeaseTestHook{entered: make(chan struct{}), resume: make(chan struct{})}
	client.AddHook(hook)
	release := sync.OnceFunc(func() { close(hook.resume) })
	stale := first
	stale.Sequence = 2
	stale.Sessions = []SessionView{{SessionID: "stale"}}
	done := make(chan error, 1)
	received := false
	go func() {
		done <- store.PublishWithLease(context.WithValue(t.Context(), publishLeaseTestKey{}, true), stale, lease, "old-owner")
	}()
	defer func() {
		release()
		if !received {
			<-done
		}
	}()
	select {
	case <-hook.entered:
	case <-time.After(time.Second):
		t.Fatal("old publisher did not reach transaction commit")
	}
	if err := other.Set(t.Context(), lease, "new-owner", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	next := first
	next.Sequence = 3
	next.Sessions = []SessionView{{SessionID: "successor"}}
	if err := successor.PublishWithLease(t.Context(), next, lease, "new-owner"); err != nil {
		t.Fatal(err)
	}
	release()
	err := <-done
	received = true
	if err == nil {
		t.Error("publisher committed after its lease was replaced")
	}
	loaded, err := successor.Load(t.Context())
	if err != nil || loaded.Sequence != 3 || len(loaded.Sessions) != 1 || loaded.Sessions[0].SessionID != "successor" {
		t.Fatalf("old publisher overwrote its successor: snapshot=%+v error=%v", loaded, err)
	}
	if store.publishCount != 1 || !store.needFullResync || len(store.published) != 0 {
		t.Fatalf("aborted transaction advanced publisher state: count=%d full=%v fields=%d", store.publishCount, store.needFullResync, len(store.published))
	}
}

func TestRedisStorePublishLeaseRejectsMissingOwnerAndCancellation(t *testing.T) {
	client := redisTestClient(t)
	cfg := testRedisStoreConfig("publisher")
	store := NewRedisStore(client, cfg, nil)
	lease := "test:telemetry-lease:" + uuid.NewString()
	t.Cleanup(func() {
		_ = client.Del(context.Background(), lease, store.snapshotKey(cfg.PublisherID), store.rosterKey()).Err()
	})
	snapshot := Snapshot{PublisherID: cfg.PublisherID, Sequence: 1, CapturedAt: time.Now()}
	if err := store.PublishWithLease(t.Context(), snapshot, lease, "missing"); err == nil {
		t.Fatal("publisher without a lease was accepted")
	}
	if err := client.Set(t.Context(), lease, "owner", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.PublishWithLease(ctx, snapshot, lease, "owner"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled publication error = %v", err)
	}
	if count, err := client.Exists(t.Context(), store.snapshotKey(cfg.PublisherID)).Result(); err != nil || count != 0 {
		t.Fatalf("rejected publication wrote a snapshot: count=%d error=%v", count, err)
	}
}
