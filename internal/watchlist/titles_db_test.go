package watchlist

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// titleTMDBStub answers detail and find calls from maps and counts them.
type titleTMDBStub struct {
	mu          sync.Mutex
	details     map[string]*tmdb.MediaDetail
	detailErrs  map[string]error
	finds       map[string][]tmdb.MediaResult
	findErr     error
	detailCalls int
}

func newTitleTMDBStub() *titleTMDBStub {
	return &titleTMDBStub{
		details:    map[string]*tmdb.MediaDetail{},
		detailErrs: map[string]error{},
		finds:      map[string][]tmdb.MediaResult{},
	}
}

func (s *titleTMDBStub) GetMediaDetail(_ context.Context, mediaType string, id int) (*tmdb.MediaDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detailCalls++
	key := mediaType + ":" + strconv.Itoa(id)
	if err := s.detailErrs[key]; err != nil {
		return nil, err
	}
	if d := s.details[key]; d != nil {
		return d, nil
	}
	return nil, fmt.Errorf("%w: %s", tmdb.ErrNotFound, key)
}

func (s *titleTMDBStub) FindByExternalID(_ context.Context, source, externalID string) ([]tmdb.MediaResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findErr != nil {
		return nil, s.findErr
	}
	return s.finds[source+":"+externalID], nil
}

func (s *titleTMDBStub) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detailCalls
}

// promotionStoreStub is an in-memory library watchlist. Like the Postgres
// user store it reports added_at in whole seconds.
type promotionStoreStub struct {
	mu      sync.Mutex
	entries map[string]time.Time
	// beforeAdd, when set, runs at the start of every add.
	beforeAdd func()
}

func (s *promotionStoreStub) AddToWatchlistAt(_ context.Context, profileID, mediaItemID string, addedAt time.Time) (bool, error) {
	if s.beforeAdd != nil {
		s.beforeAdd()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := profileID + "/" + mediaItemID
	if _, ok := s.entries[key]; ok {
		return false, nil
	}
	s.entries[key] = addedAt
	return true, nil
}

func (s *promotionStoreStub) GetWatchlistEntry(_ context.Context, profileID, mediaItemID string) (*userstore.WatchlistEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.entries[profileID+"/"+mediaItemID]
	if !ok {
		return nil, nil
	}
	return &userstore.WatchlistEntry{ProfileID: profileID, MediaItemID: mediaItemID, AddedAt: at.UTC().Format(time.RFC3339)}, nil
}

func (s *promotionStoreStub) set(profileID, mediaItemID string, addedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[profileID+"/"+mediaItemID] = addedAt
}

func (s *promotionStoreStub) get(profileID, mediaItemID string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.entries[profileID+"/"+mediaItemID]
	return at, ok
}

type effectsRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (e *effectsRecorder) WatchlistPromoted(_ context.Context, userID int, profileID, contentID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, fmt.Sprintf("%d/%s/%s", userID, profileID, contentID))
}

func (e *effectsRecorder) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.calls)
}

type titlesFixture struct {
	pool    *pgxpool.Pool
	svc     *Titles
	tmdb    *titleTMDBStub
	store   *promotionStoreStub
	effects *effectsRecorder
	prefix  string
	userID  int
	folder  int
	base    int
	now     time.Time
}

func newTitlesFixture(t *testing.T) *titlesFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	stamp := time.Now().UnixNano()
	f := &titlesFixture{
		pool:    pool,
		tmdb:    newTitleTMDBStub(),
		store:   &promotionStoreStub{entries: map[string]time.Time{}},
		effects: &effectsRecorder{},
		prefix:  fmt.Sprintf("wlt-%d", stamp),
		// TMDB IDs unique to this run, below the integer column's limit.
		base: 1_000_000_000 + int(stamp/1000%100_000)*10_000,
		now:  time.Now().UTC().Truncate(time.Second),
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, f.prefix).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('mixed', $1, TRUE) RETURNING id`, f.prefix).Scan(&f.folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		f.svc.waitChecks()
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM watchlist_titles WHERE tmdb_id BETWEEN $1 AND $2`, f.base, f.base+9_999)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, f.prefix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, f.folder)
	})
	f.svc = NewTitles(pool, catalog.NewItemRepository(pool), nil, f.tmdb, f.effects)
	f.svc.storeFor = func(context.Context, int) (promotionStore, error) { return f.store, nil }
	f.svc.now = func() time.Time { return f.now }
	return f
}

func (f *titlesFixture) id(n int) int { return f.base + n }

func (f *titlesFixture) viewer(profileID string) Viewer {
	return Viewer{UserID: f.userID, ProfileID: profileID}
}

