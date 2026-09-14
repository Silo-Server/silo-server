package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

type WatchTogetherSuggestionService interface {
	CheckSuggestionRoomProof(string, int, string, string) error
	ListSuggestionPage(context.Context, string, int, string, int, *watchtogether.SuggestionPosition) ([]watchtogether.Suggestion, bool, error)
	SetSuggestionVote(context.Context, string, string, int, string, bool) error
}

type WatchTogetherSuggestionListInput struct {
	RoomID    string `path:"room_id"`
	RoomToken string `header:"X-Room-Token" maxLength:"4096"`
	LimitParam
	Cursor string `query:"cursor"`
}
type WatchTogetherSuggestionVoteInput struct {
	RoomID       string `path:"room_id"`
	SuggestionID string `path:"suggestion_id"`
	RoomToken    string `header:"X-Room-Token" maxLength:"4096"`
}
type WatchTogetherSuggestion struct {
	ID                 ID      `json:"id"`
	RoomID             ID      `json:"room_id"`
	SuggesterUserID    ID      `json:"suggester_user_id"`
	SuggesterProfileID ID      `json:"suggester_profile_id"`
	ContentID          ID      `json:"content_id"`
	ContentType        string  `json:"content_type" enum:"movie,episode"`
	Title              string  `json:"title"`
	Subtitle           string  `json:"subtitle"`
	PosterURL          string  `json:"poster_url"`
	Note               string  `json:"note"`
	VoteCount          int     `json:"vote_count"`
	VotedByMe          bool    `json:"voted_by_me"`
	CreatedAt          Instant `json:"created_at"`
}
type WatchTogetherSuggestionListOutput struct {
	Body Collection[WatchTogetherSuggestion]
}

func suggestionProblem(err error) error {
	switch {
	case errors.Is(err, watchtogether.ErrRoomNotFound), errors.Is(err, watchtogether.ErrSuggestionNotFound):
		return NewProblem(TypeNotFound, "The room or suggestion was not found.")
	case errors.Is(err, watchtogether.ErrRoomClosed):
		return NewProblem(TypeConflict, "The room is closed.")
	default:
		return serviceProblem(err)
	}
}
func (reg *Registry) suggestionProof(ctx context.Context, room, token string) (int, string, error) {
	if reg.deps.WatchTogetherSuggestions == nil {
		return 0, "", unavailable("watch together")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return 0, "", p
	}
	if err := reg.deps.WatchTogetherSuggestions.CheckSuggestionRoomProof(room, user, profile, token); err != nil {
		return 0, "", suggestionProblem(err)
	}
	return user, profile, nil
}
func registerWatchTogetherSuggestions(reg *Registry) {
	const root = Prefix + "/watch-together/rooms/{room_id}/suggestions"
	const listID = "listWatchTogetherSuggestions"
	const creationOrder = "created_at,id"
	cursors := NewCursors(reg.deps.CursorSecret)
	list := Operation{Operation: humaOp(http.MethodGet, root, listID, "realtime", "List room suggestions in bounded creation order. Vote changes do not reorder the live traversal; this is not a snapshot."), Class: ClassProfileScoped, ServiceBacked: true}
	Register(reg, list, func(ctx context.Context, in *WatchTogetherSuggestionListInput) (*WatchTogetherSuggestionListOutput, error) {
		user, profile, err := reg.suggestionProof(ctx, in.RoomID, in.RoomToken)
		if err != nil {
			return nil, err
		}
		scope := CursorScope{OperationID: listID, Security: strconv.Itoa(user) + ":" + profile + ":" + viewerScopeDigest(ctx), Filter: in.RoomID + ":" + strconv.Itoa(in.Limit), Sort: creationOrder, Tiebreaker: "id"}
		var after *watchtogether.SuggestionPosition
		if in.Cursor != "" {
			after = new(watchtogether.SuggestionPosition)
			if p := cursors.Decode(scope, in.Cursor, after); p != nil {
				return nil, p
			}
		}
		rows, more, err := reg.deps.WatchTogetherSuggestions.ListSuggestionPage(ctx, in.RoomID, user, profile, in.Limit, after)
		if err != nil {
			return nil, suggestionProblem(err)
		}
		items := make([]WatchTogetherSuggestion, 0, len(rows))
		for _, s := range rows {
			items = append(items, WatchTogetherSuggestion{ID: ID(s.ID), RoomID: ID(s.RoomID), SuggesterUserID: ID(strconv.Itoa(s.SuggesterUserID)), SuggesterProfileID: ID(s.SuggesterProfileID), ContentID: ID(s.ContentID), ContentType: s.ContentType, Title: s.Title, Subtitle: s.Subtitle, PosterURL: s.PosterURL, Note: s.Note, VoteCount: s.VoteCount, VotedByMe: s.VotedByMe, CreatedAt: NewInstant(s.CreatedAt)})
		}
		next := ""
		if more {
			if len(rows) == 0 {
				return nil, unavailable("suggestion pagination")
			}
			last := rows[len(rows)-1]
			next, err = cursors.Encode(scope, watchtogether.SuggestionPosition{CreatedAt: last.CreatedAt, ID: last.ID})
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &WatchTogetherSuggestionListOutput{Body: Collection[WatchTogetherSuggestion]{Items: items, Page: &PageInfo{HasMore: more, NextCursor: next}}}, nil
	})
	for _, v := range []struct {
		method, id string
		vote       bool
	}{{http.MethodPost, "voteWatchTogetherSuggestion", true}, {http.MethodDelete, "unvoteWatchTogetherSuggestion", false}} {
		op := Operation{Operation: humaOp(v.method, root+"/{suggestion_id}/vote", v.id, "realtime", "Set this profile's vote membership. An already satisfied vote state returns an empty receipt; opposing writes are not ordered."), Class: ClassProfileScoped, ServiceBacked: true, DemoRestricted: isMutatingMethod(v.method), RetrySafety: RetrySafetyNaturalIdempotent}
		if v.vote {
			op.RetrySafety = RetrySafetyUniqueConstraint
		}
		op.DefaultStatus = http.StatusNoContent
		op.Errors = []int{409}
		Register(reg, op, func(ctx context.Context, in *WatchTogetherSuggestionVoteInput) (*struct{}, error) {
			user, profile, err := reg.suggestionProof(ctx, in.RoomID, in.RoomToken)
			if err != nil {
				return nil, err
			}
			if err = reg.deps.WatchTogetherSuggestions.SetSuggestionVote(ctx, in.RoomID, in.SuggestionID, user, profile, v.vote); err != nil {
				return nil, suggestionProblem(err)
			}
			return &struct{}{}, nil
		})
	}
}
