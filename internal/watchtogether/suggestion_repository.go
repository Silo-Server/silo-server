package watchtogether

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SuggestionRepository implements SuggestionStore using PostgreSQL.
type SuggestionRepository struct {
	pool *pgxpool.Pool
}

func NewSuggestionRepository(pool *pgxpool.Pool) *SuggestionRepository {
	return &SuggestionRepository{pool: pool}
}

func (r *SuggestionRepository) CreateSuggestion(ctx context.Context, s Suggestion) (*Suggestion, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("suggestion repository unavailable")
	}

	const query = `
		INSERT INTO watch_together_suggestions (
			id, room_id, suggester_user_id, suggester_profile_id,
			content_id, content_type, title, subtitle, poster_url, note,
			vote_count, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING
			id, room_id, suggester_user_id, suggester_profile_id,
			content_id, content_type, title, subtitle, poster_url, note,
			vote_count, created_at
	`

	row := r.pool.QueryRow(ctx, query,
		s.ID, s.RoomID, s.SuggesterUserID, s.SuggesterProfileID,
		s.ContentID, s.ContentType, s.Title, s.Subtitle, s.PosterURL, s.Note,
		s.VoteCount, s.CreatedAt,
	)
	return scanSuggestion(row)
}

func (r *SuggestionRepository) GetSuggestion(ctx context.Context, id string) (*Suggestion, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("suggestion repository unavailable")
	}

	const query = `
		SELECT id, room_id, suggester_user_id, suggester_profile_id,
			content_id, content_type, title, subtitle, poster_url, note,
			vote_count, created_at
		FROM watch_together_suggestions
		WHERE id = $1
	`

	row := r.pool.QueryRow(ctx, query, id)
	return scanSuggestion(row)
}

func (r *SuggestionRepository) ListSuggestions(ctx context.Context, roomID string, voterUserID int, voterProfileID string) ([]Suggestion, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("suggestion repository unavailable")
	}

	const query = `
		SELECT s.id, s.room_id, s.suggester_user_id, s.suggester_profile_id,
			s.content_id, s.content_type, s.title, s.subtitle, s.poster_url, s.note,
			s.vote_count, s.created_at,
			(v.suggestion_id IS NOT NULL) AS voted_by_me
		FROM watch_together_suggestions s
		LEFT JOIN watch_together_votes v
			ON v.suggestion_id = s.id AND v.voter_user_id = $2 AND v.voter_profile_id = $3
		WHERE s.room_id = $1
		ORDER BY s.vote_count DESC, s.created_at ASC
	`

	rows, err := r.pool.Query(ctx, query, roomID, voterUserID, voterProfileID)
	if err != nil {
		return nil, fmt.Errorf("list suggestions: %w", err)
	}
	defer rows.Close()

	var suggestions []Suggestion
	for rows.Next() {
		var s Suggestion
		if err := rows.Scan(
			&s.ID, &s.RoomID, &s.SuggesterUserID, &s.SuggesterProfileID,
			&s.ContentID, &s.ContentType, &s.Title, &s.Subtitle, &s.PosterURL, &s.Note,
			&s.VoteCount, &s.CreatedAt,
			&s.VotedByMe,
		); err != nil {
			return nil, fmt.Errorf("scan suggestion row: %w", err)
		}
		suggestions = append(suggestions, s)
	}
	if suggestions == nil {
		suggestions = []Suggestion{}
	}
	return suggestions, rows.Err()
}

func (r *SuggestionRepository) DeleteSuggestion(ctx context.Context, id string) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("suggestion repository unavailable")
	}

	tag, err := r.pool.Exec(ctx, `DELETE FROM watch_together_suggestions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete suggestion: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSuggestionNotFound
	}
	return nil
}

func (r *SuggestionRepository) AddVote(ctx context.Context, suggestionID string, voterUserID int, voterProfileID string) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("suggestion repository unavailable")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin add vote tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`INSERT INTO watch_together_votes (suggestion_id, voter_user_id, voter_profile_id, created_at)
			 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		suggestionID, voterUserID, voterProfileID, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert vote: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDuplicateVote
	}

	_, err = tx.Exec(ctx,
		`UPDATE watch_together_suggestions SET vote_count = vote_count + 1 WHERE id = $1`,
		suggestionID,
	)
	if err != nil {
		return fmt.Errorf("increment vote count: %w", err)
	}

	return tx.Commit(ctx)
}

func (r *SuggestionRepository) RemoveVote(ctx context.Context, suggestionID string, voterUserID int, voterProfileID string) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("suggestion repository unavailable")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin remove vote tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`DELETE FROM watch_together_votes WHERE suggestion_id = $1 AND voter_user_id = $2 AND voter_profile_id = $3`,
		suggestionID, voterUserID, voterProfileID,
	)
	if err != nil {
		return fmt.Errorf("delete vote: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotVoted
	}

	_, err = tx.Exec(ctx,
		`UPDATE watch_together_suggestions SET vote_count = vote_count - 1 WHERE id = $1`,
		suggestionID,
	)
	if err != nil {
		return fmt.Errorf("decrement vote count: %w", err)
	}

	return tx.Commit(ctx)
}

func scanSuggestion(row pgx.Row) (*Suggestion, error) {
	var s Suggestion
	if err := row.Scan(
		&s.ID, &s.RoomID, &s.SuggesterUserID, &s.SuggesterProfileID,
		&s.ContentID, &s.ContentType, &s.Title, &s.Subtitle, &s.PosterURL, &s.Note,
		&s.VoteCount, &s.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSuggestionNotFound
		}
		return nil, fmt.Errorf("scan suggestion: %w", err)
	}
	return &s, nil
}
