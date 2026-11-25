package handlers

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

// insightsTestEventBus dispatches published events to handlers subscribed to
// the matching channel, standing in for Redis pub/sub in unit tests.
type insightsTestEventBus struct {
	mu       sync.Mutex
	handlers map[string][]cache.EventHandler
}

func (b *insightsTestEventBus) Publish(_ context.Context, channel string, event cache.Event) error {
	b.mu.Lock()
	handlers := slices.Clone(b.handlers[channel])
	b.mu.Unlock()
	for _, handler := range handlers {
		handler(event)
	}
	return nil
}

func (b *insightsTestEventBus) Subscribe(_ context.Context, channel string, handler cache.EventHandler) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.handlers == nil {
		b.handlers = make(map[string][]cache.EventHandler)
	}
	b.handlers[channel] = append(b.handlers[channel], handler)
	return nil
}

func (b *insightsTestEventBus) Close() error { return nil }

func TestAdminInsightsProviderEventBusInvalidation(t *testing.T) {
	relevant := []struct {
		name    string
		channel string
		event   cache.Event
	}{
		{"playback_sessions_changed", cache.ChannelPlayback, cache.Event{Type: cache.EventPlaybackSessionsChanged, Payload: "node-1"}},
		{"admin_stats_invalidated", cache.ChannelAdmin, cache.Event{Type: cache.EventAdminStatsInvalidated, Payload: "42"}},
		{"user_disabled", cache.ChannelAdmin, cache.Event{Type: cache.EventUserDisabled, Payload: "42"}},
		{"user_deleted", cache.ChannelAdmin, cache.Event{Type: cache.EventUserDeleted, Payload: "42"}},
		{"node_pool_changed", cache.ChannelAdmin, cache.Event{Type: cache.EventNodePoolChanged}},
		{"item_updated", cache.ChannelAdmin, cache.Event{Type: cache.EventItemUpdated, Payload: "movie-1"}},
		{"user_sessions_revoked", cache.ChannelAdmin, cache.Event{Type: cache.EventUserSessionsRevoked, Payload: "42"}},
	}
	unrelated := []struct {
		name    string
		channel string
		event   cache.Event
	}{
		{"watch_together_room_state", cache.ChannelPlayback, cache.Event{Type: "watch_together_room_state", Payload: `{}`}},
		{"watch_together_suggestions", cache.ChannelPlayback, cache.Event{Type: "watch_together_suggestions", Payload: `{}`}},
		{"subtitle_ready", cache.ChannelPlayback, cache.Event{Type: cache.EventSubtitleReady, Payload: `{}`}},
		{"subtitle_timing_changed", cache.ChannelPlayback, cache.Event{Type: cache.EventSubtitleTimingChanged, Payload: `{}`}},
		{"subtitle_sync_updated", cache.ChannelPlayback, cache.Event{Type: cache.EventSubtitleSyncUpdated, Payload: `{}`}},
		{"markers_updated", cache.ChannelPlayback, cache.Event{Type: cache.EventMarkersUpdated, Payload: `{}`}},
		{"marker_thumbnail_ready", cache.ChannelPlayback, cache.Event{Type: cache.EventMarkerThumbnailReady, Payload: `{}`}},
		{"settings_changed", cache.ChannelAdmin, cache.Event{Type: cache.EventSettingsChanged, Payload: "some.setting"}},
		{"node_unreachable", cache.ChannelAdmin, cache.Event{Type: cache.EventNodeUnreachable, Payload: `{}`}},
		{"node_drain_changed", cache.ChannelAdmin, cache.Event{Type: cache.EventNodeDrainChanged, Payload: "1"}},
		{"policy_changed", cache.ChannelAdmin, cache.Event{Type: cache.EventPolicyChanged}},
		{"plugins_changed", cache.ChannelAdmin, cache.Event{Type: cache.EventPluginsChanged}},
		{"plugin_tasks_changed", cache.ChannelAdmin, cache.Event{Type: cache.EventPluginTasksChanged}},
		{"auth_providers_changed", cache.ChannelAdmin, cache.Event{Type: cache.EventAuthProvidersChanged}},
		{"marker_provider_config_changed", cache.ChannelAdmin, cache.Event{Type: cache.EventMarkerProviderConfigChanged}},
		{"wrong_channel_playback_on_admin", cache.ChannelAdmin, cache.Event{Type: cache.EventPlaybackSessionsChanged}},
		{"wrong_channel_admin_on_playback", cache.ChannelPlayback, cache.Event{Type: cache.EventAdminStatsInvalidated}},
	}
	// A playback event marks entries stale and keeps serving them; an admin
	// event drops them; anything else leaves them fresh.
	check := func(t *testing.T, channel string, event cache.Event, wantStale, wantDropped bool) {
		bus := &insightsTestEventBus{}
		p, err := NewAdminInsightsProvider(t.Context(), nil, bus)
		if err != nil {
			t.Fatalf("NewAdminInsightsProvider: %v", err)
		}
		defer p.Close()
		p.mu.Lock()
		p.publishLocked("k", p.gen, p.dropGen, time.Now(), nil, insightsFill[int]{1})
		before := p.gen
		p.mu.Unlock()
		if err := bus.Publish(t.Context(), channel, event); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		p.mu.Lock()
		stale, dropped := p.gen != before, p.entries["k"] == nil
		p.mu.Unlock()
		if stale != wantStale || dropped != wantDropped {
			t.Fatalf("stale = %v, dropped = %v; want %v, %v", stale, dropped, wantStale, wantDropped)
		}
	}
	for _, tc := range relevant {
		admin := tc.channel == cache.ChannelAdmin
		t.Run("invalidates/"+tc.name, func(t *testing.T) { check(t, tc.channel, tc.event, true, admin) })
	}
	for _, tc := range unrelated {
		t.Run("ignores/"+tc.name, func(t *testing.T) { check(t, tc.channel, tc.event, false, false) })
	}
}

