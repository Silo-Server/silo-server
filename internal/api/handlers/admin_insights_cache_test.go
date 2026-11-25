package handlers

import (
	"context"
	"errors"
	"slices"
	"sync"
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

func seedInsightsCaches(p *AdminInsightsProvider) {
	p.activity.Set(adminInsightsCacheKeyPrefix+"activity:test", &AdminInsightsActivity{}, time.Minute)
	p.people.Set(adminInsightsCacheKeyPrefix+"people:test", &AdminInsightsPeople{}, time.Minute)
	p.devices.Set(adminInsightsCacheKeyPrefix+"devices:test", &AdminInsightsDevices{}, time.Minute)
	p.bandwidth.Set(adminInsightsCacheKeyPrefix+"bandwidth:test", &AdminInsightsBandwidth{}, time.Minute)
	p.titles.Set(adminInsightsCacheKeyPrefix+"titles:test", &AdminInsightsTitles{}, time.Minute)
	p.library.Set(adminInsightsCacheKeyPrefix+"library:test", &AdminInsightsLibrary{}, time.Minute)
}

func assertInsightsCachesPresent(t *testing.T, p *AdminInsightsProvider, want bool) {
	t.Helper()
	checks := []struct {
		name string
		ok   bool
	}{
		{"activity", func() bool { _, ok := p.activity.Get(adminInsightsCacheKeyPrefix + "activity:test"); return ok }()},
		{"people", func() bool { _, ok := p.people.Get(adminInsightsCacheKeyPrefix + "people:test"); return ok }()},
		{"devices", func() bool { _, ok := p.devices.Get(adminInsightsCacheKeyPrefix + "devices:test"); return ok }()},
		{"bandwidth", func() bool { _, ok := p.bandwidth.Get(adminInsightsCacheKeyPrefix + "bandwidth:test"); return ok }()},
		{"titles", func() bool { _, ok := p.titles.Get(adminInsightsCacheKeyPrefix + "titles:test"); return ok }()},
		{"library", func() bool { _, ok := p.library.Get(adminInsightsCacheKeyPrefix + "library:test"); return ok }()},
	}
	for _, c := range checks {
		if c.ok != want {
			t.Fatalf("%s cache present = %v, want %v", c.name, c.ok, want)
		}
	}
}

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

	for _, tc := range relevant {
		t.Run("invalidates/"+tc.name, func(t *testing.T) {
			bus := &insightsTestEventBus{}
			p, err := NewAdminInsightsProvider(t.Context(), nil, bus)
			if err != nil {
				t.Fatalf("NewAdminInsightsProvider: %v", err)
			}
			defer p.Close()

			seedInsightsCaches(p)
			beforeGen := p.generation()

			if err := bus.Publish(t.Context(), tc.channel, tc.event); err != nil {
				t.Fatalf("Publish: %v", err)
			}

			assertInsightsCachesPresent(t, p, false)
			if got := p.generation(); got != beforeGen+1 {
				t.Fatalf("generation = %d, want %d", got, beforeGen+1)
			}
		})
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

	for _, tc := range unrelated {
		t.Run("ignores/"+tc.name, func(t *testing.T) {
			bus := &insightsTestEventBus{}
			p, err := NewAdminInsightsProvider(t.Context(), nil, bus)
			if err != nil {
				t.Fatalf("NewAdminInsightsProvider: %v", err)
			}
			defer p.Close()

			seedInsightsCaches(p)
			beforeGen := p.generation()

			if err := bus.Publish(t.Context(), tc.channel, tc.event); err != nil {
				t.Fatalf("Publish: %v", err)
			}

			assertInsightsCachesPresent(t, p, true)
			if got := p.generation(); got != beforeGen {
				t.Fatalf("generation = %d, want unchanged %d", got, beforeGen)
			}
		})
	}
}

func TestAdminInsightsInFlightFillPreservedOnUnrelatedAndDiscardedOnRelevantEvent(t *testing.T) {
	t.Run("unrelated_events_do_not_discard_inflight_fill", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			bus := &insightsTestEventBus{}
			p, err := NewAdminInsightsProvider(t.Context(), nil, bus)
			if err != nil {
				t.Fatalf("NewAdminInsightsProvider: %v", err)
			}
			defer p.Close()

			key := adminInsightsCacheKeyPrefix + "activity:inflight"
			release := make(chan struct{})
			val := &AdminInsightsActivity{}

			done := make(chan error, 1)
			go func() {
				_, err := cachedInsightsRead(t.Context(), p, p.activity, key, func(context.Context) (*AdminInsightsActivity, error) {
					<-release
					return val, nil
				})
				done <- err
			}()
			synctest.Wait()

			for _, pub := range []struct {
				channel string
				event   cache.Event
			}{
				{cache.ChannelPlayback, cache.Event{Type: "watch_together_room_state"}},
				{cache.ChannelPlayback, cache.Event{Type: "watch_together_suggestions"}},
				{cache.ChannelPlayback, cache.Event{Type: cache.EventSubtitleReady}},
				{cache.ChannelPlayback, cache.Event{Type: cache.EventSubtitleTimingChanged}},
				{cache.ChannelPlayback, cache.Event{Type: cache.EventSubtitleSyncUpdated}},
				{cache.ChannelPlayback, cache.Event{Type: cache.EventMarkersUpdated}},
				{cache.ChannelPlayback, cache.Event{Type: cache.EventMarkerThumbnailReady}},
				{cache.ChannelAdmin, cache.Event{Type: cache.EventNodeUnreachable}},
				{cache.ChannelAdmin, cache.Event{Type: cache.EventSettingsChanged}},
			} {
				if err := bus.Publish(t.Context(), pub.channel, pub.event); err != nil {
					t.Fatalf("Publish %s/%s: %v", pub.channel, pub.event.Type, err)
				}
			}

			close(release)
			if err := <-done; err != nil {
				t.Fatalf("cachedInsightsRead: %v", err)
			}
			got, ok := p.activity.Get(key)
			if !ok || got != val {
				t.Fatalf("in-flight fill was discarded despite only unrelated bus traffic (ok=%v)", ok)
			}
		})
	})

	t.Run("relevant_event_discards_stale_inflight_fill", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			bus := &insightsTestEventBus{}
			p, err := NewAdminInsightsProvider(t.Context(), nil, bus)
			if err != nil {
				t.Fatalf("NewAdminInsightsProvider: %v", err)
			}
			defer p.Close()

			key := adminInsightsCacheKeyPrefix + "activity:stale"
			release := make(chan struct{})
			val := &AdminInsightsActivity{}

			done := make(chan error, 1)
			go func() {
				_, err := cachedInsightsRead(t.Context(), p, p.activity, key, func(context.Context) (*AdminInsightsActivity, error) {
					<-release
					return val, nil
				})
				done <- err
			}()
			synctest.Wait()

			if err := bus.Publish(t.Context(), cache.ChannelPlayback, cache.Event{
				Type:    cache.EventPlaybackSessionsChanged,
				Payload: "reconciler",
			}); err != nil {
				t.Fatalf("Publish: %v", err)
			}

			close(release)
			if err := <-done; err != nil {
				t.Fatalf("cachedInsightsRead: %v", err)
			}
			if _, ok := p.activity.Get(key); ok {
				t.Fatal("stale in-flight fill published a cache entry after playback_sessions_changed")
			}
		})
	})
}

// TestCachedInsightsReadCancelsAFillNobodyWaitsFor pins that a shared fill
// stops once every request waiting on it has left, and keeps running while
// one still waits. An abandoned fill would otherwise hold a read-gate slot,
// or its place in the queue for one, for the full read timeout.
func TestCachedInsightsReadCancelsAFillNobodyWaitsFor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &AdminInsightsProvider{ttl: time.Minute}
		c := cache.NewTTLCache[*int]()
		defer c.Close()

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
		go func() { _, err := cachedInsightsRead(leaving, p, c, "k", load); first <- err }()
		synctest.Wait()
		second := make(chan error, 1)
		go func() {
			_, err := cachedInsightsRead(staying, p, c, "k", func(context.Context) (*int, error) {
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
		if _, ok := c.Get("k"); ok {
			t.Fatal("canceled fill published a cache entry")
		}
	})
}
