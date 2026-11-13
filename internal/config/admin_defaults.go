package config

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
)

// AdminDefaultsCacheTTL bounds how long a node serves its snapshot of the
// administrator's setting defaults without re-reading them. Admin writes and
// the settings-changed event refresh the snapshot at once (see Changed), so the
// TTL only bounds how long a node that missed the event keeps an old value.
const AdminDefaultsCacheTTL = 10 * time.Second

// adminDefaultsRetryDelay is how long a failed refresh keeps serving the last
// snapshot before the next attempt, so an unreachable settings table costs one
// query per node per delay instead of one per request.
const adminDefaultsRetryDelay = 5 * time.Second

// adminDefaultsReadTimeout bounds one refresh. A refresh never inherits a
// request's cancellation: the snapshot is shared, and one canceled request
// should not count as a failed read for everyone.
const adminDefaultsReadTimeout = 2 * time.Second

// ManySettingReader reads several server settings in one query. Keys without
// a row are absent from the map.
type ManySettingReader interface {
	GetMany(ctx context.Context, keys ...string) (map[string]string, error)
}

// adminDefaultsSnapshot is one read of every administrator default. It is
// replaced whole, never mutated, so readers need no lock.
type adminDefaultsSnapshot struct {
	values  map[string]string
	expires time.Time
}

// AdminDefaults serves the administrator's defaults for every setting the
// contract marks admin_default, from one per-node snapshot. Viewer-scope
// resolution runs on nearly every authenticated request, so:
//
//   - a fresh snapshot is read without a lock;
//   - the snapshot is loaded with one query for all keys;
//   - at most one refresh runs at a time, and once a snapshot exists an
//     expired one is refreshed in the background while callers keep it;
//   - a failed refresh keeps the last snapshot (or none, which means the
//     contract defaults) and is retried after adminDefaultsRetryDelay.
//
// Run keeps the snapshot fresh in the background, so in production a read
// rarely finds it expired; the on-demand refresh covers callers without it.
//
// It satisfies settingsresolve.ServerSettings. One instance is shared by every
// resolver on the node so the settings pages, Home, and Jellyfin clients agree.
type AdminDefaults struct {
	settings ManySettingReader
	keys     []string
	known    map[string]bool
	ttl      time.Duration
	now      func() time.Time

	snapshot atomic.Pointer[adminDefaultsSnapshot]

	mu sync.Mutex
	// inflight is closed when the running refresh finishes; nil when none runs.
	inflight chan struct{}
	// retryAt holds off refreshes after a failure.
	retryAt time.Time
	// started numbers reads as they begin. fence is the oldest read whose
	// result may still be published: Changed raises it to its own read, so a
	// read that began before an admin write can never publish over it.
	started uint64
	fence   uint64
}

// NewAdminDefaults binds the snapshot to a settings reader. A nil reader, or a
// nil *AdminDefaults, answers every key with "" (no administrator default).
func NewAdminDefaults(settings ManySettingReader) *AdminDefaults {
	a := &AdminDefaults{settings: settings, known: map[string]bool{}, ttl: AdminDefaultsCacheTTL, now: time.Now}
	contract, err := settingscontract.Load()
	if err != nil {
		slog.Error("administrator defaults disabled: loading the settings contract failed",
			"component", "config", "error", err)
		return a
	}
	for _, def := range contract.Definitions {
		if def.AdminDefault {
			key := settingscontract.AdminDefaultSettingKey(def.Key)
			a.keys = append(a.keys, key)
			a.known[key] = true
		}
	}
	return a
}

