package watchsync

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	ratingTestUserID    = 7
	ratingTestProfileID = "profile-1"
	ratingTestConnID    = "conn-ratings"
	ratingTestMovieA    = "movie-a"
	ratingTestMovieB    = "movie-b"
	ratingTestSeries    = "series-a"
)

func TestRatingScaleConversion(t *testing.T) {
	want := map[int]int{1: 1, 2: 1, 3: 2, 4: 2, 5: 3, 6: 3, 7: 4, 8: 4, 9: 5, 10: 5, 0: 1, 11: 5}
	for rating, stars := range want {
		if got := starsFromProviderRating(rating); got != stars {
			t.Errorf("starsFromProviderRating(%d) = %d, want %d", rating, got, stars)
		}
	}
	for stars := 1; stars <= 5; stars++ {
		if got := starsFromProviderRating(providerRatingFromStars(stars)); got != stars {
			t.Errorf("stars %d do not round-trip through the provider scale (got %d)", stars, got)
		}
	}
}

func TestDecideRating(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	for _, tc := range []struct {
		name                string
		local, remote, base int
		localAt, remoteAt   time.Time
		want                ratingAction
	}{
		{name: "all agree", local: 4, remote: 4, base: 4, want: ratingKeep},
		{name: "all unrated", want: ratingKeep},
		{name: "first sync equal values rebase", local: 4, remote: 4, want: ratingRebase},
		{name: "both removed rebase", base: 3, want: ratingRebase},
		{name: "only local changed", local: 2, remote: 4, base: 4, want: ratingExport},
		{name: "local removed", remote: 4, base: 4, want: ratingExport},
		{name: "only remote changed", local: 4, remote: 5, base: 4, want: ratingImport},
		{name: "remote removed", local: 4, base: 4, want: ratingImport},
		{name: "first sync local only", local: 3, want: ratingExport},
		{name: "first sync remote only", remote: 3, want: ratingImport},
		{name: "conflict local removal loses", remote: 5, base: 4, localAt: newer, remoteAt: older, want: ratingImport},
		{name: "conflict remote removal loses", local: 5, base: 4, localAt: older, remoteAt: newer, want: ratingExport},
		{name: "conflict newer remote wins", local: 2, remote: 5, base: 4, localAt: older, remoteAt: newer, want: ratingImport},
		{name: "conflict newer local wins", local: 2, remote: 5, base: 4, localAt: newer, remoteAt: older, want: ratingExport},
		{name: "conflict tie goes to silo", local: 2, remote: 5, base: 4, localAt: older, remoteAt: older, want: ratingExport},
		{name: "conflict unknown remote time goes to silo", local: 2, remote: 5, base: 4, localAt: older, want: ratingExport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideRating(tc.local, tc.remote, tc.base, tc.localAt, tc.remoteAt); got != tc.want {
				t.Fatalf("decideRating(%d, %d, %d) = %d, want %d", tc.local, tc.remote, tc.base, got, tc.want)
			}
		})
	}
}

func TestSyncRatingsFirstSyncUnionsBothSides(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 3) // Silo only
	h.provider.batch = RatingImportBatch{
		Rows: []RemoteRating{
			h.remoteRow(ratingTestMovieB, 7), // provider only
			h.remoteRow(ratingTestSeries, 10),
		},
		SnapshotKinds: []string{historyimport.KindMovie, historyimport.KindSeries},
	}

	result := h.sync()

	if got := h.store.stars(ratingTestMovieB); got != 4 {
		t.Fatalf("imported movie B = %d stars, want 4", got)
	}
	if got := h.store.stars(ratingTestSeries); got != 5 {
		t.Fatalf("imported series = %d stars, want 5", got)
	}
	if len(h.provider.exported) != 1 || h.provider.exported[0].MediaItemID != ratingTestMovieA || h.provider.exported[0].Rating != 6 {
		t.Fatalf("exported = %#v, want movie A at 6", h.provider.exported)
	}
	if result.Imported != 2 || result.Sent != 1 || result.RemoteFound != 2 || result.LocalFound != 1 {
		t.Fatalf("result = %#v", result)
	}
	if !h.stale {
		t.Fatal("imports must mark the profile's recommendations stale")
	}
	if s := h.state(ratingTestMovieB); s == nil || s.SyncedRating != 4 || !s.RemoteSeen {
		t.Fatalf("imported base = %#v, want 4 stars seen", s)
	}
	if s := h.state(ratingTestMovieA); s == nil || s.SyncedRating != 3 || s.RemoteSeen {
		t.Fatalf("exported base = %#v, want 3 stars not yet seen", s)
	}
}

func TestSyncRatingsEquivalentValuesRebaseWithoutWrites(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.provider.batch = RatingImportBatch{
		Rows:          []RemoteRating{h.remoteRow(ratingTestMovieA, 7)},
		SnapshotKinds: []string{historyimport.KindMovie},
	}

	h.sync()

	if len(h.provider.exported) != 0 || len(h.provider.removed) != 0 {
		t.Fatalf("a remote 7 and 4 stars agree; provider writes = %#v / %#v", h.provider.exported, h.provider.removed)
	}
	if s := h.state(ratingTestMovieA); s == nil || s.SyncedRating != 4 || !s.RemoteSeen {
		t.Fatalf("base = %#v, want 4 stars seen", s)
	}

	// A remote change inside the same star is not a change.
	h.provider.batch.Rows = []RemoteRating{h.remoteRow(ratingTestMovieA, 8)}
	h.sync()
	if len(h.provider.exported) != 0 || h.store.stars(ratingTestMovieA) != 4 {
		t.Fatalf("7 to 8 must not write either side; exported=%#v local=%d", h.provider.exported, h.store.stars(ratingTestMovieA))
	}
}

func TestSyncRatingsPropagatesChangesAfterAgreement(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.store.set(ratingTestMovieB, 2)
	h.agree(ratingTestMovieA, 4, true)
	h.agree(ratingTestMovieB, 2, true)
	h.store.set(ratingTestMovieA, 1) // changed in Silo
	h.provider.batch = RatingImportBatch{
		Rows: []RemoteRating{
			h.remoteRow(ratingTestMovieA, 8),
			h.remoteRow(ratingTestMovieB, 10), // changed on the provider
		},
		SnapshotKinds: []string{historyimport.KindMovie},
	}

	h.sync()

	if len(h.provider.exported) != 1 || h.provider.exported[0].MediaItemID != ratingTestMovieA || h.provider.exported[0].Rating != 2 {
		t.Fatalf("exported = %#v, want movie A at 2", h.provider.exported)
	}
	if got := h.store.stars(ratingTestMovieB); got != 5 {
		t.Fatalf("movie B = %d stars, want 5", got)
	}
}

