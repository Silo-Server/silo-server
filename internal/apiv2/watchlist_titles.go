package apiv2

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/watchlist"
)

// Watchlist titles: the acting profile's watchlist entries for movies and
// series the library doesn't have yet, keyed by TMDB ID. They live on the
// requests surface, so the three operations answer 409 capability_disabled
// while requests are off; the entries are kept. An entry whose title reaches
// the library moves onto the library watchlist (GET /watchlist) on the next
// read.

// WatchlistTitleService is the slice of *handlers.PersonalDataHandler the
// watchlist title operations use. Every method acts as the viewer's profile
// and returns an *handlers.APIError on failure.
type WatchlistTitleService interface {
	// ListWatchlistTitlesPage answers at most limit of the entries ordered by
	// (added_at DESC, title id DESC) strictly after the key (nil = from the
	// newest). The first page first moves the entries the library now has
	// onto the library watchlist.
	ListWatchlistTitlesPage(ctx context.Context, viewer handlers.PersonalListViewer, after *watchlist.PageKey, limit int) ([]watchlist.Entry, error)
	// FindWatchlistTitle answers the title holding the TMDB ID, current or
	// former, or nil when no watchlist tracks it.
	FindWatchlistTitle(ctx context.Context, mediaType string, tmdbID int) (*watchlist.Title, error)
	// AddWatchlistTitle puts the title on the library watchlist when the
	// viewer may see the one library item that has it, else keeps it as an
	// entry. A title already there keeps its added_at.
	AddWatchlistTitle(ctx context.Context, viewer handlers.PersonalListViewer, snap watchlist.Snapshot) (handlers.WatchlistTitleAdded, error)
	// RemoveWatchlistTitle takes the title holding the TMDB ID off the
	// watchlist, entry and library item both, and answers the title (nil
	// when no watchlist tracks the ID).
	RemoveWatchlistTitle(ctx context.Context, viewer handlers.PersonalListViewer, mediaType string, tmdbID int) (*watchlist.Title, error)
	// WatchlistMembership answers which titles (by any TMDB ID they have
	// held) and which library items are on the viewer's watchlist.
	WatchlistMembership(ctx context.Context, viewer handlers.PersonalListViewer, keys []watchlist.TitleKey, itemIDs []string) (map[watchlist.TitleKey]bool, map[string]bool, error)
	// WatchlistTitleOff answers whether the title is off the viewer's
	// watchlist both as an entry and, once the library has it, as a library
	// watchlist item.
	WatchlistTitleOff(ctx context.Context, viewer handlers.PersonalListViewer, snap watchlist.Snapshot) (bool, error)
}

// WatchlistRequestService is the slice of *requests.Service the watchlist
// title operations use: the requests gate and rating ceiling, the TMDB
// detail of a title being added, and watchlist requests.
type WatchlistRequestService interface {
	WatchlistCeiling(ctx context.Context, viewer mediarequests.Viewer) (string, error)
	WatchlistTitleDetail(ctx context.Context, viewer mediarequests.Viewer, mediaType mediarequests.MediaType, tmdbID int) (*tmdb.MediaDetail, error)
	RequestFromWatchlist(ctx context.Context, viewer mediarequests.Viewer, title mediarequests.WatchlistTitle) (mediarequests.RequestState, error)
	WithdrawWatchlistRequest(ctx context.Context, viewer mediarequests.Viewer, mediaType mediarequests.MediaType, tmdbID int) error
	WatchlistRequestStates(ctx context.Context, viewer mediarequests.Viewer, titles []mediarequests.WatchlistTitle) (map[mediarequests.WatchlistKey]mediarequests.RequestState, error)
}

// WatchlistTitleListInput is the listWatchlistTitles query.
type WatchlistTitleListInput struct {
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJvIjo1MH0"`
}