func (f *titlesFixture) snap(mediaType string, n int, imdbID string, tvdbID int) Snapshot {
	release := time.Date(2001, 5, 4, 0, 0, 0, 0, time.UTC)
	vote := 7.25
	return Snapshot{
		MediaType: mediaType, TMDBID: f.id(n), IMDbID: imdbID, TVDBID: tvdbID,
		Title: fmt.Sprintf("Title %d", n), Year: 2001, ReleaseDate: &release,
		PosterPath: "/p.jpg", Certification: "PG", VoteAverage: &vote,
	}
}

func (f *titlesFixture) detail(snap Snapshot) *tmdb.MediaDetail {
	d := &tmdb.MediaDetail{
		MediaType: snap.MediaType, ID: snap.TMDBID, IMDbID: snap.IMDbID, TVDBID: snap.TVDBID,
		Title: snap.Title, Year: snap.Year, PosterPath: snap.PosterPath, USCertification: snap.Certification,
	}
	if snap.ReleaseDate != nil {
		d.ReleaseDate = snap.ReleaseDate.Format(time.DateOnly)
	}
	if snap.VoteAverage != nil {
		d.VoteAverage, d.VoteCount = *snap.VoteAverage, 10
	}
	return d
}

func (f *titlesFixture) add(t *testing.T, profileID string, snap Snapshot, addedAt time.Time) Entry {
	t.Helper()
	entry, _, err := f.svc.AddSnapshot(t.Context(), f.viewer(profileID), snap, addedAt)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func (f *titlesFixture) title(t *testing.T, id int64) *Title {
	t.Helper()
	title, err := f.svc.repo.titleByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return title
}

func (f *titlesFixture) aliases(t *testing.T, id int64) []string {
	t.Helper()
	aliases, err := f.svc.repo.aliasesOf(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(aliases))
	for i, a := range aliases {
		out[i] = a.Provider + ":" + a.ProviderID
	}
	return out
}

func (f *titlesFixture) entryAddedAt(t *testing.T, profileID string, titleID int64) (time.Time, bool) {
	t.Helper()
	var at time.Time
	err := f.pool.QueryRow(t.Context(), `
		SELECT added_at FROM user_watchlist_titles WHERE user_id = $1 AND profile_id = $2 AND title_id = $3`,
		f.userID, profileID, titleID).Scan(&at)
	if err != nil {
		return time.Time{}, false
	}
	return at.UTC(), true
}

// item creates a library item in the fixture's enabled folder.
func (f *titlesFixture) item(t *testing.T, suffix, mediaType, tmdbID, imdbID string) string {
	t.Helper()
	contentID := f.prefix + "-" + suffix
	if _, err := f.pool.Exec(t.Context(), `
		INSERT INTO media_items (content_id, type, title, genres, tmdb_id, imdb_id)
		VALUES ($1, $2, $1, '{}'::text[], $3, $4)`, contentID, mediaType, tmdbID, imdbID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, f.folder); err != nil {
		t.Fatal(err)
	}
	return contentID
}

// confirmable marks a title as having had one 404 more than a day ago, so
// the next 404 confirms the deletion.
func (f *titlesFixture) confirmable(t *testing.T, id int64) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `
		UPDATE watchlist_titles SET not_found_count = 1, last_not_found_at = $2, next_check_at = $3 WHERE id = $1`,
		id, f.now.Add(-25*time.Hour), f.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestTitlesAddListRemove(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	t0 := f.now.Add(-3 * time.Hour)
	a := f.add(t, "p1", f.snap("movie", 1, "tt-a", 0), t0)
	b := f.add(t, "p1", f.snap("movie", 2, "", 0), t0.Add(time.Hour))
	c := f.add(t, "p1", f.snap("series", 3, "", 77), t0.Add(2*time.Hour))
	if got := f.aliases(t, c.Title.ID); !slices.Equal(got, []string{"tmdb:" + strconv.Itoa(f.id(3)), "tvdb:77"}) {
		t.Fatalf("series aliases = %v", got)
	}
	if a.Title.VoteAverage == nil || *a.Title.VoteAverage != 7.3 {
		t.Fatalf("vote average = %v, want 7.3", a.Title.VoteAverage)
	}

	// A repeat add keeps the original added_at.
	again, inserted, err := f.svc.AddSnapshot(ctx, f.viewer("p1"), f.snap("movie", 1, "tt-a", 0), f.now)
	if err != nil || inserted || !again.AddedAt.Equal(t0) || again.Title.ID != a.Title.ID {
		t.Fatalf("repeat add = %+v inserted=%v err=%v", again, inserted, err)
	}

	page, err := f.svc.ListPage(ctx, f.viewer("p1"), nil, 2)
	if err != nil || len(page) != 2 || page[0].Title.ID != c.Title.ID || page[1].Title.ID != b.Title.ID {
		t.Fatalf("first page = %+v err=%v", page, err)
	}
	after := page[1].PageKey()
	page, err = f.svc.ListPage(ctx, f.viewer("p1"), &after, 2)
	if err != nil || len(page) != 1 || page[0].Title.ID != a.Title.ID {
		t.Fatalf("second page = %+v err=%v", page, err)
	}

	on, err := f.svc.OnWatchlist(ctx, f.viewer("p1"), []TitleKey{a.Title.Key(), {MediaType: "series", TMDBID: f.id(1)}, c.Title.Key()})
	if err != nil || !on[a.Title.Key()] || !on[c.Title.Key()] || len(on) != 2 {
		t.Fatalf("OnWatchlist = %v err=%v", on, err)
	}

	// A title two profiles keep survives one removal and goes with the last.
	f.add(t, "p2", f.snap("movie", 1, "tt-a", 0), f.now)
	title, removed, err := f.svc.Remove(ctx, f.viewer("p1"), "movie", f.id(1))
	if err != nil || !removed || title == nil || title.ID != a.Title.ID {
		t.Fatalf("remove = %+v %v %v", title, removed, err)
	}
	if f.title(t, a.Title.ID) == nil {
		t.Fatal("title removed while another profile keeps it")
	}
	if _, removed, err := f.svc.Remove(ctx, f.viewer("p1"), "movie", f.id(1)); err != nil || removed {
		t.Fatalf("second remove = %v %v", removed, err)
	}
	if _, removed, err := f.svc.Remove(ctx, f.viewer("p2"), "movie", f.id(1)); err != nil || !removed {
		t.Fatalf("last remove = %v %v", removed, err)
	}
	if f.title(t, a.Title.ID) != nil {
		t.Fatal("orphaned title kept")
	}
	if title, removed, err := f.svc.Remove(ctx, f.viewer("p1"), "movie", f.id(99)); err != nil || removed || title != nil {
		t.Fatalf("unknown remove = %v %v %v", title, removed, err)
	}
}

func TestTitlesAddRacesRemovalOfLastEntry(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	for i := range 40 {
		snap := f.snap("movie", 100+i, "", 0)
		f.add(t, "leaver", snap, f.now)
		var wg sync.WaitGroup
		var addErr, removeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, removeErr = f.svc.Remove(ctx, f.viewer("leaver"), "movie", snap.TMDBID)
		}()
		go func() {
			defer wg.Done()
			_, _, addErr = f.svc.AddSnapshot(ctx, f.viewer("joiner"), snap, f.now)
		}()
		wg.Wait()
		if addErr != nil || removeErr != nil {
			t.Fatalf("round %d: add err %v, remove err %v", i, addErr, removeErr)
		}
		title, err := f.svc.Find(ctx, "movie", snap.TMDBID)
		if err != nil || title == nil {
			t.Fatalf("round %d: title gone after concurrent add: %v", i, err)
		}
		if _, ok := f.entryAddedAt(t, "joiner", title.ID); !ok {
			t.Fatalf("round %d: joiner's entry missing", i)
		}
		if _, ok := f.entryAddedAt(t, "leaver", title.ID); ok {
			t.Fatalf("round %d: leaver's entry kept", i)
		}
	}
}

