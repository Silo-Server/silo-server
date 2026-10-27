package handlers

import (
	"context"
	"errors"
	"math"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type fakeRateLimitStore struct {
	values       map[string]string
	setCalls     int
	setManyCalls int
	atomicCalls  int
}

func newFakeRateLimitStore() *fakeRateLimitStore {
	return &fakeRateLimitStore{values: make(map[string]string)}
}

func (s *fakeRateLimitStore) Get(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *fakeRateLimitStore) Set(_ context.Context, key, value string) error {
	s.setCalls++
	s.values[key] = value
	return nil
}

func (s *fakeRateLimitStore) SetMany(_ context.Context, values map[string]string) error {
	s.setManyCalls++
	for key, value := range values {
		s.values[key] = value
	}
	return nil
}

func (s *fakeRateLimitStore) GetAll(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out, nil
}

func (s *fakeRateLimitStore) UpdateAtomic(
	ctx context.Context,
	update func(current map[string]string) (map[string]string, error),
) error {
	s.atomicCalls++
	current, err := s.GetAll(ctx)
	if err != nil {
		return err
	}
	writes, err := update(current)
	if err != nil || len(writes) == 0 {
		return err
	}
	return s.SetMany(ctx, writes)
}

type postCommitOverwriteRateLimitStore struct {
	*fakeRateLimitStore
}

func (s *postCommitOverwriteRateLimitStore) UpdateAtomic(
	ctx context.Context,
	update func(current map[string]string) (map[string]string, error),
) error {
	s.atomicCalls++
	current, err := s.GetAll(ctx)
	if err != nil {
		return err
	}
	writes, err := update(current)
	if err != nil || len(writes) == 0 {
		return err
	}
	if err := s.SetMany(ctx, writes); err != nil {
		return err
	}

	// Model a later request committing before this handler performs its
	// post-commit reload and restart decision.
	s.values["ratelimit.enabled"] = "false"
	return nil
}

func newRunningRateLimitMiddleware(t *testing.T, store ratelimit.SettingsStore) *ratelimit.Middleware {
	t.Helper()
	perKey := ratelimit.NewMemoryLimiter()
	global := ratelimit.NewMemoryLimiter()
	t.Cleanup(func() {
		perKey.Close()
		global.Close()
	})
	mw := ratelimit.NewMiddleware(perKey, global, store, true)
	if err := mw.Init(context.Background()); err != nil {
		t.Fatalf("init middleware: %v", err)
	}
	return mw
}

func updateRateLimits(t *testing.T, h *RateLimitHandler, req AdminRateLimitUpdate) AdminRateLimitUpdateResult {
	t.Helper()
	result, err := h.UpdateAdminRateLimitConfig(t.Context(), req, nil)
	if err != nil {
		t.Fatalf("update %+v: %v", req, err)
	}
	return result
}

// TestRateLimitUpdateRestartDecision: the limiter is built at startup, so
// enabling it while none runs, or switching backend while one runs, needs a
// restart; toggling a running limiter hot-reloads. The config must stay
// editable with no limiter at all so an admin can re-enable it.
func TestRateLimitUpdateRestartDecision(t *testing.T) {
	t.Run("no running limiter", func(t *testing.T) {
		store := newFakeRateLimitStore()
		store.values["ratelimit.enabled"] = "false"
		tracker := NewServerRestartStatusTracker()
		h := NewRateLimitHandler(store, nil, nil, tracker, true)

		view, err := h.ReadAdminRateLimitConfig(t.Context())
		if err != nil || view.Enabled || view.Active {
			t.Fatalf("read = %+v (%v), want disabled and inactive", view, err)
		}
		if got := updateRateLimits(t, h, AdminRateLimitUpdate{Enabled: new(true), Backend: "redis"}); !got.RestartRequired {
			t.Error("enabling without a running limiter: restart_required = false, want true")
		}
		if store.values["ratelimit.enabled"] != "true" || store.values["ratelimit.backend"] != "redis" || store.setManyCalls != 1 {
			t.Fatalf("stored %v in %d writes, want enabled redis in one", store.values, store.setManyCalls)
		}
		if !tracker.Snapshot().RestartRequired {
			t.Fatal("enabling a stopped limiter did not update server restart status")
		}
		if got := updateRateLimits(t, h, AdminRateLimitUpdate{Enabled: new(false), Backend: "redis"}); got.RestartRequired {
			t.Error("saving a disabled config without a running limiter: restart_required = true, want false")
		}
	})

	t.Run("running limiter", func(t *testing.T) {
		store := newFakeRateLimitStore()
		h := NewRateLimitHandler(store, newRunningRateLimitMiddleware(t, store), nil, NewServerRestartStatusTracker(), true)

		view, err := h.ReadAdminRateLimitConfig(t.Context())
		if err != nil || !view.Active || view.ActiveBackend != "memory" {
			t.Fatalf("read = %+v (%v), want active memory limiter", view, err)
		}
		if got := updateRateLimits(t, h, AdminRateLimitUpdate{Enabled: new(false), Backend: "memory"}); got.RestartRequired {
			t.Error("toggle on a running limiter: restart_required = true, want false")
		}
		if got := updateRateLimits(t, h, AdminRateLimitUpdate{Enabled: new(true), Backend: "redis"}); !got.RestartRequired {
			t.Error("backend change: restart_required = false, want true")
		}
	})

	t.Run("decides from the latest committed settings", func(t *testing.T) {
		base := newFakeRateLimitStore()
		base.values["ratelimit.enabled"] = "false"
		store := &postCommitOverwriteRateLimitStore{fakeRateLimitStore: base}
		h := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())

		if got := updateRateLimits(t, h, AdminRateLimitUpdate{Enabled: new(true)}); got.RestartRequired {
			t.Fatal("restart_required = true, want false from the later committed disabled state")
		}
	})
}

