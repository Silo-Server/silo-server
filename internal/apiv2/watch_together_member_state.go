package apiv2

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

// WatchTogetherMemberStateService answers per-member watch state for content
// the caller may access. The member set comes from the shared room runtime.
type WatchTogetherMemberStateService interface {
	CheckSuggestionRoomProof(string, int, string, string) error
	RoomMemberState(context.Context, string, int, string, []string, catalogpkg.AccessFilter) ([]watchtogether.MemberSummary, []watchtogether.ItemMemberState, error)
}
type WatchTogetherMemberStateInput struct {
	RoomID    string `path:"room_id"`
	RoomToken string `header:"X-Room-Token" maxLength:"4096"`
	Body      struct {
		ContentIDs []ID `json:"content_ids" minItems:"1" maxItems:"200"`
	}
}
type WatchTogetherMemberWatchState struct {
	UserID          ID       `json:"user_id"`
	ProfileID       string   `json:"profile_id"`
	State           string   `json:"state" enum:"unseen,in_progress,watched"`
	PositionSeconds *float64 `json:"position_seconds,omitempty"`
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
	OnWatchlist     bool     `json:"on_watchlist"`
}
type WatchTogetherItemMemberState struct {
	ContentID ID                              `json:"content_id"`
	Members   []WatchTogetherMemberWatchState `json:"members"`
}
type WatchTogetherMemberState struct {
	Members []WatchTogetherRoomMember      `json:"members" doc:"Connected room members across API servers, host first"`
	Items   []WatchTogetherItemMemberState `json:"items" doc:"One entry per distinct accessible requested content id, in request order"`
}
type WatchTogetherMemberStateOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         WatchTogetherMemberState
}

func registerWatchTogetherMemberState(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/watch-together/rooms/{room_id}/member-state", "queryWatchTogetherMemberState", "realtime", "Read what each connected room member has watched of the named content: unseen, in progress (with position) or watched, and whether it is on their watchlist. A POST-shaped read: the id set exceeds what a query string carries. Members across API servers are read; inaccessible content ids are omitted."), Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.MaxBodyBytes = 16384
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherMemberStateInput) (*WatchTogetherMemberStateOutput, error) {
		svc := reg.deps.WatchTogetherMemberState
		if svc == nil {
			return nil, unavailable("watch together")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		if err := svc.CheckSuggestionRoomProof(in.RoomID, user, profile, in.RoomToken); err != nil {
			return nil, serviceProblem(err)
		}
		if reg.deps.CatalogAccess == nil {
			return nil, unavailable("catalog access")
		}
		filter, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
		if err != nil {
			return nil, collectionProblem(err)
		}
		ids := make([]string, 0, len(in.Body.ContentIDs))
		for i, id := range in.Body.ContentIDs {
			if id == "" {
				return nil, NewProblem(TypeValidationFailed, "Content ids must be non-empty.").WithErrors(ProblemError{Location: "body.content_ids[" + strconv.Itoa(i) + "]", Code: codeInvalid})
			}
			ids = append(ids, string(id))
		}
		members, items, err := svc.RoomMemberState(ctx, in.RoomID, user, profile, ids, filter)
		if err != nil {
			return nil, suggestionProblem(err)
		}
		out := &WatchTogetherMemberStateOutput{CacheControl: adminLogsSocketCacheControl}
		out.Body.Members = watchTogetherMembersOf(members)
		out.Body.Items = make([]WatchTogetherItemMemberState, 0, len(items))
		for _, item := range items {
			entry := WatchTogetherItemMemberState{ContentID: ID(item.ContentID), Members: make([]WatchTogetherMemberWatchState, 0, len(item.Members))}
			for _, m := range item.Members {
				entry.Members = append(entry.Members, WatchTogetherMemberWatchState{UserID: IDFromInt(int64(m.UserID)), ProfileID: m.ProfileID, State: string(m.State), PositionSeconds: m.PositionSeconds, DurationSeconds: m.DurationSeconds, OnWatchlist: m.OnWatchlist})
			}
			out.Body.Items = append(out.Body.Items, entry)
		}
		return out, nil
	})
}

func watchTogetherMembersOf(rows []watchtogether.MemberSummary) []WatchTogetherRoomMember {
	out := make([]WatchTogetherRoomMember, 0, len(rows))
	for _, m := range rows {
		out = append(out, WatchTogetherRoomMember{UserID: IDFromInt(int64(m.UserID)), ProfileID: m.ProfileID, DisplayName: m.DisplayName, IsHost: m.IsHost, IsSelf: m.IsSelf, Connected: m.Connected, LobbyReady: m.LobbyReady})
	}
	return out
}
