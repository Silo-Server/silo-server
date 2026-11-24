package watchsync

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// A provider's watchlist can list titles the library doesn't have. Silo keeps
// those as watchlist entries outside the library (the watchlist's unavailable
// side), so the import keeps them there too instead of skipping them. Their
// shadow rows carry a synthetic media item id naming the title, which no
// library item can hold, so the shared list pipeline tracks them like any
// other row: an entry added in Silo is exported, a provider removal takes the
// entry off, and a local removal goes to the provider when removals sync.

// listTitlePrefix starts the media item id of a shadow row for a watchlist
// entry outside the library: "watchlist-title:<kind>:<tmdb id>".
const listTitlePrefix = "watchlist-title:"

// WatchlistTitle names a movie or series on a watchlist by its TMDB ID, with
// the other IDs and display fields the provider reported.
type WatchlistTitle struct {
	Kind   string // historyimport.KindMovie or historyimport.KindSeries
	TMDBID int
	IMDbID string
	TVDBID int
	Title  string
	Year   int
	// AddedAt is when the entry joined the watchlist, on a listed entry.
	AddedAt time.Time
}

// WatchlistTitles keeps the watchlist entries for titles the library doesn't
// have. *watchlist.Titles provides it through WatchSync.
type WatchlistTitles interface {
	// LibraryHas reports whether any library item carries the title's IDs.
	LibraryHas(ctx context.Context, title WatchlistTitle) (bool, error)
	// AddWatchlistTitle keeps the title as the profile's watchlist entry,
	// keeping an existing entry's added_at. It reports whether it created one.
	// With request set, a new entry is requested the way an entry added by
	// hand is.
	AddWatchlistTitle(ctx context.Context, userID int, profileID string, title WatchlistTitle, addedAt time.Time, request bool) (bool, error)
	// RemoveWatchlistTitle takes the profile's entry for the title off.
	RemoveWatchlistTitle(ctx context.Context, userID int, profileID string, title WatchlistTitle) error
	// RequestQueuedWatchlistTitles applies watchlist requests, in order, to
	// the listed titles, so a title a request quota refused at import is
	// requested on a later run.
	RequestQueuedWatchlistTitles(ctx context.Context, userID int, profileID string, titles []WatchlistTitle) error
	// ListWatchlistTitles returns every entry of the profile.
	ListWatchlistTitles(ctx context.Context, userID int, profileID string) ([]WatchlistTitle, error)
	// WatchlistTitleIDs returns the prefixed IDs ("tmdb:550",
	// "imdb:tt0137523", "tvdb:121361") the profile's entries for the titles
	// have held, current and former, reached through any TMDB ID the entry's
	// title has held. A title without an entry has none.
	WatchlistTitleIDs(ctx context.Context, userID int, profileID string, titles []WatchlistTitle) (map[WatchlistTitle][]string, error)
	// PromotedWatchlistItems returns the library items on the profile's
	// library watchlist that carry the title's IDs or any of ids, where
	// promotion moves an entry once the library has its title.
	PromotedWatchlistItems(ctx context.Context, userID int, profileID string, title WatchlistTitle, ids []string) ([]string, error)
}

type watchlistTitlesRef struct{ WatchlistTitles }

// SetWatchlistTitles wires the watchlist entries for titles the library
// doesn't have. It may run after syncs started; until then, a watchlist
// import skips those titles as unmatched.
func (s *Service) SetWatchlistTitles(titles WatchlistTitles) {
	if s == nil {
		return
	}
	if titles == nil {
		s.titles.Store(nil)
		return
	}
	s.titles.Store(&watchlistTitlesRef{titles})
}

func (s *Service) watchlistTitles(b listBinding) WatchlistTitles {
	if b.kind != ListKindWatchlist {
		return nil
	}
	if ref := s.titles.Load(); ref != nil {
		return ref.WatchlistTitles
	}
	return nil
}

func (t WatchlistTitle) listItemID() string {
	return listTitlePrefix + t.Kind + ":" + strconv.Itoa(t.TMDBID)
}

