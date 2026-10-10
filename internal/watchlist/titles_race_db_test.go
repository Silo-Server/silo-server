package watchlist

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// waitForLockWait blocks until a statement matching the pattern waits on a
// row lock, or fails the test when done closes first or the deadline passes.
func (f *titlesFixture) waitForLockWait(t *testing.T, pattern string, done <-chan struct{}) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := f.pool.QueryRow(context.Background(), `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE wait_event_type = 'Lock' AND query LIKE $1)`, pattern).Scan(&waiting); err != nil {
			t.Error(err)
			return
		}
		if waiting {
			return
		}
		select {
		case <-done:
			t.Error("the statement finished instead of waiting on the title lock")
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Error("timed out waiting for the statement to block on the title lock")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A remove that runs while promotion is adding the item to the library
// watchlist must wait for that add, so the caller's follow-up library check
// sees the item and takes it off.
func TestTitlesPromotionHoldsTitleLockAcrossLibraryAdd(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	snap := f.snap("movie", 800, "", 0)
	entry := f.add(t, "p1", snap, f.now.Add(-time.Hour))
	contentID := f.item(t, "race", "movie", strconv.Itoa(snap.TMDBID), "")

	removeDone := make(chan struct{})
	var removed bool
	var removeErr error
	var onLibraryAtRemove bool
	f.store.beforeAdd = func() {
		f.store.beforeAdd = nil
		go func() {
			defer close(removeDone)
			_, removed, removeErr = f.svc.Remove(context.Background(), f.viewer("p1"), "movie", snap.TMDBID)
			_, onLibraryAtRemove = f.store.get("p1", contentID)
		}()
		f.waitForLockWait(t, "%FOR UPDATE OF t%", removeDone)
	}
	promoted, err := f.svc.PromoteProfile(ctx, f.viewer("p1"))
	if err != nil || !slices.Equal(promoted, []string{contentID}) {
		t.Fatalf("PromoteProfile = %v %v", promoted, err)
	}
	<-removeDone
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	if removed {
		t.Fatal("remove deleted the entry promotion was moving")
	}
	if !onLibraryAtRemove {
		t.Fatal("remove returned before the promoted library entry existed; the caller's library check would miss it")
	}
	if f.title(t, entry.Title.ID) != nil {
		t.Fatal("promoted title kept")
	}

	// A remove that commits first leaves promotion nothing to add.
	second := f.snap("movie", 801, "", 0)
	f.add(t, "p1", second, f.now)
	secondItem := f.item(t, "race-2", "movie", strconv.Itoa(second.TMDBID), "")
	if _, removed, err := f.svc.Remove(ctx, f.viewer("p1"), "movie", second.TMDBID); err != nil || !removed {
		t.Fatalf("remove = %v %v", removed, err)
	}
	if promoted, err := f.svc.PromoteProfile(ctx, f.viewer("p1")); err != nil || len(promoted) != 0 {
		t.Fatalf("promotion after remove = %v %v", promoted, err)
	}
	if _, ok := f.store.get("p1", secondItem); ok {
		t.Fatal("promotion added an item whose entry was removed")
	}
}

// An item the profile already had on its library watchlist absorbs the entry
// without firing the add effects again; one whose library entry carries the
// entry's added_at is a promotion a dead node left half done, and fires them.
func TestTitlesPromotionEffectsOnlyForNewLibraryEntries(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	entryAt := f.now.Add(-48 * time.Hour).Add(123 * time.Millisecond)

	listed := f.add(t, "p1", f.snap("movie", 810, "", 0), entryAt)
	listedItem := f.item(t, "listed", "movie", strconv.Itoa(f.id(810)), "")
	manualAt := f.now.Add(-time.Hour)
	f.store.set("p1", listedItem, manualAt)

	halfDone := f.add(t, "p1", f.snap("movie", 811, "", 0), entryAt)
	halfDoneItem := f.item(t, "half", "movie", strconv.Itoa(f.id(811)), "")
	f.store.set("p1", halfDoneItem, entryAt)

	promoted, err := f.svc.PromoteProfile(ctx, f.viewer("p1"))
	if err != nil || !slices.Equal(promoted, []string{halfDoneItem}) {
		t.Fatalf("PromoteProfile = %v %v, want only %s", promoted, err, halfDoneItem)
	}
	want := []string{fmt.Sprintf("%d/p1/%s", f.userID, halfDoneItem)}
	if got := f.effects.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("effects = %v, want %v", got, want)
	}
	if f.title(t, listed.Title.ID) != nil || f.title(t, halfDone.Title.ID) != nil {
		t.Fatal("promoted entries kept")
	}
	if at, _ := f.store.get("p1", listedItem); !at.Equal(manualAt) {
		t.Fatalf("existing library entry added_at = %v, want %v", at, manualAt)
	}
}

