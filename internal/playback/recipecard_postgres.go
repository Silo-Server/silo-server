package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// PostgresRecipeStore is the production RecipeStore. A nil *pgxpool.Pool yields a
// disabled store whose methods all no-op (used by tests/standalone setups with no
// DB); in practice Postgres is always present, so this store is effectively
// always enabled and the restart-resilience feature needs no extra dependency.
//
// Expiry is explicit: a TTL is emulated with an expires_at column, re-armed on
// activity. Reads filter on expires_at so a lapsed card is invisible immediately;
// physical rows are reclaimed by DeleteExpired on the reconciler janitor tick.
type PostgresRecipeStore struct {
	pool *pgxpool.Pool
	ttl  time.Duration
}

// NewPostgresRecipeStore wraps a pgx pool. pool may be nil (store disabled).
func NewPostgresRecipeStore(pool *pgxpool.Pool) *PostgresRecipeStore {
	return &PostgresRecipeStore{pool: pool, ttl: recipeTTL}
}

func (s *PostgresRecipeStore) Enabled() bool { return s != nil && s.pool != nil }

func (s *PostgresRecipeStore) Save(ctx context.Context, card RecipeCard) error {
	if !s.Enabled() {
		return nil
	}
	if card.SessionID == "" {
		return fmt.Errorf("recipe card: empty session id")
	}
	opts, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("recipe card marshal: %w", err)
	}
	const q = `
		INSERT INTO transcode_recipes
			(session_id, user_id, profile_id, media_file_id, transcode_node_url, opts, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, now() + $7::interval)
		ON CONFLICT (session_id) DO UPDATE SET
			user_id            = EXCLUDED.user_id,
			profile_id         = EXCLUDED.profile_id,
			media_file_id      = EXCLUDED.media_file_id,
			transcode_node_url = EXCLUDED.transcode_node_url,
			opts               = EXCLUDED.opts,
			expires_at         = EXCLUDED.expires_at`
	_, err = s.pool.Exec(ctx, q,
		card.SessionID, card.UserID, card.ProfileID, card.MediaFileID,
		card.TranscodeNodeURL, opts, s.intervalArg(),
	)
	return err
}

func (s *PostgresRecipeStore) Get(ctx context.Context, sessionID string) (RecipeCard, bool, error) {
	var card RecipeCard
	if !s.Enabled() || sessionID == "" {
		return card, false, nil
	}
	const q = `SELECT opts FROM transcode_recipes WHERE session_id = $1 AND expires_at > now()`
	var raw []byte
	err := s.pool.QueryRow(ctx, q, sessionID).Scan(&raw)
	if err != nil {
		if isNoRows(err) {
			return card, false, nil
		}
		return card, false, err
	}
	if err := json.Unmarshal(raw, &card); err != nil {
		return card, false, fmt.Errorf("recipe card unmarshal: %w", err)
	}
	return card, true, nil
}

func (s *PostgresRecipeStore) Delete(ctx context.Context, sessionID string) error {
	if !s.Enabled() || sessionID == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM transcode_recipes WHERE session_id = $1`, sessionID)
	return err
}

func (s *PostgresRecipeStore) Refresh(ctx context.Context, sessionID string) error {
	if !s.Enabled() || sessionID == "" {
		return nil
	}
	// Only re-arms an existing, non-expired row; a missing/expired row is a no-op
	// (the session was stopped or abandoned).
	_, err := s.pool.Exec(ctx,
		`UPDATE transcode_recipes SET expires_at = now() + $2::interval WHERE session_id = $1 AND expires_at > now()`,
		sessionID, s.intervalArg(),
	)
	return err
}

func (s *PostgresRecipeStore) ActiveSessionIDs(ctx context.Context) (map[string]struct{}, error) {
	ids := make(map[string]struct{})
	if !s.Enabled() {
		return ids, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT session_id FROM transcode_recipes WHERE expires_at > now()`)
	if err != nil {
		return ids, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return ids, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

// DeleteExpired physically removes lapsed rows. Functional correctness does not
// depend on it (reads already filter on expires_at); it bounds table growth and
// is meant to run on the reconciler janitor cadence and at boot cleanup.
func (s *PostgresRecipeStore) DeleteExpired(ctx context.Context) (int64, error) {
	if !s.Enabled() {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM transcode_recipes WHERE expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (s *PostgresRecipeStore) intervalArg() string {
	// Postgres interval literal, e.g. "1800 seconds".
	return fmt.Sprintf("%d seconds", int(s.ttl/time.Second))
}
