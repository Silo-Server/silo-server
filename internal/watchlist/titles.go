package watchlist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Media types a watchlist title can have, as TMDB details report them.
const (
	mediaTypeMovie  = "movie"
	mediaTypeSeries = "series"
)

// TitleState is a watchlist title's standing on TMDB.
type TitleState string

const (
	// TitleActive is a title TMDB lists under its current ID.
	TitleActive TitleState = "active"
	// TitleNeedsReview is a title whose TMDB ID was deleted and whose other
	// IDs led to several candidates. The user picks one.
	TitleNeedsReview TitleState = "needs_review"
	// TitleRemoved is a title whose TMDB ID was deleted and whose other IDs
	// led nowhere. The entry stays until the user removes it.
	TitleRemoved TitleState = "removed"
)

// Title is a movie or series some profile watchlisted before the library had
// it, with the display snapshot from its last TMDB check.
type Title struct {
	ID             int64
	MediaType      string // "movie" or "series"
	TMDBID         int
	IMDbID         string
	TVDBID         int
	Title          string
	Year           int
	ReleaseDate    *time.Time
	PosterPath     string
	Certification  string // US certification
	VoteAverage    *float64
	State          TitleState
	NotFoundCount  int
	LastNotFoundAt *time.Time
	CheckedAt      *time.Time
	NextCheckAt    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// FormerTMDBIDs are the TMDB IDs the title held before TMDB repointed
	// it. Requests made under one of them still belong to the title.
	FormerTMDBIDs []int
}

// Key returns the title's current TMDB identity.
func (t Title) Key() TitleKey { return TitleKey{MediaType: t.MediaType, TMDBID: t.TMDBID} }

// Snapshot returns the title's stored IDs and display fields, as a TMDB
// detail last reported them.
func (t Title) Snapshot() Snapshot {
	return Snapshot{
		MediaType:     t.MediaType,
		TMDBID:        t.TMDBID,
		IMDbID:        t.IMDbID,
		TVDBID:        t.TVDBID,
		Title:         t.Title,
		Year:          t.Year,
		ReleaseDate:   t.ReleaseDate,
		PosterPath:    t.PosterPath,
		Certification: t.Certification,
		VoteAverage:   t.VoteAverage,
		FormerTMDBIDs: t.FormerTMDBIDs,
	}
}

// Entry is one profile's watchlist entry for a title.
type Entry struct {
	Title   Title
	AddedAt time.Time
}

// PageKey is the keyset position of an entry in a profile's list, which is
// ordered by added_at then title id, both descending.
func (e Entry) PageKey() PageKey { return PageKey{AddedAt: e.AddedAt, TitleID: e.Title.ID} }

// PageKey is a keyset cursor position. TitleID is internal and must be
// wrapped in an opaque cursor before it reaches a client.
type PageKey struct {
	AddedAt time.Time
	TitleID int64
}

// TitleKey names a title by media type and a TMDB ID it has held.
type TitleKey struct {
	MediaType string
	TMDBID    int
}

// Snapshot is what a TMDB detail says about a title: its current IDs and the
// fields a watchlist page displays.
type Snapshot struct {
	MediaType     string
	TMDBID        int
	IMDbID        string
	TVDBID        int
	Title         string
	Year          int
	ReleaseDate   *time.Time
	PosterPath    string
	Certification string
	VoteAverage   *float64
	// FormerTMDBIDs is filled from a stored title; a TMDB detail has none.
	FormerTMDBIDs []int
}

