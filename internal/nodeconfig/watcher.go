package nodeconfig

import (
	"context"
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
}

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
	reloadCh  chan struct{} // buffered(1), event bus writes here
}

// NewWatcher creates a new config watcher. Call Start to begin watching. The
// cipher decrypts sensitive server_settings values (read here via raw SQL)
// before they reach config.LoadFromDB, so a hot reload never feeds ciphertext
// into the live config (which would, e.g., break JWT validation).
func NewWatcher(pool *pgxpool.Pool, cipher *secret.Cipher, eventBus cache.EventBus, bootstrap BootstrapOverrides) *Watcher {
	return &Watcher{
		pool:      pool,
		cipher:    cipher,
		eventBus:  eventBus,
		bootstrap: bootstrap,
		reloadCh:  make(chan struct{}, 1),
	}
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
	return w.reload(ctx)
}

// RequestReload asks the poll goroutine to reload soon. Non-blocking and
// coalescing — safe to call from request handlers. Unlike ForceReload, the
// reload runs on the poll goroutine, so concurrent requests can never swap a
// stale snapshot over a newer one.
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
func (w *Watcher) reload(ctx context.Context) error {
	m, err := w.fetchSettings(ctx)
	if err != nil {
		return err
	}
	return w.applySettings(m)
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
// bootstrap overrides, swaps the config pointer, and notifies OnChange
// callbacks when the config actually changed.
func (w *Watcher) applySettings(m map[string]string) error {
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