// TestAdminInsightsFillStartedBeforeAnEvent pins what a fill in flight does
// when an event arrives: after a playback event it still publishes, being
// newer than what it replaces; after an admin change it publishes nothing, so
// the next read shows the change.
func TestAdminInsightsFillStartedBeforeAnEvent(t *testing.T) {
	for _, tc := range []struct {
		name        string
		channel     string
		event       cache.Event
		wantPublish bool
	}{
		{"playback", cache.ChannelPlayback, cache.Event{Type: cache.EventPlaybackSessionsChanged}, true},
		{"admin", cache.ChannelAdmin, cache.Event{Type: cache.EventUserDeleted, Payload: "42"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				bus := &insightsTestEventBus{}
				p, err := NewAdminInsightsProvider(t.Context(), nil, bus)
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				release := make(chan struct{})
				done := make(chan error, 1)
				go func() {
					_, err := cachedInsightsRead(t.Context(), p, "k", func(context.Context) (int, error) {
						<-release
						return 1, nil
					})
					done <- err
				}()
				synctest.Wait()
				if err := bus.Publish(t.Context(), tc.channel, tc.event); err != nil {
					t.Fatal(err)
				}
				close(release)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				p.mu.Lock()
				_, published := p.entries["k"]
				p.mu.Unlock()
				if published != tc.wantPublish {
					t.Fatalf("published = %v, want %v", published, tc.wantPublish)
				}
			})
		})
	}
}