func TestSyncRatingsRemovalsFollowTheChangedSide(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.store.set(ratingTestMovieB, 3)
	h.agree(ratingTestMovieA, 4, true)
	h.agree(ratingTestMovieB, 3, true)
	h.store.remove(ratingTestMovieB) // removed in Silo
	// Movie A is absent from a complete snapshot: removed on the provider.
	h.provider.batch = RatingImportBatch{
		Rows:          []RemoteRating{h.remoteRow(ratingTestMovieB, 6)},
		SnapshotKinds: []string{historyimport.KindMovie},
	}

	h.sync()

	if got := h.store.stars(ratingTestMovieA); got != 0 {
		t.Fatalf("movie A = %d stars, want removed", got)
	}
	if len(h.provider.removed) != 1 || h.provider.removed[0].MediaItemID != ratingTestMovieB {
		t.Fatalf("removed = %#v, want movie B", h.provider.removed)
	}
	if h.state(ratingTestMovieA) != nil {
		t.Fatalf("an imported removal must clear the agreed rating: %#v", h.state(ratingTestMovieA))
	}
	// A sent removal stays agreed until a read confirms the title is unrated.
	if h.state(ratingTestMovieB) == nil {
		t.Fatal("a sent removal must keep its agreed rating until a read confirms it")
	}

	h.provider.batch = RatingImportBatch{
		Rows:          []RemoteRating{h.remoteRow(ratingTestSeries, 2)},
		SnapshotKinds: []string{historyimport.KindMovie},
	}
	h.sync()
	if h.state(ratingTestMovieB) != nil || len(h.provider.removed) != 0 {
		t.Fatalf("a confirmed removal must clear the agreed rating without resending: state=%#v removed=%#v", h.state(ratingTestMovieB), h.provider.removed)
	}
}

func TestSyncRatingsResendsARemovalTheProviderDidNotFullyApply(t *testing.T) {
	h := newRatingHarness(t)
	h.agree(ratingTestMovieA, 4, true)
	// Silo removed the rating and sent the removal, but another provider entry
	// for the same title is still rated.
	h.provider.batch = RatingImportBatch{
		Rows:          []RemoteRating{h.remoteRow(ratingTestMovieA, 8)},
		SnapshotKinds: []string{historyimport.KindMovie},
	}
	h.sync()
	if got := h.store.stars(ratingTestMovieA); got != 0 {
		t.Fatalf("the removed rating came back as %d stars", got)
	}
	if len(h.provider.removed) != 1 {
		t.Fatalf("removed = %#v, want the removal sent again", h.provider.removed)
	}
}

func TestSyncRatingsUnseenAbsenceResendsInsteadOfDeleting(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	// Silo sent this rating, but no provider read has confirmed it yet.
	h.agree(ratingTestMovieA, 4, false)
	h.provider.batch = RatingImportBatch{
		Rows:          []RemoteRating{h.remoteRow(ratingTestMovieB, 6)},
		SnapshotKinds: []string{historyimport.KindMovie},
	}

	h.sync()

	if got := h.store.stars(ratingTestMovieA); got != 4 {
		t.Fatalf("an unconfirmed rating must not be deleted locally; got %d stars", got)
	}
	if len(h.provider.exported) != 1 || h.provider.exported[0].MediaItemID != ratingTestMovieA {
		t.Fatalf("exported = %#v, want movie A resent", h.provider.exported)
	}
}

func TestSyncRatingsAbsenceOutsideSnapshotKindsIsUnknown(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestSeries, 4)
	h.agree(ratingTestSeries, 4, true)
	// The provider skipped series (unchanged), so their absence means nothing.
	h.provider.batch = RatingImportBatch{
		Rows:          []RemoteRating{h.remoteRow(ratingTestMovieB, 6)},
		SnapshotKinds: []string{historyimport.KindMovie},
	}

	h.sync()

	if got := h.store.stars(ratingTestSeries); got != 4 {
		t.Fatalf("series = %d stars, want unchanged 4", got)
	}
	if len(h.provider.exported) != 0 || len(h.provider.removed) != 0 {
		t.Fatalf("provider writes = %#v / %#v, want none", h.provider.exported, h.provider.removed)
	}
}

func TestSyncRatingsEmptySnapshotDoesNotDelete(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.store.set(ratingTestMovieB, 2)
	h.agree(ratingTestMovieA, 4, true)
	h.agree(ratingTestMovieB, 2, true)
	h.provider.batch = RatingImportBatch{SnapshotKinds: []string{historyimport.KindMovie}}

	result := h.sync()

	if h.store.stars(ratingTestMovieA) != 4 || h.store.stars(ratingTestMovieB) != 2 {
		t.Fatalf("an empty snapshot must not delete several ratings: A=%d B=%d", h.store.stars(ratingTestMovieA), h.store.stars(ratingTestMovieB))
	}
	if len(result.Warnings) == 0 {
		t.Fatal("an ignored empty snapshot should warn")
	}
}

func TestSyncRatingsEmptySnapshotRemovesTheLastRating(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.agree(ratingTestMovieA, 4, true)
	h.provider.batch = RatingImportBatch{SnapshotKinds: []string{historyimport.KindMovie}}

	h.sync()

	if got := h.store.stars(ratingTestMovieA); got != 0 {
		t.Fatalf("removing the only rating on the provider must import; got %d stars", got)
	}
}

func TestSyncRatingsUnmatchedRowSharingAnIDIsNotARemoval(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.agree(ratingTestMovieA, 4, true)
	// The provider still lists the title, but the matcher cannot place the row:
	// it carries only an IMDb id, which this matcher does not use.
	row := h.remoteRow(ratingTestMovieA, 8)
	row.TMDBID = ""
	row.ProviderItemKey = ""
	row.IMDbID = "tt0101"
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{row}, SnapshotKinds: []string{historyimport.KindMovie}}

	h.sync()

	if got := h.store.stars(ratingTestMovieA); got != 4 {
		t.Fatalf("movie A = %d stars, want unchanged 4", got)
	}
	if len(h.provider.removed) != 0 {
		t.Fatalf("removed = %#v, want none", h.provider.removed)
	}
}