func TestTitlesConcurrentAddsCreateOneTitle(t *testing.T) {
	f := newTitlesFixture(t)
	snap := f.snap("movie", 200, "tt-200", 0)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = f.svc.AddSnapshot(t.Context(), f.viewer("p"+strconv.Itoa(i)), snap, f.now)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var titles, entries int
	if err := f.pool.QueryRow(t.Context(), `
		SELECT count(DISTINCT t.id), count(e.*) FROM watchlist_titles t
		LEFT JOIN user_watchlist_titles e ON e.title_id = t.id WHERE t.tmdb_id = $1`, snap.TMDBID).Scan(&titles, &entries); err != nil {
		t.Fatal(err)
	}
	if titles != 1 || entries != 8 {
		t.Fatalf("titles=%d entries=%d, want 1 and 8", titles, entries)
	}
}

func TestTitlesPurgeProfileAndUserDelete(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	shared := f.add(t, "gone", f.snap("movie", 300, "", 0), f.now)
	f.add(t, "stays", f.snap("movie", 300, "", 0), f.now)
	own := f.add(t, "gone", f.snap("movie", 301, "", 0), f.now)
	if err := f.svc.PurgeProfile(ctx, f.userID, "gone"); err != nil {
		t.Fatal(err)
	}
	if f.title(t, own.Title.ID) != nil || f.title(t, shared.Title.ID) == nil {
		t.Fatal("purge removed the wrong titles")
	}
	if _, ok := f.entryAddedAt(t, "gone", shared.Title.ID); ok {
		t.Fatal("purged profile's entry kept")
	}

	// The Postgres user store deletes entries without the title lock; the
	// sweep that follows removes the orphan.
	cascaded := f.add(t, "cascaded", f.snap("movie", 302, "", 0), f.now)
	if _, err := f.pool.Exec(ctx, `DELETE FROM user_watchlist_titles WHERE user_id = $1 AND profile_id = 'cascaded'`, f.userID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.PurgeProfile(ctx, f.userID, "cascaded"); err != nil {
		t.Fatal(err)
	}
	if f.title(t, cascaded.Title.ID) != nil {
		t.Fatal("sweep kept an orphaned title")
	}

	if _, err := f.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.userID); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.entryAddedAt(t, "stays", shared.Title.ID); ok {
		t.Fatal("user delete kept an entry")
	}
}