// WatchlistTitleInput names one title by its TMDB identity.
type WatchlistTitleInput struct {
	MediaType string `path:"media_type" enum:"movie,series" doc:"The media type" example:"movie"`
	TMDBID    int    `path:"tmdb_id" minimum:"1" doc:"TMDB identifier (external, not a Silo ID). A title's former TMDB ID still names it" example:"949"`
}

// WatchlistTitle is one watchlist entry for a title the library doesn't have.
type WatchlistTitle struct {
	MediaType     string            `json:"media_type" doc:"movie or series" example:"movie"`
	TMDBID        int               `json:"tmdb_id" doc:"The title's current TMDB identifier (external, not a Silo ID)" example:"949"`
	Title         string            `json:"title" example:"Heat"`
	Year          int               `json:"year,omitempty" example:"1995"`
	ReleaseDate   string            `json:"release_date,omitempty" doc:"Calendar date, YYYY-MM-DD: the release date of a movie, the first air date of a series" example:"1995-12-15"`
	PosterPath    string            `json:"poster_path,omitempty" doc:"TMDB image path" example:"/abc.jpg"`
	VoteAverage   *float64          `json:"vote_average,omitempty" doc:"TMDB rating out of 10; absent while the title has no votes" example:"7.9"`
	ContentRating string            `json:"content_rating,omitempty" doc:"US certification" example:"R"`
	AddedAt       Instant           `json:"added_at" doc:"When the title joined the watchlist" example:"2026-01-02T03:04:05.000Z"`
	Status        string            `json:"status" doc:"active, needs_review (TMDB deleted the ID and several titles could replace it) or removed (TMDB deleted the ID and nothing replaces it). More values may be added: read an unknown one as active" example:"active"`
	Request       RequestMediaState `json:"request" doc:"The title's request state for the viewer, with download progress while it downloads"`
}

// WatchlistTitleCollection is the named envelope the contract carries: the
// profile's watchlist titles, newest entry first.
type WatchlistTitleCollection struct {
	Collection[WatchlistTitle]
}

// WatchlistTitleCollectionOutput is the listWatchlistTitles response.
type WatchlistTitleCollectionOutput struct {
	Body WatchlistTitleCollection
}

// WatchlistTitleEntry is where addWatchlistTitle put a title.
type WatchlistTitleEntry struct {
	MediaType string            `json:"media_type" doc:"movie or series" example:"movie"`
	TMDBID    int               `json:"tmdb_id" doc:"The title's current TMDB identifier (external, not a Silo ID)" example:"949"`
	AddedAt   Instant           `json:"added_at" doc:"When the title joined the watchlist; adding it again keeps the first time" example:"2026-01-02T03:04:05.000Z"`
	ItemID    ID                `json:"item_id,omitempty" doc:"The catalog item, when the library has the title and the entry went to the library watchlist" example:"movie:heat-1995"`
	Request   RequestMediaState `json:"request" doc:"The title's request state after the add, including why a watchlist request was refused"`
}

// WatchlistTitleEntryOutput is the addWatchlistTitle response.
type WatchlistTitleEntryOutput struct {
	Body WatchlistTitleEntry
}

// watchlistTitlePosition is the listWatchlistTitles cursor payload: the
// keyset of the last entry the previous page emitted.
type watchlistTitlePosition struct {
	AddedAt time.Time `json:"a"`
	TitleID int64     `json:"t"`
}

const (
	opListWatchlistTitles  = "listWatchlistTitles"
	opAddWatchlistTitle    = "addWatchlistTitle"
	opDeleteWatchlistTitle = "deleteWatchlistTitle"
)

