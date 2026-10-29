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
// settings-changed event. Patch writes through and refreshes the snapshot.
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
	next, err := s.Load(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cache = next
	s.mu.Unlock()
	return nil
}

// Load reads every provider config row from the database and returns them
// without touching the snapshot. Callers that must not act on a stale snapshot
// use it instead of Get or List. It leaves the snapshot alone so a read that
// started before an admin edit cannot overwrite the edit's own Reload.
func (s *ProviderConfigStore) Load(ctx context.Context) (map[string]ProviderConfig, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("marker provider config store unavailable")
	}
	rows, err := s.pool.Query(ctx, `SELECT `+providerConfigColumns+` FROM marker_provider_config`)
	if err != nil {
		return nil, fmt.Errorf("load marker provider config: %w", err)
	}
	defer rows.Close()

	next := make(map[string]ProviderConfig)
	for rows.Next() {
		c, err := scanProviderConfig(rows)
		if err != nil {
			return nil, err
		}
		next[c.Provider] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate marker provider config: %w", err)
	}
	return next, nil
}

func scanProviderConfig(row pgx.Row) (ProviderConfig, error) {
	var c ProviderConfig
	if err := row.Scan(
		&c.Provider,
		&c.FetchEnabled,
		&c.FetchPriority,
		&c.FetchMinConfidence,
		&c.ContributeEnabled,
		&c.ContributeAutoLocal,
		&c.ContributeMinConfidence,
	); err != nil {
		return ProviderConfig{}, fmt.Errorf("scan marker provider config: %w", err)
	}
	return c, nil
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

// ProviderConfigPatch holds the fields an admin edit sets. A nil field keeps
// the stored value.
type ProviderConfigPatch struct {
	FetchEnabled            *bool
	FetchPriority           *int
	FetchMinConfidence      *float64
	ContributeEnabled       *bool
	ContributeAutoLocal     *bool
	ContributeMinConfidence *float64
}

// Patch writes only the fields p sets onto the stored row, refreshes the
// snapshot, and returns the row. ok is false when the provider has no row.
// Omitted fields keep their stored values, not this node's cached ones, so an
// edit to one setting cannot restore sharing that an opt-out turned off,
// whether this node missed the change event or the opt-out commits while the
// edit waits on its lock.
func (s *ProviderConfigStore) Patch(ctx context.Context, provider string, p ProviderConfigPatch) (ProviderConfig, bool, error) {
	if s == nil || s.pool == nil {
		return ProviderConfig{}, false, errors.New("marker provider config store unavailable")
	}
	c, err := scanProviderConfig(s.pool.QueryRow(ctx, `
		UPDATE marker_provider_config SET
			fetch_enabled = COALESCE($2::boolean, fetch_enabled),
			fetch_priority = COALESCE($3::integer, fetch_priority),
			fetch_min_confidence = COALESCE($4::double precision, fetch_min_confidence),
			contribute_enabled = COALESCE($5::boolean, contribute_enabled),
			contribute_auto_local = COALESCE($6::boolean, contribute_auto_local),
			contribute_min_confidence = COALESCE($7::double precision, contribute_min_confidence),
			updated_at = now()
		WHERE provider = $1
		RETURNING `+providerConfigColumns,
		provider, p.FetchEnabled, p.FetchPriority, p.FetchMinConfidence,
		p.ContributeEnabled, p.ContributeAutoLocal, p.ContributeMinConfidence,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderConfig{}, false, nil
	}
	if err != nil {
		return ProviderConfig{}, false, fmt.Errorf("update marker provider config: %w", err)
	}
	return c, true, s.Reload(ctx)
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
// to start on. It starts with contribution and automatic sharing off unless
// markers.share_by_default is true. The setting read and the insert hold the
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
