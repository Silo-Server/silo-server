package watchtogether

import (
	"context"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Member watch state is what a room may know about its members' viewing so
// the picker can say "you are all mid-way through this" or "Theo has not seen
// episode 3". It is deliberately narrow: only connected room members
// are read, only the content the caller asks about (or the small "together"
// rows the server itself computes) is answered, and nothing about a member's
// history beyond the classified state of those items ever leaves the server.

// MemberWatchStateKind classifies one member's relation to one item.
type MemberWatchStateKind string

const (
	MemberWatchStateUnseen     MemberWatchStateKind = "unseen"
	MemberWatchStateInProgress MemberWatchStateKind = "in_progress"
	MemberWatchStateWatched    MemberWatchStateKind = "watched"
)

// MemberWatchState is one member's state for one item.
type MemberWatchState struct {
	UserID          int
	ProfileID       string
	State           MemberWatchStateKind
	PositionSeconds *float64
	DurationSeconds *float64
	OnWatchlist     bool
}

// ItemMemberState is every connected member's state for one requested item.
type ItemMemberState struct {
	ContentID string
	Members   []MemberWatchState
}

// PickerMember is a member's contribution to a picker row.
type PickerMember struct {
	UserID          int
	ProfileID       string
	DisplayName     string
	PositionSeconds *float64
	DurationSeconds *float64
}

// PickerNextUp is the episode most of the room would play next in a series.
type PickerNextUp struct {
	ContentID     string
	SeasonNumber  int
	EpisodeNumber int
	Title         string
	MemberCount   int
}

// PickerEntry is one row of a server-computed picker list. ContentID names a
// movie or a series; episodes are collapsed to their series.
type PickerEntry struct {
	ContentID string
	Members   []PickerMember
	NextUp    *PickerNextUp
}

// PickerRows are the "together" rows the picker leads with.
type PickerRows struct {
	ContinueTogether []PickerEntry
	WatchlistUnion   []PickerEntry
}

// PickerViewEntry is a picker row resolved to the caller's view of the item.
// Item is nil when the caller may not see it; the adapter drops such rows so
// a member's watching never names an item the caller cannot browse to.
type PickerViewEntry struct {
	Item    *catalog.ItemDetail
	Members []PickerMember
	NextUp  *PickerNextUp
}

// PickerView is the picker resolved for one caller.
type PickerView struct {
	Members          []MemberSummary
	ContinueTogether []PickerViewEntry
	WatchlistUnion   []PickerViewEntry
}

// ItemDetailLookup resolves content ids to the card-level detail the viewer
// may see. The picker shows cards, so it takes the batch card path rather
// than the full item-page build: one artwork resolution and a handful of
// queries for the whole page instead of per-item work.
type ItemDetailLookup interface {
	GetItemCardsByIDs(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) (map[string]*catalog.ItemDetail, error)
}

// ResolvePicker turns picker rows into the caller's view, dropping rows whose
// item the caller cannot see.
func ResolvePicker(ctx context.Context, details ItemDetailLookup, filter catalog.AccessFilter, members []MemberSummary, rows PickerRows) (PickerView, error) {
	view := PickerView{Members: members, ContinueTogether: []PickerViewEntry{}, WatchlistUnion: []PickerViewEntry{}}
	if details == nil {
		return view, nil
	}
	ids := make([]string, 0, len(rows.ContinueTogether)+len(rows.WatchlistUnion))
	for _, row := range rows.ContinueTogether {
		ids = append(ids, row.ContentID)
	}
	for _, row := range rows.WatchlistUnion {
		ids = append(ids, row.ContentID)
	}
	resolved, err := details.GetItemCardsByIDs(ctx, uniqueNonEmpty(ids), filter)
	if err != nil {
		return view, err
	}
	project := func(entries []PickerEntry) []PickerViewEntry {
		out := make([]PickerViewEntry, 0, len(entries))
		for _, entry := range entries {
			item := resolved[entry.ContentID]
			if item == nil {
				continue
			}
			out = append(out, PickerViewEntry{Item: item, Members: entry.Members, NextUp: entry.NextUp})
		}
		return out
	}
	view.ContinueTogether = project(rows.ContinueTogether)
	view.WatchlistUnion = project(rows.WatchlistUnion)
	return view, nil
}

// EpisodeLookup resolves episode content ids to their series.
type EpisodeLookup interface {
	GetByIDs(ctx context.Context, contentIDs []string) ([]*models.Episode, error)
}

// NextUpLister answers "what would this profile play next in this series".
type NextUpLister interface {
	ListNextUp(ctx context.Context, q catalog.NextUpQuery) ([]catalog.NextUpResult, error)
}

const (
	// memberProgressScan bounds how many in-progress rows are read per member
	// when computing the together rows. It is a page, not a history.
	memberProgressScan = 50
	// memberWatchlistScan bounds the watchlist rows read per member.
	memberWatchlistScan = 50
	// MaxMemberStateIDs bounds one member-state read.
	MaxMemberStateIDs = 200
	// pickerContinueLimit and pickerWatchlistLimit cap the rows returned.
	pickerContinueLimit  = 20
	pickerWatchlistLimit = 30
)

// MemberStateReader computes member watch state from the per-user stores.
type MemberStateReader struct {
	provider userstore.UserStoreProvider
	episodes EpisodeLookup
	nextUp   NextUpLister
}

// NewMemberStateReader wires the reader. episodes and nextUp may be nil; the
// reader then cannot collapse episodes to series or name a next-up episode.
func NewMemberStateReader(provider userstore.UserStoreProvider, episodes EpisodeLookup, nextUp NextUpLister) *MemberStateReader {
	if provider == nil {
		return nil
	}
	return &MemberStateReader{provider: provider, episodes: episodes, nextUp: nextUp}
}

// ConnectedMembers reads the shared runtime, including connection leases, so
// picker reads see the same roster regardless of which API server serves them.
func (s *Service) ConnectedMembers(ctx context.Context, roomID string, userID int, profileID string) ([]MemberSummary, error) {
	return withRoomOperation(ctx, s, roomID, func(ctx context.Context) ([]MemberSummary, error) {
		_, live, err := s.getOrLoadLiveRoom(ctx, roomID)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if live.room.Phase == RoomPhaseEnded {
			return nil, ErrRoomClosed
		}
		return s.buildSnapshotLocked(live, userID, profileID).Members, nil
	})
}

// memberProgress is one member's bounded in-progress page, resolved to
// series where the row is an episode.
type memberProgress struct {
	member   MemberSummary
	rows     []userstore.WatchProgress
	seriesOf map[string]*models.Episode // episode content id → episode
}

// MemberState classifies every requested content id for every member. Series
// ids are in_progress when the member has an in-progress episode of that
// series; movies and episodes use the progress row itself, with completed
// history folded in.
func (r *MemberStateReader) MemberState(ctx context.Context, members []MemberSummary, contentIDs []string) ([]ItemMemberState, error) {
	ids := uniqueNonEmpty(contentIDs)
	out := make([]ItemMemberState, 0, len(ids))
	if r == nil || len(ids) == 0 {
		return out, nil
	}
	byID := make(map[string]*ItemMemberState, len(ids))
	for _, id := range ids {
		entry := ItemMemberState{ContentID: id, Members: make([]MemberWatchState, 0, len(members))}
		out = append(out, entry)
		byID[id] = &out[len(out)-1]
	}
	for _, member := range members {
		store, err := r.provider.ForUser(ctx, member.UserID)
		if err != nil || store == nil {
			return nil, err
		}
		progress, err := userstore.ListProgressWithCompletedHistory(ctx, store, member.ProfileID, ids)
		if err != nil {
			return nil, err
		}
		watchlist, err := store.ListWatchlistByMediaItems(ctx, member.ProfileID, ids)
		if err != nil {
			return nil, err
		}
		// A series id has no progress row of its own. It is in progress for a
		// member when one of their in-progress episodes belongs to it.
		seriesInProgress := map[string]userstore.WatchProgress{}
		if page, err := r.loadMemberProgress(ctx, store, member); err == nil {
			for _, row := range page.rows {
				if ep := page.seriesOf[row.MediaItemID]; ep != nil {
					if prev, ok := seriesInProgress[ep.SeriesID]; !ok || row.UpdatedAt > prev.UpdatedAt {
						seriesInProgress[ep.SeriesID] = row
					}
				}
			}
		} else {
			return nil, err
		}
		for _, id := range ids {
			state := MemberWatchState{UserID: member.UserID, ProfileID: member.ProfileID, State: MemberWatchStateUnseen, OnWatchlist: watchlist[id]}
			if row, ok := progress[id]; ok {
				state = classifyProgress(state, row)
			} else if row, ok := seriesInProgress[id]; ok {
				state = classifyProgress(state, row)
			}
			byID[id].Members = append(byID[id].Members, state)
		}
	}
	return out, nil
}

func classifyProgress(state MemberWatchState, row userstore.WatchProgress) MemberWatchState {
	switch {
	case row.Completed:
		state.State = MemberWatchStateWatched
	case row.PositionSeconds > 0:
		state.State = MemberWatchStateInProgress
		position, duration := row.PositionSeconds, row.DurationSeconds
		state.PositionSeconds = &position
		if duration > 0 {
			state.DurationSeconds = &duration
		}
	}
	return state
}

func (r *MemberStateReader) loadMemberProgress(ctx context.Context, store userstore.UserStore, member MemberSummary) (memberProgress, error) {
	page := memberProgress{member: member, seriesOf: map[string]*models.Episode{}}
	rows, err := store.ListProgress(ctx, member.ProfileID, "in_progress", memberProgressScan, 0)
	if err != nil {
		return page, err
	}
	page.rows = rows
	if r.episodes == nil || len(rows) == 0 {
		return page, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.MediaItemID)
	}
	page.seriesOf, err = r.episodesByID(ctx, ids)
	return page, err
}