func registerWatchlistTitles(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)

	list := humaOp(http.MethodGet, Prefix+"/watchlist/titles", opListWatchlistTitles, "watchlist",
		"List the acting profile's watchlist entries for titles the library doesn't have, newest first. Entries whose title reached the library move to the library watchlist first; titles above the viewer's rating ceiling are omitted.")
	list.Errors = []int{http.StatusConflict}
	Register(reg, viewerOperation(list), func(ctx context.Context, in *WatchlistTitleListInput) (*WatchlistTitleCollectionOutput, error) {
		return reg.listWatchlistTitles(ctx, cursors, in)
	})

	add := humaOp(http.MethodPut, Prefix+"/watchlist/titles/{media_type}/{tmdb_id}", opAddWatchlistTitle, "watchlist",
		"Add a title to the acting profile's watchlist. A title the library has goes onto the library watchlist; otherwise it is kept by TMDB ID and, when watchlist requests apply, requested or followed. Automatic retries are unsafe because provider, refresh and request effects are not change-gated.")
	add.Errors = []int{http.StatusNotFound, http.StatusConflict}
	Register(reg, Operation{Operation: add, Class: ClassProfileScoped, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNonRetryable}, reg.addWatchlistTitle)

	remove := humaOp(http.MethodDelete, Prefix+"/watchlist/titles/{media_type}/{tmdb_id}", opDeleteWatchlistTitle, "watchlist",
		"Remove a title from the acting profile's watchlist by its current or a former TMDB ID, from the library watchlist too, and withdraw the request the watchlist made for it while nothing has been sent. An absent entry succeeds, but automatic retries can repeat provider and refresh effects.")
	remove.DefaultStatus = http.StatusNoContent
	remove.Errors = []int{http.StatusConflict}
	Register(reg, Operation{Operation: remove, Class: ClassProfileScoped, ServiceBacked: true, DemoRestricted: true, RetrySafety: RetrySafetyNonRetryable}, reg.deleteWatchlistTitle)
}

// watchlistTitleViewer resolves the viewer for both services and the rating
// ceiling. It answers 409 capability_disabled while requests are off.
func (reg *Registry) watchlistTitleViewer(ctx context.Context) (handlers.PersonalListViewer, mediarequests.Viewer, string, *Problem) {
	if reg.deps.WatchlistTitles == nil {
		return handlers.PersonalListViewer{}, mediarequests.Viewer{}, "", unavailable("watchlist")
	}
	if reg.deps.WatchlistRequests == nil {
		return handlers.PersonalListViewer{}, mediarequests.Viewer{}, "", unavailable("requests")
	}
	viewer, p := personalListViewer(ctx, "")
	if p != nil {
		return handlers.PersonalListViewer{}, mediarequests.Viewer{}, "", p
	}
	rv := lifecycleViewer(ctx)
	ceiling, err := reg.deps.WatchlistRequests.WatchlistCeiling(ctx, rv)
	if err != nil {
		return handlers.PersonalListViewer{}, mediarequests.Viewer{}, "", requestProblem(err)
	}
	return viewer, rv, ceiling, nil
}

// watchlistTitleScope binds the cursor to the profile and the viewer policy,
// whose rating ceiling filters the page, and to the keyset it pages by.
func watchlistTitleScope(ctx context.Context, viewer handlers.PersonalListViewer) CursorScope {
	return CursorScope{
		OperationID: opListWatchlistTitles,
		Security:    strconv.Itoa(viewer.UserID) + "/" + viewer.ProfileID + "/" + viewerScopeDigest(ctx),
		Sort:        "-added_at,-title_id",
		Tiebreaker:  "title_id",
	}
}