// TestAdminInsightsServesStaleWhileRefreshing pins the contract that keeps a
// busy server's tabs instant: a stale entry is served at once and refreshed
// in the background, no more often than insightsRefreshInterval; after
// Invalidate the next read waits for fresh data; the sweep keeps a key warm
// for adminInsightsKeepWarmFor after its last read, and drops it once nobody
// has read it for adminInsightsServeStaleFor.
func TestAdminInsightsServesStaleWhileRefreshing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, err := NewAdminInsightsProvider(t.Context(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()

		var loads atomic.Int32
		release := make(chan int)
		load := func(ctx context.Context) (*int, error) {
			loads.Add(1)
			select {
			case v := <-release:
				return &v, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		read := func(ctx context.Context) int {
			t.Helper()
			v, err := cachedInsightsRead(ctx, p, "k", load)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			return *v
		}
		// readNow reads a key that must be served without waiting on a load.
		readNow := func() int {
			t.Helper()
			got := make(chan int, 1)
			go func() { got <- read(t.Context()) }()
			synctest.Wait()
			select {
			case v := <-got:
				return v
			default:
				t.Fatal("read waited on a load instead of serving the cached entry")
				return 0
			}
		}

		// A miss waits for its fill.
		first := make(chan int, 1)
		go func() { first <- read(t.Context()) }()
		synctest.Wait()
		release <- 1
		if v := <-first; v != 1 {
			t.Fatalf("first read = %d, want 1", v)
		}

		// Stale by an event, but refreshed too recently: served, no refresh.
		p.markStale()
		if v := readNow(); v != 1 || loads.Load() != 1 {
			t.Fatalf("read = %d with %d loads, want the cached 1 and no refresh", v, loads.Load())
		}

		// Past the refresh interval: served at once, refreshed behind it.
		time.Sleep(insightsRefreshInterval(0)) //nolint:forbidigo // synctest fake clock: sleeping advances time; nothing waits on the real clock.
		if v := readNow(); v != 1 || loads.Load() != 2 {
			t.Fatalf("read = %d with %d loads, want the cached 1 and one refresh", v, loads.Load())
		}
		release <- 2
		synctest.Wait()
		if v := readNow(); v != 2 {
			t.Fatalf("read after refresh = %d, want 2", v)
		}

		// Invalidate (an admin change, the Refresh button) drops the entry,
		// so the next read waits for fresh data even though it was fresh.
		p.Invalidate()
		fresh := make(chan int, 1)
		go func() { fresh <- read(t.Context()) }()
		synctest.Wait()
		release <- 3
		if v := <-fresh; v != 3 {
			t.Fatalf("read after Invalidate = %d, want 3", v)
		}

		// Nobody reads: the sweep refreshes the stale key in the background.
		p.markStale()
		time.Sleep(insightsKeepWarmInterval(0) + adminInsightsSweepInterval) //nolint:forbidigo // synctest fake clock: sleeping advances time; nothing waits on the real clock.
		release <- 4
		synctest.Wait()
		if v := readNow(); v != 4 {
			t.Fatalf("read after keep-warm refresh = %d, want 4", v)
		}

		// Unread for adminInsightsServeStaleFor: dropped, so the next read waits.
		before := loads.Load()
		for range int(adminInsightsServeStaleFor / insightsKeepWarmInterval(0)) {
			time.Sleep(insightsKeepWarmInterval(0)) //nolint:forbidigo // synctest fake clock: sleeping advances time; nothing waits on the real clock.
			synctest.Wait()
			select {
			case release <- 0:
			default:
			}
		}
		time.Sleep(adminInsightsSweepInterval) //nolint:forbidigo // synctest fake clock: sleeping advances time; nothing waits on the real clock.
		synctest.Wait()
		// Warm for adminInsightsKeepWarmFor after the last read, every
		// insightsKeepWarmInterval: a couple of refreshes, then none.
		if n, most := loads.Load()-before, int32(adminInsightsKeepWarmFor/insightsKeepWarmInterval(0)); n == 0 || n > most {
			t.Fatalf("sweep refreshed an unread key %d times, want 1..%d", n, most)
		}
		p.mu.Lock()
		_, kept := p.entries["k"]
		p.mu.Unlock()
		if kept {
			t.Fatal("sweep kept a key nobody read for adminInsightsServeStaleFor")
		}
	})
}

// TestCachedInsightsReadCancelsAFillNobodyWaitsFor pins that a shared fill
// stops once every request waiting on it has left, and keeps running while
// one still waits. An abandoned fill would otherwise hold a read-gate slot,
// or its place in the queue for one, for the full read timeout.
func TestCachedInsightsReadCancelsAFillNobodyWaitsFor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &AdminInsightsProvider{}

		fillErr := make(chan error, 1)
		load := func(ctx context.Context) (*int, error) {
			<-ctx.Done()
			fillErr <- ctx.Err()
			return nil, ctx.Err()
		}

		// Two requests share one fill; the first leaves while the second waits.
		leaving, leave := context.WithCancel(context.Background())
		staying, stay := context.WithCancel(context.Background())
		defer stay()
		first := make(chan error, 1)
		go func() { _, err := cachedInsightsRead(leaving, p, "k", load); first <- err }()
		synctest.Wait()
		second := make(chan error, 1)
		go func() {
			_, err := cachedInsightsRead(staying, p, "k", func(context.Context) (*int, error) {
				t.Error("second request started its own fill instead of joining the first")
				return nil, nil
			})
			second <- err
		}()
		synctest.Wait()

		leave()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatalf("first request: err = %v, want context.Canceled", err)
		}
		synctest.Wait()
		select {
		case err := <-fillErr:
			t.Fatalf("fill ended (%v) while a request still waited on it", err)
		default:
		}

		// The last waiter leaving cancels the fill.
		stay()
		if err := <-second; !errors.Is(err, context.Canceled) {
			t.Fatalf("second request: err = %v, want context.Canceled", err)
		}
		synctest.Wait()
		select {
		case err := <-fillErr:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("fill ended with %v, want its context canceled", err)
			}
		default:
			t.Fatal("fill kept running after every waiter left")
		}
		if _, ok := p.entries["k"]; ok {
			t.Fatal("canceled fill published a cache entry")
		}
	})
}

