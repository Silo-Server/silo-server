package workersource

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func testRedisStore(t *testing.T) (*RedisStore, *RedisStore) {
	t.Helper()
	raw := os.Getenv("SILO_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("SILO_TEST_REDIS_URL not set")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	prefix := "silo:{workersource-test-" + uuid.NewString() + "}:"
	t.Cleanup(func() {
		keys, err := client.Keys(t.Context(), prefix+"*").Result()
		if err == nil && len(keys) > 0 {
			client.Del(t.Context(), keys...)
		}
	})
	return NewRedisStore(client, prefix), NewRedisStore(client, prefix)
}
func testRecord(size int64) Record {
	return Record{Source: workerwire.Source{Grant: uuid.NewString(), NodeID: 7, WorkID: "work", Generation: 3, Size: size}, Path: "/core-only/source", ExpiresAt: time.Now().Add(DefaultTTL)}
}

func TestRedisReplicaReadRenewAndRevocation(t *testing.T) {
	a, b := testRedisStore(t)
	r := testRecord(10)
	l := Limits{MaxGrants: 3, MaxSourceBytes: 30}
	if err := a.Create(t.Context(), r, l); err != nil {
		t.Fatal(err)
	}
	got, err := b.Get(t.Context(), r.Source.Grant)
	if err != nil || got.Source != r.Source || got.Path != r.Path {
		t.Fatalf("replica record = %+v, %v", got, err)
	}
	expires := time.Now().Add(2 * DefaultTTL)
	if err := b.Extend(t.Context(), r.Source.Grant, expires); err != nil {
		t.Fatal(err)
	}
	got, err = a.Get(t.Context(), r.Source.Grant)
	if err != nil || got.ExpiresAt.UnixMilli() != expires.UnixMilli() {
		t.Fatalf("renewed expiry=%v,%v", got.ExpiresAt, err)
	}
	if err := a.RevokeWork(t.Context(), 7, "work", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(t.Context(), r.Source.Grant); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked record = %v", err)
	}
	if err := b.Extend(t.Context(), r.Source.Grant, expires); !errors.Is(err, ErrNotFound) {
		t.Fatalf("renewal revived stopped record=%v", err)
	}
	if err := b.Create(t.Context(), testRecord(10), l); !errors.Is(err, ErrInactive) {
		t.Fatalf("revoked generation recreated=%v", err)
	}
}

func TestRedisQuotasAtomicAcrossReplicas(t *testing.T) {
	a, b := testRedisStore(t)
	l := Limits{MaxGrants: 5, MaxSourceBytes: 20}
	var admitted atomic.Int32
	var group sync.WaitGroup
	for i := range 30 {
		group.Add(1)
		go func() {
			defer group.Done()
			store := a
			if i%2 == 0 {
				store = b
			}
			err := store.Create(t.Context(), testRecord(5), l)
			if err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, ErrQuota) {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if admitted.Load() != 4 {
		t.Fatalf("shared byte quota admitted %d, want 4", admitted.Load())
	}
	if err := b.RevokeWork(t.Context(), 7, "work", 3); err != nil {
		t.Fatal(err)
	}
	r := testRecord(20)
	r.Source.Generation = 4
	if err := a.Create(t.Context(), r, l); err != nil {
		t.Fatalf("revocation did not release quota: %v", err)
	}
	if err := b.Delete(t.Context(), r.Source.Grant); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(t.Context(), r.Source.Grant); err != nil {
		t.Fatal(err)
	}
	r.Source.Grant = uuid.NewString()
	if err := a.Create(t.Context(), r, l); err != nil {
		t.Fatalf("idempotent delete corrupted quota: %v", err)
	}
}

func TestRedisExpiryReleasesQuotaAndCannotRenew(t *testing.T) {
	a, b := testRedisStore(t)
	r := testRecord(10)
	r.ExpiresAt = time.Now().Add(-time.Second)
	l := Limits{MaxGrants: 1, MaxSourceBytes: 10}
	if err := a.Create(t.Context(), r, l); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired creation=%v", err)
	}
	r.ExpiresAt = time.Now().Add(DefaultTTL)
	if err := a.Create(t.Context(), r, l); err != nil {
		t.Fatal(err)
	}
	// Renew must not act as an insert when a concurrent replica deletes the grant.
	if err := b.Delete(t.Context(), r.Source.Grant); err != nil {
		t.Fatal(err)
	}
	if err := a.Extend(t.Context(), r.Source.Grant, time.Now().Add(DefaultTTL)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted grant renewed=%v", err)
	}
	next := testRecord(10)
	if err := a.Create(t.Context(), next, l); err != nil {
		t.Fatalf("released slot missing=%v", err)
	}
}

func TestRedisNilClientFailsClosed(t *testing.T) {
	s := NewRedisStore(nil, "")
	if _, err := s.Get(t.Context(), "missing"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing backend=%v", err)
	}
	if err := s.Create(t.Context(), testRecord(1), Limits{MaxGrants: 1, MaxSourceBytes: 1}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing backend admitted=%v", err)
	}
}