// A merge that deletes the title a remove is waiting on moves the TMDB alias
// to the survivor; the remove must follow it rather than report no title.
func TestTitlesRemoveFollowsAliasMergedWhileWaiting(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	loser := f.add(t, "p1", f.snap("movie", 820, "", 0), f.now)
	survivor := f.add(t, "p2", f.snap("movie", 821, "", 0), f.now)

	merger, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = merger.Rollback(context.Background()) }()
	if _, err := merger.Exec(ctx, `SELECT id FROM watchlist_titles WHERE id = ANY($1) ORDER BY id FOR UPDATE`,
		[]int64{loser.Title.ID, survivor.Title.ID}); err != nil {
		t.Fatal(err)
	}

	removeDone := make(chan struct{})
	var title *Title
	var removed bool
	var removeErr error
	go func() {
		defer close(removeDone)
		title, removed, removeErr = f.svc.Remove(context.Background(), f.viewer("p1"), "movie", f.id(820))
	}()
	f.waitForLockWait(t, "%FOR UPDATE OF t%", removeDone)
	if t.Failed() {
		t.FailNow()
	}

	// The merge body, as titlesRepo.merge runs it once both rows are locked.
	for _, stmt := range []string{
		`INSERT INTO user_watchlist_titles (user_id, profile_id, title_id, added_at)
		 SELECT user_id, profile_id, $2, added_at FROM user_watchlist_titles WHERE title_id = $1
		 ON CONFLICT (user_id, profile_id, title_id) DO NOTHING`,
		`UPDATE watchlist_title_aliases SET title_id = $2 WHERE title_id = $1`,
		`DELETE FROM watchlist_titles WHERE id = $1 AND $2::bigint IS NOT NULL`,
	} {
		if _, err := merger.Exec(ctx, stmt, loser.Title.ID, survivor.Title.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := merger.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-removeDone
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	if !removed || title == nil || title.ID != survivor.Title.ID {
		t.Fatalf("remove = %+v removed=%v, want the survivor removed", title, removed)
	}
	if _, ok := f.entryAddedAt(t, "p1", survivor.Title.ID); ok {
		t.Fatal("the merged entry stayed on the watchlist")
	}
	if _, ok := f.entryAddedAt(t, "p2", survivor.Title.ID); !ok {
		t.Fatal("another profile's entry was removed")
	}
}

// User deletion drops entries through the users foreign key and leaves
// orphaned titles. Reads skip them, an add refreshes the stale snapshot, and
// the sweep deletes them.
func TestTitlesOrphansAfterUserDelete(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	var other int
	if err := f.pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, f.prefix+"-other").Scan(&other); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, other) })
	otherViewer := Viewer{UserID: other, ProfileID: "p1"}

	stale := f.snap("movie", 830, "", 0)
	stale.Certification = "PG"
	gone, _, err := f.svc.AddSnapshot(ctx, otherViewer, stale, f.now)
	if err != nil {
		t.Fatal(err)
	}
	reused, _, err := f.svc.AddSnapshot(ctx, otherViewer, f.snap("movie", 831, "", 0), f.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, other); err != nil {
		t.Fatal(err)
	}
	if f.title(t, gone.Title.ID) == nil {
		t.Fatal("setup: expected the user delete to leave an orphaned title")
	}
	if found, err := f.svc.Find(ctx, "movie", f.id(830)); err != nil || found != nil {
		t.Fatalf("Find returned an orphaned title: %+v %v", found, err)
	}

	fresh := f.snap("movie", 831, "", 0)
	fresh.Title, fresh.Certification = "Renamed", "R"
	entry, inserted, err := f.svc.AddSnapshot(ctx, f.viewer("p1"), fresh, f.now)
	if err != nil || !inserted {
		t.Fatalf("add = %v %v", inserted, err)
	}
	if entry.Title.ID != reused.Title.ID || entry.Title.Title != "Renamed" || entry.Title.Certification != "R" {
		t.Fatalf("add attached to an orphan without refreshing it: %+v", entry.Title)
	}

	if err := f.svc.SweepOrphanTitles(ctx); err != nil {
		t.Fatal(err)
	}
	if f.title(t, gone.Title.ID) != nil {
		t.Fatal("sweep kept the orphaned title")
	}
	if f.title(t, reused.Title.ID) == nil {
		t.Fatal("sweep deleted a title with an entry")
	}
}