// TestRateLimitConfigReportsRedisAvailability: the read answers with the rule
// the save path enforces, so the UI can disable the Redis option up front.
func TestRateLimitConfigReportsRedisAvailability(t *testing.T) {
	for _, tc := range []struct {
		name           string
		redisURL       string
		bootstrapAvail bool
		want           bool
	}{
		{name: "nothing configured"},
		{name: "malformed persisted url", redisURL: "not-a-url"},
		{name: "canonical persisted url", redisURL: "redis://cache.example.invalid:6379", want: true},
		{name: "bootstrap redis despite stale persisted url", redisURL: " redis://cache.example.invalid:6379 ", bootstrapAvail: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeRateLimitStore()
			if tc.redisURL != "" {
				store.values["redis.url"] = tc.redisURL
			}
			h := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker(), tc.bootstrapAvail)
			view, err := h.ReadAdminRateLimitConfig(t.Context())
			if err != nil || view.RedisAvailable != tc.want {
				t.Errorf("redis_available = %v (%v), want %v", view.RedisAvailable, err, tc.want)
			}
		})
	}
}

func TestRateLimitUpdateRejectsInvalidConfigWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  AdminRateLimitUpdate
		// activeRedis models a process currently using Redis with no stored or
		// bootstrap transport: that state cannot survive the next restart, so
		// it does not make Redis selectable.
		activeRedis bool
	}{
		{name: "explicit zero", req: AdminRateLimitUpdate{Tiers: map[string]tierConfigRequest{"standard": {RequestsPerSecond: new(0.0)}}}},
		{name: "overflowing global rate", req: AdminRateLimitUpdate{GlobalReqPerSecond: new(1e308)}},
		{name: "overflowing burst", req: AdminRateLimitUpdate{IPBurst: new(math.MaxInt)}},
		{name: "redis without configuration", req: AdminRateLimitUpdate{Backend: "redis"}},
		{name: "redis only because it is active", req: AdminRateLimitUpdate{Backend: "redis"}, activeRedis: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeRateLimitStore()
			var mw *ratelimit.Middleware
			if tc.activeRedis {
				perKey, global := ratelimit.NewMemoryLimiter(), ratelimit.NewMemoryLimiter()
				t.Cleanup(func() {
					perKey.Close()
					global.Close()
				})
				mw = ratelimit.NewMiddleware(perKey, global, store, false)
			}
			h := NewRateLimitHandler(store, mw, nil, NewServerRestartStatusTracker())

			_, err := h.UpdateAdminRateLimitConfig(t.Context(), tc.req, nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
				t.Fatalf("err = %v, want a 400 decision", err)
			}
			if store.setManyCalls != 0 || store.setCalls != 0 {
				t.Fatalf("invalid update wrote settings: SetMany=%d Set=%d", store.setManyCalls, store.setCalls)
			}
		})
	}
}