// Get returns the stored administrator default for a server-settings key such
// as "defaults.ui.next_up_mode", or "" when none is set. It never returns an
// error: an unreadable snapshot answers with the last one read, and before any
// read succeeds, with "".
func (a *AdminDefaults) Get(ctx context.Context, key string) (string, error) {
	if a == nil || a.settings == nil || !a.known[key] {
		return "", nil
	}
	now := a.now()
	if snap := a.snapshot.Load(); snap != nil && now.Before(snap.expires) {
		return snap.values[key], nil
	}

	a.mu.Lock()
	snap := a.snapshot.Load()
	if snap != nil && now.Before(snap.expires) {
		a.mu.Unlock()
		return snap.values[key], nil
	}
	wait := a.inflight
	var generation uint64
	if wait == nil && !now.Before(a.retryAt) {
		generation = a.beginLocked()
	}
	a.mu.Unlock()

	if snap != nil {
		// Keep answering from the expired snapshot; refresh behind it.
		if generation != 0 {
			go a.read(context.WithoutCancel(ctx), generation)
		}
		return snap.values[key], nil
	}
	// Nothing to serve yet: load it, or wait for the load already running.
	if generation != 0 {
		a.read(context.WithoutCancel(ctx), generation)
	} else if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
		}
	}
	if snap := a.snapshot.Load(); snap != nil {
		return snap.values[key], nil
	}
	return "", nil
}

// Changed reloads the snapshot when serverSettingKey holds an administrator
// default, waiting for the read. Call it after an admin write commits and when
// another node reports a settings change. Reads that began earlier can no
// longer publish, and if this read fails the next request retries.
func (a *AdminDefaults) Changed(ctx context.Context, serverSettingKey string) {
	if a == nil || a.settings == nil || !a.known[serverSettingKey] {
		return
	}
	a.mu.Lock()
	generation := a.beginLocked()
	a.fence = generation
	a.retryAt = time.Time{}
	a.mu.Unlock()
	a.read(context.WithoutCancel(ctx), generation)
}

// Run refreshes the snapshot every half TTL until ctx ends, so it never
// depends on a request to notice it expired. A node that missed another
// node's change event then serves the new default within the TTL even while
// idle, which is what the web client's delayed re-read relies on. A tick is
// skipped while another refresh runs.
func (a *AdminDefaults) Run(ctx context.Context) {
	if a == nil || a.settings == nil || len(a.keys) == 0 {
		return
	}
	ticker := time.NewTicker(a.ttl / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		a.mu.Lock()
		if a.inflight != nil {
			a.mu.Unlock()
			continue
		}
		generation := a.beginLocked()
		a.mu.Unlock()
		a.read(ctx, generation)
	}
}

// beginLocked numbers a new read and, when no refresh is running, marks this
// one as it. Callers hold a.mu.
func (a *AdminDefaults) beginLocked() uint64 {
	a.started++
	if a.inflight == nil {
		a.inflight = make(chan struct{})
	}
	return a.started
}

// read loads every administrator default and publishes the snapshot unless a
// read fenced off this one or one that began later already published.
func (a *AdminDefaults) read(ctx context.Context, generation uint64) {
	ctx, cancel := context.WithTimeout(ctx, adminDefaultsReadTimeout)
	defer cancel()
	values, err := a.settings.GetMany(ctx, a.keys...)

	a.mu.Lock()
	defer a.mu.Unlock()
	// Publish before waking waiters, which read the snapshot without a lock.
	defer func() {
		if generation == a.started && a.inflight != nil {
			// The newest read finished: nothing newer is running.
			close(a.inflight)
			a.inflight = nil
		}
	}()
	if generation < a.fence {
		return
	}
	if err != nil {
		slog.WarnContext(ctx, "keeping the last administrator defaults: reading them failed",
			"component", "config", "error", err)
		a.retryAt = a.now().Add(adminDefaultsRetryDelay)
		if snap := a.snapshot.Load(); snap != nil && generation == a.fence {
			// An admin write's read failed: the snapshot may predate the
			// write, so let the next request refresh it after the delay.
			a.snapshot.Store(&adminDefaultsSnapshot{values: snap.values, expires: a.retryAt})
		}
		return
	}
	a.fence = generation
	a.snapshot.Store(&adminDefaultsSnapshot{values: values, expires: a.now().Add(a.ttl)})
}