// A one-connection pool can't give the library watchlist write (which, in the
// Postgres user store, takes its own connection) a second connection while
// the title lock's transaction holds the first. Promotion writes first there
// instead of waiting until the request times out.
func TestTitlesPromotionCompletesOnOneConnectionPool(t *testing.T) {
	f := newTitlesFixture(t)
	cfg, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	single, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(single.Close)
	svc := NewTitles(single, catalog.NewItemRepository(f.pool), nil, f.tmdb, f.effects)
	svc.storeFor = func(context.Context, int) (promotionStore, error) { return f.store, nil }
	svc.now = func() time.Time { return f.now }
	if svc.addUnderLock {
		t.Fatal("a one-connection pool must not hold the title lock across the library write")
	}

	snap := f.snap("movie", 810, "", 0)
	f.add(t, "p1", snap, f.now.Add(-time.Hour))
	contentID := f.item(t, "single", "movie", strconv.Itoa(snap.TMDBID), "")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	f.store.beforeAdd = func() {
		f.store.beforeAdd = nil
		// Stands in for the Postgres user store's write on the same pool.
		if _, err := single.Exec(ctx, `SELECT 1`); err != nil {
			t.Errorf("library write could not get a connection: %v", err)
		}
	}
	promoted, err := svc.PromoteProfile(ctx, f.viewer("p1"))
	if err != nil || !slices.Equal(promoted, []string{contentID}) {
		t.Fatalf("PromoteProfile = %v %v, want %s promoted without waiting on the pool", promoted, err, contentID)
	}
}

// A library item known only by a TMDB ID the title has since lost is still
// the title's copy: matching uses every TMDB ID the caller passes.
func TestTitlesLibraryMatchesThroughFormerTMDBID(t *testing.T) {
	f := newTitlesFixture(t)
	former := f.id(820)
	contentID := f.item(t, "former", "movie", strconv.Itoa(former), "")
	current := f.id(821)
	only, err := f.svc.LibraryMatches(t.Context(), "movie", []int{current}, "", 0)
	if err != nil || len(only) != 0 {
		t.Fatalf("current ID alone = %v %v, want no match", only, err)
	}
	all, err := f.svc.LibraryMatches(t.Context(), "movie", []int{current, former}, "", 0)
	if err != nil || !slices.Equal(all, []string{contentID}) {
		t.Fatalf("with the former ID = %v %v, want %s", all, err, contentID)
	}
}

