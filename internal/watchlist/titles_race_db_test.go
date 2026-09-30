package watchlist

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"
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