// SnapshotFromDetail converts a TMDB movie or series detail. A title without
// votes has no rating rather than a zero one.
func SnapshotFromDetail(d *tmdb.MediaDetail) (Snapshot, error) {
	if d == nil {
		return Snapshot{}, errors.New("watchlist: nil tmdb detail")
	}
	if d.MediaType != mediaTypeMovie && d.MediaType != mediaTypeSeries {
		return Snapshot{}, fmt.Errorf("watchlist: unsupported media type %q", d.MediaType)
	}
	if d.ID <= 0 {
		return Snapshot{}, fmt.Errorf("watchlist: invalid tmdb id %d", d.ID)
	}
	snap := Snapshot{
		MediaType:     d.MediaType,
		TMDBID:        d.ID,
		IMDbID:        strings.TrimSpace(d.IMDbID),
		TVDBID:        max(d.TVDBID, 0),
		Title:         d.Title,
		Year:          d.Year,
		PosterPath:    d.PosterPath,
		Certification: d.USCertification,
	}
	date := d.ReleaseDate
	if date == "" {
		date = d.FirstAirDate
	}
	if parsed, err := time.Parse(time.DateOnly, date); err == nil {
		snap.ReleaseDate = &parsed
	}
	if d.VoteCount > 0 {
		vote := d.VoteAverage
		snap.VoteAverage = &vote
	}
	return snap, nil
}

// Viewer is the profile a watchlist call acts for, with the access filter
// that decides which library items it can see.
type Viewer struct {
	UserID    int
	ProfileID string
	Access    catalog.AccessFilter
}

// Effects runs the side effects of a manual watchlist add when promotion moves
// an entry onto the library watchlist: the provider export event, the
// recommendations refresh and the realtime user_state.changed event.
// handlers.PersonalDataHandler implements it.
type Effects interface {
	WatchlistPromoted(ctx context.Context, userID int, profileID, contentID string)
}

// TitleCatalog is the slice of the catalog titles need.
type TitleCatalog interface {
	ResolveProviderAliases(ctx context.Context, aliases []catalog.ProviderAlias) (map[int64][]string, error)
	ItemProviderAliases(ctx context.Context, contentID string) (string, []catalog.ProviderAlias, error)
	EnsureAccessibleIDs(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) (map[string]bool, error)
}

// TitleTMDB is the slice of the TMDB client ID repair needs.
type TitleTMDB interface {
	GetMediaDetail(ctx context.Context, mediaType string, id int) (*tmdb.MediaDetail, error)
	FindByExternalID(ctx context.Context, source, externalID string) ([]tmdb.MediaResult, error)
}

// promotionStore is the slice of the user store promotion writes through.
// Pass the notification-wrapped provider so a promoted series queues an
// interest recompute.
type promotionStore interface {
	AddToWatchlistAt(ctx context.Context, profileID, mediaItemID string, addedAt time.Time) (bool, error)
	GetWatchlistEntry(ctx context.Context, profileID, mediaItemID string) (*userstore.WatchlistEntry, error)
}

const (
	// maxChecksPerRead bounds the TMDB checks one list read starts.
	maxChecksPerRead = 3
	// maxConcurrentChecks bounds the checks running on this node.
	maxConcurrentChecks = 4
	// maxConcurrentPromotions bounds the promotions on this node that hold a
	// title lock while the library add takes a second pool connection.
	maxConcurrentPromotions = 4
	checkTimeout            = 30 * time.Second
	// claimLease is how long a claimed check is reserved for its node; a node
	// that dies mid-check releases it by letting it expire.
	claimLease = time.Hour
	// notFoundConfirmGap separates the two 404s that confirm a deletion.
	notFoundConfirmGap = 24 * time.Hour
	errorBackoff       = 6 * time.Hour
	recentRelease      = 180 * 24 * time.Hour
	checkRecent        = 24 * time.Hour
	checkSettled       = 30 * 24 * time.Hour
)

// nextCheckAfterSuccess is the cadence for a title TMDB answered for: daily
// while its release is unknown, upcoming or recent (IDs still settle), then
// monthly.
func nextCheckAfterSuccess(now time.Time, releaseDate *time.Time) time.Time {
	if releaseDate == nil || releaseDate.After(now) || now.Sub(*releaseDate) <= recentRelease {
		return now.Add(checkRecent)
	}
	return now.Add(checkSettled)
}

