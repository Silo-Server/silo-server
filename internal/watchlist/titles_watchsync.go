package watchlist

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// SyncRequests applies watchlist requests to the entries watch provider sync
// adds and removes, as adding or removing a title by hand does: a new entry
// requests the title or follows its request, and a removed one withdraws the
// request it made while nothing has been sent. A failed request is the
// implementation's to log and leaves the entry; a failed withdrawal is
// returned, so the entry stays until a later sync withdraws it.
type SyncRequests interface {
	RequestWatchlistTitle(ctx context.Context, userID int, profileID string, title Title)
	WithdrawWatchlistTitle(ctx context.Context, userID int, profileID string, mediaType string, tmdbIDs []int) error
	// RequestQueuedWatchlistTitles requests, in order, the titles no request
	// was ever made for, while the profile's quota has room.
	RequestQueuedWatchlistTitles(ctx context.Context, userID int, profileID string, titles []Title)
}

// WatchSync adapts the service for watch provider sync, which keeps the
// titles a provider's watchlist lists and the library doesn't have as
// watchlist entries. requests may be nil, which leaves requests alone.
func (s *Titles) WatchSync(requests SyncRequests) watchsync.WatchlistTitles {
	return syncTitles{titles: s, requests: requests}
}

type syncTitles struct {
	titles   *Titles
	requests SyncRequests
}

var _ watchsync.WatchlistTitles = syncTitles{}

func (t syncTitles) LibraryHas(ctx context.Context, title watchsync.WatchlistTitle) (bool, error) {
	matches, err := t.titles.LibraryMatches(ctx, title.Kind, []int{title.TMDBID}, title.IMDbID, title.TVDBID)
	return len(matches) > 0, err
}

// AddWatchlistTitle stores the provider's IDs and display fields. Providers
// send no poster, so a new title is checked on TMDB right away rather than at
// its next scheduled check; a failed check leaves that schedule to retry.
func (t syncTitles) AddWatchlistTitle(ctx context.Context, userID int, profileID string, title watchsync.WatchlistTitle, addedAt time.Time) (bool, error) {
	if addedAt.IsZero() {
		addedAt = t.titles.now()
	}
	entry, inserted, err := t.titles.AddSnapshot(ctx, Viewer{UserID: userID, ProfileID: profileID}, Snapshot{
		MediaType: title.Kind,
		TMDBID:    title.TMDBID,
		IMDbID:    title.IMDbID,
		TVDBID:    title.TVDBID,
		Title:     title.Title,
		Year:      title.Year,
	}, addedAt)
	if err != nil {
		return false, err
	}
	if !inserted {
		return false, nil
	}
	if entry.Title.PosterPath == "" && t.titles.tmdb != nil {
		if err := t.titles.checkTitle(ctx, entry.Title.ID); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "watchlist title check after a provider import failed",
				"component", "watchlist", "title_id", entry.Title.ID, "error", err)
		}
	}
	if t.requests != nil {
		// Request with the snapshot the check just refreshed.
		stored := &entry.Title
		if found, err := t.titles.Find(ctx, title.Kind, title.TMDBID); err == nil && found != nil {
			stored = found
		}
		t.requests.RequestWatchlistTitle(ctx, userID, profileID, *stored)
		// A delete of the same title can run between the save and the
		// request, as it can for an add by hand: it withdraws, then removes
		// the entry. Rechecking the entry after requesting leaves no request
		// behind whichever way the two interleave.
		if err := t.withdrawIfRemoved(ctx, userID, profileID, title, *stored); err != nil {
			return true, err
		}
	}
	return true, nil
}

// withdrawIfRemoved withdraws the title's request when its entry left the
// watchlist other than by promotion onto the library watchlist.
func (t syncTitles) withdrawIfRemoved(ctx context.Context, userID int, profileID string, title watchsync.WatchlistTitle, stored Title) error {
	key := TitleKey{MediaType: title.Kind, TMDBID: title.TMDBID}
	on, err := t.titles.OnWatchlist(ctx, Viewer{UserID: userID, ProfileID: profileID}, []TitleKey{key})
	if err != nil || on[key] {
		return err
	}
	promoted, err := t.WatchlistTitlePromoted(ctx, userID, profileID, title)
	if err != nil || promoted {
		return err
	}
	return t.requests.WithdrawWatchlistTitle(ctx, userID, profileID, title.Kind, heldTMDBIDs(title.TMDBID, &stored))
}