// On a one-connection pool the library write runs before the lock, so it
// first checks the entry is still there: an entry a remove already took is
// not written back onto the library watchlist.
func TestTitlesOneConnectionPromotionSkipsRemovedEntry(t *testing.T) {
	f := newTitlesFixture(t)
	f.svc.addUnderLock = false
	snap := f.snap("movie", 830, "", 0)
	entry := f.add(t, "p1", snap, f.now.Add(-time.Hour))
	contentID := f.item(t, "gone", "movie", strconv.Itoa(snap.TMDBID), "")
	if _, _, err := f.svc.Remove(t.Context(), f.viewer("p1"), "movie", snap.TMDBID); err != nil {
		t.Fatal(err)
	}
	moved, _, err := f.svc.promoteOne(t.Context(), f.store, f.viewer("p1"), entry.Title.ID, contentID, entry.AddedAt)
	if err != nil || moved {
		t.Fatalf("promoteOne = %v %v, want nothing moved", moved, err)
	}
	if _, ok := f.store.get("p1", contentID); ok {
		t.Fatal("a removed entry was written onto the library watchlist")
	}
}

// A provider ID another title already owns is never stored in a title's own
// IMDb or TVDB field: library matching and request presence read those
// fields, and would match the other title's copy. A new title leaves the
// field empty; a refresh keeps the ID the title already owns.
func TestTitlesKeepProviderIDsToOwnedAliases(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	owner := f.add(t, "p1", f.snap("movie", 840, "tt84000001", 8400001), f.now)

	// A new title reporting the owner's IDs keeps neither.
	dup := f.add(t, "p1", f.snap("movie", 841, "tt84000001", 8400001), f.now)
	got := f.title(t, dup.Title.ID)
	if got.IMDbID != "" || got.TVDBID != 0 {
		t.Fatalf("new title stored imdb %q tvdb %d that the other title owns", got.IMDbID, got.TVDBID)
	}

	// A title with its own IDs that a refresh reports as the owner's keeps its own.
	own := f.add(t, "p1", f.snap("movie", 842, "tt84000042", 8400042), f.now)
	if err := f.svc.repo.applyDetail(ctx, own.Title.ID, f.snap("movie", 842, "tt84000001", 8400001), f.now); err != nil {
		t.Fatal(err)
	}
	got = f.title(t, own.Title.ID)
	if got.IMDbID != "tt84000042" || got.TVDBID != 8400042 {
		t.Fatalf("refreshed title imdb %q tvdb %d, want its own tt84000042 / 8400042", got.IMDbID, got.TVDBID)
	}
	// The owner is untouched.
	if o := f.title(t, owner.Title.ID); o.IMDbID != "tt84000001" || o.TVDBID != 8400001 {
		t.Fatalf("owner imdb %q tvdb %d changed", o.IMDbID, o.TVDBID)
	}
}

// A duplicate copy in a library the viewer can't open doesn't make the title
// ambiguous for that viewer: only the copies they may see count, so the entry
// promotes onto the one they can see.
func TestTitlesPromotionCountsOnlyAccessibleCopies(t *testing.T) {
	f := newTitlesFixture(t)
	ctx := t.Context()
	snap := f.snap("movie", 850, "", 0)
	f.add(t, "p1", snap, f.now.Add(-time.Hour))
	visible := f.item(t, "visible", "movie", strconv.Itoa(snap.TMDBID), "")

	var other int
	if err := f.pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('mixed', $1, TRUE) RETURNING id`, f.prefix+"-other").Scan(&other); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, other) })
	hidden := f.prefix + "-hidden"
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, genres, tmdb_id) VALUES ($1, 'movie', $1, '{}'::text[], $2)`,
		hidden, strconv.Itoa(snap.TMDBID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, hidden, other); err != nil {
		t.Fatal(err)
	}

	viewer := f.viewer("p1")
	viewer.Access = catalog.AccessFilter{AllowedLibraryIDs: []int{f.folder}}
	promoted, err := f.svc.PromoteProfile(ctx, viewer)
	if err != nil || !slices.Equal(promoted, []string{visible}) {
		t.Fatalf("PromoteProfile = %v %v, want the one copy the viewer can see (%s)", promoted, err, visible)
	}
}