// Titles keeps profiles' watchlist entries for titles the library doesn't
// have: adding and removing them, moving them onto the library watchlist once
// the library has the title (promotion), and keeping their TMDB IDs current
// (repair). Repair has no scheduled task: detail views and list reads drive
// it.
type Titles struct {
	repo     *titlesRepo
	catalog  TitleCatalog
	storeFor func(ctx context.Context, userID int) (promotionStore, error)
	tmdb     TitleTMDB
	effects  Effects
	now      func() time.Time

	checkSlots chan struct{}
	checks     sync.WaitGroup
	// addUnderLock runs the library watchlist write while the title lock is
	// held. A one-connection pool can't, so it writes first and then locks;
	// a remove racing that promotion can leave the item on the library
	// watchlist.
	addUnderLock bool
	// promoteSlots bounds the promotions holding a transaction while they
	// wait for a second connection, so they can never take the whole pool.
	promoteSlots chan struct{}
}

// NewTitles builds the service. stores should be the notification-wrapped
// provider. tmdbClient may be nil, which disables repair; effects may be nil.
func NewTitles(pool *pgxpool.Pool, items TitleCatalog, stores userstore.UserStoreProvider, tmdbClient TitleTMDB, effects Effects) *Titles {
	t := &Titles{
		repo:       &titlesRepo{pool: pool},
		catalog:    items,
		tmdb:       tmdbClient,
		effects:    effects,
		now:        func() time.Time { return time.Now().UTC() },
		checkSlots: make(chan struct{}, maxConcurrentChecks),
	}
	promotions := maxConcurrentPromotions
	t.addUnderLock = true
	if pool != nil {
		// Each promotion holds up to two connections; half the pool leaves the
		// rest for everything else.
		maxConns := int(pool.Config().MaxConns)
		promotions = min(maxConcurrentPromotions, max(1, maxConns/2))
		// With one connection the library write could never get the second
		// connection it needs while the lock's transaction holds the first.
		t.addUnderLock = maxConns >= 2
	}
	t.promoteSlots = make(chan struct{}, promotions)
	if stores != nil {
		t.storeFor = func(ctx context.Context, userID int) (promotionStore, error) {
			return stores.ForUser(ctx, userID)
		}
	}
	return t
}

// SetEffects wires the promotion side effects after construction, for callers
// whose Effects implementation is built later.
func (s *Titles) SetEffects(effects Effects) {
	if s != nil {
		s.effects = effects
	}
}

// Add puts the title from a TMDB detail on the viewer's watchlist. Adding a
// title already there keeps its original added_at; inserted reports whether
// this call created the entry.
func (s *Titles) Add(ctx context.Context, v Viewer, detail *tmdb.MediaDetail) (Entry, bool, error) {
	snap, err := SnapshotFromDetail(detail)
	if err != nil {
		return Entry{}, false, err
	}
	now := s.now()
	return s.repo.add(ctx, v.UserID, v.ProfileID, snap, now, now)
}

// AddSnapshot is Add for callers that already hold a snapshot, such as an
// import that keeps the source's added_at.
func (s *Titles) AddSnapshot(ctx context.Context, v Viewer, snap Snapshot, addedAt time.Time) (Entry, bool, error) {
	if snap.MediaType != mediaTypeMovie && snap.MediaType != mediaTypeSeries || snap.TMDBID <= 0 {
		return Entry{}, false, fmt.Errorf("watchlist: invalid title %s/%d", snap.MediaType, snap.TMDBID)
	}
	return s.repo.add(ctx, v.UserID, v.ProfileID, snap, addedAt, s.now())
}

// Remove takes the title holding the TMDB ID, current or former, off the
// viewer's watchlist. It returns the title (nil when no title holds the ID)
// and whether an entry was removed.
func (s *Titles) Remove(ctx context.Context, v Viewer, mediaType string, tmdbID int) (*Title, bool, error) {
	return s.repo.removeByTMDB(ctx, v.UserID, v.ProfileID, mediaType, tmdbID)
}

