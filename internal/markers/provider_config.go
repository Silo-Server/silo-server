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
	if _, err := s.pool.Exec(ctx, `
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
	return s.Reload(ctx)
}

// StopSharing turns contribution and automatic sharing off for the given
// providers and reports whether any row changed. A reload calls it for a
// provider it seeded with sharing on when markers.share_by_default turned off
// meanwhile.
func (s *ProviderConfigStore) StopSharing(ctx context.Context, providers []string) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("marker provider config store unavailable")
	}
	changed, err := stopSharing(ctx, s.pool, providers)
	if err != nil || !changed {
		return false, err
	}
	return true, s.Reload(ctx)
}

// StopDefaultSharingInTx turns sharing off for the providers listProviders
// returns, inside tx, the transaction that saves markers.share_by_default=false,
// so the setting and the change commit or fail together. It locks
// marker_provider_config against writes before listing: a provider row seeded
// with sharing on either exists by then and is updated here, or its insert waits
// for this commit and the reload that seeded it then reads the new setting.
// Reload every node's store after tx commits.
func StopDefaultSharingInTx(ctx context.Context, tx pgx.Tx, listProviders func(context.Context) ([]string, error)) error {
	if tx == nil {
		return errors.New("stop marker sharing needs the settings transaction")
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE marker_provider_config IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock marker provider config: %w", err)
	}
	providers, err := listProviders(ctx)
	if err != nil {
		return err
	}
	_, err = stopSharing(ctx, tx, providers)
	return err
}

func stopSharing(ctx context.Context, db interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, providers []string,
) (bool, error) {
	if len(providers) == 0 {
		return false, nil
	}
	tag, err := db.Exec(ctx, `
		UPDATE marker_provider_config
		SET contribute_enabled = false, contribute_auto_local = false, updated_at = now()
		WHERE provider = ANY($1) AND (contribute_enabled OR contribute_auto_local)`,
		providers,
	)
	if err != nil {
		return false, fmt.Errorf("stop marker sharing: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
