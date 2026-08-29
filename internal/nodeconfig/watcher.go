package nodeconfig

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// BootstrapOverrides holds values from env/CLI that must survive config
// reloads. These are set once at startup and re-applied after every
// LoadFromDB call.
type BootstrapOverrides struct {
	Listen      string // from PORT env
	Mode        string // from MODE env
	DatabaseURL string // from DATABASE_URL env
	JFListen    string // from JF_PORT env
	RedisURL    string // from REDIS_URL env
	// NodeURL is this process's own stream_nodes identity (from NODE_URL env),
	// set only in proxy/transcode mode. It is what lets a node find its own row
	// and overlay that row's acceleration overrides onto the cluster-wide
	// playback settings. Empty on an API host, which has no stream_nodes row.
	NodeURL string
	// NodeName is this process's own registered node name (from NODE_NAME
	// env). It is the override-row fallback identity when the registered url
	// and NODE_URL differ, as they do on split-horizon topologies.
	NodeName string
}

// nodeHWOverrides is one node's own acceleration policy, as stored on its
// stream_nodes row. A nil field means the node inherits the cluster-wide
// playback setting.
type nodeHWOverrides struct {
	HWAccel  *string
	HWDevice *string
}

// loadNodeHWOverrides reads one node's overrides, matching its registered row
// by URL first and by NODE_NAME as the fallback. found is false when the node
// has no stream_nodes row at all — a legitimate deployment (a node nobody
// registered yet), not an error. It is a field on the Watcher so the overlay
// can be exercised without a database.
type loadNodeHWOverrides func(ctx context.Context, nodeURL, nodeName string) (overrides nodeHWOverrides, found bool, err error)

// Watcher watches for configuration changes in the database and
// automatically reloads the Config when changes are detected.
type Watcher struct {
	mu        sync.RWMutex
	cfg       *config.Config
	pool      *pgxpool.Pool
	cipher    *secret.Cipher
	eventBus  cache.EventBus
	bootstrap BootstrapOverrides
	onChange  []func(old, updated *config.Config)
	// normalizers run on every config the watcher constructs, after bootstrap
	// overrides and before the config becomes visible, so derived repairs
	// (e.g. resolving the seeded ffmpeg path) survive hot reloads.
	normalizers []func(*config.Config)
	reloadCh    chan struct{} // buffered(1), event bus writes here

	// loadOverrides reads this node's own acceleration overrides; see the
	// overlay in applySettings.
	loadOverrides loadNodeHWOverrides
	// overrides is the last successfully read overlay, kept so a database
	// hiccup during a reload cannot silently flip a node back onto the
	// cluster-wide backend. overridesLoaded distinguishes "read, and there is
	// nothing to overlay" from "never read".
	overrides           nodeHWOverrides
	overridesLoaded     bool
	missingRowLogged    bool
	duplicateRowLogged  bool
	ambiguousNameLogged bool
	// nodeRowID is the row this worker has resolved to, kept so a later rename
	// or repoint cannot sever the association. Zero until a lookup succeeds.
	nodeRowID int

	// reloadMu makes one reload atomic from the read of server_settings through
	// the config swap and its callbacks; see reload.
	reloadMu sync.Mutex
	// fetchSettings, when set, replaces the read of server_settings. Only a
	// test sets it — nothing in production needs to read settings from
	// anywhere but the database.
	fetchSettingsFn func(context.Context) (map[string]string, error)
}

// rememberNodeRowID records the row a lookup resolved to.
func (w *Watcher) rememberNodeRowID(id int) {
	if id <= 0 {
		return
	}
	w.mu.Lock()
	w.nodeRowID = id
	w.mu.Unlock()
}

// rememberedNodeRowID returns the row this worker previously resolved to.
func (w *Watcher) rememberedNodeRowID() (int, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.nodeRowID, w.nodeRowID > 0
}

// forgetNodeRowID drops a remembered row that no longer exists.
func (w *Watcher) forgetNodeRowID() {
	w.mu.Lock()
	w.nodeRowID = 0
	w.mu.Unlock()
}