func TestSyncRatingsTombstoneRemovesByProviderKey(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.agree(ratingTestMovieA, 4, false)
	tombstone := RemoteRating{RemoteFavorite: RemoteFavorite{ProviderItemKey: h.media[ratingTestMovieA].ProviderItemKey, Removed: true}}
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{tombstone}}

	h.sync()

	if got := h.store.stars(ratingTestMovieA); got != 0 {
		t.Fatalf("movie A = %d stars, want removed by tombstone", got)
	}
}

func TestSyncRatingsRemembersTheProviderKeyForTombstonesAndWrites(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	row := h.remoteRow(ratingTestMovieA, 8)
	row.ProviderItemKey = "floppy:movie:101"
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{row}, SnapshotKinds: []string{historyimport.KindMovie}}
	h.sync()
	if s := h.state(ratingTestMovieA); s == nil || s.ProviderItemKey != "floppy:movie:101" {
		t.Fatalf("agreed row = %#v, want the provider's own key", s)
	}

	// A later local change is sent under the provider's key.
	h.store.set(ratingTestMovieA, 2)
	h.provider.batch = RatingImportBatch{}
	h.sync()
	if len(h.provider.exported) != 1 || h.provider.exported[0].ProviderItemKey != "floppy:movie:101" {
		t.Fatalf("exported = %#v, want the provider's key", h.provider.exported)
	}

	// An incremental tombstone naming that key removes the rating.
	h.agree(ratingTestMovieA, 2, true)
	h.repo.ratingStates[0].ProviderItemKey = "floppy:movie:101"
	tombstone := RemoteRating{RemoteFavorite: RemoteFavorite{ProviderItemKey: "floppy:movie:101", Removed: true}}
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{tombstone}}
	h.sync()
	if got := h.store.stars(ratingTestMovieA); got != 0 {
		t.Fatalf("movie A = %d stars, want removed by the provider's tombstone", got)
	}
}

func TestSyncRatingsSkipsKindsTheProviderDoesNotRate(t *testing.T) {
	h := newRatingHarness(t)
	h.provider.kinds = map[string]bool{historyimport.KindMovie: true}
	h.store.set(ratingTestSeries, 3)
	h.store.set(ratingTestMovieA, 4)
	h.agree(ratingTestMovieA, 4, true)

	result := h.sync()

	if len(h.provider.exported) != 0 || len(result.Warnings) != 0 {
		t.Fatalf("a movie-only provider must not be sent series ratings: exported=%#v warnings=%v", h.provider.exported, result.Warnings)
	}
	if result.LocalFound != 1 {
		t.Fatalf("local found = %d, want only the movie", result.LocalFound)
	}
}

func TestSyncRatingsConcurrentLocalEditWins(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.agree(ratingTestMovieA, 4, true)
	h.store.conflicts[ratingTestMovieA] = true // the user edits between read and write
	h.provider.batch = RatingImportBatch{
		Rows:          []RemoteRating{h.remoteRow(ratingTestMovieA, 2)},
		SnapshotKinds: []string{historyimport.KindMovie},
	}

	result := h.sync()

	if result.Imported != 0 {
		t.Fatalf("a lost compare-and-set must not count as imported: %#v", result)
	}
	if s := h.state(ratingTestMovieA); s == nil || s.SyncedRating != 4 {
		t.Fatalf("a lost compare-and-set must leave the agreed rating: %#v", s)
	}
}

func TestSyncRatingsDirectionToggles(t *testing.T) {
	t.Run("import only", func(t *testing.T) {
		h := newRatingHarness(t)
		h.conn.ExportRatingsEnabled = false
		h.store.set(ratingTestMovieA, 3)
		h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieB, 7)}, SnapshotKinds: []string{historyimport.KindMovie}}
		h.sync()
		if len(h.provider.exported) != 0 || h.store.stars(ratingTestMovieB) != 4 {
			t.Fatalf("exported=%#v movieB=%d", h.provider.exported, h.store.stars(ratingTestMovieB))
		}
		if h.state(ratingTestMovieA) != nil {
			t.Fatal("a blocked export must not record agreement")
		}
	})
	t.Run("export only", func(t *testing.T) {
		h := newRatingHarness(t)
		h.conn.ImportRatingsEnabled = false
		h.store.set(ratingTestMovieA, 3)
		h.provider.batch = RatingImportBatch{
			Rows:           []RemoteRating{h.remoteRow(ratingTestMovieB, 7)},
			SnapshotKinds:  []string{historyimport.KindMovie},
			UpdatedCursors: map[string]string{"test.ratings.movies": "c1"},
		}
		h.sync()
		if h.store.stars(ratingTestMovieB) != 0 || len(h.provider.exported) != 1 {
			t.Fatalf("exported=%#v movieB=%d", h.provider.exported, h.store.stars(ratingTestMovieB))
		}
		cursors := h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)].SyncCursors
		if cursors["test.ratings.movies"] != "c1" || cursors[ratingImportCursorKey] != "" {
			t.Fatalf("send-only cursors = %#v, want the read cursor saved without the import marker", cursors)
		}

		// Turning import on reads everything again: cursors saved in
		// send-only mode may have skipped changes that were never imported.
		h.conn = h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)]
		h.conn.ImportRatingsEnabled = true
		h.sync()
		if h.provider.fetchedCursors["test.ratings.movies"] != "" {
			t.Fatalf("the first import read got cursors %#v, want none", h.provider.fetchedCursors)
		}
		if marker := h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)].SyncCursors[ratingImportCursorKey]; marker == "" {
			t.Fatal("an import read must set the import marker")
		}
	})
}

func TestSyncRatingsPersistsCursorsWhenImporting(t *testing.T) {
	h := newRatingHarness(t)
	h.provider.batch = RatingImportBatch{UpdatedCursors: map[string]string{"test.ratings.movies": "c1"}}
	h.sync()
	if cursor := h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)].SyncCursors["test.ratings.movies"]; cursor != "c1" {
		t.Fatalf("cursor = %q, want c1", cursor)
	}
}