// IDs returns the title's prefixed IDs, the form ListItemState.TitleIDs keeps.
func (t WatchlistTitle) IDs() []string {
	ids := []string{prefixedID("tmdb", strconv.Itoa(t.TMDBID))}
	if t.IMDbID != "" {
		ids = append(ids, prefixedID("imdb", t.IMDbID))
	}
	if t.TVDBID > 0 {
		ids = append(ids, prefixedID("tvdb", strconv.Itoa(t.TVDBID)))
	}
	return ids
}

// keptTitleIDs returns the IDs a title row knows its title by: the ones it
// kept, the TMDB ID it is named for (a row from before title IDs were kept
// may have no others), and its provider key, which names one of them when it
// is an IMDb, TMDB, or TVDB ID.
func (state ListItemState) keptTitleIDs() []string {
	ids := append(slices.Clone(state.TitleIDs), state.ProviderItemKey)
	if title, ok := watchlistTitleFromListItemID(state.MediaItemID); ok {
		ids = append(ids, title.IDs()...)
	}
	return ids
}

// watchlistTitleFromRow returns the title a provider row names when it is a
// movie or series with a TMDB ID.
func watchlistTitleFromRow(row RemoteFavorite) (WatchlistTitle, bool) {
	if row.Kind != historyimport.KindMovie && row.Kind != historyimport.KindSeries {
		return WatchlistTitle{}, false
	}
	tmdbID, err := strconv.Atoi(strings.TrimSpace(row.TMDBID))
	if err != nil || tmdbID <= 0 {
		return WatchlistTitle{}, false
	}
	tvdbID, _ := strconv.Atoi(strings.TrimSpace(row.TVDBID))
	return WatchlistTitle{
		Kind:   row.Kind,
		TMDBID: tmdbID,
		IMDbID: strings.TrimSpace(row.IMDbID),
		TVDBID: max(tvdbID, 0),
		Title:  row.Title,
		Year:   row.Year,
	}, true
}

// watchlistTitleFromListItemID parses a shadow row's synthetic media item id.
func watchlistTitleFromListItemID(id string) (WatchlistTitle, bool) {
	rest, ok := strings.CutPrefix(id, listTitlePrefix)
	if !ok {
		return WatchlistTitle{}, false
	}
	kind, tmdb, ok := strings.Cut(rest, ":")
	if !ok || (kind != historyimport.KindMovie && kind != historyimport.KindSeries) {
		return WatchlistTitle{}, false
	}
	tmdbID, err := strconv.Atoi(tmdb)
	if err != nil || tmdbID <= 0 {
		return WatchlistTitle{}, false
	}
	return WatchlistTitle{Kind: kind, TMDBID: tmdbID}, true
}

// importListTitle keeps a row that matched no library item as a watchlist
// entry outside the library, when it names a title with a TMDB ID that no
// library item carries. A title some library item carries is left unmatched:
// the matcher found it ambiguous, and an entry outside the library would
// show it as missing. It returns the row's shadow state, or nil when the row
// stays unmatched or waits for a local removal to reach the provider. A new
// entry is requested only with request set; see listTitlesSince. An add that
// kept the entry and then failed returns the state with the error.
func (s *Service) importListTitle(
	ctx context.Context,
	conn Connection,
	b listBinding,
	row RemoteFavorite,
	key string,
	removalsPending map[string]time.Time,
	pendingTitles map[string][]string,
	request bool,
) (state *ListItemState, kept, added bool, err error) {
	titles := s.watchlistTitles(b)
	if titles == nil {
		return nil, false, false, nil
	}
	title, ok := watchlistTitleFromRow(row)
	if !ok {
		return nil, false, false, nil
	}
	inLibrary, err := titles.LibraryHas(ctx, title)
	if err != nil || inLibrary {
		return nil, false, false, err
	}
	id := title.listItemID()
	// A pending library row counts too: the library lost the item, and the
	// listing now names its title outside the library.
	pending := pendingTitleRows(pendingTitles, title.Kind, append(title.IDs(), key))
	waits, err := s.waitsForListRemoval(ctx, conn, b, id, pending, row, key, removalsPending)
	if err != nil || waits {
		return nil, waits, false, err
	}
	added, err = titles.AddWatchlistTitle(ctx, conn.UserID, conn.ProfileID, title, row.FavoritedAt, request)
	if err != nil && !added {
		return nil, false, false, err
	}
	listedAt := row.FavoritedAt
	return &ListItemState{
		ConnectionID:     conn.ID,
		ListKind:         b.kind,
		MediaItemID:      id,
		ProviderItemKey:  key,
		Kind:             row.Kind,
		Title:            row.Title,
		Year:             row.Year,
		RemotePresent:    true,
		LocalPresent:     true,
		LastSeenRemoteAt: &listedAt,
		LastSeenLocalAt:  &listedAt,
		TitleIDs:         title.IDs(),
	}, true, added, err
}