// NewWatcher creates a new config watcher. Call Start to begin watching. The
// cipher decrypts sensitive server_settings values (read here via raw SQL)
// before they reach config.LoadFromDB, so a hot reload never feeds ciphertext
// into the live config (which would, e.g., break JWT validation).
func NewWatcher(pool *pgxpool.Pool, cipher *secret.Cipher, eventBus cache.EventBus, bootstrap BootstrapOverrides) *Watcher {
	w := &Watcher{
		pool:      pool,
		cipher:    cipher,
		eventBus:  eventBus,
		bootstrap: bootstrap,
		reloadCh:  make(chan struct{}, 1),
	}
	w.loadOverrides = w.queryNodeHWOverrides
	return w
}

// OnLoad registers a normalization applied to every config this watcher
// constructs (initial load and every reload). Register before Start.
func (w *Watcher) OnLoad(fn func(*config.Config)) {
	w.normalizers = append(w.normalizers, fn)
}

// Config returns the current config. Safe for concurrent use.
// Returns nil if Start has not been called.
func (w *Watcher) Config() *config.Config {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.cfg
}

// OnChange registers a callback invoked after a config swap whose new value
// differs from the old one. The callback receives the old and new config.
// Safe to call before or after Start; callbacks registered after Start only
// see reloads that happen after registration.
func (w *Watcher) OnChange(fn func(old, updated *config.Config)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onChange = append(w.onChange, fn)
}

// Start performs the initial config load from the database, subscribes to
// EventSettingsChanged on the admin channel, and starts the background
// poll goroutine. Returns an error if the initial load fails.
func (w *Watcher) Start(ctx context.Context) error {
	if err := w.reload(ctx); err != nil {
		return fmt.Errorf("initial config load: %w", err)
	}

	// Subscribe to settings change events for immediate reload.
	if err := w.eventBus.Subscribe(ctx, cache.ChannelAdmin, func(event cache.Event) {
		if event.Type == cache.EventSettingsChanged {
			select {
			case w.reloadCh <- struct{}{}:
			default:
				// Already pending — coalesce.
			}
		}
	}); err != nil {
		slog.WarnContext(ctx, "config watcher: subscribe to admin channel failed, using poll-only mode", "component", "nodeconfig", "error", err)
	}

	go w.poll(ctx)
	return nil
}

// ForceReload triggers an immediate config reload from the database.
func (w *Watcher) ForceReload(ctx context.Context) error {
	if w == nil || (w.pool == nil && w.fetchSettingsFn == nil) {
		// A watcher with no database is one a test constructed, or a mode that
		// runs entirely off bootstrap overrides. Either way there is nothing to
		// re-read, and an error is a far better answer than the nil dereference
		// this used to be for a route that an operator can reach.
		return errors.New("no database pool")
	}
	return w.reload(ctx)
}

// RequestReload asks the poll goroutine to reload soon. Non-blocking and
// coalescing — safe to call from request handlers. Unlike ForceReload it does
// not wait for the reload, and does not report whether it succeeded.
func (w *Watcher) RequestReload() {
	select {
	case w.reloadCh <- struct{}{}:
	default:
		// Already pending — coalesce.
	}
}

// SetConfigForTest sets the config directly without loading from DB.
// This is intended for use in tests only.
func (w *Watcher) SetConfigForTest(cfg *config.Config) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cfg = cfg
}

// reload fetches all settings from the database, builds a new Config,
// applies bootstrap overrides, and atomically swaps the config pointer.
//
// Read and swap are one critical section. Reloads arrive from three places —
// the 60s poll, the settings-changed event, and ForceReload on a request
// goroutine — and only the first two share a goroutine. Letting them overlap
// would make the winner the one that finished last rather than the one that
// read last: a poll that sampled server_settings before an operator's edit can
// return after ForceReload has already applied the edit, and put its pre-edit
// snapshot back. The node would then answer 204 to the endpoint, the API would
// reload its pool believing the node adopted the new backend and device, and
// the node would go on transcoding with the old ones until something reloaded
// it again.
//
// Serializing the whole operation, rather than just the swap, is what fixes
// that: a reload's fetch cannot begin until the previous reload's swap is
// done, so a later swap is always built on a later read. The callbacks run
// inside the section too, so an older reload can't announce its config as the
// current one after a newer swap.
func (w *Watcher) reload(ctx context.Context) error {
	w.reloadMu.Lock()
	defer w.reloadMu.Unlock()

	fetch := w.fetchSettings
	if w.fetchSettingsFn != nil {
		fetch = w.fetchSettingsFn
	}
	m, err := fetch(ctx)
	if err != nil {
		return err
	}
	return w.applySettings(ctx, m)
}

