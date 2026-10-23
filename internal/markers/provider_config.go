package markers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProviderConfig is the per-provider behavior row from marker_provider_config.
// fetch_* control multi-source read dispatch, including the confidence below
// which a provider's markers are left out; contribute_* gate submission and
// default off so contribution is opt-in per provider.
type ProviderConfig struct {
	Provider                string
	FetchEnabled            bool
	FetchPriority           int
	FetchMinConfidence      float64
	ContributeEnabled       bool
	ContributeAutoLocal     bool
	ContributeMinConfidence float64
}

const providerConfigColumns = `provider, fetch_enabled, fetch_priority, fetch_min_confidence, contribute_enabled, contribute_auto_local, contribute_min_confidence`

// ProviderConfigStore is a cached read/write facade over marker_provider_config.
// Reads serve from an in-memory snapshot; call Reload at startup and after a
// settings-changed event. Update writes through and refreshes the snapshot.
type ProviderConfigStore struct {
	pool  *pgxpool.Pool
	mu    sync.RWMutex
	cache map[string]ProviderConfig
}

// NewProviderConfigStore constructs a store backed by the supplied pool.
func NewProviderConfigStore(pool *pgxpool.Pool) *ProviderConfigStore {
	return &ProviderConfigStore{pool: pool, cache: map[string]ProviderConfig{}}
}

// RuntimeRevision identifies the database state used to load marker providers.
// It reads metadata only; credential values never enter the fingerprint.
func (s *ProviderConfigStore) RuntimeRevision(ctx context.Context) (string, error) {
	if s == nil || s.pool == nil {
		return "", fmt.Errorf("marker provider config store unavailable")
	}
	var revision string
	err := s.pool.QueryRow(ctx, `
		SELECT md5(COALESCE(string_agg(part, E'\n' ORDER BY part COLLATE "C"), ''))
		FROM (
			SELECT jsonb_build_array(
				'plugin', installation.id, installation.version,
				capability.capability_id, extract(epoch FROM capability.updated_at),
				config.config_key, extract(epoch FROM config.updated_at)
			)::text AS part
			FROM plugin_installations installation
			JOIN plugin_capabilities capability
				ON capability.plugin_installation_id = installation.id
				AND capability.capability_type = 'marker_provider.v1'
			LEFT JOIN plugin_runtime_configs config ON config.plugin_installation_id = installation.id
			WHERE installation.enabled AND installation.kind <> 'builtin'
			UNION ALL
			SELECT jsonb_build_array(
				'provider', provider, fetch_enabled, fetch_priority, extract(epoch FROM updated_at)
			)::text
			FROM marker_provider_config
		) revisions`).Scan(&revision)
	if err != nil {
		return "", fmt.Errorf("load marker provider runtime revision: %w", err)
	}
	return revision, nil
}

// Reload replaces the in-memory snapshot from the database.
func (s *ProviderConfigStore) Reload(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+providerConfigColumns+` FROM marker_provider_config`)
	if err != nil {
		return fmt.Errorf("load marker provider config: %w", err)
	}
	defer rows.Close()

	next := make(map[string]ProviderConfig)
	for rows.Next() {
		var c ProviderConfig
		if err := rows.Scan(
			&c.Provider,
			&c.FetchEnabled,
			&c.FetchPriority,
			&c.FetchMinConfidence,
			&c.ContributeEnabled,
			&c.ContributeAutoLocal,
			&c.ContributeMinConfidence,
		); err != nil {
			return fmt.Errorf("scan marker provider config: %w", err)
		}
		next[c.Provider] = c
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate marker provider config: %w", err)
	}

	s.mu.Lock()
	s.cache = next
	s.mu.Unlock()
	return nil
}

// Get returns the config for a provider id from the snapshot.
func (s *ProviderConfigStore) Get(provider string) (ProviderConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.cache[provider]
	return c, ok
}