func (r *MemberStateReader) episodesByID(ctx context.Context, ids []string) (map[string]*models.Episode, error) {
	byID := map[string]*models.Episode{}
	if r.episodes == nil || len(ids) == 0 {
		return byID, nil
	}
	episodes, err := r.episodes.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, ep := range episodes {
		if ep != nil && ep.SeriesID != "" {
			byID[ep.ContentID] = ep
		}
	}
	return byID, nil
}

// Picker computes the rows the room's picker opens with: what two or more
// members are mid-way through, and the union of their watchlists.
func (r *MemberStateReader) Picker(ctx context.Context, members []MemberSummary) (PickerRows, error) {
	rows := PickerRows{ContinueTogether: []PickerEntry{}, WatchlistUnion: []PickerEntry{}}
	if r == nil || len(members) == 0 {
		return rows, nil
	}
	type unitState struct {
		entry   PickerEntry
		latest  string
		perMemb map[string]userstore.WatchProgress // member key → their row for this unit
	}
	units := map[string]*unitState{}
	stores := map[string]userstore.UserStore{}
	for _, member := range members {
		store, err := r.provider.ForUser(ctx, member.UserID)
		if err != nil || store == nil {
			return rows, err
		}
		stores[memberKey(member)] = store
		page, err := r.loadMemberProgress(ctx, store, member)
		if err != nil {
			return rows, err
		}
		seen := map[string]bool{}
		for _, row := range page.rows {
			if row.PositionSeconds <= 0 {
				continue
			}
			unitID := row.MediaItemID
			if ep := page.seriesOf[row.MediaItemID]; ep != nil {
				unitID = ep.SeriesID
			}
			if seen[unitID] {
				continue
			}
			seen[unitID] = true
			unit := units[unitID]
			if unit == nil {
				unit = &unitState{entry: PickerEntry{ContentID: unitID}, perMemb: map[string]userstore.WatchProgress{}}
				units[unitID] = unit
			}
			position, duration := row.PositionSeconds, row.DurationSeconds
			pm := PickerMember{UserID: member.UserID, ProfileID: member.ProfileID, DisplayName: member.DisplayName, PositionSeconds: &position}
			if duration > 0 {
				pm.DurationSeconds = &duration
			}
			unit.entry.Members = append(unit.entry.Members, pm)
			unit.perMemb[memberKey(member)] = row
			if row.UpdatedAt > unit.latest {
				unit.latest = row.UpdatedAt
			}
		}
	}
	together := make([]*unitState, 0, len(units))
	for _, unit := range units {
		if len(unit.entry.Members) >= 2 {
			together = append(together, unit)
		}
	}
	sort.Slice(together, func(i, j int) bool {
		if len(together[i].entry.Members) != len(together[j].entry.Members) {
			return len(together[i].entry.Members) > len(together[j].entry.Members)
		}
		if together[i].latest != together[j].latest {
			return together[i].latest > together[j].latest
		}
		return together[i].entry.ContentID < together[j].entry.ContentID
	})
	if len(together) > pickerContinueLimit {
		together = together[:pickerContinueLimit]
	}
	for _, unit := range together {
		entry := unit.entry
		entry.NextUp = r.modalNextUp(ctx, members, unit.entry.ContentID, unit.perMemb)
		rows.ContinueTogether = append(rows.ContinueTogether, entry)
	}

	type listState struct {
		entry  PickerEntry
		newest string
	}
	lists := map[string]*listState{}
	for _, member := range members {
		store := stores[memberKey(member)]
		entries, err := store.ListWatchlist(ctx, member.ProfileID, memberWatchlistScan, 0)
		if err != nil {
			return rows, err
		}
		ids := make([]string, 0, len(entries))
		for _, entry := range entries {
			ids = append(ids, entry.MediaItemID)
		}
		episodes, err := r.episodesByID(ctx, ids)
		if err != nil {
			return rows, err
		}
		seen := map[string]bool{}
		for _, entry := range entries {
			unitID := entry.MediaItemID
			if ep := episodes[unitID]; ep != nil {
				unitID = ep.SeriesID
			}
			state := lists[unitID]
			if state == nil {
				state = &listState{entry: PickerEntry{ContentID: unitID}}
				lists[unitID] = state
			}
			if !seen[unitID] {
				state.entry.Members = append(state.entry.Members, PickerMember{UserID: member.UserID, ProfileID: member.ProfileID, DisplayName: member.DisplayName})
				seen[unitID] = true
			}
			if entry.AddedAt > state.newest {
				state.newest = entry.AddedAt
			}
		}
	}
	union := make([]*listState, 0, len(lists))
	for _, state := range lists {
		union = append(union, state)
	}
	sort.Slice(union, func(i, j int) bool {
		if len(union[i].entry.Members) != len(union[j].entry.Members) {
			return len(union[i].entry.Members) > len(union[j].entry.Members)
		}
		if union[i].newest != union[j].newest {
			return union[i].newest > union[j].newest
		}
		return union[i].entry.ContentID < union[j].entry.ContentID
	})
	if len(union) > pickerWatchlistLimit {
		union = union[:pickerWatchlistLimit]
	}
	for _, state := range union {
		rows.WatchlistUnion = append(rows.WatchlistUnion, state.entry)
	}
	return rows, nil
}