// trackListTitle imports a row that matched no library item through
// importListTitle, writes its shadow state right away, and counts it in
// result. It reports whether the row was kept.
func (s *Service) trackListTitle(
	ctx context.Context,
	conn Connection,
	b listBinding,
	row RemoteFavorite,
	key string,
	removalsPending map[string]time.Time,
	pendingTitles map[string][]string,
	request bool,
	result *ImportListResult,
) (bool, error) {
	state, kept, added, err := s.importListTitle(ctx, conn, b, row, key, removalsPending, pendingTitles, request)
	// Counted first: the entry is saved, so a run that fails below still
	// reports it.
	if added {
		result.added++
	}
	if state != nil {
		result.Imported++
		// An entry the add kept is tracked even when the add then failed, so
		// a later provider removal still finds it.
		if upsertErr := s.repo.UpsertListItemStates(ctx, []ListItemState{*state}); upsertErr != nil {
			return kept, errors.Join(err, upsertErr)
		}
	}
	return kept, err
}

// waitsForMatchedListRemoval reports whether a listing that matched a library
// item waits for a local removal to reach the provider: the item's own, or
// one of its title as a watchlist entry outside the library, from before the
// library got the title. Other library items with the title's IDs don't hold
// it, as they don't hold each other's removals on export. A listing doesn't
// come back while the item is on the list, so a title's removal holds it only
// while the item isn't; on the list, the import tracks the item as before.
func (s *Service) waitsForMatchedListRemoval(
	ctx context.Context,
	conn Connection,
	store userstore.UserStore,
	b listBinding,
	mediaItemID string,
	row RemoteFavorite,
	ids []string,
	removalsPending map[string]time.Time,
	pendingTitles map[string][]string,
) (bool, error) {
	// Only a title row still waiting is worth the watchlist read below.
	pending := slices.DeleteFunc(pendingTitleRows(pendingTitles, row.Kind, ids), func(id string) bool {
		_, isTitle := watchlistTitleFromListItemID(id)
		_, waiting := removalsPending[id]
		return !isTitle || !waiting
	})
	if len(pending) > 0 {
		listed, err := store.InWatchlist(ctx, conn.ProfileID, mediaItemID)
		if err != nil {
			return false, err
		}
		if listed {
			pending = nil
		}
	}
	// ids starts with the listing's provider key.
	return s.waitsForListRemoval(ctx, conn, b, mediaItemID, pending, row, ids[0], removalsPending)
}

// waitsForListRemoval reports whether a listing the import would keep as id
// waits for a local removal of its title to reach the provider: id's own, or
// a pending row's that names the same title. A title's removal can wait under
// another TMDB ID when the provider moved the title to a new one, under
// several when the title had more than one row, and on the title's other
// form, a library item or an entry outside the library. As for library
// items, the provider listing the title again after every local removal is a
// newer add, which supersedes the removals waiting on the other rows so they
// can't take the add off the provider; an older or undated listing is the one
// the removals will take off, so they go out under key, the listing's.
func (s *Service) waitsForListRemoval(
	ctx context.Context,
	conn Connection,
	b listBinding,
	id string,
	pending []string,
	row RemoteFavorite,
	key string,
	removalsPending map[string]time.Time,
) (bool, error) {
	if !slices.Contains(pending, id) {
		pending = append(pending, id)
	}
	var superseded []string
	for _, pendingID := range pending {
		removedAt, ok := removalsPending[pendingID]
		switch {
		case !ok:
			continue
		case row.FavoritedAtGuessed || !row.FavoritedAt.After(removedAt):
			return true, s.moveListRemovals(ctx, conn, b, pending, key, removalsPending)
		case pendingID != id:
			superseded = append(superseded, pendingID)
		}
	}
	for _, pendingID := range superseded {
		if err := s.repo.MarkListItemRemoteRemoved(ctx, conn.ID, b.kind, pendingID, s.now()); err != nil {
			return false, err
		}
		delete(removalsPending, pendingID)
	}
	// The listing puts id back on the list, so a later listing of the title
	// in this batch doesn't take it for a removal still waiting.
	delete(removalsPending, id)
	return false, nil
}

