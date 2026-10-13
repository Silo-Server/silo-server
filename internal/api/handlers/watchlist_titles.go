package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/watchlist"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// Watchlist entries for titles the library doesn't have yet. The entries live
// in watchlist.Titles; this handler adds the library-watchlist side: an added
// title the library already has goes onto the library watchlist, promotion
// runs ahead of library watchlist reads, and a promoted entry fires the same
// effects as a manual add.

var _ watchlist.Effects = (*PersonalDataHandler)(nil)

// SetWatchlistTitles wires the service that keeps watchlist entries for
// titles the library doesn't have. Without it the watchlist reads skip
// promotion and the title operations answer 500.
func (h *PersonalDataHandler) SetWatchlistTitles(titles *watchlist.Titles) {
	h.watchlistTitles = titles
}

// WatchlistPromoted implements watchlist.Effects: an entry that moved onto the
// library watchlist notifies the same listeners as a manual add.
func (h *PersonalDataHandler) WatchlistPromoted(ctx context.Context, userID int, profileID, contentID string) {
	h.dispatchLocalListEvent(ctx, watchsync.ListKindWatchlist, watchsync.ListChangeAdded, userID, profileID, contentID)
	triggerProfileRefresh(ctx, h.profileStaler, h.profileRefreshRequester, userID, profileID)
	publishUserStateEvent(ctx, h.EventsHub, userID, profileID, contentID, "", "watchlist", userStateEventState{
		InWatchlist: boolPtr(true),
	})
}

// watchlistAccess is the viewer's access filter naming the viewer, as the
// watchlist promotion hooks read it.
func (v PersonalListViewer) watchlistAccess() catalog.AccessFilter {
	access := v.Access
	access.UserID, access.ProfileID = v.UserID, v.ProfileID
	return access
}

func (v PersonalListViewer) watchlistViewer() watchlist.Viewer {
	return watchlist.Viewer{UserID: v.UserID, ProfileID: v.ProfileID, Access: v.watchlistAccess()}
}

// promoteWatchlist moves the viewer's entries whose titles the library now
// has onto the library watchlist before a library watchlist read. A failure
// is logged and the read goes on.
func (h *PersonalDataHandler) promoteWatchlist(ctx context.Context, viewer PersonalListViewer) {
	if h.watchlistTitles != nil {
		h.watchlistTitles.PromoteWatchlist(ctx, viewer.watchlistAccess())
	}
}

func (h *PersonalDataHandler) promoteWatchlistItem(ctx context.Context, viewer PersonalListViewer, itemID string) {
	if h.watchlistTitles != nil {
		h.watchlistTitles.PromoteWatchlistItem(ctx, viewer.watchlistAccess(), itemID)
	}
}

func (h *PersonalDataHandler) titlesOrError() (*watchlist.Titles, error) {
	if h.watchlistTitles == nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Watchlist titles are not configured")
	}
	return h.watchlistTitles, nil
}

// ListWatchlistTitlesPage answers at most limit of the viewer's entries for
// titles the library doesn't have, newest first, strictly after the key (nil
// = from the newest). The first page (nil key) first promotes the entries the
// library now has.
func (h *PersonalDataHandler) ListWatchlistTitlesPage(ctx context.Context, viewer PersonalListViewer, after *watchlist.PageKey, limit int) ([]watchlist.Entry, error) {
	titles, err := h.titlesOrError()
	if err != nil {
		return nil, err
	}
	// Promotion scans the whole profile, so a paged read runs it once, on
	// the first page, rather than once per page.
	if after == nil {
		h.promoteWatchlist(ctx, viewer)
	}
	entries, err := titles.ListPage(ctx, viewer.watchlistViewer(), after, limit)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list watchlist titles")
	}
	return entries, nil
}

// FindWatchlistTitle returns the title holding the TMDB ID, current or
// former, or nil when no watchlist tracks it.
func (h *PersonalDataHandler) FindWatchlistTitle(ctx context.Context, mediaType string, tmdbID int) (*watchlist.Title, error) {
	titles, err := h.titlesOrError()
	if err != nil {
		return nil, err
	}
	title, err := titles.Find(ctx, mediaType, tmdbID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to look up the watchlist title")
	}
	return title, nil
}

// WatchlistTitleAdded is where AddWatchlistTitle put a title: on the library
// watchlist (ItemID set) or as an entry for a title the library doesn't have.
type WatchlistTitleAdded struct {
	ItemID  string
	AddedAt time.Time
}