// TestAdminInsightsRefreshFailureAndEviction pins two limits on background
// refreshes: one that fails is retried no sooner than a successful one would
// be, so a failing read can't keep a read slot busy; and one whose key was
// evicted while it ran doesn't bring the key back.
func TestAdminInsightsRefreshFailureAndEviction(t *testing.T) {
	t.Run("failed refresh waits out its interval", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p, err := NewAdminInsightsProvider(t.Context(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			var loads atomic.Int32
			fail := errors.New("read failed")
			load := func(context.Context) (int, error) {
				if loads.Add(1) == 1 {
					return 1, nil
				}
				return 0, fail
			}
			read := func() {
				t.Helper()
				if _, err := cachedInsightsRead(t.Context(), p, "k", load); err != nil {
					t.Fatalf("read: %v", err)
				}
				synctest.Wait()
			}
			read()
			p.markStale()
			time.Sleep(insightsRefreshInterval(0)) //nolint:forbidigo // synctest fake clock: sleeping advances time; nothing waits on the real clock.
			read()                                 // stale and due: one refresh, which fails
			read()
			if n := loads.Load(); n != 2 {
				t.Fatalf("loads = %d after a failed refresh and another read, want 2", n)
			}
			time.Sleep(insightsRefreshInterval(0)) //nolint:forbidigo // synctest fake clock: sleeping advances time; nothing waits on the real clock.
			read()
			if n := loads.Load(); n != 3 {
				t.Fatalf("loads = %d once the interval passed, want 3", n)
			}
		})
	})

	t.Run("evicted key stays evicted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p, err := NewAdminInsightsProvider(t.Context(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			release := make(chan struct{})
			var loads atomic.Int32
			load := func(context.Context) (int, error) {
				if loads.Add(1) > 1 {
					<-release
				}
				return 1, nil
			}
			if _, err := cachedInsightsRead(t.Context(), p, "k", load); err != nil {
				t.Fatal(err)
			}
			p.markStale()
			time.Sleep(insightsRefreshInterval(0)) //nolint:forbidigo // synctest fake clock: sleeping advances time; nothing waits on the real clock.
			if _, err := cachedInsightsRead(t.Context(), p, "k", load); err != nil {
				t.Fatal(err)
			}
			synctest.Wait() // the refresh is now blocked in its read
			p.mu.Lock()
			delete(p.entries, "k") // as the sweep evicts an unread key
			p.mu.Unlock()
			close(release)
			synctest.Wait()
			p.mu.Lock()
			_, back := p.entries["k"]
			p.mu.Unlock()
			if back {
				t.Fatal("a refresh brought back a key evicted while it ran")
			}
		})
	})
}