func TestSyncRatingsWatchGateHoldsUnwatchedMovies(t *testing.T) {
	h := newRatingHarness(t)
	h.provider.gateMovies = true
	h.store.set(ratingTestMovieA, 4)
	h.store.set(ratingTestMovieB, 5)
	h.watched[ratingTestMovieB] = true
	h.provider.batch = RatingImportBatch{SnapshotKinds: []string{historyimport.KindMovie}}

	result := h.sync()

	if len(h.provider.exported) != 1 || h.provider.exported[0].MediaItemID != ratingTestMovieB {
		t.Fatalf("exported = %#v, want only the watched movie", h.provider.exported)
	}
	if h.state(ratingTestMovieA) != nil || len(result.Warnings) == 0 {
		t.Fatalf("a held rating stays pending with a warning: state=%#v warnings=%v", h.state(ratingTestMovieA), result.Warnings)
	}
}

func TestSyncRatingsRateLimitStopsSending(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.provider.exportErr = RateLimitedError{Provider: "test", RetryAfter: time.Minute}

	_, err := h.service.syncRatings(context.Background(), h.conn, ServerConfig{}, h.provider)

	if _, ok := AsRateLimited(err); !ok {
		t.Fatalf("err = %v, want rate limited", err)
	}
	if h.state(ratingTestMovieA) != nil {
		t.Fatal("a rate-limited rating stays pending")
	}
}

func TestLocalRatingEventSendsCurrentValue(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.agree(ratingTestMovieA, 4, true)
	h.store.set(ratingTestMovieA, 2)
	h.store.set(ratingTestMovieB, 5) // not part of the event

	err := h.service.processLocalRatingEvent(context.Background(), LocalRatingEvent{
		UserID: ratingTestUserID, ProfileID: ratingTestProfileID, MediaItemIDs: []string{ratingTestMovieA},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.provider.exported) != 1 || h.provider.exported[0].MediaItemID != ratingTestMovieA || h.provider.exported[0].Rating != 4 {
		t.Fatalf("exported = %#v, want only movie A at 4", h.provider.exported)
	}
	if h.provider.fetches != 0 {
		t.Fatal("a local event must not read the provider")
	}

	// A removal waits for the scheduled merge, which can see whether the
	// provider changed the rating in the meantime.
	h.store.remove(ratingTestMovieA)
	if err := h.service.processLocalRatingEvent(context.Background(), LocalRatingEvent{
		UserID: ratingTestUserID, ProfileID: ratingTestProfileID, MediaItemIDs: []string{ratingTestMovieA},
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.provider.removed) != 0 {
		t.Fatalf("removed = %#v, want the removal left to the scheduled merge", h.provider.removed)
	}
}

func TestSyncRatingsNeverRemovesARatingItSkipped(t *testing.T) {
	h := newRatingHarness(t)
	// Rated and agreed, but the media item has left the catalog.
	h.store.set("gone-movie", 4)
	h.repo.ratingStates = append(h.repo.ratingStates, RatingSyncState{
		ConnectionID: h.conn.ID, ProviderAccountID: h.conn.ProviderAccountID, MediaItemID: "gone-movie",
		Kind: historyimport.KindMovie, ProviderItemKey: "tmdb:999", SyncedRating: 4, RemoteSeen: true,
	})
	h.provider.batch = RatingImportBatch{SnapshotKinds: []string{historyimport.KindMovie}}
	h.sync()
	if len(h.provider.removed) != 0 {
		t.Fatalf("removed = %#v, want no removal for a rating Silo still holds", h.provider.removed)
	}
}

func TestSyncRatingsIgnoresAgreedRatingsOfAnotherAccount(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	// Agreed with the connection's previous account, then absent here.
	h.repo.ratingStates = append(h.repo.ratingStates, RatingSyncState{
		ConnectionID: h.conn.ID, ProviderAccountID: "old-account", MediaItemID: ratingTestMovieA,
		Kind: historyimport.KindMovie, ProviderItemKey: "tmdb:101", SyncedRating: 4, RemoteSeen: true,
	})
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieB, 6)}, SnapshotKinds: []string{historyimport.KindMovie}}
	h.sync()
	if got := h.store.stars(ratingTestMovieA); got != 4 {
		t.Fatalf("movie A = %d stars; another account's agreement must not delete it", got)
	}
	if len(h.provider.exported) != 1 || h.provider.exported[0].MediaItemID != ratingTestMovieA {
		t.Fatalf("exported = %#v, want movie A sent to the new account", h.provider.exported)
	}
}

func TestSyncRatingsResendsAValueChangedDuringTheSend(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 3)
	// A newer edit lands while the first write is in flight, so the provider
	// may now hold the older value: the current one is sent once more.
	edited := false
	h.provider.onExport = func() {
		if !edited {
			edited = true
			h.store.set(ratingTestMovieA, 5)
		}
	}
	h.provider.batch = RatingImportBatch{SnapshotKinds: []string{historyimport.KindMovie}}
	h.sync()
	if len(h.provider.exported) != 2 || h.provider.exported[1].Rating != 10 {
		t.Fatalf("exported = %#v, want the newer 5 stars sent last", h.provider.exported)
	}
	if s := h.state(ratingTestMovieA); s == nil || s.SyncedRating != 5 {
		t.Fatalf("agreed row = %#v, want the resent 5 stars", s)
	}
}

func TestSyncRatingsAgreesOnTheLastSentValueWhenResendsNeverSettle(t *testing.T) {
	h := newRatingHarness(t)
	h.agree(ratingTestMovieA, 1, true)
	h.store.set(ratingTestMovieA, 2)
	// Every write overlaps another edit, so no send ever confirms the current
	// value. The agreed row must name what the provider was last sent, so the
	// next merge sends the newer value instead of importing the sent one.
	stars := 2
	h.provider.onExport = func() {
		stars = stars%5 + 1
		h.store.set(ratingTestMovieA, stars)
	}
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieA, 2)}, SnapshotKinds: []string{historyimport.KindMovie}}
	result := h.sync()
	if len(h.provider.exported) != 1+maxRatingResends {
		t.Fatalf("exports = %d, want the first send and %d resends", len(h.provider.exported), maxRatingResends)
	}
	last := h.provider.exported[len(h.provider.exported)-1].Rating
	if s := h.state(ratingTestMovieA); s == nil || providerRatingFromStars(s.SyncedRating) != last || s.RemoteSeen {
		t.Fatalf("agreed row = %#v, want the last sent rating %d, unseen", s, last)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("unsettled ratings must be reported")
	}
}