// Find returns the title holding the TMDB ID, current or former, or nil.
func (s *Titles) Find(ctx context.Context, mediaType string, tmdbID int) (*Title, error) {
	return s.repo.titleByTMDB(ctx, mediaType, tmdbID)
}

// ListPage returns up to limit of the viewer's entries after the cursor,
// newest first, from stored snapshots. It starts background checks for a few
// overdue titles on the page and never waits for them. Call PromoteProfile
// first so titles the library now has leave the list.
func (s *Titles) ListPage(ctx context.Context, v Viewer, after *PageKey, limit int) ([]Entry, error) {
	if limit <= 0 {
		return nil, nil
	}
	entries, err := s.repo.listPage(ctx, v.UserID, v.ProfileID, after, limit)
	if err != nil {
		return nil, err
	}
	titles := make([]Title, len(entries))
	for i, e := range entries {
		titles[i] = e.Title
	}
	s.ScheduleChecks(ctx, titles)
	return entries, nil
}

// OnWatchlist reports which keys the viewer has an entry for, matching any
// TMDB ID the entry's title has held. Discover uses it to mark results.
func (s *Titles) OnWatchlist(ctx context.Context, v Viewer, keys []TitleKey) (map[TitleKey]bool, error) {
	return s.repo.onWatchlist(ctx, v.UserID, v.ProfileID, keys)
}

// PurgeProfile removes a deleted profile's entries and the titles left with
// none. It implements the profile handler's purge hook.
func (s *Titles) PurgeProfile(ctx context.Context, userID int, profileID string) error {
	return s.repo.purgeProfile(ctx, userID, profileID)
}

// LibraryMatches returns every library item (enabled folders, same media
// type) that carries or once carried any of the IDs. The add path uses it to
// send a title the library already has to the library watchlist instead.
func (s *Titles) LibraryMatches(ctx context.Context, mediaType string, tmdbIDs []int, imdbID string, tvdbID int) ([]string, error) {
	aliases := make([]catalog.ProviderAlias, 0, len(tmdbIDs)+2)
	for _, id := range tmdbIDs {
		aliases = append(aliases, catalog.ProviderAlias{MediaType: mediaType, Provider: contentid.ProviderTMDB, ProviderID: strconv.Itoa(id)})
	}
	if imdbID != "" {
		aliases = append(aliases, catalog.ProviderAlias{MediaType: mediaType, Provider: contentid.ProviderIMDB, ProviderID: imdbID})
	}
	if tvdbID > 0 {
		aliases = append(aliases, catalog.ProviderAlias{MediaType: mediaType, Provider: contentid.ProviderTVDB, ProviderID: strconv.Itoa(tvdbID)})
	}
	matches, err := s.catalog.ResolveProviderAliases(ctx, aliases)
	if err != nil {
		return nil, err
	}
	return matches[0], nil
}

// PromoteProfile moves every entry of the viewer whose title the library now
// has onto the library watchlist, keeping its added_at. It returns the
// content IDs this call put on the library watchlist, the ones it ran the
// side effects for. A profile with no entries costs one index
// probe.
func (s *Titles) PromoteProfile(ctx context.Context, v Viewer) ([]string, error) {
	addedAt, aliases, err := s.repo.profileEntries(ctx, v.UserID, v.ProfileID)
	if err != nil || len(addedAt) == 0 {
		return nil, err
	}
	return s.promote(ctx, v, addedAt, aliases)
}