// fetchSettings reads all server_settings rows and decrypts sensitive values.
func (w *Watcher) fetchSettings(ctx context.Context) (map[string]string, error) {
	rows, err := w.pool.Query(ctx, "SELECT key, value FROM server_settings")
	if err != nil {
		return nil, fmt.Errorf("query server_settings: %w", err)
	}
	defer rows.Close()

	m := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan server_settings row: %w", err)
		}
		// Decrypt sensitive keys (read-path contract: legacy plaintext passes
		// through, enc:v1: values decrypt, corrupt ciphertext errors) so
		// LoadFromDB always sees plaintext.
		decrypted, derr := w.cipher.DecryptIfEncrypted(v, secret.SettingsAAD(k))
		if derr != nil {
			return nil, fmt.Errorf("decrypt server_settings %q: %w", k, derr)
		}
		m[k] = decrypted
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate server_settings: %w", err)
	}
	return m, nil
}

// applySettings builds a Config from a plaintext settings map, re-applies
// bootstrap overrides, overlays this node's own acceleration policy, swaps the
// config pointer, and notifies OnChange callbacks when the config actually
// changed.
func (w *Watcher) applySettings(ctx context.Context, m map[string]string) error {
	newCfg, err := config.LoadFromDB(m)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	// Re-apply bootstrap overrides — these are immutable for the process lifetime.
	if w.bootstrap.Listen != "" {
		newCfg.Server.Listen = w.bootstrap.Listen
	}
	if w.bootstrap.Mode != "" {
		newCfg.Server.Mode = w.bootstrap.Mode
	}
	if w.bootstrap.DatabaseURL != "" {
		newCfg.Database.URL = w.bootstrap.DatabaseURL
	}
	if w.bootstrap.JFListen != "" {
		newCfg.JellyfinCompat.Listen = w.bootstrap.JFListen
	}
	if w.bootstrap.RedisURL != "" {
		newCfg.Redis.URL = w.bootstrap.RedisURL
	}

	for _, normalize := range w.normalizers {
		normalize(newCfg)
	}

	// Last word, after the bootstrap re-apply and the normalizers: the node's
	// own row decides its acceleration policy, and nothing above may put the
	// cluster value back.
	w.applyNodeHWOverrides(ctx, newCfg)

	w.mu.Lock()
	old := w.cfg
	w.cfg = newCfg
	callbacks := make([]func(old, updated *config.Config), len(w.onChange))
	copy(callbacks, w.onChange)
	w.mu.Unlock()

	// The poll path reloads every 60s regardless of whether anything changed;
	// don't fire callbacks (which may rebuild clients or log) on no-op swaps.
	if old != nil && reflect.DeepEqual(*old, *newCfg) {
		return nil
	}

	for _, fn := range callbacks {
		fn(old, newCfg)
	}

	return nil
}

