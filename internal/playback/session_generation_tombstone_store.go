package playback

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionGenerationTombstoneStore persists ended lifecycle identities for as
// long as a signed playback token can remain valid.
type SessionGenerationTombstoneStore interface {
	WasSessionGenerationEnded(ctx context.Context, sessionID, generation string, now time.Time) (bool, error)
	RecordEndedSessionGeneration(ctx context.Context, sessionID, generation string, expiresAt time.Time) error
}

// PGSessionGenerationTombstoneStore stores generation tombstones in Postgres.
type PGSessionGenerationTombstoneStore struct {
	pool *pgxpool.Pool
}

func NewPGSessionGenerationTombstoneStore(pool *pgxpool.Pool) *PGSessionGenerationTombstoneStore {
	return &PGSessionGenerationTombstoneStore{pool: pool}
}

func (s *PGSessionGenerationTombstoneStore) WasSessionGenerationEnded(
	ctx context.Context,
	sessionID, generation string,
	now time.Time,
) (bool, error) {
	if s == nil || s.pool == nil {
		return false, fmt.Errorf("playback session generation tombstone store is unavailable")
	}
	generation = persistedSessionGeneration(generation)
	var ended bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM playback_session_generation_tombstones
			WHERE session_id = $1
			  AND session_generation = $2::uuid
			  AND expires_at > $3
		)
	`, sessionID, generation, now.UTC()).Scan(&ended)
	if err != nil {
		return false, fmt.Errorf("read playback session generation tombstone: %w", err)
	}
	return ended, nil
}

func (s *PGSessionGenerationTombstoneStore) RecordEndedSessionGeneration(
	ctx context.Context,
	sessionID, generation string,
	expiresAt time.Time,
) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("playback session generation tombstone store is unavailable")
	}
	generation = persistedSessionGeneration(generation)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO playback_session_generation_tombstones
			(session_id, session_generation, expires_at)
		VALUES ($1, $2::uuid, $3)
		ON CONFLICT (session_id, session_generation) DO UPDATE SET
			expires_at = GREATEST(playback_session_generation_tombstones.expires_at, EXCLUDED.expires_at)
	`, sessionID, generation, expiresAt.UTC())
	if err != nil {
		return fmt.Errorf("record playback session generation tombstone: %w", err)
	}
	return nil
}