// moveListRemovals puts the pending removals among rows under key, the key of
// the listing they will take off. A removal sends the provider only its row's
// key, which may name an ID the provider no longer lists the title by.
func (s *Service) moveListRemovals(ctx context.Context, conn Connection, b listBinding, rows []string, key string, removalsPending map[string]time.Time) error {
	if key == "" {
		return nil
	}
	for _, row := range rows {
		if _, ok := removalsPending[row]; !ok {
			continue
		}
		if err := s.repo.SetListItemProviderKey(ctx, conn.ID, b.kind, row, key); err != nil {
			return err
		}
	}
	return nil
}

// pendingTitleKey keys pendingTitles: a title's kind and one of its prefixed
// IDs or provider keys.
func pendingTitleKey(kind, id string) string { return kind + "|" + id }

// indexPendingTitle adds a pending-removal row, a library item's or a
// watchlist title's, to pendingTitles under each ID and the provider key it
// knows.
func indexPendingTitle(pendingTitles map[string][]string, state ListItemState) {
	for _, id := range state.keptTitleIDs() {
		if id == "" {
			continue
		}
		key := pendingTitleKey(state.Kind, id)
		if !slices.Contains(pendingTitles[key], state.MediaItemID) {
			pendingTitles[key] = append(pendingTitles[key], state.MediaItemID)
		}
	}
}

// pendingTitleRows returns the media item IDs of the pending-removal title
// rows of this kind that know any of the IDs.
func pendingTitleRows(pendingTitles map[string][]string, kind string, ids []string) []string {
	var rows []string
	for _, id := range ids {
		if id == "" {
			continue
		}
		for _, row := range pendingTitles[pendingTitleKey(kind, id)] {
			if !slices.Contains(rows, row) {
				rows = append(rows, row)
			}
		}
	}
	return rows
}

// listTitlesSince returns when the connection's first watchlist import that
// kept titles outside the library finished, and whether the titles are wired
// at all. Sync requests only the titles it adds after that import, so
// connecting a provider, or the first sync after upgrading, keeps the
// provider's backlog on the watchlist without requesting all of it.
func (s *Service) listTitlesSince(ctx context.Context, conn Connection, b listBinding) (*time.Time, bool, error) {
	if s.watchlistTitles(b) == nil {
		return nil, false, nil
	}
	since, err := s.repo.WatchlistTitlesSince(ctx, conn.ID)
	return since, true, err
}

// removeLocalListItem takes a shadow row's item off the local list: a
// watchlist entry outside the library by its title, a library item through
// the binding. Before the watchlist titles are wired (a sync that starts
// while the server boots), a title's entry can't be removed, so the run
// fails and the next one retries rather than recording a removal that didn't
// happen.
func (s *Service) removeLocalListItem(ctx context.Context, store userstore.UserStore, conn Connection, b listBinding, state ListItemState) error {
	title, ok := watchlistTitleFromListItemID(state.MediaItemID)
	if !ok {
		return b.localRemove(ctx, store, conn.ProfileID, state.MediaItemID)
	}
	titles := s.watchlistTitles(b)
	if titles == nil {
		return fmt.Errorf("watchlist titles are not available yet to remove %s", state.MediaItemID)
	}
	if err := titles.RemoveWatchlistTitle(ctx, conn.UserID, conn.ProfileID, title); err != nil {
		return err
	}
	// Promotion may have moved the entry onto the library watchlist since
	// the run read it; the title leaves in that form too.
	_, err := s.removePromotedListTitle(ctx, store, conn, b, titles, title, state)
	return err
}

