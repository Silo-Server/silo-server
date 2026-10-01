package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	mediacatalog "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
)

// WatchTogetherPickerService computes the "together" rows and resolves them
// to cards the caller may see.
type WatchTogetherPickerService interface {
	CheckSuggestionRoomProof(string, int, string, string) error
	RoomPicker(context.Context, string, int, string, mediacatalog.AccessFilter) (watchtogether.PickerView, error)
}
type WatchTogetherPickerInput struct {
	RoomID    string `path:"room_id"`
	RoomToken string `header:"X-Room-Token" maxLength:"4096"`
}
type WatchTogetherPickerMember struct {
	UserID          ID       `json:"user_id"`
	ProfileID       string   `json:"profile_id"`
	DisplayName     string   `json:"display_name"`
	PositionSeconds *float64 `json:"position_seconds,omitempty"`
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
}
type WatchTogetherPickerNextUp struct {
	ContentID     ID     `json:"content_id"`
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`
	Title         string `json:"title,omitempty"`
	MemberCount   int    `json:"member_count" doc:"Members whose next episode this is"`
}
type WatchTogetherPickerEntry struct {
	Item    CatalogItem                 `json:"item"`
	Members []WatchTogetherPickerMember `json:"members" doc:"Members the row applies to"`
	NextUp  *WatchTogetherPickerNextUp  `json:"next_up,omitempty" doc:"For a series: the episode most members would play next"`
}
type WatchTogetherPicker struct {
	Members          []WatchTogetherRoomMember  `json:"members"`
	ContinueTogether []WatchTogetherPickerEntry `json:"continue_together" doc:"Items two or more connected members are mid-way through, most shared first"`
	WatchlistUnion   []WatchTogetherPickerEntry `json:"watchlist_union" doc:"Items on any connected member's watchlist, most shared first"`
}
type WatchTogetherPickerOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         WatchTogetherPicker
}

func registerWatchTogetherPicker(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/watch-together/rooms/{room_id}/picker", "getWatchTogetherRoomPicker", "realtime", "Read the rows the room picker leads with: what connected members are watching together and the union of their watchlists, resolved to cards the caller may see. Connected members across API servers are included."), Class: ClassProfileScoped, ServiceBacked: true}
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherPickerInput) (*WatchTogetherPickerOutput, error) {
		svc := reg.deps.WatchTogetherPicker
		if svc == nil {
			return nil, unavailable("watch together")
		}
		if reg.deps.CatalogAccess == nil {
			return nil, unavailable("catalog access")
		}
		user, profile, p := viewerIdentity(ctx)
		if p != nil {
			return nil, p
		}
		if err := svc.CheckSuggestionRoomProof(in.RoomID, user, profile, in.RoomToken); err != nil {
			return nil, serviceProblem(err)
		}
		filter, err := reg.deps.CatalogAccess.ContextAccessFilter(ctx, handlers.AccessFilterOptions{})
		if err != nil {
			return nil, collectionProblem(err)
		}
		view, err := svc.RoomPicker(ctx, in.RoomID, user, profile, filter)
		if err != nil {
			return nil, suggestionProblem(err)
		}
		out := &WatchTogetherPickerOutput{CacheControl: adminLogsSocketCacheControl}
		out.Body.Members = watchTogetherMembersOf(view.Members)
		out.Body.ContinueTogether = watchTogetherPickerEntriesOf(view.ContinueTogether)
		out.Body.WatchlistUnion = watchTogetherPickerEntriesOf(view.WatchlistUnion)
		return out, nil
	})
}

func watchTogetherPickerEntriesOf(rows []watchtogether.PickerViewEntry) []WatchTogetherPickerEntry {
	out := make([]WatchTogetherPickerEntry, 0, len(rows))
	for _, row := range rows {
		if row.Item == nil {
			continue
		}
		entry := WatchTogetherPickerEntry{Item: catalogItemDetailOf(row.Item).CatalogItem, Members: make([]WatchTogetherPickerMember, 0, len(row.Members))}
		for _, m := range row.Members {
			entry.Members = append(entry.Members, WatchTogetherPickerMember{UserID: IDFromInt(int64(m.UserID)), ProfileID: m.ProfileID, DisplayName: m.DisplayName, PositionSeconds: m.PositionSeconds, DurationSeconds: m.DurationSeconds})
		}
		if n := row.NextUp; n != nil {
			entry.NextUp = &WatchTogetherPickerNextUp{ContentID: ID(n.ContentID), SeasonNumber: n.SeasonNumber, EpisodeNumber: n.EpisodeNumber, Title: n.Title, MemberCount: n.MemberCount}
		}
		out = append(out, entry)
	}
	return out
}
