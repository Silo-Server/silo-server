package watchtogether

import (
	"context"
	"fmt"
	"time"
)

type SuggestionPosition struct {
	CreatedAt time.Time
	ID        string
}

// ListSuggestionsPage uses immutable creation order, so changes in vote counts
// do not move rows behind a continuation. It is a live list, not a snapshot.
func (r *SuggestionRepository) ListSuggestionsPage(ctx context.Context, roomID string, userID int, profileID string, limit int, after *SuggestionPosition) ([]Suggestion, bool, error) {
	if r == nil || r.pool == nil {
		return nil, false, fmt.Errorf("suggestion repository unavailable")
	}
	if limit < 1 || limit > 200 {
		return nil, false, fmt.Errorf("invalid suggestion page size")
	}
	var created *time.Time
	id := ""
	if after != nil {
		created = &after.CreatedAt
		id = after.ID
	}
	rows, err := r.pool.Query(ctx, `SELECT s.id,s.room_id,s.suggester_user_id,s.suggester_profile_id,s.content_id,s.content_type,s.title,s.subtitle,s.poster_url,s.note,s.vote_count,s.created_at,(v.suggestion_id IS NOT NULL)
	 FROM watch_together_suggestions s LEFT JOIN watch_together_votes v ON v.suggestion_id=s.id AND v.voter_user_id=$2 AND v.voter_profile_id=$3
	 WHERE s.room_id=$1 AND ($4::timestamptz IS NULL OR (s.created_at,s.id)>($4,$5)) ORDER BY s.created_at,s.id LIMIT $6`, roomID, userID, profileID, created, id, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []Suggestion{}
	for rows.Next() {
		var s Suggestion
		if err := rows.Scan(&s.ID, &s.RoomID, &s.SuggesterUserID, &s.SuggesterProfileID, &s.ContentID, &s.ContentType, &s.Title, &s.Subtitle, &s.PosterURL, &s.Note, &s.VoteCount, &s.CreatedAt, &s.VotedByMe); err != nil {
			return nil, false, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

func (s *Service) ListSuggestionsPage(ctx context.Context, room string, userID int, profile string, limit int, after *SuggestionPosition) ([]Suggestion, bool, error) {
	if s == nil || s.suggestions == nil {
		return nil, false, fmt.Errorf("watch together suggestions unavailable")
	}
	if _, _, err := s.getOrLoadLiveRoom(ctx, room); err != nil {
		return nil, false, err
	}
	return s.suggestions.ListSuggestionsPage(ctx, room, userID, profile, limit, after)
}
