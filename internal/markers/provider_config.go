package markers

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProviderConfig is the per-provider behavior row from marker_provider_config.
// fetch_* control multi-source read dispatch; contribute_* gate submission and
// default off so contribution is opt-in per provider.
type ProviderConfig struct {
	Provider                string
	FetchEnabled            bool
	FetchPriority           int
	ContributeEnabled       bool
	ContributeAutoLocal     bool
	ContributeMinConfidence float64
}

const providerConfigColumns = `provider, fetch_enabled, fetch_priority, contribute_enabled, contribute_auto_local, contribute_min_confidence`

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
			provider, fetch_enabled, fetch_priority,
			contribute_enabled, contribute_auto_local, contribute_min_confidence, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (provider) DO UPDATE SET
			fetch_enabled = EXCLUDED.fetch_enabled,
			fetch_priority = EXCLUDED.fetch_priority,
			contribute_enabled = EXCLUDED.contribute_enabled,
			contribute_auto_local = EXCLUDED.contribute_auto_local,
			contribute_min_confidence = EXCLUDED.contribute_min_confidence,
			updated_at = now()`,
		c.Provider, c.FetchEnabled, c.FetchPriority,
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
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO marker_provider_config (
			provider, fetch_enabled, fetch_priority,
			contribute_enabled, contribute_auto_local, contribute_min_confidence, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (provider) DO NOTHING`,
		c.Provider, c.FetchEnabled, c.FetchPriority,
		c.ContributeEnabled, c.ContributeAutoLocal, c.ContributeMinConfidence,
	); err != nil {
		return fmt.Errorf("ensure marker provider config: %w", err)
	}
	return s.Reload(ctx)
}