// applyNodeHWOverrides overlays this node's stored acceleration policy onto a
// freshly built config, so everything downstream — probes, warmup, the node's
// own fallback when a start request omits a backend — reads one effective
// value rather than consulting the row separately.
//
// It is a no-op on a host with no node identity (the API server, which has no
// stream_nodes row). Failure is deliberately conservative: a node that cannot
// read its row keeps the overlay it last read, because a database hiccup is
// not evidence that an operator cleared the override.
func (w *Watcher) applyNodeHWOverrides(ctx context.Context, cfg *config.Config) {
	if (w.bootstrap.NodeURL == "" && w.bootstrap.NodeName == "") || w.loadOverrides == nil || cfg == nil {
		return
	}

	overrides, found, err := w.loadOverrides(ctx, w.bootstrap.NodeURL, w.bootstrap.NodeName)
	switch {
	case err != nil:
		slog.WarnContext(ctx, "node acceleration override lookup failed; keeping the previous effective policy",
			"component", "nodeconfig", "error", err)
		var loaded bool
		w.mu.RLock()
		overrides, loaded = w.overrides, w.overridesLoaded
		w.mu.RUnlock()
		if !loaded {
			// Never read one: the cluster-wide settings stand as they are.
			return
		}
	case !found:
		// Logged once rather than on every 60s reload: an unregistered node is
		// a standing condition, not an event.
		w.mu.Lock()
		first := !w.missingRowLogged
		w.missingRowLogged = true
		previous, hadOverrides := w.overrides, w.overridesLoaded
		if !hadOverrides {
			w.overrides, w.overridesLoaded = nodeHWOverrides{}, true
		}
		w.mu.Unlock()
		if first {
			slog.InfoContext(ctx, "no stream_nodes row for this node; inheriting the cluster acceleration settings",
				"component", "nodeconfig", "node_url", w.bootstrap.NodeURL, "node_name", w.bootstrap.NodeName)
		}
		if !hadOverrides {
			return
		}
		// The row was there and now is not. On a split-horizon deployment the
		// match is by NODE_NAME, and renaming a node through the admin form
		// leaves this worker's environment pointing at a name nothing carries —
		// so "no row" arrives while the API is still dispatching that row's
		// overridden backend. Reverting to the cluster device here would pair
		// the two wrongly for as long as the names disagree. A row that has
		// gone is not evidence an operator cleared the override, so the last
		// one read stands, exactly as it does when the lookup errors.
		slog.WarnContext(ctx, "this node's stream_nodes row is no longer matchable; keeping the acceleration policy last read from it",
			"component", "nodeconfig", "node_url", w.bootstrap.NodeURL, "node_name", w.bootstrap.NodeName)
		overrides = previous
	default:
		w.mu.Lock()
		w.overrides, w.overridesLoaded = overrides, true
		w.mu.Unlock()
	}

	if overrides.HWAccel != nil {
		cfg.Playback.HWAccel = *overrides.HWAccel
	}
	if overrides.HWDevice != nil {
		cfg.Playback.HWDevice = *overrides.HWDevice
	}
}

// queryNodeHWOverrides reads this node's own stream_nodes row. The URL is
// matched with trailing slashes ignored on both sides, because NODE_URL and
// the registered URL are typed by different people; the scan this costs is
// irrelevant next to getting the match wrong and silently inheriting the
// cluster policy.
//
// That tolerance can match two rows, though: stream_nodes.url is unique on the
// exact string, so "http://n1" and "http://n1/" are two legal registrations
// that rtrim collapses into one key. Ordering by id makes the winner the same
// on every reload — without it the seq scan returns whichever row it reached
// first, and the 30-second health sweep rewriting those rows would silently
// flip a node between two policies. The duplicate is reported rather than
// quietly resolved, because only an operator can fix it.
func (w *Watcher) queryNodeHWOverrides(ctx context.Context, nodeURL, nodeName string) (nodeHWOverrides, bool, error) {
	if w.pool == nil {
		return nodeHWOverrides{}, false, errors.New("no database pool")
	}
	// The row this worker already resolved to, by its immutable id.
	//
	// Neither of the identities below survives an edit: a repoint changes the
	// url, and a rename changes the name — and on a split-horizon deployment the
	// name is the only match there was, so renaming a node severs the
	// association permanently. The worker then keeps whatever policy it last
	// read while the API dispatches the row's current one, which is the exact
	// backend/device mismatch this overlay exists to prevent. Once the row has
	// been identified, its id is what identifies it.
	if id, ok := w.rememberedNodeRowID(); ok {
		overrides, _, matched, err := w.queryOverrideRows(ctx,
			`SELECT id, url, hw_accel_override, hw_device_override FROM stream_nodes
			 WHERE id = $1`, id)
		if err != nil {
			return nodeHWOverrides{}, false, err
		}
		if len(matched) > 0 {
			return overrides, true, nil
		}
		// The row was deleted. Forget it and fall back to the identities, which
		// is how a re-registered node finds its new row.
		w.forgetNodeRowID()
	}

	overrides, id, matched, err := w.queryOverrideRows(ctx,
		`SELECT id, url, hw_accel_override, hw_device_override FROM stream_nodes
		 WHERE rtrim(url, '/') = rtrim($1, '/') ORDER BY id LIMIT 2`, nodeURL)
	if err != nil {
		return nodeHWOverrides{}, false, err
	}
	if len(matched) > 1 {
		w.logDuplicateNodeRows(ctx, nodeURL, matched)
	}
	if len(matched) > 0 {
		w.rememberNodeRowID(id)
		return overrides, true, nil
	}

	// The registered url is how the API reaches the node, which on a
	// split-horizon topology (public CDN url registered, internal NODE_URL on
	// the node) never equals the node's own address. The name is the identity
	// an operator controls on both sides, so it is the fallback — but names
	// carry no unique constraint, so an ambiguous match identifies nothing.
	if nodeName == "" {
		return nodeHWOverrides{}, false, nil
	}
	overrides, id, matched, err = w.queryOverrideRows(ctx,
		`SELECT id, url, hw_accel_override, hw_device_override FROM stream_nodes
		 WHERE name = $1 ORDER BY id LIMIT 2`, nodeName)
	if err != nil {
		return nodeHWOverrides{}, false, err
	}
	switch len(matched) {
	case 0:
		return nodeHWOverrides{}, false, nil
	case 1:
		w.rememberNodeRowID(id)
		return overrides, true, nil
	default:
		w.logAmbiguousNodeName(ctx, nodeName, matched)
		return nodeHWOverrides{}, false, nil
	}
}