// removePromotedListTitle takes the one library item with the title's IDs off
// the library watchlist, where promotion moves the entry. The row's IDs find
// it after promotion deleted the title's own. Several items are left alone,
// as promotion leaves them. It reports whether it removed one.
func (s *Service) removePromotedListTitle(ctx context.Context, store userstore.UserStore, conn Connection, b listBinding, titles WatchlistTitles, title WatchlistTitle, state ListItemState) (bool, error) {
	promoted, err := titles.PromotedWatchlistItems(ctx, conn.UserID, conn.ProfileID, title, state.keptTitleIDs())
	if err != nil || len(promoted) != 1 {
		return false, err
	}
	return true, b.localRemove(ctx, store, conn.ProfileID, promoted[0])
}

// heldTitleIDs returns every ID ("tmdb:550", "imdb:tt0137523") the titles
// behind the shadow rows have held, from their entries and from what the rows
// kept. TMDB can repoint a title while the provider keeps listing it under
// the other ID, and a row's key names only one of them.
func (s *Service) heldTitleIDs(ctx context.Context, conn Connection, b listBinding, states []ListItemState) (map[WatchlistTitle][]string, error) {
	titles := s.watchlistTitles(b)
	if titles == nil {
		return nil, nil
	}
	keys := make([]WatchlistTitle, 0, len(states))
	for _, state := range states {
		if title, ok := watchlistTitleFromListItemID(state.MediaItemID); ok {
			keys = append(keys, title)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	held, err := titles.WatchlistTitleIDs(ctx, conn.UserID, conn.ProfileID, keys)
	if err != nil {
		return nil, err
	}
	out := make(map[WatchlistTitle][]string, len(keys))
	for _, state := range states {
		title, ok := watchlistTitleFromListItemID(state.MediaItemID)
		if !ok {
			continue
		}
		for _, id := range append(slices.Clone(held[title]), state.TitleIDs...) {
			if !slices.Contains(out[title], id) {
				out[title] = append(out[title], id)
			}
		}
	}
	return out, nil
}

// reconcileLocalListTitles records which imported watchlist entries outside
// the library left the profile's watchlist. One the profile removed is a
// local removal, which goes to the provider when removals sync. One that
// promotion moved onto the library watchlist is still on the watchlist; its
// row retires without a removal and hands its place on the provider's list
// to the library item's row. While an entry stays, its row keeps every ID
// its title holds, since promotion deletes them with the title. A removed
// entry's row retires the same way once a library item with the title's IDs
// joins the watchlist: the removal was recorded while no such item was on
// it, so the item is a re-add, which the removal must not take off the
// provider whichever key each row carries (#4286).
func (s *Service) reconcileLocalListTitles(ctx context.Context, conn Connection, b listBinding) error {
	titles := s.watchlistTitles(b)
	if titles == nil {
		return nil
	}
	states, err := s.repo.ListListItemStates(ctx, conn.ID, b.kind)
	if err != nil {
		return err
	}
	rows := map[WatchlistTitle]ListItemState{}
	tracked := make(map[string]ListItemState, len(states))
	keys := make([]WatchlistTitle, 0, len(states))
	var removed []ListItemState
	for _, state := range states {
		tracked[state.MediaItemID] = state
		title, ok := watchlistTitleFromListItemID(state.MediaItemID)
		switch {
		case !ok:
		case state.LocalPresent:
			rows[title] = state
			keys = append(keys, title)
		case state.RemotePresent:
			removed = append(removed, state)
		}
	}
	for _, state := range removed {
		if err := s.retireReAddedListTitle(ctx, conn, b, titles, state, tracked); err != nil {
			return err
		}
	}
	if len(keys) == 0 {
		return nil
	}
	// Rows the provider lists go first, so when several title rows promote to
	// one library item, the item takes a provider key the provider lists.
	slices.SortStableFunc(keys, func(a, b WatchlistTitle) int {
		return cmp.Compare(boolRank(rows[b].RemotePresent), boolRank(rows[a].RemotePresent))
	})
	// A title has IDs exactly while the profile has an entry for it.
	held, err := titles.WatchlistTitleIDs(ctx, conn.UserID, conn.ProfileID, keys)
	if err != nil {
		return err
	}
	if err := s.keepListTitleIDs(ctx, conn, b, held, rows); err != nil {
		return err
	}
	now := s.now()
	for _, title := range keys {
		if len(held[title]) > 0 {
			continue
		}
		state := rows[title]
		promoted, err := titles.PromotedWatchlistItems(ctx, conn.UserID, conn.ProfileID, title, state.keptTitleIDs())
		if err != nil {
			return err
		}
		if len(promoted) == 1 {
			existing, ok := tracked[promoted[0]]
			item, err := s.handOverListTitle(ctx, b, state, promoted[0], existing, ok)
			if err != nil {
				return err
			}
			tracked[promoted[0]] = item
		}
		// The remote side goes first: a row left local-removed but remote
		// present would send the provider a removal.
		if len(promoted) > 0 {
			if err := s.repo.MarkListItemRemoteRemoved(ctx, conn.ID, b.kind, state.MediaItemID, now); err != nil {
				return err
			}
		}
		if err := s.repo.MarkListItemLocalRemoved(ctx, conn.ID, b.kind, state.MediaItemID, now); err != nil {
			return err
		}
	}
	return nil
}

// retireReAddedListTitle retires the row of a removed entry whose removal
// waits when a library item with the title's IDs is on the watchlist, and
// hands its place on the provider's list to the item when there is one.
func (s *Service) retireReAddedListTitle(ctx context.Context, conn Connection, b listBinding, titles WatchlistTitles, state ListItemState, tracked map[string]ListItemState) error {
	title, _ := watchlistTitleFromListItemID(state.MediaItemID)
	promoted, err := titles.PromotedWatchlistItems(ctx, conn.UserID, conn.ProfileID, title, state.keptTitleIDs())
	if err != nil || len(promoted) == 0 {
		return err
	}
	if len(promoted) == 1 {
		existing, ok := tracked[promoted[0]]
		item, err := s.handOverListTitle(ctx, b, state, promoted[0], existing, ok)
		if err != nil {
			return err
		}
		tracked[promoted[0]] = item
	}
	return s.repo.MarkListItemRemoteRemoved(ctx, conn.ID, b.kind, state.MediaItemID, s.now())
}

// boolRank orders false before true.
func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// keepListTitleIDs adds to the rows of the titles still listed the IDs their
// titles hold, current and former.
func (s *Service) keepListTitleIDs(ctx context.Context, conn Connection, b listBinding, held map[WatchlistTitle][]string, rows map[WatchlistTitle]ListItemState) error {
	for title, ids := range held {
		state := rows[title]
		unkept := func(id string) bool { return !slices.Contains(state.TitleIDs, id) }
		if !slices.ContainsFunc(ids, unkept) {
			continue
		}
		if err := s.repo.AddListItemTitleIDs(ctx, conn.ID, b.kind, state.MediaItemID, ids); err != nil {
			return err
		}
	}
	return nil
}

// handOverListTitle gives the library item promotion moved a title onto the
// title row's place on the provider's list, so a provider removal before the
// next import still takes the item off and export doesn't send it again. The
// item keeps the title's IDs, so a complete read listing the title under any
// of them keeps it. It takes the title row's provider key unless it has a row
// whose key the provider lists, or that this row can't improve on. It returns
// the item's row as stored.
func (s *Service) handOverListTitle(ctx context.Context, b listBinding, state ListItemState, contentID string, existing ListItemState, tracked bool) (ListItemState, error) {
	item := ListItemState{
		ConnectionID:  state.ConnectionID,
		ListKind:      b.kind,
		MediaItemID:   contentID,
		Kind:          state.Kind,
		RemotePresent: state.RemotePresent,
		LocalPresent:  true,
		TitleIDs:      state.keptTitleIDs(),
	}
	if !tracked || (!existing.RemotePresent && state.RemotePresent) {
		item.ProviderItemKey, item.Title, item.Year = state.ProviderItemKey, state.Title, state.Year
	}
	if err := s.repo.UpsertListItemStates(ctx, []ListItemState{item}); err != nil {
		return ListItemState{}, err
	}
	item.RemotePresent = item.RemotePresent || existing.RemotePresent
	item.ProviderItemKey = cmp.Or(item.ProviderItemKey, existing.ProviderItemKey)
	return item, nil
}

// localListTitles returns the profile's watchlist entries outside the library
// as export items, with their shadow states, the way localItemsFromRows does
// for library items.
func (s *Service) localListTitles(ctx context.Context, conn Connection, b listBinding) ([]LocalFavorite, []ListItemState, error) {
	titles := s.watchlistTitles(b)
	if titles == nil {
		return nil, nil, nil
	}
	entries, err := titles.ListWatchlistTitles(ctx, conn.UserID, conn.ProfileID)
	if err != nil {
		return nil, nil, err
	}
	items := make([]LocalFavorite, 0, len(entries))
	states := make([]ListItemState, 0, len(entries))
	for _, title := range entries {
		item := LocalFavorite{
			MediaItemID: title.listItemID(),
			Kind:        title.Kind,
			Title:       title.Title,
			Year:        title.Year,
			IMDbID:      title.IMDbID,
			TMDBID:      strconv.Itoa(title.TMDBID),
			FavoritedAt: title.AddedAt,
		}
		if title.TVDBID > 0 {
			item.TVDBID = strconv.Itoa(title.TVDBID)
		}
		if item.FavoritedAt.IsZero() {
			item.FavoritedAt = s.now()
		}
		item.ProviderItemKey = providerItemKeyForLocalFavorite(item)
		items = append(items, item)
		listedAt := item.FavoritedAt
		states = append(states, ListItemState{
			ConnectionID:    conn.ID,
			ListKind:        b.kind,
			MediaItemID:     item.MediaItemID,
			ProviderItemKey: item.ProviderItemKey,
			Kind:            item.Kind,
			Title:           item.Title,
			Year:            item.Year,
			LocalPresent:    true,
			LastSeenLocalAt: &listedAt,
			TitleIDs:        title.IDs(),
		})
	}
	return items, states, nil
}

// requestQueuedListTitles applies watchlist requests to every entry outside
// the library that the provider lists and sync added after since (see
// listTitlesSince), oldest listing first. A new entry is requested as it is
// added; one a request quota refused then gets its request on a later run.
// The titles come from the stored rows rather than the provider's batch,
// which an incremental provider sends only for changes. A failure is logged
// and leaves the import's result alone.
func (s *Service) requestQueuedListTitles(ctx context.Context, conn Connection, b listBinding, since *time.Time) {
	titles := s.watchlistTitles(b)
	if titles == nil || since == nil {
		return
	}
	states, err := s.repo.ListListItemStates(ctx, conn.ID, b.kind)
	if err != nil {
		slog.WarnContext(ctx, "reading queued watchlist titles failed", "component", "watchsync",
			"connection_id", conn.ID, "error", err)
		return
	}
	listed := make([]ListItemState, 0, len(states))
	for _, state := range states {
		if _, ok := watchlistTitleFromListItemID(state.MediaItemID); ok && state.LocalPresent && state.RemotePresent && state.CreatedAt.After(*since) {
			listed = append(listed, state)
		}
	}
	sort.SliceStable(listed, func(i, j int) bool {
		return listedBefore(listed[i].LastSeenLocalAt, listed[j].LastSeenLocalAt)
	})
	queued := make([]WatchlistTitle, 0, len(listed))
	for _, state := range listed {
		title, _ := watchlistTitleFromListItemID(state.MediaItemID)
		queued = append(queued, title)
	}
	if len(queued) == 0 {
		return
	}
	if err := titles.RequestQueuedWatchlistTitles(ctx, conn.UserID, conn.ProfileID, queued); err != nil {
		slog.WarnContext(ctx, "requesting queued watchlist titles failed", "component", "watchsync",
			"connection_id", conn.ID, "error", err)
	}
}

// listedBefore orders listing times oldest first, unknown last.
func listedBefore(a, b *time.Time) bool {
	switch {
	case a == nil:
		return false
	case b == nil:
		return true
	default:
		return a.Before(*b)
	}
}
