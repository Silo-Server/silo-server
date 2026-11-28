//go:build redislab

package directconn

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/redis/go-redis/v9"
)

// These tests run against a disposable Redis through scripts/direct-redis-lab.sh.
// They flush their database, so they refuse any database index but labDB.
const labDB = 15

func labClient(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := os.Getenv("SILO_DIRECT_TEST_REDIS_URL")
	if rawURL == "" {
		t.Skip("set SILO_DIRECT_TEST_REDIS_URL to a disposable Redis database (index 15)")
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatal("invalid SILO_DIRECT_TEST_REDIS_URL")
	}
	if opts.DB != labDB {
		t.Fatalf("SILO_DIRECT_TEST_REDIS_URL must use disposable database index %d", labDB)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.FlushDB(t.Context()).Err(); err != nil {
		t.Fatalf("flush disposable database: %v", err)
	}
	return client
}

// labStore returns a store on the flushed lab database, with the engine lease
// set to the store's fence so its host and confirmation writes go through.
func labStore(t *testing.T) (*redis.Client, CoordinationStore) {
	t.Helper()
	client := labClient(t)
	seedLease(t, client, labFence)
	return client, newRedisStore(client, labLabels, labFence)
}

// The conditional operations behave on a real Redis as on miniredis.
func TestRedisLabConditionalStoreConformance(t *testing.T) {
	runConditionalStoreConformance(t, func(t *testing.T) CoordinationStore {
		_, store := labStore(t)
		return store
	}, redisConditionalHooks)
}

func TestRedisEnrollmentLimitAndConflict(t *testing.T) {
	const limit = 32
	// enrollFullFor fills the account with leases of lifetime from labNow.
	enrollFullFor := func(t *testing.T, lifetime time.Duration) (server.Store, []server.Device) {
		t.Helper()
		_, store := labStore(t)
		devices := make([]server.Device, limit)
		for i := range devices {
			devices[i] = labDevice("1", fmt.Sprintf("%02d", i), byte(i+1))
			devices[i].LeaseExpires = labNow.Add(lifetime)
			if _, err := store.PutDevice(t.Context(), devices[i], limit, labNow); err != nil {
				t.Fatalf("enroll %d: %v", i, err)
			}
		}
		return store, devices
	}
	enrollFull := func(t *testing.T) (server.Store, []server.Device) {
		t.Helper()
		return enrollFullFor(t, time.Minute)
	}
	put := func(t *testing.T, store server.Store, d server.Device, at time.Time) error {
		t.Helper()
		_, err := store.PutDevice(t.Context(), d, limit, at)
		return err
	}
	t.Run("33rd live enrollment is refused", func(t *testing.T) {
		store, _ := enrollFull(t)
		if err := put(t, store, labDevice("1", "33rd", 100), labNow); !errors.Is(err, server.ErrDeviceLimit) {
			t.Fatalf("33rd live enrollment: %v", err)
		}
	})
	t.Run("a lapsed lease frees its slot", func(t *testing.T) {
		// Redis time decides expiry, so the leases lapse in real time. This is
		// the one check of TIME inside the store's scripts on a real Redis.
		store, _ := enrollFullFor(t, 2*time.Second)
		deadline := time.Now().Add(10 * time.Second)
		for {
			listed, err := store.ListDevices(t.Context(), "1", labNow)
			if err != nil {
				t.Fatal(err)
			}
			if len(listed) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d leases of 2 s still live after 10 s", len(listed))
			}
			time.Sleep(50 * time.Millisecond) //nolint:forbidigo // Real Redis time has no event to wait on.
		}
		candidate := labDevice("1", "after-lapse", 101)
		if err := put(t, store, candidate, labNow); err != nil {
			t.Fatalf("enroll after the leases lapsed: %v", err)
		}
		if _, err := store.Device(t.Context(), candidate.Ref, labNow); err != nil {
			t.Fatalf("enrollment after the leases lapsed is not live: %v", err)
		}
	})
	t.Run("a node key held by another account conflicts", func(t *testing.T) {
		store, devices := enrollFull(t)
		if err := put(t, store, labDevice("2", "other", devices[1].NodeKey[0]), labNow); !errors.Is(err, server.ErrNodeKeyConflict) {
			t.Fatalf("node key of another account: %v", err)
		}
	})
	t.Run("a withdrawal keeps the siblings and the cap", func(t *testing.T) {
		store, devices := enrollFull(t)
		if err := store.DeleteDevice(t.Context(), devices[0].Ref); err != nil {
			t.Fatal(err)
		}
		for _, d := range devices[1:] {
			if _, err := store.Device(t.Context(), d.Ref, labNow); err != nil {
				t.Fatalf("sibling %s lost: %v", d.Ref.Device, err)
			}
		}
		listed, err := store.ListDevices(t.Context(), "1", labNow)
		if err != nil || len(listed) != limit-1 {
			t.Fatalf("ListDevices = %d devices, %v; want %d", len(listed), err, limit-1)
		}
		if err := put(t, store, labDevice("1", "first", 102), labNow); err != nil {
			t.Fatalf("first new enrollment after the withdrawal: %v", err)
		}
		if err := put(t, store, labDevice("1", "second", 103), labNow); !errors.Is(err, server.ErrDeviceLimit) {
			t.Fatalf("second new enrollment after the withdrawal: %v", err)
		}
	})
}

// TestRedisFlushForcesReenrollment proves a flush ends every enrollment: a
// valid ticket stops authorizing, and no principal can pass as that device
// until it enrolls again.
func TestRedisFlushForcesReenrollment(t *testing.T) {
	client, store := labStore(t)
	ctx := t.Context()
	c, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: func() time.Time { return labNow }})
	if err != nil {
		t.Fatal(err)
	}
	d := labDevice("1", "flushed", 1)
	enrollment, err := c.Enroll(ctx, EnrollRequest{UserID: 1, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: "flushed", NodeKey: d.NodeKey, ConnectionPath: "automatic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Authorize(ctx, enrollment.Ticket); err != nil {
		t.Fatalf("ticket before the flush: %v", err)
	}
	if err := store.PutRegistration(ctx, d.Ref, labRegistration(d), labNow.Add(time.Minute), labNow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Device(ctx, d.Ref, labNow); err != nil {
		t.Fatalf("enrolled device: %v", err)
	}
	if err := client.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Authorize(ctx, enrollment.Ticket); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("ticket after the flush: %v, want ErrInvalidTicket", err)
	}
	if _, err := store.Device(ctx, d.Ref, labNow); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("Device after flush: %v", err)
	}
	if _, err := store.DeviceByNodeKey(ctx, d.NodeKey, labNow); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("DeviceByNodeKey after flush: %v", err)
	}
	if listed, err := store.ListDevices(ctx, "1", labNow); err != nil || len(listed) != 0 {
		t.Fatalf("ListDevices after flush = %d, %v", len(listed), err)
	}
	if err := store.PutRegistration(ctx, d.Ref, labRegistration(d), labNow.Add(time.Minute), labNow); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("PutRegistration after flush: %v", err)
	}
	if _, err := store.PutDevice(ctx, d, 32, labNow); err != nil {
		t.Fatalf("re-enrollment after flush: %v", err)
	}
}