// PromoteItem promotes the viewer's entries whose title carries any of the
// catalog item's current or former provider IDs. Item detail calls it before
// reporting in_watchlist.
func (s *Titles) PromoteItem(ctx context.Context, v Viewer, contentID string) ([]string, error) {
	mediaType, itemAliases, err := s.catalog.ItemProviderAliases(ctx, contentID)
	if err != nil || len(itemAliases) == 0 {
		return nil, err
	}
	providers := make([]string, len(itemAliases))
	providerIDs := make([]string, len(itemAliases))
	for i, a := range itemAliases {
		providers[i], providerIDs[i] = a.Provider, a.ProviderID
	}
	addedAt, aliases, err := s.repo.profileEntriesMatching(ctx, v.UserID, v.ProfileID, mediaType, providers, providerIDs)
	if err != nil || len(addedAt) == 0 {
		return nil, err
	}
	return s.promote(ctx, v, addedAt, aliases)
}

// promote moves each resolved entry onto the library watchlist. Under the
// title lock, and only while the entry still exists, it adds the item to the
// library watchlist and then deletes the entry, so a concurrent remove either
// runs first (and nothing is added) or finds the library entry to remove. Only
// the caller whose delete removed the entry runs the side effects, and only
// when the library entry is new to the user: its add inserted it, or it
// carries the entry's added_at because a node died between the add and the
// delete. An item that was already on the library watchlist fires nothing.
func (s *Titles) promote(ctx context.Context, v Viewer, addedAt map[int64]time.Time, aliases []titleAlias) ([]string, error) {
	if s.catalog == nil || s.storeFor == nil {
		return nil, nil
	}
	requested := make([]catalog.ProviderAlias, len(aliases))
	for i, a := range aliases {
		requested[i] = catalog.ProviderAlias{Key: a.TitleID, MediaType: a.MediaType, Provider: a.Provider, ProviderID: a.ProviderID}
	}
	matches, err := s.catalog.ResolveProviderAliases(ctx, requested)
	if err != nil || len(matches) == 0 {
		return nil, err
	}
	titleIDs := make([]int64, 0, len(matches))
	for id := range matches {
		titleIDs = append(titleIDs, id)
	}
	slices.Sort(titleIDs)

	// One access check for every matched copy, rather than one per copy.
	var candidates []string
	for _, ids := range matches {
		for _, id := range ids {
			if !slices.Contains(candidates, id) {
				candidates = append(candidates, id)
			}
		}
	}
	accessible, err := s.catalog.EnsureAccessibleIDs(ctx, candidates, v.Access)
	if err != nil {
		return nil, fmt.Errorf("checking access to watchlist titles' library items: %w", err)
	}

	store, err := s.storeFor(ctx, v.UserID)
	if err != nil {
		return nil, fmt.Errorf("opening user store for watchlist promotion: %w", err)
	}
	var promoted []string
	var errs []error
	for _, titleID := range titleIDs {
		// Only the copies this viewer may see count: duplicates in libraries
		// the viewer can't open don't make the title ambiguous for them.
		var contentIDs []string
		for _, id := range matches[titleID] {
			if accessible[id] {
				contentIDs = append(contentIDs, id)
			}
		}
		if len(contentIDs) == 0 {
			continue
		}
		if len(contentIDs) > 1 {
			slog.WarnContext(ctx, "watchlist title matches several library items; not promoting until they are merged",
				"component", "watchlist", "title_id", titleID, "content_ids", contentIDs)
			continue
		}
		contentID := contentIDs[0]
		moved, newToUser, err := s.promoteOne(ctx, store, v, titleID, contentID, addedAt[titleID])
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !moved || !newToUser || slices.Contains(promoted, contentID) {
			continue
		}
		promoted = append(promoted, contentID)
		if s.effects != nil {
			s.effects.WatchlistPromoted(ctx, v.UserID, v.ProfileID, contentID)
		}
	}
	return promoted, errors.Join(errs...)
}

