package autoscan

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// --- Settings ---

func (r *Repository) GetSettings(ctx context.Context) (Settings, error) {
	var s Settings
	err := r.pool.QueryRow(ctx, `
		SELECT enabled, default_poll_interval_seconds, debounce_seconds
		FROM autoscan_settings WHERE id = true`).
		Scan(&s.Enabled, &s.DefaultPollIntervalSeconds, &s.DebounceSeconds)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Settings{
				Enabled:                    false,
				DefaultPollIntervalSeconds: 600,
				DebounceSeconds:            60,
			}, nil
		}
		return Settings{}, fmt.Errorf("get autoscan settings: %w", err)
	}
	return s, nil
}

func (r *Repository) UpdateSettings(ctx context.Context, s Settings) (Settings, error) {
	var out Settings
	err := r.pool.QueryRow(ctx, `
		INSERT INTO autoscan_settings (
			id, enabled, default_poll_interval_seconds, debounce_seconds, updated_at
		)
		VALUES (true, $1, $2, $3, now())
		ON CONFLICT (id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			default_poll_interval_seconds = EXCLUDED.default_poll_interval_seconds,
			debounce_seconds = EXCLUDED.debounce_seconds,
			updated_at = now()
		RETURNING enabled, default_poll_interval_seconds, debounce_seconds`,
		s.Enabled, s.DefaultPollIntervalSeconds, s.DebounceSeconds).
		Scan(&out.Enabled, &out.DefaultPollIntervalSeconds, &out.DebounceSeconds)
	if err != nil {
		return Settings{}, fmt.Errorf("update autoscan settings: %w", err)
	}
	return out, nil
}

// --- Connections ---

const connectionColumns = `id, name, kind, base_url, api_key_ref, request_integration_id`

func scanConnection(row interface{ Scan(...any) error }) (Connection, error) {
	var c Connection
	var baseURL, apiKeyRef, reqIntegrationID *string
	if err := row.Scan(&c.ID, &c.Name, &c.Kind, &baseURL, &apiKeyRef, &reqIntegrationID); err != nil {
		return Connection{}, err
	}
	if baseURL != nil {
		c.BaseURL = *baseURL
	}
	if apiKeyRef != nil {
		c.APIKeyRef = *apiKeyRef
	}
	c.RequestIntegrationID = reqIntegrationID
	return c, nil
}

// nullable returns nil for empty strings so they map to SQL NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (r *Repository) CreateConnection(ctx context.Context, c Connection) (Connection, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO autoscan_connections (name, kind, base_url, api_key_ref, request_integration_id)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+connectionColumns,
		c.Name, c.Kind, nullable(c.BaseURL), nullable(c.APIKeyRef), c.RequestIntegrationID)
	out, err := scanConnection(row)
	if err != nil {
		return Connection{}, fmt.Errorf("create autoscan connection: %w", err)
	}
	return out, nil
}

func (r *Repository) UpdateConnection(ctx context.Context, c Connection) (Connection, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE autoscan_connections
		SET name = $2, kind = $3, base_url = $4, api_key_ref = $5,
		    request_integration_id = $6, updated_at = now()
		WHERE id = $1
		RETURNING `+connectionColumns,
		c.ID, c.Name, c.Kind, nullable(c.BaseURL), nullable(c.APIKeyRef), c.RequestIntegrationID)
	out, err := scanConnection(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, fmt.Errorf("%w: connection %s", ErrNotFound, c.ID)
		}
		return Connection{}, fmt.Errorf("update autoscan connection: %w", err)
	}
	return out, nil
}

func (r *Repository) DeleteConnection(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM autoscan_connections WHERE id = $1`, id)
	if err != nil {
		// A source still references this connection (ON DELETE RESTRICT, 23503).
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return fmt.Errorf("autoscan: connection %s is in use by a source", id)
		}
		return fmt.Errorf("delete autoscan connection: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: connection %s", ErrNotFound, id)
	}
	return nil
}