func TestSyncRatingsSendsARemovalMadeDuringTheLastResend(t *testing.T) {
	h := newRatingHarness(t)
	h.agree(ratingTestMovieA, 1, true)
	h.store.set(ratingTestMovieA, 2)
	// The edits keep coming, and the last one removes the rating.
	sends, stars := 0, 2
	h.provider.onExport = func() {
		sends++
		if sends > maxRatingResends {
			h.store.remove(ratingTestMovieA)
			return
		}
		stars = stars%5 + 1
		h.store.set(ratingTestMovieA, stars)
	}
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieA, 2)}, SnapshotKinds: []string{historyimport.KindMovie}}
	h.sync()
	if len(h.provider.exported) != 1+maxRatingResends {
		t.Fatalf("exports = %d, want the first send and %d resends", len(h.provider.exported), maxRatingResends)
	}
	last := h.provider.exported[len(h.provider.exported)-1].Rating

	// The provider now holds the last sent rating. The next run must send the
	// removal rather than import that rating back.
	h.provider.onExport = nil
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieA, last)}, SnapshotKinds: []string{historyimport.KindMovie}}
	h.sync()
	if got := h.store.stars(ratingTestMovieA); got != 0 {
		t.Fatalf("local rating = %d, want the removal kept", got)
	}
	if len(h.provider.removed) != 1 {
		t.Fatalf("removed = %#v, want the removal sent", h.provider.removed)
	}
}

func TestSyncRatingsResendsARatingSetDuringARemoval(t *testing.T) {
	h := newRatingHarness(t)
	h.agree(ratingTestMovieA, 4, true)
	h.provider.onRemove = func() { h.store.set(ratingTestMovieA, 2) }
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieA, 8)}, SnapshotKinds: []string{historyimport.KindMovie}}
	h.sync()
	if len(h.provider.removed) != 1 || len(h.provider.exported) != 1 || h.provider.exported[0].Rating != 4 {
		t.Fatalf("removed = %#v exported = %#v, want the new rating sent after the removal", h.provider.removed, h.provider.exported)
	}
}

func TestSyncRatingsKeepsTitlesOfUnusableRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  func(h *ratingHarness) RemoteRating
	}{
		{"out of range", func(h *ratingHarness) RemoteRating {
			row := h.remoteRow(ratingTestMovieA, 11)
			return row
		}},
		{"provider ids only", func(h *ratingHarness) RemoteRating {
			return RemoteRating{RemoteFavorite: RemoteFavorite{ProviderItemKey: "mdblist:abc", Kind: historyimport.KindMovie}, Rating: 7}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newRatingHarness(t)
			h.store.set(ratingTestMovieA, 4)
			h.agree(ratingTestMovieA, 4, true)
			h.provider.batch = RatingImportBatch{Rows: []RemoteRating{tc.row(h)}, SnapshotKinds: []string{historyimport.KindMovie}}
			h.sync()
			if got := h.store.stars(ratingTestMovieA); got != 4 {
				t.Fatalf("movie A = %d stars; an unusable row must not turn its title into a removal", got)
			}
		})
	}
}

func TestSyncRatingsSkipsAmbiguousTombstones(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.store.set(ratingTestSeries, 3)
	h.agree(ratingTestMovieA, 4, true)
	h.agree(ratingTestSeries, 3, true)
	// The movie and the series both recorded the key tmdb:101.
	tombstone := RemoteRating{RemoteFavorite: RemoteFavorite{ProviderItemKey: "tmdb:101", Removed: true}}
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{tombstone}}
	result := h.sync()
	if h.store.stars(ratingTestMovieA) != 4 || h.store.stars(ratingTestSeries) != 3 || len(result.Warnings) == 0 {
		t.Fatalf("an ambiguous tombstone must remove nothing: movie=%d series=%d warnings=%v",
			h.store.stars(ratingTestMovieA), h.store.stars(ratingTestSeries), result.Warnings)
	}
}

func TestSyncRatingsResolvesTombstonesWithinTheirKind(t *testing.T) {
	t.Run("both kinds use the key", func(t *testing.T) {
		h := newRatingHarness(t)
		h.store.set(ratingTestMovieA, 4)
		h.store.set(ratingTestSeries, 3)
		h.agree(ratingTestMovieA, 4, true)
		h.agree(ratingTestSeries, 3, true)
		tombstone := RemoteRating{RemoteFavorite: RemoteFavorite{ProviderItemKey: "tmdb:101", Kind: historyimport.KindSeries, Removed: true}}
		h.provider.batch = RatingImportBatch{Rows: []RemoteRating{tombstone}}
		h.sync()
		if h.store.stars(ratingTestSeries) != 0 || h.store.stars(ratingTestMovieA) != 4 {
			t.Fatalf("series=%d movie=%d, want only the series rating removed", h.store.stars(ratingTestSeries), h.store.stars(ratingTestMovieA))
		}
	})
	t.Run("only the other kind uses the key", func(t *testing.T) {
		h := newRatingHarness(t)
		h.store.set(ratingTestMovieA, 4)
		h.agree(ratingTestMovieA, 4, true)
		tombstone := RemoteRating{RemoteFavorite: RemoteFavorite{ProviderItemKey: "tmdb:101", Kind: historyimport.KindSeries, Removed: true}}
		h.provider.batch = RatingImportBatch{Rows: []RemoteRating{tombstone}}
		h.sync()
		if h.store.stars(ratingTestMovieA) != 4 {
			t.Fatalf("movie=%d, a series tombstone must not remove a movie rating", h.store.stars(ratingTestMovieA))
		}
	})
}

func TestSyncRatingsInvalidRowsKeepTheEmptySnapshotGuard(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.store.set(ratingTestMovieB, 2)
	h.agree(ratingTestMovieA, 4, true)
	h.agree(ratingTestMovieB, 2, true)
	// The only movie row is unusable and names neither title.
	bad := RemoteRating{RemoteFavorite: RemoteFavorite{ProviderItemKey: "imdb:tt9999", Kind: historyimport.KindMovie, IMDbID: "tt9999"}, Rating: 11}
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{bad}, SnapshotKinds: []string{historyimport.KindMovie}}
	result := h.sync()
	if h.store.stars(ratingTestMovieA) != 4 || h.store.stars(ratingTestMovieB) != 2 {
		t.Fatalf("movieA=%d movieB=%d, a snapshot with no usable rows must not remove ratings", h.store.stars(ratingTestMovieA), h.store.stars(ratingTestMovieB))
	}
	found := false
	for _, w := range result.Warnings {
		found = found || strings.Contains(w, "returned no movie ratings")
	}
	if !found {
		t.Fatalf("warnings = %v, want the empty-snapshot guard reported", result.Warnings)
	}
}

