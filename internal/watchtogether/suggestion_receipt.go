package watchtogether

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	suggestionEpisodeType = "episode"
	suggestionMovieType   = "movie"
)

var ErrSuggestionIdentityConflict = errors.New("suggestion identity was already used")

// CreateSuggestionOnce atomically binds a v2 caller-selected identity to its
// original request. A retained receipt prevents resurrection after deletion.
// Legacy CreateSuggestion intentionally keeps its original insert behavior.
func (r *SuggestionRepository) CreateSuggestionOnce(ctx context.Context, suggestion Suggestion) (*Suggestion, bool, error) {
	if r == nil || r.pool == nil {
		return nil, false, fmt.Errorf("suggestion repository unavailable")
	}
	if suggestion.ID == "" || suggestion.RoomID == "" || suggestion.SuggesterUserID <= 0 || suggestion.SuggesterProfileID == "" || suggestion.ContentID == "" || suggestion.Title == "" || (suggestion.ContentType != suggestionMovieType && suggestion.ContentType != suggestionEpisodeType) {
		return nil, false, ErrInvalidSelection
	}
	payload, err := json.Marshal(struct{ ContentID, ContentType, Title, Subtitle, PosterURL, Note string }{suggestion.ContentID, suggestion.ContentType, suggestion.Title, suggestion.Subtitle, suggestion.PosterURL, suggestion.Note})
	if err != nil {
		return nil, false, err
	}
	digest := sha256.Sum256(payload)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var phase string
	if err = tx.QueryRow(ctx, `SELECT phase FROM watch_together_rooms WHERE id=$1 FOR UPDATE`, suggestion.RoomID).Scan(&phase); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrRoomNotFound
		}
		return nil, false, err
	}
	if phase == string(RoomPhaseEnded) {
		return nil, false, ErrRoomClosed
	}
	tag, err := tx.Exec(ctx, `INSERT INTO watch_together_suggestion_receipts(suggestion_id,user_id,profile_id,room_id,request_digest) VALUES($1,$2,$3,$4,$5) ON CONFLICT(suggestion_id) DO NOTHING`, suggestion.ID, suggestion.SuggesterUserID, suggestion.SuggesterProfileID, suggestion.RoomID, digest[:])
	if err != nil {
		return nil, false, err
	}
	inserted := tag.RowsAffected() == 1
	if !inserted {
		var user int
		var profile, room string
		var original []byte
		if err = tx.QueryRow(ctx, `SELECT user_id,profile_id,room_id,request_digest FROM watch_together_suggestion_receipts WHERE suggestion_id=$1 FOR UPDATE`, suggestion.ID).Scan(&user, &profile, &room, &original); err != nil {
			return nil, false, err
		}
		if user != suggestion.SuggesterUserID || profile != suggestion.SuggesterProfileID || room != suggestion.RoomID || !bytes.Equal(original, digest[:]) {
			return nil, false, ErrSuggestionIdentityConflict
		}
	}
	const columns = `id,room_id,suggester_user_id,suggester_profile_id,content_id,content_type,title,subtitle,poster_url,note,vote_count,created_at`
	var result *Suggestion
	if inserted {
		result, err = scanSuggestion(tx.QueryRow(ctx, `INSERT INTO watch_together_suggestions (`+columns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,0,$11) ON CONFLICT(id) DO NOTHING RETURNING `+columns, suggestion.ID, suggestion.RoomID, suggestion.SuggesterUserID, suggestion.SuggesterProfileID, suggestion.ContentID, suggestion.ContentType, suggestion.Title, suggestion.Subtitle, suggestion.PosterURL, suggestion.Note, suggestion.CreatedAt.UTC()))
	} else {
		result, err = scanSuggestion(tx.QueryRow(ctx, `SELECT `+columns+` FROM watch_together_suggestions WHERE id=$1 FOR UPDATE`, suggestion.ID))
	}
	if errors.Is(err, ErrSuggestionNotFound) {
		return nil, false, ErrSuggestionIdentityConflict
	}
	if err != nil {
		return nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return result, inserted, nil
}