// listWatchlistTitles pages by keyset. A limit+1 probe decides has_more from
// the raw rows, so a title above the ceiling never hides the rows behind it.
func (reg *Registry) listWatchlistTitles(ctx context.Context, cursors *Cursors, in *WatchlistTitleListInput) (*WatchlistTitleCollectionOutput, error) {
	viewer, rv, ceiling, p := reg.watchlistTitleViewer(ctx)
	if p != nil {
		return nil, p
	}
	scope := watchlistTitleScope(ctx, viewer)
	var after *watchlist.PageKey
	if in.Cursor != "" {
		var pos watchlistTitlePosition
		if p := cursors.Decode(scope, in.Cursor, &pos); p != nil {
			return nil, p
		}
		after = &watchlist.PageKey{AddedAt: pos.AddedAt, TitleID: pos.TitleID}
	}
	entries, err := reg.deps.WatchlistTitles.ListWatchlistTitlesPage(ctx, viewer, after, in.Limit+1)
	if err != nil {
		return nil, serviceProblem(err)
	}
	next := ""
	if len(entries) > in.Limit {
		entries = entries[:in.Limit]
		last := entries[len(entries)-1].PageKey()
		if next, err = cursors.Encode(scope, watchlistTitlePosition{AddedAt: last.AddedAt, TitleID: last.TitleID}); err != nil {
			return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
		}
	}
	visible := make([]watchlist.Entry, 0, len(entries))
	titles := make([]mediarequests.WatchlistTitle, 0, len(entries))
	for _, e := range entries {
		// The stored US certification alone, as Discover filters; a title
		// without one fails closed under a ceiling.
		if ceiling != "" && !access.RatingAllowed(e.Title.Certification, ceiling) {
			continue
		}
		visible = append(visible, e)
		titles = append(titles, requestTitleOf(e.Title.Snapshot()))
	}
	states, err := reg.deps.WatchlistRequests.WatchlistRequestStates(ctx, rv, titles)
	if err != nil {
		return nil, requestProblem(err)
	}
	items := make([]WatchlistTitle, 0, len(visible))
	for _, e := range visible {
		t := e.Title
		item := WatchlistTitle{
			MediaType: t.MediaType, TMDBID: t.TMDBID, Title: t.Title, Year: t.Year, PosterPath: t.PosterPath,
			VoteAverage: t.VoteAverage, ContentRating: t.Certification, AddedAt: NewInstant(e.AddedAt),
			Status:  string(t.State),
			Request: requestMediaStateOf(states[mediarequests.WatchlistKey{MediaType: mediarequests.MediaType(t.MediaType), TMDBID: t.TMDBID}]),
		}
		if t.ReleaseDate != nil {
			item.ReleaseDate = t.ReleaseDate.Format(time.DateOnly)
		}
		items = append(items, item)
	}
	return &WatchlistTitleCollectionOutput{Body: WatchlistTitleCollection{Collection: Paginated(items, next)}}, nil
}

// addWatchlistTitle saves the entry first and applies watchlist requests
// after, so a refused or failed request never loses the entry, and a repeat
// is a no-op once either step has succeeded.
func (reg *Registry) addWatchlistTitle(ctx context.Context, in *WatchlistTitleInput) (*WatchlistTitleEntryOutput, error) {
	viewer, rv, ceiling, p := reg.watchlistTitleViewer(ctx)
	if p != nil {
		return nil, p
	}
	snap, p := reg.watchlistTitleSnapshot(ctx, rv, ceiling, in)
	if p != nil {
		return nil, p
	}
	added, err := reg.deps.WatchlistTitles.AddWatchlistTitle(ctx, viewer, snap)
	if err != nil {
		return nil, serviceProblem(err)
	}
	title := requestTitleOf(snap)
	var state mediarequests.RequestState
	if added.ItemID != "" {
		// The library has it: nothing to request, only the state to report.
		states, err := reg.deps.WatchlistRequests.WatchlistRequestStates(ctx, rv, []mediarequests.WatchlistTitle{title})
		if err != nil {
			return nil, requestProblem(err)
		}
		state = states[mediarequests.WatchlistKey{MediaType: title.MediaType, TMDBID: title.TMDBID}]
	} else {
		if state, err = reg.deps.WatchlistRequests.RequestFromWatchlist(ctx, rv, title); err != nil {
			return nil, requestProblem(err)
		}
		// A delete of the same title can run between the save and the
		// request: it withdraws, then removes the entry. Rechecking the entry
		// after requesting, with the delete withdrawing again after its
		// removal, leaves no request behind whichever way the two interleave.
		off, err := reg.deps.WatchlistTitles.WatchlistTitleOff(ctx, viewer, snap)
		if err != nil {
			return nil, serviceProblem(err)
		}
		if off {
			if err := reg.withdrawWatchlistRequests(ctx, rv, snap.MediaType, append([]int{snap.TMDBID}, snap.FormerTMDBIDs...)); err != nil {
				return nil, requestProblem(err)
			}
			states, err := reg.deps.WatchlistRequests.WatchlistRequestStates(ctx, rv, []mediarequests.WatchlistTitle{title})
			if err != nil {
				return nil, requestProblem(err)
			}
			state = states[mediarequests.WatchlistKey{MediaType: title.MediaType, TMDBID: title.TMDBID}]
		}
	}
	return &WatchlistTitleEntryOutput{Body: WatchlistTitleEntry{
		MediaType: snap.MediaType, TMDBID: snap.TMDBID, AddedAt: NewInstant(added.AddedAt),
		ItemID: ID(added.ItemID), Request: requestMediaStateOf(state),
	}}, nil
}