func TestDeleteConnectionWithoutAConnectionTakesNoLock(t *testing.T) {
	h := newRatingHarness(t)
	if err := h.service.DeleteConnection(context.Background(), h.conn.UserID, "another-profile", h.conn.Provider); err != nil {
		t.Fatal(err)
	}
	if len(h.repo.ratingLocks) != 0 || len(h.repo.connections) != 1 {
		t.Fatalf("locks=%v connections=%d, want nothing touched", h.repo.ratingLocks, len(h.repo.connections))
	}
}

func TestSyncRatingsWatchGateLetsHeldTitlesChange(t *testing.T) {
	h := newRatingHarness(t)
	h.provider.gateMovies = true
	h.store.set(ratingTestMovieA, 4)
	h.agree(ratingTestMovieA, 4, true)
	h.store.set(ratingTestMovieA, 2)
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieA, 8)}, SnapshotKinds: []string{historyimport.KindMovie}}
	h.sync()
	if len(h.provider.exported) != 1 {
		t.Fatalf("exported = %#v; a title the provider already rates is not held for a watch", h.provider.exported)
	}
}

func TestExecuteSyncRunRecordsRatingCounters(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 3)
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieB, 7)}, SnapshotKinds: []string{historyimport.KindMovie}}

	created, err := h.repo.CreateSyncRun(context.Background(), SyncRun{ConnectionID: h.conn.ID, Provider: h.conn.Provider})
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.service.executeSyncRun(context.Background(), h.conn, created)
	if err != nil {
		t.Fatal(err)
	}
	if run.InboundRatingsFound != 1 || run.InboundRatingsImported != 1 || run.OutboundRatingsFound != 1 || run.OutboundRatingsSent != 1 {
		t.Fatalf("run counters = %#v", run)
	}
}

func TestPersistConnectionEnablesRatingsForNewConnections(t *testing.T) {
	repo := newServiceFakeRepo()
	service := NewService(repo, NewRegistry())
	conn, err := service.persistConnection(context.Background(), "test", 1, "p", TokenSet{AccessToken: "a"}, ProviderAccount{ID: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	if !conn.ImportRatingsEnabled || !conn.ExportRatingsEnabled {
		t.Fatalf("new connection rating toggles = %v/%v, want on", conn.ImportRatingsEnabled, conn.ExportRatingsEnabled)
	}
}

func TestPersistConnectionAccountChangeClearsAgreedRatings(t *testing.T) {
	repo := newServiceFakeRepo()
	existing := Connection{
		ID: ratingTestConnID, Provider: "test", UserID: 1, ProfileID: "p", ProviderAccountID: "old",
		SyncCursors: map[string]string{"test.ratings.movies": "c1", "test.watched": "w1"},
	}
	repo.connections[connectionKey("test", 1, "p")] = existing
	repo.ratingStates = []RatingSyncState{{ConnectionID: ratingTestConnID, MediaItemID: ratingTestMovieA, SyncedRating: 4, RemoteSeen: true}}
	service := NewService(repo, NewRegistry())

	conn, err := service.persistConnection(context.Background(), "test", 1, "p", TokenSet{AccessToken: "a"}, ProviderAccount{ID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.ratingStates) != 0 {
		t.Fatalf("agreed ratings survived an account change: %#v", repo.ratingStates)
	}
	if conn.SyncCursors["test.ratings.movies"] != "" || conn.SyncCursors["test.watched"] != "w1" {
		t.Fatalf("cursors = %#v, want only rating cursors dropped", conn.SyncCursors)
	}

	// Reconnecting the same account keeps the agreement.
	repo.ratingStates = []RatingSyncState{{ConnectionID: ratingTestConnID, MediaItemID: ratingTestMovieA, SyncedRating: 4, RemoteSeen: true}}
	if _, err := service.persistConnection(context.Background(), "test", 1, "p", TokenSet{AccessToken: "a"}, ProviderAccount{ID: "new"}); err != nil {
		t.Fatal(err)
	}
	if len(repo.ratingStates) != 1 {
		t.Fatal("reconnecting the same account must keep agreed ratings")
	}
	// Only the switch waited for the rating sync lock.
	if !slices.Equal(repo.ratingLocks, []string{"wait:" + ratingTestConnID}) {
		t.Fatalf("rating sync locks = %v, want one wait by the account switch", repo.ratingLocks)
	}
}

func TestDeleteConnectionWaitsForTheRatingSyncLock(t *testing.T) {
	h := newRatingHarness(t)
	if err := h.service.DeleteConnection(context.Background(), h.conn.UserID, h.conn.ProfileID, h.conn.Provider); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.repo.ratingLocks, []string{"wait:" + ratingTestConnID}) {
		t.Fatalf("rating sync locks = %v, want the disconnect to wait for the lock", h.repo.ratingLocks)
	}
	if len(h.repo.connections) != 0 {
		t.Fatal("the connection was not deleted")
	}
}

func TestSyncRatingsMarksTheProfileStaleWhenBookkeepingFailsAfterAnImport(t *testing.T) {
	h := newRatingHarness(t)
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieB, 8)}, SnapshotKinds: []string{historyimport.KindMovie}}
	h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)] = h.conn
	// The run's deadline ends right after the import commits.
	ctx, cancel := context.WithCancel(context.Background())
	h.repo.upsertRatingErr = context.Canceled
	h.store.afterWrite = cancel
	staleCtxErr := errors.New("unset")
	h.staleCtx = func(ctx context.Context) { staleCtxErr = ctx.Err() }
	if _, err := h.service.syncRatings(ctx, h.conn, ServerConfig{}, h.provider); err == nil {
		t.Fatal("want the bookkeeping error")
	}
	if staleCtxErr != nil {
		t.Fatalf("stale mark ran with context error %v, want a live context", staleCtxErr)
	}
	if h.store.stars(ratingTestMovieB) != 4 || !h.stale {
		t.Fatalf("movieB=%d stale=%v, want the committed import to mark recommendations stale", h.store.stars(ratingTestMovieB), h.stale)
	}
}