// heldTMDBIDs returns tmdbID and every TMDB ID the stored title (nil when
// unknown) has held, once each: a request keeps the ID it was made under.
func heldTMDBIDs(tmdbID int, stored *Title) []int {
	ids := []int{tmdbID}
	if stored == nil {
		return ids
	}
	for _, id := range append([]int{stored.TMDBID}, stored.FormerTMDBIDs...) {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// ListWatchlistTitles returns every entry of the profile outside the library.
func (t syncTitles) ListWatchlistTitles(ctx context.Context, userID int, profileID string) ([]watchsync.WatchlistTitle, error) {
	entries, err := t.listEntries(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	out := make([]watchsync.WatchlistTitle, 0, len(entries))
	for _, e := range entries {
		out = append(out, watchsync.WatchlistTitle{
			Kind:    e.Title.MediaType,
			TMDBID:  e.Title.TMDBID,
			IMDbID:  e.Title.IMDbID,
			TVDBID:  e.Title.TVDBID,
			Title:   e.Title.Title,
			Year:    e.Title.Year,
			AddedAt: e.AddedAt,
		})
	}
	return out, nil
}

// listEntries reads every entry of the profile, a page at a time.
func (t syncTitles) listEntries(ctx context.Context, userID int, profileID string) ([]Entry, error) {
	const pageSize = 500
	var out []Entry
	var after *PageKey
	for {
		entries, err := t.titles.repo.listPage(ctx, userID, profileID, after, pageSize)
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
		if len(entries) < pageSize {
			return out, nil
		}
		key := entries[len(entries)-1].PageKey()
		after = &key
	}
}

// RequestQueuedWatchlistTitles hands the profile's stored titles, with the
// snapshots their checks keep current, to the request service in the order
// given. One read of the profile's entries loads them all.
func (t syncTitles) RequestQueuedWatchlistTitles(ctx context.Context, userID int, profileID string, titles []watchsync.WatchlistTitle) error {
	if t.requests == nil || len(titles) == 0 {
		return nil
	}
	entries, err := t.listEntries(ctx, userID, profileID)
	if err != nil {
		return err
	}
	byKey := make(map[TitleKey]Title, len(entries))
	// A stored row keeps the TMDB ID it was listed under, which a check may
	// since have repointed, so former IDs resolve too.
	for _, e := range entries {
		for _, id := range e.Title.FormerTMDBIDs {
			byKey[TitleKey{MediaType: e.Title.MediaType, TMDBID: id}] = e.Title
		}
	}
	for _, e := range entries {
		byKey[e.Title.Key()] = e.Title
	}
	stored := make([]Title, 0, len(titles))
	seen := make(map[TitleKey]bool, len(titles))
	for _, title := range titles {
		found, ok := byKey[TitleKey{MediaType: title.Kind, TMDBID: title.TMDBID}]
		if ok && !seen[found.Key()] {
			seen[found.Key()] = true
			stored = append(stored, found)
		}
	}
	t.requests.RequestQueuedWatchlistTitles(ctx, userID, profileID, stored)
	return nil
}

// RemoveWatchlistTitle takes the profile's entry for the title off, the way
// removing it by hand does: the request goes first, under every TMDB ID the
// title has held, since a request keeps the ID it was made under. A failed
// withdrawal leaves the entry, so the next sync still finds the title and
// retries. The second withdrawal catches a request an overlapping add made
// after the first; it is skipped when a re-add already put the title back.
func (t syncTitles) RemoveWatchlistTitle(ctx context.Context, userID int, profileID string, title watchsync.WatchlistTitle) error {
	viewer := Viewer{UserID: userID, ProfileID: profileID}
	ids := []int{title.TMDBID}
	if t.requests != nil {
		known, err := t.titles.Find(ctx, title.Kind, title.TMDBID)
		if err != nil {
			return err
		}
		ids = heldTMDBIDs(title.TMDBID, known)
		if err := t.requests.WithdrawWatchlistTitle(ctx, userID, profileID, title.Kind, ids); err != nil {
			return err
		}
	}
	if _, _, err := t.titles.Remove(ctx, viewer, title.Kind, title.TMDBID); err != nil {
		return err
	}
	if t.requests == nil {
		return nil
	}
	key := TitleKey{MediaType: title.Kind, TMDBID: title.TMDBID}
	on, err := t.titles.OnWatchlist(ctx, viewer, []TitleKey{key})
	if err != nil || on[key] {
		return err
	}
	return t.requests.WithdrawWatchlistTitle(ctx, userID, profileID, title.Kind, ids)
}

func (t syncTitles) WatchlistTitlesListed(ctx context.Context, userID int, profileID string, titles []watchsync.WatchlistTitle) (map[watchsync.WatchlistTitle]bool, error) {
	keys := make([]TitleKey, len(titles))
	for i, title := range titles {
		keys[i] = TitleKey{MediaType: title.Kind, TMDBID: title.TMDBID}
	}
	on, err := t.titles.OnWatchlist(ctx, Viewer{UserID: userID, ProfileID: profileID}, keys)
	if err != nil {
		return nil, err
	}
	out := make(map[watchsync.WatchlistTitle]bool, len(titles))
	for i, title := range titles {
		out[title] = on[keys[i]]
	}
	return out, nil
}

// WatchlistTitleTMDBIDs reads the profile's entries once and returns each
// title's current and former TMDB IDs, reached through any of them.
func (t syncTitles) WatchlistTitleTMDBIDs(ctx context.Context, userID int, profileID string, titles []watchsync.WatchlistTitle) (map[watchsync.WatchlistTitle][]int, error) {
	entries, err := t.listEntries(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	byKey := make(map[TitleKey][]int, len(entries))
	for _, e := range entries {
		held := append([]int{e.Title.TMDBID}, e.Title.FormerTMDBIDs...)
		for _, id := range held {
			byKey[TitleKey{MediaType: e.Title.MediaType, TMDBID: id}] = held
		}
	}
	out := make(map[watchsync.WatchlistTitle][]int, len(titles))
	for _, title := range titles {
		out[title] = byKey[TitleKey{MediaType: title.Kind, TMDBID: title.TMDBID}]
	}
	return out, nil
}

// WatchlistTitlePromoted checks the profile's library watchlist for the
// library items carrying the title's IDs. The library having the title is not
// enough: the profile may have removed the entry before promotion ran.
func (t syncTitles) WatchlistTitlePromoted(ctx context.Context, userID int, profileID string, title watchsync.WatchlistTitle) (bool, error) {
	if t.titles.storeFor == nil {
		return false, nil
	}
	matches, err := t.titles.LibraryMatches(ctx, title.Kind, []int{title.TMDBID}, title.IMDbID, title.TVDBID)
	if err != nil || len(matches) == 0 {
		return false, err
	}
	store, err := t.titles.storeFor(ctx, userID)
	if err != nil {
		return false, err
	}
	listed, err := store.ListWatchlistByMediaItems(ctx, profileID, matches)
	if err != nil {
		return false, err
	}
	for _, id := range matches {
		if listed[id] {
			return true, nil
		}
	}
	return false, nil
}