// watchlistTitleSnapshot resolves what the add stores: a title a watchlist
// already tracks under the ID (current or former) keeps its stored snapshot,
// which the refresh keeps current; any other title is read from TMDB. A
// title TMDB doesn't have or above the viewer's ceiling is a 404, as the
// title detail answers it.
func (reg *Registry) watchlistTitleSnapshot(ctx context.Context, rv mediarequests.Viewer, ceiling string, in *WatchlistTitleInput) (watchlist.Snapshot, *Problem) {
	notFound := NewProblem(TypeNotFound, "The title was not found.")
	known, err := reg.deps.WatchlistTitles.FindWatchlistTitle(ctx, in.MediaType, in.TMDBID)
	if err != nil {
		return watchlist.Snapshot{}, serviceProblem(err)
	}
	if known != nil {
		if ceiling != "" && !access.RatingAllowed(known.Certification, ceiling) {
			return watchlist.Snapshot{}, notFound
		}
		return known.Snapshot(), nil
	}
	detail, err := reg.deps.WatchlistRequests.WatchlistTitleDetail(ctx, rv, mediarequests.MediaType(in.MediaType), in.TMDBID)
	if errors.Is(err, mediarequests.ErrNotFound) {
		return watchlist.Snapshot{}, notFound
	}
	if err != nil {
		return watchlist.Snapshot{}, requestProblem(err)
	}
	snap, err := watchlist.SnapshotFromDetail(detail)
	if err != nil {
		slog.WarnContext(ctx, "watchlist title detail is unusable", "component", "watchlist",
			"media_type", in.MediaType, "tmdb_id", in.TMDBID, "error", err)
		return watchlist.Snapshot{}, notFound
	}
	return snap, nil
}