func TestSyncRatingsLeavesRatingsToARunHoldingTheLock(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 3)
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieB, 8)}, SnapshotKinds: []string{historyimport.KindMovie}}
	// Another node is reconciling this connection's ratings.
	h.repo.ratingLockBusy = map[string]bool{ratingTestConnID: true}
	result := h.sync()
	if h.provider.fetches != 0 || len(h.provider.exported) != 0 || h.store.stars(ratingTestMovieB) != 0 {
		t.Fatalf("a run without the lock touched ratings: fetches=%d exported=%v movieB=%d", h.provider.fetches, h.provider.exported, h.store.stars(ratingTestMovieB))
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "already syncing") {
		t.Fatalf("warnings = %v, want the skip reported", result.Warnings)
	}
	h.repo.ratingLockBusy = nil
	h.sync()
	if h.provider.fetches != 1 || h.store.stars(ratingTestMovieB) != 4 {
		t.Fatalf("fetches=%d movieB=%d, want the next run to sync", h.provider.fetches, h.store.stars(ratingTestMovieB))
	}
	if !slices.Contains(h.repo.ratingLocks, "try:"+ratingTestConnID) {
		t.Fatalf("rating sync locks = %v, want the scheduled run to try the lock", h.repo.ratingLocks)
	}
}

// --- harness ---

type ratingHarness struct {
	t        *testing.T
	repo     *serviceFakeRepo
	store    *fakeRatingStore
	provider *ratingProviderStub
	service  *Service
	conn     Connection
	media    map[string]LocalFavorite
	watched  map[string]bool
	stale    bool
	// staleCtx, when set, sees the context the stale mark ran with.
	staleCtx func(context.Context)
}

func newRatingHarness(t *testing.T) *ratingHarness {
	t.Helper()
	h := &ratingHarness{
		t:        t,
		repo:     newServiceFakeRepo(),
		store:    newFakeRatingStore(),
		provider: &ratingProviderStub{},
		watched:  map[string]bool{},
		media: map[string]LocalFavorite{
			ratingTestMovieA: {MediaItemID: ratingTestMovieA, Kind: historyimport.KindMovie, IMDbID: "tt0101", TMDBID: "101", ProviderItemKey: "tmdb:101"},
			ratingTestMovieB: {MediaItemID: ratingTestMovieB, Kind: historyimport.KindMovie, TMDBID: "102", ProviderItemKey: "tmdb:102"},
			ratingTestSeries: {MediaItemID: ratingTestSeries, Kind: historyimport.KindSeries, TMDBID: "101", ProviderItemKey: "tmdb:101"},
		},
	}
	h.repo.listMedia = h.media
	h.conn = Connection{
		ID: ratingTestConnID, Provider: h.provider.Key(), UserID: ratingTestUserID, ProfileID: ratingTestProfileID,
		AccessToken: "token", ProviderAccountID: "acct", ImportRatingsEnabled: true, ExportRatingsEnabled: true,
	}
	h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)] = h.conn
	registry := NewRegistry()
	if err := registry.Register(h.provider); err != nil {
		t.Fatal(err)
	}
	h.service = NewService(h.repo, registry).
		WithMatcher(ratingMatcherStub{media: h.media}).
		WithUserStoreProvider(ratingHistoryStoreProvider{watched: h.watched}).
		WithRatingStore(h.store, ratingStalerFunc(func(ctx context.Context) {
			h.stale = true
			if h.staleCtx != nil {
				h.staleCtx(ctx)
			}
		}))
	return h
}

func (h *ratingHarness) sync() SyncRatingsResult {
	h.t.Helper()
	h.provider.exported, h.provider.removed = nil, nil
	// The sync re-reads the connection under its lock, so it must see the
	// toggles this test set.
	h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)] = h.conn
	result, err := h.service.syncRatings(context.Background(), h.conn, ServerConfig{}, h.provider)
	if err != nil {
		h.t.Fatal(err)
	}
	return result
}

func (h *ratingHarness) remoteRow(mediaItemID string, rating int) RemoteRating {
	item := h.media[mediaItemID]
	return RemoteRating{
		RemoteFavorite: RemoteFavorite{Provider: "test", ProviderItemKey: item.ProviderItemKey, Kind: item.Kind, TMDBID: item.TMDBID},
		Rating:         rating,
		RatedAt:        time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}
}

func (h *ratingHarness) agree(mediaItemID string, stars int, seen bool) {
	item := h.media[mediaItemID]
	_ = h.repo.UpsertRatingSyncStates(context.Background(), []RatingSyncState{{
		ConnectionID: h.conn.ID, ProviderAccountID: h.conn.ProviderAccountID, MediaItemID: mediaItemID, Kind: item.Kind,
		ProviderItemKey: item.ProviderItemKey, SyncedRating: stars, RemoteSeen: seen,
	}})
}

func (h *ratingHarness) state(mediaItemID string) *RatingSyncState {
	for i := range h.repo.ratingStates {
		if h.repo.ratingStates[i].MediaItemID == mediaItemID {
			return &h.repo.ratingStates[i]
		}
	}
	return nil
}

type fakeRatingStore struct {
	ratings   map[string]catalog.UserRating
	conflicts map[string]bool
	clock     time.Time
	// afterWrite runs after each applied import write when set.
	afterWrite func()
}

func newFakeRatingStore() *fakeRatingStore {
	return &fakeRatingStore{
		ratings:   map[string]catalog.UserRating{},
		conflicts: map[string]bool{},
		clock:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func (s *fakeRatingStore) set(id string, stars int) {
	s.clock = s.clock.Add(time.Minute)
	s.ratings[id] = catalog.UserRating{UserID: ratingTestUserID, ProfileID: ratingTestProfileID, MediaItemID: id, Rating: stars, RatedAt: s.clock}
}

func (s *fakeRatingStore) remove(id string) { delete(s.ratings, id) }

func (s *fakeRatingStore) stars(id string) int { return s.ratings[id].Rating }

func (s *fakeRatingStore) matches(id string, observed catalog.ObservedRating) bool {
	current, ok := s.ratings[id]
	if observed.Rating == 0 {
		return !ok
	}
	return ok && current.Rating == observed.Rating && current.RatedAt.Equal(observed.RatedAt)
}

func (s *fakeRatingStore) ListAll(_ context.Context, _ int, _ string) ([]catalog.UserRating, error) {
	all := make([]catalog.UserRating, 0, len(s.ratings))
	for _, rating := range s.ratings {
		all = append(all, rating)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].MediaItemID < all[j].MediaItemID })
	return all, nil
}