func TestTitlesPromoteProfile(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	addedAt := f.now.Add(-48 * time.Hour)
	byTMDB := f.add(t, "p1", f.snap("movie", 400, "", 0), addedAt)
	byIMDb := f.add(t, "p1", f.snap("movie", 401, "tt-401", 0), addedAt.Add(time.Minute))
	ambiguous := f.add(t, "p1", f.snap("movie", 402, "tt-402", 0), f.now)
	missing := f.add(t, "p1", f.snap("movie", 403, "", 0), f.now)
	movieA := f.item(t, "a", "movie", strconv.Itoa(f.id(400)), "")
	movieB := f.item(t, "b", "movie", "", "tt-401")
	f.item(t, "dup-1", "movie", strconv.Itoa(f.id(402)), "")
	f.item(t, "dup-2", "movie", "", "tt-402")

	// A viewer who can see no library keeps every entry external.
	hidden := f.viewer("p1")
	hidden.Access = catalog.AccessFilter{AllowedLibraryIDs: []int{}}
	if promoted, err := f.svc.PromoteProfile(ctx, hidden); err != nil || len(promoted) != 0 {
		t.Fatalf("inaccessible promotion = %v %v", promoted, err)
	}

	promoted, err := f.svc.PromoteProfile(ctx, f.viewer("p1"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(promoted)
	if !slices.Equal(promoted, []string{movieA, movieB}) {
		t.Fatalf("promoted = %v", promoted)
	}
	if at, ok := f.store.get("p1", movieA); !ok || !at.Equal(addedAt) {
		t.Fatalf("library entry added_at = %v %v, want %v", at, ok, addedAt)
	}
	if at, ok := f.store.get("p1", movieB); !ok || !at.Equal(addedAt.Add(time.Minute)) {
		t.Fatalf("library entry added_at = %v %v", at, ok)
	}
	if f.title(t, byTMDB.Title.ID) != nil || f.title(t, byIMDb.Title.ID) != nil {
		t.Fatal("promoted titles kept")
	}
	if f.title(t, ambiguous.Title.ID) == nil || f.title(t, missing.Title.ID) == nil {
		t.Fatal("unresolved titles removed")
	}
	if got := f.effects.snapshot(); len(got) != 2 {
		t.Fatalf("effects = %v", got)
	}
	// Nothing left to do: no further effects.
	if promoted, err := f.svc.PromoteProfile(ctx, f.viewer("p1")); err != nil || len(promoted) != 0 {
		t.Fatalf("second promotion = %v %v", promoted, err)
	}
}

func TestTitlesConcurrentPromotionFiresEffectsOnce(t *testing.T) {
	f := newTitlesFixture(t)
	f.add(t, "p1", f.snap("series", 500, "", 0), f.now)
	contentID := f.item(t, "s", "series", strconv.Itoa(f.id(500)), "")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.svc.PromoteProfile(t.Context(), f.viewer("p1"))
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{fmt.Sprintf("%d/p1/%s", f.userID, contentID)}
	if got := f.effects.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("effects = %v, want %v", got, want)
	}
}

func TestTitlesPromoteItemThroughDeadAlias(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	// The entry was added under a TMDB ID the library item has since
	// rejected as a duplicate.
	entry := f.add(t, "p1", f.snap("movie", 600, "", 0), f.now)
	contentID := f.item(t, "m", "movie", strconv.Itoa(f.id(601)), "")
	if _, err := f.pool.Exec(ctx, `INSERT INTO stale_media_ids (content_id, provider, provider_id) VALUES ($1, 'tmdb', $2)`, contentID, strconv.Itoa(f.id(600))); err != nil {
		t.Fatal(err)
	}
	other := f.add(t, "p1", f.snap("movie", 602, "", 0), f.now)
	promoted, err := f.svc.PromoteItem(ctx, f.viewer("p1"), contentID)
	if err != nil || !slices.Equal(promoted, []string{contentID}) {
		t.Fatalf("PromoteItem = %v %v", promoted, err)
	}
	if f.title(t, entry.Title.ID) != nil || f.title(t, other.Title.ID) == nil {
		t.Fatal("PromoteItem touched the wrong titles")
	}
	if promoted, err := f.svc.PromoteItem(ctx, f.viewer("p1"), f.prefix+"-absent"); err != nil || len(promoted) != 0 {
		t.Fatalf("absent item = %v %v", promoted, err)
	}
}

func TestTitlesCheckCadence(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	old := f.add(t, "p1", f.snap("movie", 700, "", 0), f.now)
	recentSnap := f.snap("movie", 701, "", 0)
	recentDate := f.now.AddDate(0, -1, 0).Truncate(24 * time.Hour)
	recentSnap.ReleaseDate = &recentDate
	recent := f.add(t, "p1", recentSnap, f.now)
	unknownSnap := f.snap("movie", 702, "", 0)
	unknownSnap.ReleaseDate = nil
	unknown := f.add(t, "p1", unknownSnap, f.now)

	for _, tc := range []struct {
		entry Entry
		snap  Snapshot
		want  time.Duration
	}{
		{old, f.snap("movie", 700, "", 0), checkSettled},
		{recent, recentSnap, checkRecent},
		{unknown, unknownSnap, checkRecent},
	} {
		f.tmdb.details["movie:"+strconv.Itoa(tc.snap.TMDBID)] = f.detail(tc.snap)
		if err := f.svc.checkTitle(ctx, tc.entry.Title.ID); err != nil {
			t.Fatal(err)
		}
		got := f.title(t, tc.entry.Title.ID)
		if !got.NextCheckAt.Equal(f.now.Add(tc.want)) || got.CheckedAt == nil {
			t.Errorf("tmdb %d next check = %v, want now+%v", tc.snap.TMDBID, got.NextCheckAt, tc.want)
		}
	}
}

func TestTitlesRepairFailureTable(t *testing.T) {
	t.Run("new imdb id is recorded, one another title holds is skipped", func(t *testing.T) {
		f := newTitlesFixture(t)
		title := f.add(t, "p1", f.snap("movie", 800, "", 0), f.now)
		holder := f.add(t, "p1", f.snap("series", 801, "", 55), f.now)
		updated := f.snap("movie", 800, "tt-800", 0)
		updated.Title = "Renamed"
		f.tmdb.details["movie:"+strconv.Itoa(f.id(800))] = f.detail(updated)
		if err := f.svc.checkTitle(t.Context(), title.Title.ID); err != nil {
			t.Fatal(err)
		}
		got := f.title(t, title.Title.ID)
		if got.IMDbID != "tt-800" || got.Title != "Renamed" {
			t.Fatalf("refreshed title = %+v", got)
		}
		if aliases := f.aliases(t, title.Title.ID); !slices.Contains(aliases, "imdb:tt-800") {
			t.Fatalf("aliases = %v", aliases)
		}
		// Invariant 4: a live observation never takes another title's ID.
		seriesSnap := f.snap("series", 802, "", 55)
		stolen := f.add(t, "p2", f.snap("series", 802, "", 0), f.now)
		f.svc.ObservedDetail(t.Context(), "series", f.id(802), f.detail(seriesSnap))
		if aliases := f.aliases(t, stolen.Title.ID); slices.Contains(aliases, "tvdb:55") {
			t.Fatalf("observation took another title's alias: %v", aliases)
		}
		if aliases := f.aliases(t, holder.Title.ID); !slices.Contains(aliases, "tvdb:55") {
			t.Fatalf("holder lost its alias: %v", aliases)
		}
		if f.title(t, stolen.Title.ID) == nil || f.title(t, holder.Title.ID) == nil {
			t.Fatal("observation merged titles")
		}
	})

	t.Run("a single 404 only schedules the confirming check", func(t *testing.T) {
		f := newTitlesFixture(t)
		title := f.add(t, "p1", f.snap("movie", 810, "tt-810", 0), f.now)
		if err := f.svc.checkTitle(t.Context(), title.Title.ID); err != nil {
			t.Fatal(err)
		}
		got := f.title(t, title.Title.ID)
		if got.State != TitleActive || got.NotFoundCount != 1 || !got.NextCheckAt.Equal(f.now.Add(24*time.Hour)) {
			t.Fatalf("after one 404 = %+v", got)
		}
		// A second 404 within the day does not confirm.
		f.now = f.now.Add(2 * time.Hour)
		if err := f.svc.checkTitle(t.Context(), title.Title.ID); err != nil {
			t.Fatal(err)
		}
		got = f.title(t, title.Title.ID)
		if got.State != TitleActive || got.NotFoundCount != 1 || !got.NextCheckAt.Equal(f.now.Add(22*time.Hour)) {
			t.Fatalf("after an early second 404 = %+v", got)
		}
	})

	t.Run("confirmed 404 with one imdb candidate repoints", func(t *testing.T) {
		f := newTitlesFixture(t)
		title := f.add(t, "p1", f.snap("movie", 820, "tt-820", 0), f.now)
		f.confirmable(t, title.Title.ID)
		f.tmdb.finds["imdb_id:tt-820"] = []tmdb.MediaResult{{ID: f.id(821), MediaType: "movie"}, {ID: 9, MediaType: "series"}}
		fresh := f.snap("movie", 821, "tt-820", 0)
		fresh.Title = "Kept Duplicate"
		f.tmdb.details["movie:"+strconv.Itoa(f.id(821))] = f.detail(fresh)
		if err := f.svc.checkTitle(t.Context(), title.Title.ID); err != nil {
			t.Fatal(err)
		}
		got := f.title(t, title.Title.ID)
		if got.TMDBID != f.id(821) || got.State != TitleActive || got.NotFoundCount != 0 || got.Title != "Kept Duplicate" {
			t.Fatalf("repointed title = %+v", got)
		}
		aliases := f.aliases(t, title.Title.ID)
		if !slices.Contains(aliases, "tmdb:"+strconv.Itoa(f.id(820))) || !slices.Contains(aliases, "tmdb:"+strconv.Itoa(f.id(821))) {
			t.Fatalf("aliases = %v", aliases)
		}
		// Removing by the dead ID still finds the entry.
		if _, removed, err := f.svc.Remove(t.Context(), f.viewer("p1"), "movie", f.id(820)); err != nil || !removed {
			t.Fatalf("remove by old id = %v %v", removed, err)
		}
	})

	t.Run("recovery falls back to tvdb", func(t *testing.T) {
		f := newTitlesFixture(t)
		title := f.add(t, "p1", f.snap("series", 825, "tt-825", 825), f.now)
		f.confirmable(t, title.Title.ID)
		f.tmdb.finds["tvdb_id:825"] = []tmdb.MediaResult{{ID: f.id(826), MediaType: "series"}}
		if err := f.svc.checkTitle(t.Context(), title.Title.ID); err != nil {
			t.Fatal(err)
		}
		if got := f.title(t, title.Title.ID); got.TMDBID != f.id(826) {
			t.Fatalf("tvdb recovery = %+v", got)
		}
	})

	t.Run("recovery onto another title's id merges, keeping the earliest added_at", func(t *testing.T) {
		f := newTitlesFixture(t)
		early := f.now.Add(-72 * time.Hour)
		late := f.now.Add(-time.Hour)
		dead := f.add(t, "both", f.snap("movie", 830, "tt-830", 0), early)
		f.add(t, "dead-only", f.snap("movie", 830, "tt-830", 0), late)
		live := f.add(t, "both", f.snap("movie", 831, "", 0), late)
		f.confirmable(t, dead.Title.ID)
		f.tmdb.finds["imdb_id:tt-830"] = []tmdb.MediaResult{{ID: f.id(831), MediaType: "movie"}}
		if err := f.svc.checkTitle(t.Context(), dead.Title.ID); err != nil {
			t.Fatal(err)
		}
		if f.title(t, dead.Title.ID) != nil {
			t.Fatal("merged title kept")
		}
		if at, ok := f.entryAddedAt(t, "both", live.Title.ID); !ok || !at.Equal(early) {
			t.Fatalf("merged entry added_at = %v %v, want %v", at, ok, early)
		}
		if _, ok := f.entryAddedAt(t, "dead-only", live.Title.ID); !ok {
			t.Fatal("entry not moved to the surviving title")
		}
		var entries int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM user_watchlist_titles WHERE user_id = $1 AND profile_id = 'both'`, f.userID).Scan(&entries); err != nil {
			t.Fatal(err)
		}
		if entries != 1 {
			t.Fatalf("profile that had both duplicates has %d entries, want 1", entries)
		}
		aliases := f.aliases(t, live.Title.ID)
		for _, want := range []string{"tmdb:" + strconv.Itoa(f.id(830)), "tmdb:" + strconv.Itoa(f.id(831)), "imdb:tt-830"} {
			if !slices.Contains(aliases, want) {
				t.Fatalf("survivor aliases = %v, missing %s", aliases, want)
			}
		}
	})

	t.Run("several candidates need review", func(t *testing.T) {
		f := newTitlesFixture(t)
		title := f.add(t, "p1", f.snap("movie", 840, "tt-840", 0), f.now)
		f.confirmable(t, title.Title.ID)
		f.tmdb.finds["imdb_id:tt-840"] = []tmdb.MediaResult{{ID: f.id(841), MediaType: "movie"}, {ID: f.id(842), MediaType: "movie"}}
		if err := f.svc.checkTitle(t.Context(), title.Title.ID); err != nil {
			t.Fatal(err)
		}
		if got := f.title(t, title.Title.ID); got.State != TitleNeedsReview || got.TMDBID != f.id(840) {
			t.Fatalf("title = %+v", got)
		}
	})

	t.Run("no candidates mark the title removed and keep the entry", func(t *testing.T) {
		f := newTitlesFixture(t)
		title := f.add(t, "p1", f.snap("movie", 850, "", 0), f.now)
		f.confirmable(t, title.Title.ID)
		if err := f.svc.checkTitle(t.Context(), title.Title.ID); err != nil {
			t.Fatal(err)
		}
		got := f.title(t, title.Title.ID)
		if got.State != TitleRemoved || !got.NextCheckAt.Equal(f.now.Add(30*24*time.Hour)) || got.NotFoundCount != 2 {
			t.Fatalf("title = %+v", got)
		}
		if _, ok := f.entryAddedAt(t, "p1", title.Title.ID); !ok {
			t.Fatal("removed title lost its entry")
		}
	})

	t.Run("an outage backs off and leaves the state alone", func(t *testing.T) {
		f := newTitlesFixture(t)
		title := f.add(t, "p1", f.snap("movie", 860, "tt-860", 0), f.now)
		f.tmdb.detailErrs["movie:"+strconv.Itoa(f.id(860))] = fmt.Errorf("tmdb: status 503")
		if err := f.svc.checkTitle(t.Context(), title.Title.ID); err == nil {
			t.Fatal("expected the outage to be reported")
		}
		got := f.title(t, title.Title.ID)
		if got.State != TitleActive || got.NotFoundCount != 0 || !got.NextCheckAt.Equal(f.now.Add(6*time.Hour)) {
			t.Fatalf("title = %+v", got)
		}
		// An outage during recovery backs off too, without confirming.
		f.confirmable(t, title.Title.ID)
		delete(f.tmdb.detailErrs, "movie:"+strconv.Itoa(f.id(860)))
		f.tmdb.findErr = fmt.Errorf("tmdb: status 503")
		if err := f.svc.checkTitle(t.Context(), title.Title.ID); err == nil {
			t.Fatal("expected the find outage to be reported")
		}
		got = f.title(t, title.Title.ID)
		if got.State != TitleActive || got.NotFoundCount != 1 || !got.NextCheckAt.Equal(f.now.Add(6*time.Hour)) {
			t.Fatalf("title after find outage = %+v", got)
		}
	})

	t.Run("the library imports the title under the dead id", func(t *testing.T) {
		f := newTitlesFixture(t)
		title := f.add(t, "p1", f.snap("movie", 870, "", 0), f.now)
		// Repointed: the entry's current ID is 871; the library item carries 870.
		if _, err := f.svc.repo.repoint(t.Context(), title.Title.ID, f.id(870), f.id(871), f.now); err != nil {
			t.Fatal(err)
		}
		contentID := f.item(t, "dead", "movie", strconv.Itoa(f.id(870)), "")
		promoted, err := f.svc.PromoteProfile(t.Context(), f.viewer("p1"))
		if err != nil || !slices.Equal(promoted, []string{contentID}) {
			t.Fatalf("promotion = %v %v", promoted, err)
		}
	})
}

func TestTitlesScheduleChecksClaimsAndBounds(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	var titles []Title
	for i := range 5 {
		snap := f.snap("movie", 900+i, "", 0)
		titles = append(titles, f.add(t, "p1", snap, f.now).Title)
		f.tmdb.details["movie:"+strconv.Itoa(snap.TMDBID)] = f.detail(snap)
	}
	// Titles are due at creation only once their next check passes.
	f.now = f.now.Add(2 * checkSettled)
	for i := range titles {
		titles[i].NextCheckAt = f.title(t, titles[i].ID).NextCheckAt
	}
	f.svc.ScheduleChecks(ctx, titles)
	f.svc.waitChecks()
	if got := f.tmdb.calls(); got != maxChecksPerRead {
		t.Fatalf("checks = %d, want %d", got, maxChecksPerRead)
	}
	if len(f.svc.checkSlots) != 0 {
		t.Fatalf("%d check slots leaked", len(f.svc.checkSlots))
	}
	// A claim is won once.
	due := []int64{titles[3].ID}
	first, err := f.svc.repo.claimDue(ctx, due, f.now, claimLease)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim = %v %v", first, err)
	}
	second, err := f.svc.repo.claimDue(ctx, due, f.now, claimLease)
	if err != nil || len(second) != 0 {
		t.Fatalf("second claim = %v %v", second, err)
	}
	// An expired claim can be taken again.
	if again, err := f.svc.repo.claimDue(ctx, due, f.now.Add(claimLease), claimLease); err != nil || len(again) != 1 {
		t.Fatalf("claim after lease = %v %v", again, err)
	}
}

func TestTitlesObserver(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	snap := f.snap("movie", 950, "tt-950", 0)
	title := f.add(t, "p1", snap, f.now)
	before := f.title(t, title.Title.ID)

	// Unchanged detail: no write.
	f.now = f.now.Add(time.Hour)
	f.svc.ObservedDetail(ctx, "movie", snap.TMDBID, f.detail(snap))
	if got := f.title(t, title.Title.ID); !got.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("unchanged observation wrote: %v -> %v", before.UpdatedAt, got.UpdatedAt)
	}
	// Untracked titles are ignored.
	f.svc.ObservedDetail(ctx, "movie", f.id(951), f.detail(f.snap("movie", 951, "", 0)))
	f.svc.ObservedNotFound(ctx, "movie", f.id(951))
	if other, _ := f.svc.Find(ctx, "movie", f.id(951)); other != nil {
		t.Fatal("observation created a title")
	}

	changed := snap
	changed.TVDBID = 0
	changed.PosterPath = "/new.jpg"
	f.svc.ObservedDetail(ctx, "movie", snap.TMDBID, f.detail(changed))
	if got := f.title(t, title.Title.ID); got.PosterPath != "/new.jpg" {
		t.Fatalf("changed observation not stored: %+v", got)
	}

	f.svc.ObservedNotFound(ctx, "movie", snap.TMDBID)
	got := f.title(t, title.Title.ID)
	if got.NotFoundCount != 1 || got.LastNotFoundAt == nil || !got.NextCheckAt.Equal(f.now.Add(24*time.Hour)) {
		t.Fatalf("observed 404 = %+v", got)
	}
	// A repeat within the day writes nothing.
	f.svc.ObservedNotFound(ctx, "movie", snap.TMDBID)
	if again := f.title(t, title.Title.ID); !again.UpdatedAt.Equal(got.UpdatedAt) || again.NotFoundCount != 1 {
		t.Fatalf("repeat 404 wrote: %+v", again)
	}
	// Once the confirming check is due, a 404 starts it in the background.
	f.now = f.now.Add(25 * time.Hour)
	f.svc.ObservedNotFound(ctx, "movie", snap.TMDBID)
	f.svc.waitChecks()
	if confirmed := f.title(t, title.Title.ID); confirmed.State != TitleRemoved {
		t.Fatalf("confirmed 404 = %+v", confirmed)
	}
}

func TestSnapshotFromDetail(t *testing.T) {
	snap, err := SnapshotFromDetail(&tmdb.MediaDetail{MediaType: "series", ID: 5, FirstAirDate: "2020-02-03", VoteAverage: 8, VoteCount: 0})
	if err != nil || snap.VoteAverage != nil || snap.ReleaseDate == nil || snap.ReleaseDate.Format(time.DateOnly) != "2020-02-03" {
		t.Fatalf("snapshot = %+v %v", snap, err)
	}
	if _, err := SnapshotFromDetail(&tmdb.MediaDetail{MediaType: "person", ID: 5}); err == nil {
		t.Fatal("expected an unsupported media type error")
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(-1, 0, 0)
	future := now.AddDate(0, 1, 0)
	for _, tc := range []struct {
		date *time.Time
		want time.Duration
	}{{nil, checkRecent}, {&future, checkRecent}, {&old, checkSettled}} {
		if got := nextCheckAfterSuccess(now, tc.date).Sub(now); got != tc.want {
			t.Errorf("next check for %v = %v, want %v", tc.date, got, tc.want)
		}
	}
}