// deleteWatchlistTitle withdraws the watchlist's request under every TMDB ID
// the title has had, removes the entries, then withdraws once more if the
// title is still off the watchlist. A request keeps the ID it was made under.
// The first withdrawal runs before the removal because removing the last
// entry drops the title and its former IDs: a failure part way leaves the
// entry, so a retry still knows every ID. The second catches a request an
// overlapping add made after the first; it is skipped when a re-add already
// put the title back, whose request is wanted. The add rechecks its own entry
// after requesting for the other order.
func (reg *Registry) deleteWatchlistTitle(ctx context.Context, in *WatchlistTitleInput) (*struct{}, error) {
	viewer, rv, _, p := reg.watchlistTitleViewer(ctx)
	if p != nil {
		return nil, p
	}
	known, err := reg.deps.WatchlistTitles.FindWatchlistTitle(ctx, in.MediaType, in.TMDBID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	snap := watchlist.Snapshot{MediaType: in.MediaType, TMDBID: in.TMDBID}
	ids := []int{in.TMDBID}
	if known != nil {
		snap = known.Snapshot()
		ids = append(ids, known.TMDBID)
		ids = append(ids, known.FormerTMDBIDs...)
	}
	if err := reg.withdrawWatchlistRequests(ctx, rv, in.MediaType, ids); err != nil {
		return nil, requestProblem(err)
	}
	if _, err := reg.deps.WatchlistTitles.RemoveWatchlistTitle(ctx, viewer, in.MediaType, in.TMDBID); err != nil {
		return nil, serviceProblem(err)
	}
	off, err := reg.deps.WatchlistTitles.WatchlistTitleOff(ctx, viewer, snap)
	if err != nil {
		return nil, serviceProblem(err)
	}
	if off {
		if err := reg.withdrawWatchlistRequests(ctx, rv, in.MediaType, ids); err != nil {
			return nil, requestProblem(err)
		}
	}
	return nil, nil
}

// withdrawWatchlistRequests withdraws the viewer's watchlist request under
// each distinct TMDB ID.
func (reg *Registry) withdrawWatchlistRequests(ctx context.Context, rv mediarequests.Viewer, mediaType string, ids []int) error {
	seen := make([]int, 0, len(ids))
	for _, id := range ids {
		if slices.Contains(seen, id) {
			continue
		}
		seen = append(seen, id)
		if err := reg.deps.WatchlistRequests.WithdrawWatchlistRequest(ctx, rv, mediarequests.MediaType(mediaType), id); err != nil {
			return err
		}
	}
	return nil
}

// requestTitleOf is the requests service's view of a stored title.
func requestTitleOf(s watchlist.Snapshot) mediarequests.WatchlistTitle {
	return mediarequests.WatchlistTitle{
		MediaType: mediarequests.MediaType(s.MediaType), TMDBID: s.TMDBID, IMDbID: s.IMDbID, TVDBID: s.TVDBID,
		Title: s.Title, Year: s.Year, PosterPath: s.PosterPath, FormerTMDBIDs: s.FormerTMDBIDs,
	}
}

// watchlistMark is one discovery result or detail whose in_watchlist a page
// hydrates.
type watchlistMark struct {
	mediaType string
	tmdbID    int
	itemID    string
	in        *bool
}

func watchlistMarksOf(results []RequestMediaResult) []watchlistMark {
	marks := make([]watchlistMark, 0, len(results))
	for i := range results {
		r := &results[i]
		marks = append(marks, watchlistMark{mediaType: r.MediaType, tmdbID: r.TMDBID, itemID: r.LibraryContentID, in: &r.InWatchlist})
	}
	return marks
}

// markInWatchlist sets in_watchlist on a page of results from two reads: the
// watchlist titles by TMDB ID, and the library watchlist by catalog item. A
// failure is logged and leaves the flags false; it never fails the page.
func (reg *Registry) markInWatchlist(ctx context.Context, marks []watchlistMark) {
	if reg.deps.WatchlistTitles == nil || len(marks) == 0 {
		return
	}
	viewer, p := personalListViewer(ctx, "")
	if p != nil {
		return
	}
	keys := make([]watchlist.TitleKey, 0, len(marks))
	itemIDs := make([]string, 0, len(marks))
	for _, m := range marks {
		if m.tmdbID > 0 {
			keys = append(keys, watchlist.TitleKey{MediaType: m.mediaType, TMDBID: m.tmdbID})
		}
		if m.itemID != "" {
			itemIDs = append(itemIDs, m.itemID)
		}
	}
	onTitles, onItems, err := reg.deps.WatchlistTitles.WatchlistMembership(ctx, viewer, keys, itemIDs)
	if err != nil {
		slog.WarnContext(ctx, "reading watchlist membership for request results failed", "component", "watchlist", "error", err)
		return
	}
	for _, m := range marks {
		*m.in = onTitles[watchlist.TitleKey{MediaType: m.mediaType, TMDBID: m.tmdbID}] || (m.itemID != "" && onItems[m.itemID])
	}
}