// modalNextUp names the episode most members of a shared series would play
// next: a member's own in-progress episode if they have one, else their next
// unwatched episode. Members with no answer do not vote. Nil for movies.
func (r *MemberStateReader) modalNextUp(ctx context.Context, members []MemberSummary, unitID string, inProgress map[string]userstore.WatchProgress) *PickerNextUp {
	if r.episodes == nil {
		return nil
	}
	// A unit is a series only if the members' rows resolved to episodes; a
	// movie unit has rows keyed by the movie itself.
	episodeIDs := make([]string, 0, len(inProgress))
	for _, row := range inProgress {
		episodeIDs = append(episodeIDs, row.MediaItemID)
	}
	episodes, err := r.episodes.GetByIDs(ctx, episodeIDs)
	if err != nil {
		return nil
	}
	byID := map[string]*models.Episode{}
	for _, ep := range episodes {
		if ep != nil && ep.SeriesID == unitID {
			byID[ep.ContentID] = ep
		}
	}
	if len(byID) == 0 {
		return nil
	}
	type candidate struct {
		ep    *models.Episode
		count int
	}
	votes := map[string]*candidate{}
	vote := func(ep *models.Episode) {
		if ep == nil {
			return
		}
		c := votes[ep.ContentID]
		if c == nil {
			c = &candidate{ep: ep}
			votes[ep.ContentID] = c
		}
		c.count++
	}
	for _, member := range members {
		if row, ok := inProgress[memberKey(member)]; ok {
			if ep := byID[row.MediaItemID]; ep != nil {
				vote(ep)
				continue
			}
		}
		if r.nextUp == nil {
			continue
		}
		results, err := r.nextUp.ListNextUp(ctx, catalog.NextUpQuery{UserID: member.UserID, ProfileID: member.ProfileID, SeriesID: unitID, Limit: 1, EnableResumable: true})
		if err != nil || len(results) == 0 {
			continue
		}
		res := results[0]
		vote(&models.Episode{ContentID: res.ContentID, SeriesID: res.SeriesID, SeasonNumber: res.SeasonNumber, EpisodeNumber: res.EpisodeNumber})
	}
	var best *candidate
	for _, c := range votes {
		if best == nil || c.count > best.count || (c.count == best.count && episodeOrdinal(c.ep) < episodeOrdinal(best.ep)) {
			best = c
		}
	}
	if best == nil {
		return nil
	}
	title := best.ep.Title
	if title == "" {
		// An unwatched next-up episode may not appear in any progress page.
		ep := byID[best.ep.ContentID]
		if ep == nil {
			if loaded, err := r.episodesByID(ctx, []string{best.ep.ContentID}); err == nil {
				ep = loaded[best.ep.ContentID]
			}
		}
		if ep != nil && ep.SeriesID == unitID {
			title = ep.Title
		}
	}
	return &PickerNextUp{ContentID: best.ep.ContentID, SeasonNumber: best.ep.SeasonNumber, EpisodeNumber: best.ep.EpisodeNumber, Title: title, MemberCount: best.count}
}

func episodeOrdinal(ep *models.Episode) int {
	if ep == nil {
		return 0
	}
	return ep.SeasonNumber*100000 + ep.EpisodeNumber
}

func memberKey(m MemberSummary) string { return buildMemberKey(m.UserID, m.ProfileID) }

func uniqueNonEmpty(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