// promoteOne moves one entry under its title lock. moved reports that this
// call deleted the entry; newToUser that the library entry is the promotion's
// own rather than one the profile already had.
func (s *Titles) promoteOne(ctx context.Context, store promotionStore, v Viewer, titleID int64, contentID string, entryAddedAt time.Time) (moved, newToUser bool, err error) {
	select {
	case s.promoteSlots <- struct{}{}:
	case <-ctx.Done():
		return false, false, ctx.Err()
	}
	defer func() { <-s.promoteSlots }()
	addToLibrary := func(ctx context.Context) error {
		inserted, err := store.AddToWatchlistAt(ctx, v.ProfileID, contentID, entryAddedAt)
		if err != nil {
			return fmt.Errorf("adding promoted watchlist item: %w", err)
		}
		if inserted {
			newToUser = true
			return nil
		}
		existing, err := store.GetWatchlistEntry(ctx, v.ProfileID, contentID)
		if err != nil {
			return fmt.Errorf("reading promoted watchlist item: %w", err)
		}
		newToUser = existing != nil && sameStoredTime(existing.AddedAt, entryAddedAt)
		return nil
	}
	if !s.addUnderLock {
		// Skip an entry a remove already took, so the window in which a
		// remove can race the write shrinks to the moments before the lock.
		if present, err := s.repo.entryExists(ctx, v.UserID, v.ProfileID, titleID); err != nil || !present {
			return false, false, err
		}
		if err := addToLibrary(ctx); err != nil {
			return false, false, err
		}
		moved, err = s.repo.promoteEntry(ctx, v.UserID, v.ProfileID, titleID, func(context.Context) error { return nil })
		return moved, newToUser, err
	}
	moved, err = s.repo.promoteEntry(ctx, v.UserID, v.ProfileID, titleID, addToLibrary)
	return moved, newToUser, err
}

// sameStoredTime reports whether a user store timestamp is t. Stores keep
// whole seconds, so both sides are compared at that precision.
func sameStoredTime(stored string, t time.Time) bool {
	parsed, err := time.Parse(time.RFC3339Nano, stored)
	if err != nil {
		return false
	}
	return parsed.Truncate(time.Second).Equal(t.Truncate(time.Second))
}

// PromoteWatchlist runs PromoteProfile ahead of a library watchlist read for
// the profile the access filter names. A failure is logged, not returned: the
// read is still right about the entries it has, and the next read retries.
// It implements catalog.WatchlistPromoter.
func (s *Titles) PromoteWatchlist(ctx context.Context, access catalog.AccessFilter) {
	v, ok := viewerFromAccess(access)
	if s == nil || !ok {
		return
	}
	if _, err := s.PromoteProfile(ctx, v); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "watchlist promotion failed; the read continues without it",
			"component", "watchlist", "user_id", v.UserID, "profile_id", v.ProfileID, "error", err)
	}
}

// PromoteWatchlistItem runs PromoteItem ahead of a read that reports whether
// the item is on the profile's watchlist, logging a failure like
// PromoteWatchlist.
func (s *Titles) PromoteWatchlistItem(ctx context.Context, access catalog.AccessFilter, contentID string) {
	v, ok := viewerFromAccess(access)
	if s == nil || !ok || contentID == "" {
		return
	}
	if _, err := s.PromoteItem(ctx, v, contentID); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "watchlist promotion failed; the read continues without it",
			"component", "watchlist", "user_id", v.UserID, "profile_id", v.ProfileID, "content_id", contentID, "error", err)
	}
}

func viewerFromAccess(access catalog.AccessFilter) (Viewer, bool) {
	profileID := strings.TrimSpace(access.ProfileID)
	if access.UserID <= 0 || profileID == "" {
		return Viewer{}, false
	}
	return Viewer{UserID: access.UserID, ProfileID: profileID, Access: access}, true
}

// SweepOrphanTitles deletes up to a batch of titles no entry references. User
// deletion removes entries through the users foreign key without the title
// lock; the admin delete paths call this afterwards.
func (s *Titles) SweepOrphanTitles(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.repo.sweepOrphans(ctx)
}
