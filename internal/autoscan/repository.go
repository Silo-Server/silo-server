package autoscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) GetSettings(ctx context.Context) (Settings, error) {
	var s Settings
	err := r.pool.QueryRow(ctx, `
		SELECT enabled, poll_interval_minutes, debounce_seconds, updated_at
		FROM autoscan_settings WHERE id = true`).
		Scan(&s.Enabled, &s.PollIntervalMinutes, &s.DebounceSeconds, &s.UpdatedAt)
	if err != nil {
		return Settings{}, fmt.Errorf("get autoscan settings: %w", err)
	}
	return s, nil
}

func (r *Repository) UpdateSettings(ctx context.Context, s Settings) (Settings, error) {
	var out Settings
	err := r.pool.QueryRow(ctx, `
		UPDATE autoscan_settings
		SET enabled = $1, poll_interval_minutes = $2, debounce_seconds = $3, updated_at = now()
		WHERE id = true
		RETURNING enabled, poll_interval_minutes, debounce_seconds, updated_at`,
		s.Enabled, s.PollIntervalMinutes, s.DebounceSeconds).
		Scan(&out.Enabled, &out.PollIntervalMinutes, &out.DebounceSeconds, &out.UpdatedAt)
	if err != nil {
		return Settings{}, fmt.Errorf("update autoscan settings: %w", err)
	}
	return out, nil
}

const sourceSelect = `
	SELECT ri.id, ri.kind, ri.name, ri.base_url, ri.api_key_ref,
	       COALESCE(s.enabled, false), COALESCE(s.path_rewrites, '[]'::jsonb), s.last_poll_at
	FROM request_integrations ri
	LEFT JOIN autoscan_sources s ON s.integration_id = ri.id`

func scanSource(row interface{ Scan(...any) error }) (Source, error) {
	var src Source
	var rewritesRaw []byte
	var lastPoll *time.Time
	if err := row.Scan(&src.IntegrationID, &src.Kind, &src.Name, &src.BaseURL, &src.APIKeyRef,
		&src.Enabled, &rewritesRaw, &lastPoll); err != nil {
		return Source{}, err
	}
	if len(rewritesRaw) > 0 {
		if err := json.Unmarshal(rewritesRaw, &src.PathRewrites); err != nil {
			return Source{}, fmt.Errorf("unmarshal path_rewrites for %s: %w", src.IntegrationID, err)
		}
	}
	src.LastPollAt = lastPoll
	return src, nil
}

// ListAllSources returns every Radarr/Sonarr instance with its autoscan state
// (admin UI — includes disabled).
func (r *Repository) ListAllSources(ctx context.Context) ([]Source, error) {
	rows, err := r.pool.Query(ctx, sourceSelect+` ORDER BY ri.kind, ri.name`)
	if err != nil {
		return nil, fmt.Errorf("list autoscan sources: %w", err)
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// ListEnabledSources returns only autoscan-enabled instances whose integration is
// itself enabled (the poll set).
func (r *Repository) ListEnabledSources(ctx context.Context) ([]Source, error) {
	rows, err := r.pool.Query(ctx, sourceSelect+
		` WHERE s.enabled = true AND ri.enabled = true ORDER BY ri.kind, ri.name`)
	if err != nil {
		return nil, fmt.Errorf("list enabled autoscan sources: %w", err)
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// UpsertSource sets the per-instance autoscan toggle + rewrites.
func (r *Repository) UpsertSource(ctx context.Context, integrationID string, u SourceUpdate) (*Source, error) {
	rewrites, err := json.Marshal(u.PathRewrites)
	if err != nil {
		return nil, fmt.Errorf("marshal path_rewrites: %w", err)
	}
	if u.PathRewrites == nil {
		rewrites = []byte("[]")
	}
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO autoscan_sources (integration_id, enabled, path_rewrites, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (integration_id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			path_rewrites = EXCLUDED.path_rewrites,
			updated_at = now()`,
		integrationID, u.Enabled, rewrites); err != nil {
		// A non-existent integration trips the FK constraint (SQLSTATE 23503);
		// surface it as a 404 rather than a 500.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return nil, fmt.Errorf("%w: %s", ErrIntegrationNotFound, integrationID)
		}
		return nil, fmt.Errorf("upsert autoscan source: %w", err)
	}
	row := r.pool.QueryRow(ctx, sourceSelect+` WHERE ri.id = $1`, integrationID)
	src, err := scanSource(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrIntegrationNotFound, integrationID)
		}
		return nil, err
	}
	return &src, nil
}

// AdvanceLastPoll sets last_poll_at for a source (creating the row if needed).
func (r *Repository) AdvanceLastPoll(ctx context.Context, integrationID string, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO autoscan_sources (integration_id, last_poll_at, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (integration_id) DO UPDATE SET
			last_poll_at = GREATEST(autoscan_sources.last_poll_at, $2),
			updated_at = now()`,
		integrationID, at)
	if err != nil {
		return fmt.Errorf("advance autoscan last_poll: %w", err)
	}
	return nil
}