// queryOverrideRows runs one identity query and returns the first row plus the
// urls of every row it matched, so callers can act on ambiguity.
func (w *Watcher) queryOverrideRows(ctx context.Context, query string, arg any) (nodeHWOverrides, int, []string, error) {
	rows, err := w.pool.Query(ctx, query, arg)
	if err != nil {
		return nodeHWOverrides{}, 0, nil, fmt.Errorf("query node acceleration overrides: %w", err)
	}
	defer rows.Close()

	var (
		overrides nodeHWOverrides
		firstID   int
		matched   []string
	)
	for rows.Next() {
		var (
			id  int
			url string
			row nodeHWOverrides
		)
		if err := rows.Scan(&id, &url, &row.HWAccel, &row.HWDevice); err != nil {
			return nodeHWOverrides{}, 0, nil, fmt.Errorf("scan node acceleration overrides: %w", err)
		}
		if len(matched) == 0 {
			overrides, firstID = row, id
		}
		matched = append(matched, url)
	}
	if err := rows.Err(); err != nil {
		return nodeHWOverrides{}, 0, nil, fmt.Errorf("read node acceleration overrides: %w", err)
	}
	return overrides, firstID, matched, nil
}

// logAmbiguousNodeName warns once per process: several registered nodes share
// this node's NODE_NAME, so the name identifies nothing and the overrides of
// none of them are adopted.
func (w *Watcher) logAmbiguousNodeName(ctx context.Context, nodeName string, matched []string) {
	w.mu.Lock()
	first := !w.ambiguousNameLogged
	w.ambiguousNameLogged = true
	w.mu.Unlock()
	if first {
		slog.WarnContext(ctx, "several stream_nodes rows share this node's name; ignoring their acceleration overrides",
			"component", "nodeconfig", "node_name", nodeName, "matched_urls", matched)
	}
}

// logDuplicateNodeRows warns, once per process, that more than one
// stream_nodes row claims this node's URL. Once rather than every 60s: the
// duplicate is a standing misconfiguration, and the lowest id keeps winning
// until an operator removes the other row.
func (w *Watcher) logDuplicateNodeRows(ctx context.Context, nodeURL string, matched []string) {
	w.mu.Lock()
	first := !w.duplicateRowLogged
	w.duplicateRowLogged = true
	w.mu.Unlock()
	if !first {
		return
	}
	slog.WarnContext(ctx, "several stream_nodes rows match this node's URL; using the lowest id",
		"component", "nodeconfig", "node_url", nodeURL, "matched_urls", matched)
}

// poll runs the background loop that reloads config on timer or event.
func (w *Watcher) poll(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.reload(ctx); err != nil {
				slog.WarnContext(ctx, "config poll reload failed", "component", "nodeconfig", "error", err)
			}
		case <-w.reloadCh:
			if err := w.reload(ctx); err != nil {
				slog.WarnContext(ctx, "config event reload failed", "component", "nodeconfig", "error", err)
			}
		}
	}
}