func (r *Repository) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+connectionColumns+`
		FROM autoscan_connections ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list autoscan connections: %w", err)
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) GetConnection(ctx context.Context, id string) (Connection, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+connectionColumns+`
		FROM autoscan_connections WHERE id = $1`, id)
	c, err := scanConnection(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, fmt.Errorf("%w: connection %s", ErrNotFound, id)
		}
		return Connection{}, fmt.Errorf("get autoscan connection: %w", err)
	}
	return c, nil
}

// --- Sources ---

const sourceColumns = `id, installation_id, capability_id, connection_id, enabled,
	poll_interval_seconds, marker, last_run_at, last_error`

func scanSource(row interface{ Scan(...any) error }) (Source, error) {
	var s Source
	if err := row.Scan(&s.ID, &s.InstallationID, &s.CapabilityID, &s.ConnectionID,
		&s.Enabled, &s.PollIntervalSeconds, &s.Marker, &s.LastRunAt, &s.LastError); err != nil {
		return Source{}, err
	}
	return s, nil
}

// UpsertSource creates or updates the autoscan source for an
// (installation_id, capability_id) pair, binding it to a connection and setting
// its scheduling fields. Bookkeeping fields (marker/last_run_at/last_error) are
// left untouched on update.
func (r *Repository) UpsertSource(ctx context.Context, s Source) (Source, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO autoscan_sources (
			installation_id, capability_id, connection_id, enabled, poll_interval_seconds, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (installation_id, capability_id) DO UPDATE SET
			connection_id = EXCLUDED.connection_id,
			enabled = EXCLUDED.enabled,
			poll_interval_seconds = EXCLUDED.poll_interval_seconds,
			updated_at = now()
		RETURNING `+sourceColumns,
		s.InstallationID, s.CapabilityID, s.ConnectionID, s.Enabled, s.PollIntervalSeconds)
	out, err := scanSource(row)
	if err != nil {
		// A non-existent connection trips the FK constraint (23503).
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return Source{}, fmt.Errorf("%w: connection %s", ErrNotFound, s.ConnectionID)
		}
		return Source{}, fmt.Errorf("upsert autoscan source: %w", err)
	}
	return out, nil
}

func (r *Repository) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sourceColumns+`
		FROM autoscan_sources ORDER BY installation_id, capability_id`)
	if err != nil {
		return nil, fmt.Errorf("list autoscan sources: %w", err)
	}
	defer rows.Close()
	return collectSources(rows)
}

func (r *Repository) ListEnabledSources(ctx context.Context) ([]Source, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sourceColumns+`
		FROM autoscan_sources WHERE enabled = true
		ORDER BY installation_id, capability_id`)
	if err != nil {
		return nil, fmt.Errorf("list enabled autoscan sources: %w", err)
	}
	defer rows.Close()
	return collectSources(rows)
}

func collectSources(rows pgx.Rows) ([]Source, error) {
	var out []Source
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repository) GetSource(ctx context.Context, id string) (Source, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+sourceColumns+`
		FROM autoscan_sources WHERE id = $1`, id)
	s, err := scanSource(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Source{}, fmt.Errorf("%w: source %s", ErrNotFound, id)
		}
		return Source{}, fmt.Errorf("get autoscan source: %w", err)
	}
	return s, nil
}

// AdvanceMarker stores the opaque next marker for a source, stamps last_run_at,
// and clears any prior error. Called only after a successful enqueue.
func (r *Repository) AdvanceMarker(ctx context.Context, sourceID, marker string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE autoscan_sources
		SET marker = $2, last_run_at = now(), last_error = NULL, updated_at = now()
		WHERE id = $1`, sourceID, nullable(marker))
	if err != nil {
		return fmt.Errorf("advance autoscan marker: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: source %s", ErrNotFound, sourceID)
	}
	return nil
}

// RecordError records a poll failure for a source: it stamps last_run_at and
// stores the error message without advancing the marker.
func (r *Repository) RecordError(ctx context.Context, sourceID, msg string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE autoscan_sources
		SET last_error = $2, last_run_at = now(), updated_at = now()
		WHERE id = $1`, sourceID, msg)
	if err != nil {
		return fmt.Errorf("record autoscan error: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: source %s", ErrNotFound, sourceID)
	}
	return nil
}