func (s *fakeRatingStore) Get(_ context.Context, _ int, _ string, id string) (*catalog.UserRating, error) {
	rating, ok := s.ratings[id]
	if !ok {
		return nil, nil
	}
	return &rating, nil
}

func (s *fakeRatingStore) SetIfUnchanged(_ context.Context, _ int, _ string, id string, observed catalog.ObservedRating, rating int, ratedAt time.Time) (bool, error) {
	if s.conflicts[id] || !s.matches(id, observed) {
		return false, nil
	}
	s.ratings[id] = catalog.UserRating{UserID: ratingTestUserID, ProfileID: ratingTestProfileID, MediaItemID: id, Rating: rating, RatedAt: ratedAt}
	if s.afterWrite != nil {
		s.afterWrite()
	}
	return true, nil
}

func (s *fakeRatingStore) DeleteIfUnchanged(_ context.Context, _ int, _ string, id string, observed catalog.ObservedRating) (bool, error) {
	if s.conflicts[id] || !s.matches(id, observed) {
		return false, nil
	}
	delete(s.ratings, id)
	return true, nil
}

type ratingStalerFunc func(context.Context)

func (f ratingStalerFunc) MarkProfileStale(ctx context.Context, _ int, _ string) error {
	f(ctx)
	return nil
}

// ratingMatcherStub matches remote rows by kind and TMDB id.
type ratingMatcherStub struct {
	media map[string]LocalFavorite
}

func (m ratingMatcherStub) Match(_ context.Context, record historyimport.Record) (*historyimport.Match, string, error) {
	for _, item := range m.media {
		if record.TMDBID != "" && item.Kind == record.Kind && item.TMDBID == record.TMDBID {
			return &historyimport.Match{MediaItemID: item.MediaItemID}, "", nil
		}
	}
	return nil, "no match", nil
}

type ratingHistoryStoreProvider struct {
	watched map[string]bool
}

func (p ratingHistoryStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return ratingHistoryStore{watched: p.watched}, nil
}

func (ratingHistoryStoreProvider) Close() error { return nil }

// ratingHistoryStore answers completed-history lookups for the watch gate.
type ratingHistoryStore struct {
	userstore.UserStore
	watched map[string]bool
}

func (s ratingHistoryStore) ListCompletedHistory(_ context.Context, query userstore.CompletedHistoryQuery) ([]userstore.WatchHistoryEntry, error) {
	if query.Offset > 0 {
		return nil, nil
	}
	var rows []userstore.WatchHistoryEntry
	for _, id := range query.MediaItemIDs {
		if s.watched[id] {
			rows = append(rows, userstore.WatchHistoryEntry{MediaItemID: id})
		}
	}
	return rows, nil
}

type ratingProviderStub struct {
	batch      RatingImportBatch
	fetches    int
	exported   []LocalRating
	removed    []LocalFavorite
	exportErr  error
	gateMovies bool
	onExport   func()
	onRemove   func()
	onFetch    func()
	// fetchedCursors are the cursors the last FetchRatings call received.
	fetchedCursors map[string]string
	// kinds limits the rated kinds when set; nil rates every kind.
	kinds map[string]bool
}

func (p *ratingProviderStub) SyncsRatingKind(kind string) bool {
	return p.kinds == nil || p.kinds[kind]
}

func (*ratingProviderStub) Key() string         { return "test" }
func (*ratingProviderStub) DisplayName() string { return "Test" }
func (*ratingProviderStub) Capabilities() Capabilities {
	return Capabilities{ImportRatings: true, ExportRatings: true}
}

func (*ratingProviderStub) ConnectWithAPIKey(context.Context, string) (TokenSet, ProviderAccount, error) {
	return TokenSet{}, ProviderAccount{}, nil
}

func (p *ratingProviderStub) FetchRatings(_ context.Context, _ ServerConfig, conn Connection) (RatingImportBatch, error) {
	p.fetches++
	p.fetchedCursors = conn.SyncCursors
	if p.onFetch != nil {
		p.onFetch()
	}
	return p.batch, nil
}

func (p *ratingProviderStub) ExportRatings(_ context.Context, _ ServerConfig, _ Connection, items []LocalRating) (ExportResult, error) {
	if p.exportErr != nil {
		return ExportResult{}, p.exportErr
	}
	p.exported = append(p.exported, items...)
	if p.onExport != nil {
		p.onExport()
	}
	var result ExportResult
	for _, item := range items {
		result.Sent = append(result.Sent, item.MediaItemID, item.ProviderItemKey)
	}
	return result, nil
}

func (p *ratingProviderStub) RemoveRatings(_ context.Context, _ ServerConfig, _ Connection, items []LocalFavorite) (ExportResult, error) {
	p.removed = append(p.removed, items...)
	if p.onRemove != nil {
		p.onRemove()
	}
	var result ExportResult
	for _, item := range items {
		result.Sent = append(result.Sent, item.MediaItemID, item.ProviderItemKey)
	}
	return result, nil
}

func (p *ratingProviderStub) RatingExportRequiresWatched(kind string) bool {
	return p.gateMovies && kind == historyimport.KindMovie
}

func TestSyncRatingsSkipsApplyingAfterAnAccountRebind(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 3)
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieB, 7)}, SnapshotKinds: []string{historyimport.KindMovie}}
	// The connection is re-bound while the provider read is in flight.
	h.provider.onFetch = func() {
		key := connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)
		rebound := h.repo.connections[key]
		rebound.ProviderAccountID = "another-account"
		h.repo.connections[key] = rebound
	}

	result := h.sync()

	if h.store.stars(ratingTestMovieB) != 0 || len(h.provider.exported) != 0 || len(h.repo.ratingStates) != 0 {
		t.Fatalf("a stale run must not apply ratings: movieB=%d exported=%#v states=%#v",
			h.store.stars(ratingTestMovieB), h.provider.exported, h.repo.ratingStates)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("a skipped stale run should warn")
	}
}

func TestSyncRatingsImportLosesToASameValueResave(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 4)
	h.agree(ratingTestMovieA, 4, true)
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieA, 2)}, SnapshotKinds: []string{historyimport.KindMovie}}
	// The user re-saves the same stars after the sync read them.
	h.provider.onFetch = func() { h.store.set(ratingTestMovieA, 4) }

	result := h.sync()

	if got := h.store.stars(ratingTestMovieA); got != 4 || result.Imported != 0 {
		t.Fatalf("movie A = %d stars, imported %d; a newer local save must win", got, result.Imported)
	}
}