// List returns all configs sorted by (fetch_priority asc, provider asc).
func (s *ProviderConfigStore) List() []ProviderConfig {
	s.mu.RLock()
	out := make([]ProviderConfig, 0, len(s.cache))
	for _, c := range s.cache {
		out = append(out, c)
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].FetchPriority != out[j].FetchPriority {
			return out[i].FetchPriority < out[j].FetchPriority
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

// EnabledForFetch returns the fetch-enabled providers in priority order.
func (s *ProviderConfigStore) EnabledForFetch() []ProviderConfig {
	all := s.List()
	out := make([]ProviderConfig, 0, len(all))
	for _, c := range all {
		if c.FetchEnabled {
			out = append(out, c)
		}
	}
	return out
}

// Update upserts a provider config row and refreshes the snapshot.
func (s *ProviderConfigStore) Update(ctx context.Context, c ProviderConfig) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("marker provider config store unavailable")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO marker_provider_config (
			provider, fetch_enabled, fetch_priority, fetch_min_confidence,
			contribute_enabled, contribute_auto_local, contribute_min_confidence, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (provider) DO UPDATE SET
			fetch_enabled = EXCLUDED.fetch_enabled,
			fetch_priority = EXCLUDED.fetch_priority,
			fetch_min_confidence = EXCLUDED.fetch_min_confidence,
			contribute_enabled = EXCLUDED.contribute_enabled,
			contribute_auto_local = EXCLUDED.contribute_auto_local,
			contribute_min_confidence = EXCLUDED.contribute_min_confidence,
			updated_at = now()`,
		c.Provider, c.FetchEnabled, c.FetchPriority, c.FetchMinConfidence,
		c.ContributeEnabled, c.ContributeAutoLocal, c.ContributeMinConfidence,
	); err != nil {
		return fmt.Errorf("update marker provider config: %w", err)
	}
	return s.Reload(ctx)
}

// Ensure inserts a default provider config row if one does not already exist.
// Existing rows are left untouched so admin choices survive plugin restarts and
// upgrades.
func (s *ProviderConfigStore) Ensure(ctx context.Context, c ProviderConfig) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("marker provider config store unavailable")
	}
	if _, ok := s.Get(c.Provider); ok {
		return nil
	}
	if err := insertProviderConfig(ctx, s.pool, c); err != nil {
		return err
	}
	return s.Reload(ctx)
}

// EnsureDefaultSharing is Ensure for a row whose manifest asks for contribution
// to start on. It starts with contribution and automatic sharing off when
// markers.share_by_default is false. The setting read and the insert hold the
// lock StopDefaultSharingInTx takes, so an opt-out saved meanwhile either
// commits first and this row starts off, or waits and turns this row off.
func (s *ProviderConfigStore) EnsureDefaultSharing(ctx context.Context, c ProviderConfig) error {
	if s == nil || s.pool == nil {
		return errors.New("marker provider config store unavailable")
	}
	if _, ok := s.Get(c.Provider); ok {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin marker provider config seed: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockProviderConfig(ctx, tx); err != nil {
		return err
	}
	var share string
	err = tx.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, SettingShareByDefault).Scan(&share)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read %s: %w", SettingShareByDefault, err)
	}
	if !ShareByDefaultEnabled(share) {
		c.ContributeEnabled, c.ContributeAutoLocal = false, false
	}
	if err := insertProviderConfig(ctx, tx, c); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit marker provider config seed: %w", err)
	}
	return s.Reload(ctx)
}

// StopDefaultSharingInTx turns contribution and automatic sharing off for the
// providers that list names, inside tx, the transaction that saves
// markers.share_by_default=false, so the setting and the change commit or fail
// together. It calls list only after taking the lock EnsureDefaultSharing
// takes, so the list sees every provider whose row a seed has committed, and a
// seed that has not committed waits and reads the opt-out. Reload every node's
// store after tx commits.
func StopDefaultSharingInTx(ctx context.Context, tx pgx.Tx, list func(context.Context, pgx.Tx) ([]string, error)) error {
	if tx == nil {
		return errors.New("stop marker sharing needs the settings transaction")
	}
	if err := lockProviderConfig(ctx, tx); err != nil {
		return err
	}
	providers, err := list(ctx, tx)
	if err != nil {
		return err
	}
	if len(providers) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE marker_provider_config
		SET contribute_enabled = false, contribute_auto_local = false, updated_at = now()
		WHERE provider = ANY($1) AND (contribute_enabled OR contribute_auto_local)`,
		providers,
	); err != nil {
		return fmt.Errorf("stop marker sharing: %w", err)
	}
	return nil
}

// lockProviderConfig serializes seeding a sharing row with saving the opt-out.
// SHARE ROW EXCLUSIVE conflicts with itself and with plain writes, so admin
// edits wait too, but reads do not.
func lockProviderConfig(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `LOCK TABLE marker_provider_config IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock marker provider config: %w", err)
	}
	return nil
}

func insertProviderConfig(ctx context.Context, db interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, c ProviderConfig,
) error {
	if _, err := db.Exec(ctx, `
		INSERT INTO marker_provider_config (
			provider, fetch_enabled, fetch_priority, fetch_min_confidence,
			contribute_enabled, contribute_auto_local, contribute_min_confidence, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (provider) DO NOTHING`,
		c.Provider, c.FetchEnabled, c.FetchPriority, c.FetchMinConfidence,
		c.ContributeEnabled, c.ContributeAutoLocal, c.ContributeMinConfidence,
	); err != nil {
		return fmt.Errorf("ensure marker provider config: %w", err)
	}
	return nil
}