// AddWatchlistTitle puts a title on the viewer's watchlist. When exactly one
// library item has the title and the viewer may see it, the item goes onto
// the library watchlist through the normal add, with its effects; otherwise
// the title is kept as an entry until the library has it. Adding a title
// already there keeps its original added_at.
func (h *PersonalDataHandler) AddWatchlistTitle(ctx context.Context, viewer PersonalListViewer, snap watchlist.Snapshot) (WatchlistTitleAdded, error) {
	titles, err := h.titlesOrError()
	if err != nil {
		return WatchlistTitleAdded{}, err
	}
	tmdbIDs := append([]int{snap.TMDBID}, snap.FormerTMDBIDs...)
	itemID, err := h.accessibleLibraryItem(ctx, viewer, titles, snap.MediaType, tmdbIDs, snap.IMDbID, snap.TVDBID)
	if err != nil {
		return WatchlistTitleAdded{}, err
	}
	if itemID != "" {
		if err := h.AddToWatchlist(ctx, viewer, itemID); err != nil {
			return WatchlistTitleAdded{}, err
		}
		entry, found, err := h.GetWatchlistEntry(ctx, viewer, itemID)
		if err != nil {
			return WatchlistTitleAdded{}, err
		}
		added := time.Now().UTC()
		if found {
			if parsed, err := time.Parse(time.RFC3339Nano, entry.AddedAt); err == nil {
				added = parsed
			}
		}
		// Not found: a concurrent remove won, and the add itself succeeded.
		return WatchlistTitleAdded{ItemID: itemID, AddedAt: added}, nil
	}
	entry, _, err := titles.AddSnapshot(ctx, viewer.watchlistViewer(), snap, time.Now().UTC())
	if err != nil {
		return WatchlistTitleAdded{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to add to watchlist")
	}
	return WatchlistTitleAdded{AddedAt: entry.AddedAt}, nil
}

// RemoveWatchlistTitle takes the title holding the TMDB ID, current or
// former, off the viewer's watchlist: the entry for it, and the library
// watchlist entry of the one library item the viewer may see that has it.
// It returns the title (nil when no watchlist tracks the ID). Removing a
// title that is not on the watchlist succeeds.
//
// The library entry goes first. Removing the last entry deletes the title and
// its former IDs, and a library item may be known only by one of those, so
// until the entry goes a retry still resolves the item by every ID. The
// library entry is checked again after the entry goes, for a promotion that
// moved the entry onto it in between.
func (h *PersonalDataHandler) RemoveWatchlistTitle(ctx context.Context, viewer PersonalListViewer, mediaType string, tmdbID int) (*watchlist.Title, error) {
	titles, err := h.titlesOrError()
	if err != nil {
		return nil, err
	}
	known, err := titles.Find(ctx, mediaType, tmdbID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to look up the watchlist title")
	}
	tmdbIDs, imdbID, tvdbID := []int{tmdbID}, "", 0
	if known != nil {
		imdbID, tvdbID = known.IMDbID, known.TVDBID
		for _, id := range append([]int{known.TMDBID}, known.FormerTMDBIDs...) {
			if !slices.Contains(tmdbIDs, id) {
				tmdbIDs = append(tmdbIDs, id)
			}
		}
	}
	itemID, err := h.accessibleLibraryItem(ctx, viewer, titles, mediaType, tmdbIDs, imdbID, tvdbID)
	if err != nil {
		return nil, err
	}
	if err := h.removeLibraryWatchlistItem(ctx, viewer, itemID); err != nil {
		return nil, err
	}
	title, _, err := titles.Remove(ctx, viewer.watchlistViewer(), mediaType, tmdbID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to remove from watchlist")
	}
	if err := h.removeLibraryWatchlistItem(ctx, viewer, itemID); err != nil {
		return nil, err
	}
	if title == nil {
		title = known
	}
	return title, nil
}

// removeLibraryWatchlistItem takes the item off the viewer's library
// watchlist through the normal remove, when it is there. An absent item is
// no removal, so nothing is exported to a provider.
func (h *PersonalDataHandler) removeLibraryWatchlistItem(ctx context.Context, viewer PersonalListViewer, itemID string) error {
	if itemID == "" {
		return nil
	}
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	onList, err := store.GetWatchlistEntry(ctx, viewer.ProfileID, itemID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to check watchlist")
	}
	if onList == nil {
		return nil
	}
	return h.RemoveFromWatchlist(ctx, viewer, itemID)
}

// accessibleLibraryItem returns the library item that has the title when
// exactly one does and the viewer may see it, else "". Several matches are
// left alone until the library merges them, as promotion leaves them.
func (h *PersonalDataHandler) accessibleLibraryItem(ctx context.Context, viewer PersonalListViewer, titles *watchlist.Titles, mediaType string, tmdbIDs []int, imdbID string, tvdbID int) (string, error) {
	matches, err := titles.LibraryMatches(ctx, mediaType, tmdbIDs, imdbID, tvdbID)
	if err != nil {
		return "", apiError(http.StatusInternalServerError, "internal_error", "Failed to match the title to the library")
	}
	// Only the copies this viewer may see count: duplicates in libraries the
	// viewer can't open don't make the title ambiguous for them.
	visible := make([]string, 0, len(matches))
	for _, id := range matches {
		if h.itemRepo != nil {
			if err := h.itemRepo.EnsureAccessible(ctx, id, viewer.watchlistAccess()); err != nil {
				if errors.Is(err, catalog.ErrItemNotFound) {
					continue
				}
				return "", apiError(http.StatusInternalServerError, "internal_error", "Failed to check item access")
			}
		}
		visible = append(visible, id)
	}
	if len(visible) != 1 {
		if len(visible) > 1 {
			slog.InfoContext(ctx, "watchlist title matches several library items; keeping it as a title entry",
				"component", "watchlist", "media_type", mediaType, "tmdb_ids", tmdbIDs, "content_ids", visible)
		}
		return "", nil
	}
	return visible[0], nil
}

// WatchlistTitleOff reports whether the title is off the viewer's watchlist
// in both forms: no entry for it outside the library, and no library
// watchlist entry for the one library item the viewer may see that has it.
// A title the library received while it was on the watchlist was promoted,
// not removed, so it still counts as on.
func (h *PersonalDataHandler) WatchlistTitleOff(ctx context.Context, viewer PersonalListViewer, snap watchlist.Snapshot) (bool, error) {
	titles, err := h.titlesOrError()
	if err != nil {
		return false, err
	}
	key := watchlist.TitleKey{MediaType: snap.MediaType, TMDBID: snap.TMDBID}
	on, err := titles.OnWatchlist(ctx, viewer.watchlistViewer(), []watchlist.TitleKey{key})
	if err != nil {
		return false, apiError(http.StatusInternalServerError, "internal_error", "Failed to check watchlist titles")
	}
	if on[key] {
		return false, nil
	}
	tmdbIDs := append([]int{snap.TMDBID}, snap.FormerTMDBIDs...)
	itemID, err := h.accessibleLibraryItem(ctx, viewer, titles, snap.MediaType, tmdbIDs, snap.IMDbID, snap.TVDBID)
	if err != nil || itemID == "" {
		return itemID == "" && err == nil, err
	}
	store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
	if err != nil {
		return false, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	entry, err := store.GetWatchlistEntry(ctx, viewer.ProfileID, itemID)
	if err != nil {
		return false, apiError(http.StatusInternalServerError, "internal_error", "Failed to check watchlist")
	}
	return entry == nil, nil
}

// WatchlistMembership reports which titles and which library items are on
// the viewer's watchlist: titles by any TMDB ID their entry has held, items
// through the library watchlist. It makes two reads whatever the page size.
func (h *PersonalDataHandler) WatchlistMembership(ctx context.Context, viewer PersonalListViewer, keys []watchlist.TitleKey, itemIDs []string) (map[watchlist.TitleKey]bool, map[string]bool, error) {
	titles, err := h.titlesOrError()
	if err != nil {
		return nil, nil, err
	}
	onTitles := map[watchlist.TitleKey]bool{}
	if len(keys) > 0 {
		if onTitles, err = titles.OnWatchlist(ctx, viewer.watchlistViewer(), keys); err != nil {
			return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to check watchlist titles")
		}
	}
	onItems := map[string]bool{}
	if len(itemIDs) > 0 {
		store, err := h.storeProvider.ForUser(ctx, viewer.UserID)
		if err != nil {
			return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
		}
		if onItems, err = store.ListWatchlistByMediaItems(ctx, viewer.ProfileID, itemIDs); err != nil {
			return nil, nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to check watchlist")
		}
	}
	return onTitles, onItems, nil
}
