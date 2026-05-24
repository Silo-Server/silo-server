// Package storetest provides a shared conformance test suite for UserStore
// implementations. Both SQLite and Postgres backends run these same tests.
package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunSuite runs all conformance tests against a UserStore implementation.
// The newStore function should return a fresh, empty store for each test.
func RunSuite(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	t.Run("Profiles", func(t *testing.T) {
		testProfiles(t, newStore)
	})
	t.Run("Progress", func(t *testing.T) {
		testProgress(t, newStore)
	})
	t.Run("Favorites", func(t *testing.T) {
		testFavorites(t, newStore)
	})
	t.Run("Watchlist", func(t *testing.T) {
		testWatchlist(t, newStore)
	})
	t.Run("Collections", func(t *testing.T) {
		testCollections(t, newStore)
	})
	t.Run("Settings", func(t *testing.T) {
		testSettings(t, newStore)
	})
	t.Run("SubtitlePreferences", func(t *testing.T) {
		testSubtitlePreferences(t, newStore)
	})
	t.Run("AudioPreferences", func(t *testing.T) {
		testAudioPreferences(t, newStore)
	})
	t.Run("LibraryPlaybackPreferences", func(t *testing.T) {
		testLibraryPlaybackPreferences(t, newStore)
	})
	t.Run("ProgressHints", func(t *testing.T) {
		testProgressHints(t, newStore)
	})
	t.Run("SectionOverrides", func(t *testing.T) {
		testSectionOverrides(t, newStore)
	})
	t.Run("SectionOverridesUserAddedFields", func(t *testing.T) {
		testSectionOverridesUserAddedFields(t, newStore)
	})
	t.Run("HomeDismissals", func(t *testing.T) {
		testHomeDismissals(t, newStore)
	})
}

func testProfiles(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	// Create
	p := userstore.Profile{
		ID:                         "prof-1",
		Name:                       "Alice",
		Avatar:                     "avatar1.png",
		IsChild:                    false,
		MaxContentRating:           "R",
		QualityPreference:          "1080p",
		Language:                   "en",
		SubtitleLanguage:           "es",
		SubtitleMode:               "auto",
		AutoSkipIntro:              true,
		AutoSkipCredits:            false,
		AutoSkipRecap:              false,
		AutoPlayNextPreview:        false,
		LibraryRestrictionsEnabled: true,
		AllowedLibraryIDs:          []int{1, 3},
		MaxPlaybackQuality:         "1080p",
	}
	if err := store.CreateProfile(ctx, p); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	// Get
	got, err := store.GetProfile(ctx, "prof-1")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if got == nil {
		t.Fatal("GetProfile returned nil")
	}
	if got.Name != "Alice" {
		t.Errorf("Name = %q, want %q", got.Name, "Alice")
	}
	if got.AutoSkipIntro != true {
		t.Errorf("AutoSkipIntro = %v, want true", got.AutoSkipIntro)
	}
	if !got.LibraryRestrictionsEnabled {
		t.Errorf("LibraryRestrictionsEnabled = %v, want true", got.LibraryRestrictionsEnabled)
	}
	if len(got.AllowedLibraryIDs) != 2 || got.AllowedLibraryIDs[0] != 1 || got.AllowedLibraryIDs[1] != 3 {
		t.Errorf("AllowedLibraryIDs = %v, want [1 3]", got.AllowedLibraryIDs)
	}
	if got.MaxPlaybackQuality != "1080p" {
		t.Errorf("MaxPlaybackQuality = %q, want %q", got.MaxPlaybackQuality, "1080p")
	}

	// Get non-existent
	missing, err := store.GetProfile(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetProfile(nonexistent): %v", err)
	}
	if missing != nil {
		t.Errorf("GetProfile(nonexistent) = %v, want nil", missing)
	}

	// List
	profiles, err := store.ListProfiles(ctx)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("ListProfiles returned %d profiles, want 1", len(profiles))
	}
	if !profiles[0].LibraryRestrictionsEnabled {
		t.Errorf("ListProfiles()[0].LibraryRestrictionsEnabled = %v, want true", profiles[0].LibraryRestrictionsEnabled)
	}
	if len(profiles[0].AllowedLibraryIDs) != 2 || profiles[0].AllowedLibraryIDs[0] != 1 || profiles[0].AllowedLibraryIDs[1] != 3 {
		t.Errorf("ListProfiles()[0].AllowedLibraryIDs = %v, want [1 3]", profiles[0].AllowedLibraryIDs)
	}
	if profiles[0].MaxPlaybackQuality != "1080p" {
		t.Errorf("ListProfiles()[0].MaxPlaybackQuality = %q, want %q", profiles[0].MaxPlaybackQuality, "1080p")
	}

	// Update
	newName := "Alice Updated"
	skipCredits := true
	restrictionsEnabled := false
	allowedLibraryIDs := []int{2, 4}
	maxPlaybackQuality := "720p"
	if err := store.UpdateProfile(ctx, "prof-1", userstore.UpdateProfileInput{
		Name:                       &newName,
		AutoSkipCredits:            &skipCredits,
		LibraryRestrictionsEnabled: &restrictionsEnabled,
		AllowedLibraryIDs:          &allowedLibraryIDs,
		MaxPlaybackQuality:         &maxPlaybackQuality,
	}); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	updated, _ := store.GetProfile(ctx, "prof-1")
	if updated.Name != "Alice Updated" {
		t.Errorf("Updated Name = %q, want %q", updated.Name, "Alice Updated")
	}
	if updated.AutoSkipCredits != true {
		t.Errorf("Updated AutoSkipCredits = %v, want true", updated.AutoSkipCredits)
	}
	if updated.LibraryRestrictionsEnabled {
		t.Errorf("Updated LibraryRestrictionsEnabled = %v, want false", updated.LibraryRestrictionsEnabled)
	}
	if len(updated.AllowedLibraryIDs) != 2 || updated.AllowedLibraryIDs[0] != 2 || updated.AllowedLibraryIDs[1] != 4 {
		t.Errorf("Updated AllowedLibraryIDs = %v, want [2 4]", updated.AllowedLibraryIDs)
	}
	if updated.MaxPlaybackQuality != "720p" {
		t.Errorf("Updated MaxPlaybackQuality = %q, want %q", updated.MaxPlaybackQuality, "720p")
	}

	// PIN set and verify
	pin := "1234"
	if err := store.UpdateProfile(ctx, "prof-1", userstore.UpdateProfileInput{PIN: &pin}); err != nil {
		t.Fatalf("UpdateProfile(PIN): %v", err)
	}
	ok, err := store.VerifyPIN(ctx, "prof-1", "1234")
	if err != nil {
		t.Fatalf("VerifyPIN: %v", err)
	}
	if !ok {
		t.Error("VerifyPIN returned false for correct PIN")
	}
	ok, err = store.VerifyPIN(ctx, "prof-1", "wrong")
	if err != nil {
		t.Fatalf("VerifyPIN(wrong): %v", err)
	}
	if ok {
		t.Error("VerifyPIN returned true for wrong PIN")
	}

	// Delete
	if err := store.DeleteProfile(ctx, "prof-1"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	deleted, _ := store.GetProfile(ctx, "prof-1")
	if deleted != nil {
		t.Error("GetProfile after delete returned non-nil")
	}
}

func testProgress(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	// Setup profile
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	noThreshold := userstore.ProgressThresholds{}

	// SetProgress (unconditional) — position at 500/7200 = 6.9%, above min-resume default
	if err := store.SetProgress(ctx, "p1", "movie-1", 500, 7200, noThreshold); err != nil {
		t.Fatalf("SetProgress: %v", err)
	}
	wp, err := store.GetProgress(ctx, "p1", "movie-1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if wp == nil {
		t.Fatal("GetProgress returned nil")
	}
	if wp.PositionSeconds != 500 {
		t.Errorf("PositionSeconds = %v, want 500", wp.PositionSeconds)
	}

	// UpdateProgress forward-only: should advance
	if err := store.UpdateProgress(ctx, "p1", "movie-1", 1000, 7200, noThreshold); err != nil {
		t.Fatalf("UpdateProgress(forward): %v", err)
	}
	wp, _ = store.GetProgress(ctx, "p1", "movie-1")
	if wp.PositionSeconds != 1000 {
		t.Errorf("PositionSeconds after forward = %v, want 1000", wp.PositionSeconds)
	}

	// UpdateProgress backward: should NOT go backward
	if err := store.UpdateProgress(ctx, "p1", "movie-1", 600, 7200, noThreshold); err != nil {
		t.Fatalf("UpdateProgress(backward): %v", err)
	}
	wp, _ = store.GetProgress(ctx, "p1", "movie-1")
	if wp.PositionSeconds != 1000 {
		t.Errorf("PositionSeconds after backward = %v, want 1000 (unchanged)", wp.PositionSeconds)
	}

	// SetProgress backward: should work (unconditional)
	if err := store.SetProgress(ctx, "p1", "movie-1", 500, 7200, noThreshold); err != nil {
		t.Fatalf("SetProgress(backward): %v", err)
	}
	wp, _ = store.GetProgress(ctx, "p1", "movie-1")
	if wp.PositionSeconds != 500 {
		t.Errorf("PositionSeconds after SetProgress backward = %v, want 500", wp.PositionSeconds)
	}

	// Completion flag: >90%
	if err := store.SetProgress(ctx, "p1", "movie-2", 6500, 7200, noThreshold); err != nil {
		t.Fatalf("SetProgress(near end): %v", err)
	}
	wp, _ = store.GetProgress(ctx, "p1", "movie-2")
	if !wp.Completed {
		t.Error("Expected Completed=true for >90% progress")
	}

	// Min-resume threshold: below 5% should be silently discarded
	if err := store.SetProgress(ctx, "p1", "movie-tiny", 10, 7200, noThreshold); err != nil {
		t.Fatalf("SetProgress(below min resume): %v", err)
	}
	wp, _ = store.GetProgress(ctx, "p1", "movie-tiny")
	if wp != nil {
		t.Error("Expected nil progress for position below min-resume threshold")
	}

	// Zero-duration writes should not error and should remain incomplete.
	if err := store.SetProgress(ctx, "p1", "movie-unknown", 0, 0, noThreshold); err != nil {
		t.Fatalf("SetProgress(zero duration): %v", err)
	}
	if err := store.UpdateProgress(ctx, "p1", "movie-unknown", 30, 0, noThreshold); err != nil {
		t.Fatalf("UpdateProgress(zero duration): %v", err)
	}
	wp, _ = store.GetProgress(ctx, "p1", "movie-unknown")
	if wp == nil {
		t.Fatal("GetProgress(zero duration) returned nil")
	}
	if wp.Completed {
		t.Error("Expected Completed=false for zero-duration progress")
	}

	// ListProgress
	all, err := store.ListProgress(ctx, "p1", "all", 10, 0)
	if err != nil {
		t.Fatalf("ListProgress: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ListProgress returned %d items, want 3", len(all))
	}

	// GetProgress non-existent
	wp, err = store.GetProgress(ctx, "p1", "nonexistent")
	if err != nil {
		t.Fatalf("GetProgress(nonexistent): %v", err)
	}
	if wp != nil {
		t.Error("GetProgress(nonexistent) returned non-nil")
	}

	// AddHistory + ListHistory
	seasonNumber := 1
	episodeNumber := 2
	if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{
		ProfileID:       "p1",
		MediaItemID:     "movie-1",
		DurationSeconds: 7200,
		Completed:       true,
		Identity: userstore.WatchIdentity{
			StableType:        "episode",
			SeriesProviderIDs: map[string]string{"tmdb": "123"},
			Season:            &seasonNumber,
			Episode:           &episodeNumber,
		},
	}); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}
	history, err := store.ListHistory(ctx, "p1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("ListHistory returned %d, want 1", len(history))
	}
	if history[0].Source != userstore.WatchHistorySourceLegacy {
		t.Errorf("ListHistory Source = %q, want %q", history[0].Source, userstore.WatchHistorySourceLegacy)
	}
	if history[0].Identity.StableType != "episode" {
		t.Errorf("ListHistory Identity.StableType = %q, want episode", history[0].Identity.StableType)
	}
	if len(history[0].Identity.ProviderIDs) != 0 {
		t.Errorf("ListHistory Identity.ProviderIDs = %#v, want empty", history[0].Identity.ProviderIDs)
	}
	if history[0].Identity.SeriesProviderIDs["tmdb"] != "123" {
		t.Errorf("ListHistory Identity.SeriesProviderIDs[tmdb] = %q, want 123", history[0].Identity.SeriesProviderIDs["tmdb"])
	}
	if history[0].Identity.Season == nil || *history[0].Identity.Season != seasonNumber {
		t.Errorf("ListHistory Identity.Season = %v, want %d", history[0].Identity.Season, seasonNumber)
	}
	if history[0].Identity.Episode == nil || *history[0].Identity.Episode != episodeNumber {
		t.Errorf("ListHistory Identity.Episode = %v, want %d", history[0].Identity.Episode, episodeNumber)
	}

	if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{
		ProfileID:       "p1",
		MediaItemID:     "movie-1",
		DurationSeconds: 7200,
		Completed:       true,
		WatchedAt:       "2026-03-23T12:01:00Z",
		Source:          userstore.WatchHistorySourceManual,
	}); err != nil {
		t.Fatalf("AddHistory(manual): %v", err)
	}
	if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{
		ProfileID:       "p1",
		MediaItemID:     "movie-1",
		DurationSeconds: 7200,
		Completed:       true,
		WatchedAt:       "2026-03-23T12:02:00Z",
		Source:          userstore.WatchHistorySourcePlayback,
	}); err != nil {
		t.Fatalf("AddHistory(playback): %v", err)
	}
	if err := store.DeleteHistoryBySource(ctx, "p1", []string{"movie-1"}, userstore.WatchHistorySourceManual); err != nil {
		t.Fatalf("DeleteHistoryBySource(manual): %v", err)
	}
	history, err = store.ListHistory(ctx, "p1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory(after delete): %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("ListHistory(after delete) returned %d, want 2", len(history))
	}
	for _, entry := range history {
		if entry.Source == userstore.WatchHistorySourceManual {
			t.Fatalf("DeleteHistoryBySource removed wrong rows: %+v", history)
		}
	}

	removedAt := time.Date(2026, 3, 23, 12, 3, 0, 0, time.UTC)
	if err := store.RemoveHistoryItems(ctx, "p1", []string{"movie-1"}, removedAt); err != nil {
		t.Fatalf("RemoveHistoryItems: %v", err)
	}
	wp, err = store.GetProgress(ctx, "p1", "movie-1")
	if err != nil {
		t.Fatalf("GetProgress(after remove): %v", err)
	}
	if wp != nil {
		t.Fatalf("GetProgress(after remove) returned %+v, want nil", *wp)
	}
	history, err = store.ListHistory(ctx, "p1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory(after remove): %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("ListHistory(after remove) returned %d, want 0", len(history))
	}

	if err := store.SetProgressAt(
		ctx,
		"p1",
		"movie-1",
		7200,
		7200,
		true,
		time.Date(2026, 3, 23, 12, 2, 30, 0, time.UTC),
	); err != nil {
		t.Fatalf("SetProgressAt(hidden import): %v", err)
	}
	wp, err = store.GetProgress(ctx, "p1", "movie-1")
	if err != nil {
		t.Fatalf("GetProgress(after hidden import): %v", err)
	}
	if wp != nil {
		t.Fatalf("GetProgress(after hidden import) returned %+v, want nil", *wp)
	}

	if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{
		ProfileID:       "p1",
		MediaItemID:     "movie-1",
		DurationSeconds: 7200,
		Completed:       true,
		WatchedAt:       "2026-03-23T12:02:30Z",
		Source:          userstore.WatchHistorySourceImport,
	}); err != nil {
		t.Fatalf("AddHistory(hidden import): %v", err)
	}
	history, err = store.ListHistory(ctx, "p1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory(after hidden import): %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("ListHistory(after hidden import) returned %d, want 0", len(history))
	}

	if err := store.SetProgressAt(
		ctx,
		"p1",
		"movie-1",
		7200,
		7200,
		true,
		time.Date(2026, 3, 23, 12, 4, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("SetProgressAt(after remove): %v", err)
	}
	if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{
		ProfileID:       "p1",
		MediaItemID:     "movie-1",
		DurationSeconds: 7200,
		Completed:       true,
		WatchedAt:       "2026-03-23T12:04:00Z",
		Source:          userstore.WatchHistorySourcePlayback,
	}); err != nil {
		t.Fatalf("AddHistory(after remove): %v", err)
	}
	history, err = store.ListHistory(ctx, "p1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory(after new watch): %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("ListHistory(after new watch) returned %d, want 1", len(history))
	}
	if history[0].WatchedAt != "2026-03-23T12:04:00Z" {
		t.Fatalf("ListHistory(after new watch) watched_at = %q, want 2026-03-23T12:04:00Z", history[0].WatchedAt)
	}
	wp, err = store.GetProgress(ctx, "p1", "movie-1")
	if err != nil {
		t.Fatalf("GetProgress(after new watch): %v", err)
	}
	if wp == nil || !wp.Completed {
		t.Fatalf("GetProgress(after new watch) = %+v, want completed progress", wp)
	}

	// Manual watched state helpers.
	if err := store.MarkWatched(ctx, "p1", "movie-3", 5400); err != nil {
		t.Fatalf("MarkWatched: %v", err)
	}
	wp, err = store.GetProgress(ctx, "p1", "movie-3")
	if err != nil {
		t.Fatalf("GetProgress(after MarkWatched): %v", err)
	}
	if wp == nil {
		t.Fatal("GetProgress(after MarkWatched) returned nil")
	}
	if !wp.Completed {
		t.Error("MarkWatched should create a completed progress row")
	}
	if wp.PositionSeconds != 5400 || wp.DurationSeconds != 5400 {
		t.Errorf("MarkWatched stored position=%v duration=%v, want 5400/5400", wp.PositionSeconds, wp.DurationSeconds)
	}

	if err := store.ClearProgress(ctx, "p1", "movie-3"); err != nil {
		t.Fatalf("ClearProgress: %v", err)
	}
	wp, err = store.GetProgress(ctx, "p1", "movie-3")
	if err != nil {
		t.Fatalf("GetProgress(after ClearProgress): %v", err)
	}
	if wp != nil {
		t.Error("ClearProgress should remove the progress row")
	}

	progressMap, err := store.ListProgressByMediaItems(ctx, "p1", []string{"movie-1", "movie-2", "missing"})
	if err != nil {
		t.Fatalf("ListProgressByMediaItems: %v", err)
	}
	if len(progressMap) != 2 {
		t.Fatalf("ListProgressByMediaItems returned %d items, want 2", len(progressMap))
	}
	if progressMap["movie-1"].MediaItemID != "movie-1" {
		t.Errorf("ListProgressByMediaItems movie-1 = %+v, want media item movie-1", progressMap["movie-1"])
	}
	if !progressMap["movie-2"].Completed {
		t.Errorf("ListProgressByMediaItems movie-2 completed = %v, want true", progressMap["movie-2"].Completed)
	}

	emptyProgressMap, err := store.ListProgressByMediaItems(ctx, "p1", nil)
	if err != nil {
		t.Fatalf("ListProgressByMediaItems(empty): %v", err)
	}
	if len(emptyProgressMap) != 0 {
		t.Errorf("ListProgressByMediaItems(empty) returned %d items, want 0", len(emptyProgressMap))
	}

	// Batch helpers — exercise the unnest()/IN(...) paths used by the
	// jellycompat series mark-played/unplayed handlers.
	batchAt := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	if err := store.MarkProgressBatch(ctx, "p1", nil, batchAt); err != nil {
		t.Fatalf("MarkProgressBatch(empty): %v", err)
	}
	if err := store.MarkProgressBatch(ctx, "p1", []string{"ep-a", "ep-b", "ep-c"}, batchAt); err != nil {
		t.Fatalf("MarkProgressBatch: %v", err)
	}
	for _, id := range []string{"ep-a", "ep-b", "ep-c"} {
		wp, err := store.GetProgress(ctx, "p1", id)
		if err != nil {
			t.Fatalf("GetProgress(%s): %v", id, err)
		}
		if wp == nil || !wp.Completed {
			t.Fatalf("GetProgress(%s) = %+v, want completed row", id, wp)
		}
	}

	if err := store.ClearProgressBatch(ctx, "p1", nil, batchAt); err != nil {
		t.Fatalf("ClearProgressBatch(empty): %v", err)
	}
	if err := store.ClearProgressBatch(ctx, "p1", []string{"ep-a", "ep-b"}, batchAt); err != nil {
		t.Fatalf("ClearProgressBatch: %v", err)
	}
	for _, id := range []string{"ep-a", "ep-b"} {
		wp, err := store.GetProgress(ctx, "p1", id)
		if err != nil {
			t.Fatalf("GetProgress(after ClearProgressBatch %s): %v", id, err)
		}
		if wp == nil || wp.Completed {
			t.Fatalf("GetProgress(after ClearProgressBatch %s) = %+v, want completed=false row", id, wp)
		}
		if wp.PositionSeconds != 0 {
			t.Fatalf("GetProgress(after ClearProgressBatch %s) position = %v, want 0", id, wp.PositionSeconds)
		}
	}
	wpC, err := store.GetProgress(ctx, "p1", "ep-c")
	if err != nil {
		t.Fatalf("GetProgress(ep-c, untouched): %v", err)
	}
	if wpC == nil || !wpC.Completed {
		t.Fatalf("GetProgress(ep-c, untouched) = %+v, want still completed", wpC)
	}

	// Defense-in-depth: empty/whitespace/duplicate IDs must be compacted out
	// before they hit SQL — otherwise pgstore would insert a literal '' row
	// into user_watch_progress and userdb would bind '' into the IN clause.
	if err := store.MarkProgressBatch(ctx, "p1", []string{"only-a", "", "only-a", "  "}, batchAt); err != nil {
		t.Fatalf("MarkProgressBatch(dirty): %v", err)
	}
	wpA, err := store.GetProgress(ctx, "p1", "only-a")
	if err != nil {
		t.Fatalf("GetProgress(only-a after dirty mark): %v", err)
	}
	if wpA == nil || !wpA.Completed {
		t.Fatalf("GetProgress(only-a after dirty mark) = %+v, want completed", wpA)
	}
	if wpEmpty, err := store.GetProgress(ctx, "p1", ""); err != nil {
		t.Fatalf("GetProgress(empty after dirty mark): %v", err)
	} else if wpEmpty != nil {
		t.Fatalf("GetProgress(empty after dirty mark) = %+v, want nil — empty-string IDs must be compacted", wpEmpty)
	}
	if wpWS, err := store.GetProgress(ctx, "p1", "  "); err != nil {
		t.Fatalf("GetProgress(whitespace after dirty mark): %v", err)
	} else if wpWS != nil {
		t.Fatalf("GetProgress(whitespace after dirty mark) = %+v, want nil — whitespace IDs must be compacted", wpWS)
	}

	// Same coverage for ClearProgressBatch — passing dirty input must not
	// touch a literal '' row (and must not error).
	if err := store.ClearProgressBatch(ctx, "p1", []string{"only-a", "", "only-a", "  "}, batchAt); err != nil {
		t.Fatalf("ClearProgressBatch(dirty): %v", err)
	}
	wpA2, err := store.GetProgress(ctx, "p1", "only-a")
	if err != nil {
		t.Fatalf("GetProgress(only-a after dirty clear): %v", err)
	}
	if wpA2 == nil || wpA2.Completed {
		t.Fatalf("GetProgress(only-a after dirty clear) = %+v, want completed=false", wpA2)
	}
}

func testHomeDismissals(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	continueWatching := userstore.HomeItemDismissal{
		ProfileID:         "p1",
		Surface:           userstore.HomeSurfaceContinueWatching,
		MediaItemID:       "ep-1",
		ProgressUpdatedAt: stringPtr("2026-03-22T18:10:00Z"),
		DismissedAt:       "2026-03-22T18:11:00Z",
	}
	if err := store.UpsertHomeDismissal(ctx, continueWatching); err != nil {
		t.Fatalf("UpsertHomeDismissal(continue_watching): %v", err)
	}

	nextUp := userstore.HomeItemDismissal{
		ProfileID:   "p1",
		Surface:     userstore.HomeSurfaceNextUp,
		MediaItemID: "ep-2",
		SeriesID:    stringPtr("series-1"),
		DismissedAt: "2026-03-22T18:12:00Z",
	}
	if err := store.UpsertHomeDismissal(ctx, nextUp); err != nil {
		t.Fatalf("UpsertHomeDismissal(next_up): %v", err)
	}

	continueDismissals, err := store.ListHomeDismissals(ctx, "p1", userstore.HomeSurfaceContinueWatching)
	if err != nil {
		t.Fatalf("ListHomeDismissals(continue_watching): %v", err)
	}
	if len(continueDismissals) != 1 {
		t.Fatalf("continue watching dismissals = %d, want 1", len(continueDismissals))
	}
	if continueDismissals[0].MediaItemID != "ep-1" {
		t.Fatalf("continue watching dismissal media_item_id = %q, want ep-1", continueDismissals[0].MediaItemID)
	}
	if continueDismissals[0].ProgressUpdatedAt == nil || *continueDismissals[0].ProgressUpdatedAt != "2026-03-22T18:10:00Z" {
		t.Fatalf("continue watching dismissal progress_updated_at = %v, want 2026-03-22T18:10:00Z", continueDismissals[0].ProgressUpdatedAt)
	}

	nextUpDismissals, err := store.ListHomeDismissals(ctx, "p1", userstore.HomeSurfaceNextUp)
	if err != nil {
		t.Fatalf("ListHomeDismissals(next_up): %v", err)
	}
	if len(nextUpDismissals) != 1 {
		t.Fatalf("next up dismissals = %d, want 1", len(nextUpDismissals))
	}
	if nextUpDismissals[0].SeriesID == nil || *nextUpDismissals[0].SeriesID != "series-1" {
		t.Fatalf("next up dismissal series_id = %v, want series-1", nextUpDismissals[0].SeriesID)
	}

	updated := continueWatching
	updated.ProgressUpdatedAt = stringPtr("2026-03-23T00:00:00Z")
	updated.DismissedAt = "2026-03-23T00:01:00Z"
	if err := store.UpsertHomeDismissal(ctx, updated); err != nil {
		t.Fatalf("UpsertHomeDismissal(update): %v", err)
	}

	continueDismissals, err = store.ListHomeDismissals(ctx, "p1", userstore.HomeSurfaceContinueWatching)
	if err != nil {
		t.Fatalf("ListHomeDismissals(after update): %v", err)
	}
	if len(continueDismissals) != 1 {
		t.Fatalf("continue watching dismissals after update = %d, want 1", len(continueDismissals))
	}
	if continueDismissals[0].ProgressUpdatedAt == nil || *continueDismissals[0].ProgressUpdatedAt != "2026-03-23T00:00:00Z" {
		t.Fatalf("continue watching dismissal progress_updated_at after update = %v, want 2026-03-23T00:00:00Z", continueDismissals[0].ProgressUpdatedAt)
	}

	if err := store.DeleteHomeDismissal(ctx, "p1", userstore.HomeSurfaceContinueWatching, "ep-1"); err != nil {
		t.Fatalf("DeleteHomeDismissal: %v", err)
	}

	continueDismissals, err = store.ListHomeDismissals(ctx, "p1", userstore.HomeSurfaceContinueWatching)
	if err != nil {
		t.Fatalf("ListHomeDismissals(after delete): %v", err)
	}
	if len(continueDismissals) != 0 {
		t.Fatalf("continue watching dismissals after delete = %d, want 0", len(continueDismissals))
	}
}

func stringPtr(value string) *string {
	return &value
}

func testFavorites(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	// Add
	if err := store.AddFavorite(ctx, "p1", "item-1"); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}
	if err := store.AddFavorite(ctx, "p1", "item-2"); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}

	// Add duplicate (should be no-op)
	if err := store.AddFavorite(ctx, "p1", "item-1"); err != nil {
		t.Fatalf("AddFavorite(duplicate): %v", err)
	}

	// Check
	ok, err := store.IsFavorite(ctx, "p1", "item-1")
	if err != nil {
		t.Fatalf("IsFavorite: %v", err)
	}
	if !ok {
		t.Error("IsFavorite returned false, want true")
	}

	ok, _ = store.IsFavorite(ctx, "p1", "nonexistent")
	if ok {
		t.Error("IsFavorite(nonexistent) returned true")
	}

	// List
	favs, err := store.ListFavorites(ctx, "p1", 10, 0)
	if err != nil {
		t.Fatalf("ListFavorites: %v", err)
	}
	if len(favs) != 2 {
		t.Errorf("ListFavorites returned %d, want 2", len(favs))
	}

	favoriteMap, err := store.ListFavoritesByMediaItems(ctx, "p1", []string{"item-1", "item-2", "missing"})
	if err != nil {
		t.Fatalf("ListFavoritesByMediaItems: %v", err)
	}
	if len(favoriteMap) != 2 {
		t.Fatalf("ListFavoritesByMediaItems returned %d items, want 2", len(favoriteMap))
	}
	if !favoriteMap["item-1"] || !favoriteMap["item-2"] {
		t.Errorf("ListFavoritesByMediaItems returned %v, want item-1 and item-2 true", favoriteMap)
	}

	emptyFavoriteMap, err := store.ListFavoritesByMediaItems(ctx, "p1", nil)
	if err != nil {
		t.Fatalf("ListFavoritesByMediaItems(empty): %v", err)
	}
	if len(emptyFavoriteMap) != 0 {
		t.Errorf("ListFavoritesByMediaItems(empty) returned %d items, want 0", len(emptyFavoriteMap))
	}

	// Remove
	if err := store.RemoveFavorite(ctx, "p1", "item-1"); err != nil {
		t.Fatalf("RemoveFavorite: %v", err)
	}
	ok, _ = store.IsFavorite(ctx, "p1", "item-1")
	if ok {
		t.Error("IsFavorite after remove returned true")
	}
}

func testWatchlist(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	// Add
	if err := store.AddToWatchlist(ctx, "p1", "item-1"); err != nil {
		t.Fatalf("AddToWatchlist: %v", err)
	}

	// Check
	ok, err := store.InWatchlist(ctx, "p1", "item-1")
	if err != nil {
		t.Fatalf("InWatchlist: %v", err)
	}
	if !ok {
		t.Error("InWatchlist returned false, want true")
	}

	// List
	entries, err := store.ListWatchlist(ctx, "p1", 10, 0)
	if err != nil {
		t.Fatalf("ListWatchlist: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("ListWatchlist returned %d, want 1", len(entries))
	}

	watchlistMap, err := store.ListWatchlistByMediaItems(ctx, "p1", []string{"item-1", "missing"})
	if err != nil {
		t.Fatalf("ListWatchlistByMediaItems: %v", err)
	}
	if len(watchlistMap) != 1 {
		t.Fatalf("ListWatchlistByMediaItems returned %d items, want 1", len(watchlistMap))
	}
	if !watchlistMap["item-1"] {
		t.Errorf("ListWatchlistByMediaItems returned %v, want item-1 true", watchlistMap)
	}

	emptyWatchlistMap, err := store.ListWatchlistByMediaItems(ctx, "p1", nil)
	if err != nil {
		t.Fatalf("ListWatchlistByMediaItems(empty): %v", err)
	}
	if len(emptyWatchlistMap) != 0 {
		t.Errorf("ListWatchlistByMediaItems(empty) returned %d items, want 0", len(emptyWatchlistMap))
	}

	// Remove
	if err := store.RemoveFromWatchlist(ctx, "p1", "item-1"); err != nil {
		t.Fatalf("RemoveFromWatchlist: %v", err)
	}
	ok, _ = store.InWatchlist(ctx, "p1", "item-1")
	if ok {
		t.Error("InWatchlist after remove returned true")
	}
}

func testCollections(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	// Create
	coll, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: "p1",
		Name:             "My Collection",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if coll.Name != "My Collection" {
		t.Errorf("Collection.Name = %q, want %q", coll.Name, "My Collection")
	}

	// Get
	got, err := store.GetCollection(ctx, coll.ID)
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if got.Name != "My Collection" {
		t.Errorf("GetCollection Name = %q, want %q", got.Name, "My Collection")
	}

	// List
	colls, err := store.ListCollections(ctx, "p1")
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(colls) != 1 {
		t.Errorf("ListCollections returned %d, want 1", len(colls))
	}

	// Update
	newName := "Renamed"
	if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{
		ID:               coll.ID,
		RequestProfileID: "p1",
		Name:             &newName,
	}); err != nil {
		t.Fatalf("UpdateCollection: %v", err)
	}
	got, _ = store.GetCollection(ctx, coll.ID)
	if got.Name != "Renamed" {
		t.Errorf("Updated Name = %q, want %q", got.Name, "Renamed")
	}

	// Add items
	if err := store.AddCollectionItem(ctx, coll.ID, "item-1", 0); err != nil {
		t.Fatalf("AddCollectionItem: %v", err)
	}
	if err := store.AddCollectionItem(ctx, coll.ID, "item-2", 1); err != nil {
		t.Fatalf("AddCollectionItem: %v", err)
	}

	// List items
	items, err := store.ListCollectionItems(ctx, coll.ID)
	if err != nil {
		t.Fatalf("ListCollectionItems: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("ListCollectionItems returned %d, want 2", len(items))
	}

	// Remove item
	if err := store.RemoveCollectionItem(ctx, coll.ID, "item-1"); err != nil {
		t.Fatalf("RemoveCollectionItem: %v", err)
	}
	items, _ = store.ListCollectionItems(ctx, coll.ID)
	if len(items) != 1 {
		t.Errorf("After remove, ListCollectionItems returned %d, want 1", len(items))
	}

	// Delete collection (should also delete items)
	if err := store.DeleteCollection(ctx, coll.ID); err != nil {
		t.Fatalf("DeleteCollection: %v", err)
	}

	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p2", Name: "Viewer"}); err != nil {
		t.Fatalf("CreateProfile(p2): %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p3", Name: "Blocked"}); err != nil {
		t.Fatalf("CreateProfile(p3): %v", err)
	}

	shared, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID:  "p1",
		Name:              "Family Action",
		CollectionType:    "smart",
		IsShared:          true,
		AllowedProfileIDs: []string{"p1", "p2"},
		QueryDefinition:   `{"match":"all","groups":[]}`,
	})
	if err != nil {
		t.Fatalf("CreateCollection(shared): %v", err)
	}

	visible, err := store.ListCollections(ctx, "p2")
	if err != nil {
		t.Fatalf("ListCollections(p2): %v", err)
	}
	if len(visible) != 1 || visible[0].ID != shared.ID {
		t.Fatalf("ListCollections(p2) = %+v, want shared collection", visible)
	}

	blocked, err := store.ListCollections(ctx, "p3")
	if err != nil {
		t.Fatalf("ListCollections(p3): %v", err)
	}
	if len(blocked) != 0 {
		t.Fatalf("ListCollections(p3) returned %d collections, want 0", len(blocked))
	}

	rejectedName := "Not Allowed"
	if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{
		ID:               shared.ID,
		RequestProfileID: "p2",
		Name:             &rejectedName,
	}); err == nil {
		t.Fatal("expected creator-only UpdateCollection rejection")
	}
}

func testSettings(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	// Get non-existent
	val, err := store.GetSetting(ctx, "theme")
	if err != nil {
		t.Fatalf("GetSetting(nonexistent): %v", err)
	}
	if val != "" {
		t.Errorf("GetSetting(nonexistent) = %q, want empty", val)
	}

	// Set
	if err := store.SetSetting(ctx, "theme", "dark"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	val, err = store.GetSetting(ctx, "theme")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if val != "dark" {
		t.Errorf("GetSetting = %q, want %q", val, "dark")
	}

	// Update (upsert)
	if err := store.SetSetting(ctx, "theme", "light"); err != nil {
		t.Fatalf("SetSetting(update): %v", err)
	}
	val, _ = store.GetSetting(ctx, "theme")
	if val != "light" {
		t.Errorf("GetSetting after update = %q, want %q", val, "light")
	}

	// List
	if err := store.SetSetting(ctx, "lang", "en"); err != nil {
		t.Fatalf("SetSetting(lang): %v", err)
	}
	entries, err := store.ListSettings(ctx)
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("ListSettings returned %d, want 2", len(entries))
	}

	// Delete
	if err := store.DeleteSetting(ctx, "theme"); err != nil {
		t.Fatalf("DeleteSetting: %v", err)
	}
	val, _ = store.GetSetting(ctx, "theme")
	if val != "" {
		t.Errorf("GetSetting after delete = %q, want empty", val)
	}

	deviceSetting, err := store.GetDeviceSetting(ctx, "p1", "apple-tv", "subtitle_appearance")
	if err != nil {
		t.Fatalf("GetDeviceSetting(nonexistent): %v", err)
	}
	if deviceSetting != nil {
		t.Fatalf("GetDeviceSetting(nonexistent) = %#v, want nil", deviceSetting)
	}

	if err := store.SetDeviceSetting(ctx, userstore.DeviceSettingEntry{
		ProfileID:      "p1",
		DeviceID:       "apple-tv",
		DeviceName:     "Living Room",
		DevicePlatform: "tvOS",
		Key:            "subtitle_appearance",
		Value:          `{"fontSize":"large"}`,
	}); err != nil {
		t.Fatalf("SetDeviceSetting: %v", err)
	}
	deviceSetting, err = store.GetDeviceSetting(ctx, "p1", "apple-tv", "subtitle_appearance")
	if err != nil {
		t.Fatalf("GetDeviceSetting: %v", err)
	}
	if deviceSetting == nil || deviceSetting.Value != `{"fontSize":"large"}` || deviceSetting.DeviceName != "Living Room" {
		t.Fatalf("GetDeviceSetting = %#v, want stored override", deviceSetting)
	}

	if err := store.SetDeviceSetting(ctx, userstore.DeviceSettingEntry{
		ProfileID:      "p1",
		DeviceID:       "iphone",
		DeviceName:     "iPhone",
		DevicePlatform: "iOS",
		Key:            "subtitle_appearance",
		Value:          `{"fontSize":"small"}`,
	}); err != nil {
		t.Fatalf("SetDeviceSetting(second): %v", err)
	}
	deviceSettings, err := store.ListDeviceSettings(ctx, "subtitle_appearance")
	if err != nil {
		t.Fatalf("ListDeviceSettings: %v", err)
	}
	if len(deviceSettings) != 2 {
		t.Fatalf("ListDeviceSettings returned %d, want 2", len(deviceSettings))
	}
	allDeviceSettings, err := store.ListAllDeviceSettings(ctx)
	if err != nil {
		t.Fatalf("ListAllDeviceSettings: %v", err)
	}
	if len(allDeviceSettings) != 2 {
		t.Fatalf("ListAllDeviceSettings returned %d, want 2", len(allDeviceSettings))
	}

	if err := store.DeleteDeviceSetting(ctx, "p1", "apple-tv", "subtitle_appearance"); err != nil {
		t.Fatalf("DeleteDeviceSetting: %v", err)
	}
	deviceSetting, err = store.GetDeviceSetting(ctx, "p1", "apple-tv", "subtitle_appearance")
	if err != nil {
		t.Fatalf("GetDeviceSetting after delete: %v", err)
	}
	if deviceSetting != nil {
		t.Fatalf("GetDeviceSetting after delete = %#v, want nil", deviceSetting)
	}

	if err := store.SetDeviceSetting(ctx, userstore.DeviceSettingEntry{
		ProfileID: "p1",
		DeviceID:  "apple-tv",
		Key:       "player.playback_speed",
		Value:     "1.25",
	}); err != nil {
		t.Fatalf("SetDeviceSetting(third): %v", err)
	}
	if err := store.SetDeviceSetting(ctx, userstore.DeviceSettingEntry{
		ProfileID: "p1",
		DeviceID:  "apple-tv",
		Key:       "player.audio_sync_ms",
		Value:     "120",
	}); err != nil {
		t.Fatalf("SetDeviceSetting(fourth): %v", err)
	}
	if err := store.DeleteAllDeviceSettings(ctx, "p1", "apple-tv"); err != nil {
		t.Fatalf("DeleteAllDeviceSettings: %v", err)
	}
	deviceSetting, err = store.GetDeviceSetting(ctx, "p1", "apple-tv", "player.playback_speed")
	if err != nil {
		t.Fatalf("GetDeviceSetting after delete-all-device: %v", err)
	}
	if deviceSetting != nil {
		t.Fatalf("GetDeviceSetting after DeleteAllDeviceSettings = %#v, want nil", deviceSetting)
	}

	if err := store.DeleteDeviceSettingsByKey(ctx, "subtitle_appearance"); err != nil {
		t.Fatalf("DeleteDeviceSettingsByKey: %v", err)
	}
	deviceSettings, err = store.ListDeviceSettings(ctx, "subtitle_appearance")
	if err != nil {
		t.Fatalf("ListDeviceSettings after delete all: %v", err)
	}
	if len(deviceSettings) != 0 {
		t.Fatalf("ListDeviceSettings after delete all returned %d, want 0", len(deviceSettings))
	}
}

func testSubtitlePreferences(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	// Get non-existent
	pref, err := store.GetSubtitlePreference(ctx, "p1", "series-1")
	if err != nil {
		t.Fatalf("GetSubtitlePreference(nonexistent): %v", err)
	}
	if pref != nil {
		t.Error("GetSubtitlePreference(nonexistent) returned non-nil")
	}

	// Set
	now := "2024-01-01T00:00:00Z"
	if err := store.SetSubtitlePreference(ctx, userstore.SubtitlePreference{
		ProfileID:              "p1",
		SeriesID:               "series-1",
		SubtitleLanguage:       "ja",
		SubtitleTrackIndex:     2,
		ExternalSubtitlePath:   "/subs/ep1.srt",
		SubtitleMode:           "off",
		ShowForcedSubtitles:    true,
		HasShowForcedSubtitles: true,
		UpdatedAt:              now,
	}); err != nil {
		t.Fatalf("SetSubtitlePreference: %v", err)
	}

	// Get
	pref, err = store.GetSubtitlePreference(ctx, "p1", "series-1")
	if err != nil {
		t.Fatalf("GetSubtitlePreference: %v", err)
	}
	if pref == nil {
		t.Fatal("GetSubtitlePreference returned nil")
	}
	if pref.SubtitleLanguage != "ja" {
		t.Errorf("SubtitleLanguage = %q, want %q", pref.SubtitleLanguage, "ja")
	}
	if pref.SubtitleTrackIndex != 2 {
		t.Errorf("SubtitleTrackIndex = %d, want 2", pref.SubtitleTrackIndex)
	}
	if !pref.ShowForcedSubtitles || !pref.HasShowForcedSubtitles {
		t.Errorf("ShowForcedSubtitles = (%v, %v), want (true, true)", pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles)
	}

	// Update (upsert)
	if err := store.SetSubtitlePreference(ctx, userstore.SubtitlePreference{
		ProfileID:              "p1",
		SeriesID:               "series-1",
		SubtitleLanguage:       "en",
		SubtitleMode:           "auto",
		ShowForcedSubtitles:    false,
		HasShowForcedSubtitles: true,
		UpdatedAt:              now,
	}); err != nil {
		t.Fatalf("SetSubtitlePreference(update): %v", err)
	}
	pref, _ = store.GetSubtitlePreference(ctx, "p1", "series-1")
	if pref.SubtitleLanguage != "en" {
		t.Errorf("Updated SubtitleLanguage = %q, want %q", pref.SubtitleLanguage, "en")
	}
	if pref.ShowForcedSubtitles || !pref.HasShowForcedSubtitles {
		t.Errorf("Updated ShowForcedSubtitles = (%v, %v), want (false, true)", pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles)
	}

	// Delete
	if err := store.DeleteSubtitlePreference(ctx, "p1", "series-1"); err != nil {
		t.Fatalf("DeleteSubtitlePreference: %v", err)
	}
	pref, _ = store.GetSubtitlePreference(ctx, "p1", "series-1")
	if pref != nil {
		t.Error("GetSubtitlePreference after delete returned non-nil")
	}
}

func testAudioPreferences(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	// Create required profile first.
	if err := store.CreateProfile(ctx, userstore.Profile{
		ID: "p1", Name: "Test", CreatedAt: "2024-01-01T00:00:00Z", UpdatedAt: "2024-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	// Get non-existent
	ap, err := store.GetAudioPreference(ctx, "p1", "series-1")
	if err != nil {
		t.Fatalf("GetAudioPreference(nonexistent): %v", err)
	}
	if ap != nil {
		t.Error("GetAudioPreference(nonexistent) returned non-nil")
	}

	// Set
	now := "2024-01-01T00:00:00Z"
	if err := store.SetAudioPreference(ctx, userstore.AudioPreference{
		ProfileID:       "p1",
		SeriesID:        "series-1",
		AudioTrackIndex: 2,
		AudioLanguage:   "ja",
		UpdatedAt:       now,
	}); err != nil {
		t.Fatalf("SetAudioPreference: %v", err)
	}

	// Get
	ap, err = store.GetAudioPreference(ctx, "p1", "series-1")
	if err != nil {
		t.Fatalf("GetAudioPreference: %v", err)
	}
	if ap == nil {
		t.Fatal("GetAudioPreference returned nil")
	}
	if ap.AudioLanguage != "ja" {
		t.Errorf("AudioLanguage = %q, want %q", ap.AudioLanguage, "ja")
	}
	if ap.AudioTrackIndex != 2 {
		t.Errorf("AudioTrackIndex = %d, want %d", ap.AudioTrackIndex, 2)
	}

	// Update (upsert)
	if err := store.SetAudioPreference(ctx, userstore.AudioPreference{
		ProfileID:       "p1",
		SeriesID:        "series-1",
		AudioTrackIndex: 0,
		AudioLanguage:   "en",
		UpdatedAt:       now,
	}); err != nil {
		t.Fatalf("SetAudioPreference(update): %v", err)
	}
	ap, _ = store.GetAudioPreference(ctx, "p1", "series-1")
	if ap.AudioLanguage != "en" {
		t.Errorf("Updated AudioLanguage = %q, want %q", ap.AudioLanguage, "en")
	}
	if ap.AudioTrackIndex != 0 {
		t.Errorf("Updated AudioTrackIndex = %d, want %d", ap.AudioTrackIndex, 0)
	}

	// Delete
	if err := store.DeleteAudioPreference(ctx, "p1", "series-1"); err != nil {
		t.Fatalf("DeleteAudioPreference: %v", err)
	}
	ap, _ = store.GetAudioPreference(ctx, "p1", "series-1")
	if ap != nil {
		t.Error("GetAudioPreference after delete returned non-nil")
	}
}

func testLibraryPlaybackPreferences(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	pref, err := store.GetLibraryPlaybackPreference(ctx, "p1", 1)
	if err != nil {
		t.Fatalf("GetLibraryPlaybackPreference(nonexistent): %v", err)
	}
	if pref != nil {
		t.Error("GetLibraryPlaybackPreference(nonexistent) returned non-nil")
	}

	now := "2024-01-01T00:00:00Z"
	if err := store.UpsertLibraryPlaybackPreference(ctx, userstore.LibraryPlaybackPreference{
		ProfileID:              "p1",
		LibraryID:              1,
		AudioLanguage:          "ja",
		SubtitleLanguage:       "en",
		SubtitleMode:           "always",
		ShowForcedSubtitles:    true,
		HasShowForcedSubtitles: true,
		UpdatedAt:              now,
	}); err != nil {
		t.Fatalf("UpsertLibraryPlaybackPreference: %v", err)
	}

	pref, err = store.GetLibraryPlaybackPreference(ctx, "p1", 1)
	if err != nil {
		t.Fatalf("GetLibraryPlaybackPreference: %v", err)
	}
	if pref == nil {
		t.Fatal("GetLibraryPlaybackPreference returned nil")
	}
	if pref.AudioLanguage != "ja" {
		t.Errorf("AudioLanguage = %q, want %q", pref.AudioLanguage, "ja")
	}
	if pref.SubtitleLanguage != "en" {
		t.Errorf("SubtitleLanguage = %q, want %q", pref.SubtitleLanguage, "en")
	}
	if pref.SubtitleMode != "always" {
		t.Errorf("SubtitleMode = %q, want %q", pref.SubtitleMode, "always")
	}
	if !pref.ShowForcedSubtitles || !pref.HasShowForcedSubtitles {
		t.Errorf("ShowForcedSubtitles = (%v, %v), want (true, true)", pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles)
	}

	if err := store.UpsertLibraryPlaybackPreference(ctx, userstore.LibraryPlaybackPreference{
		ProfileID:              "p1",
		LibraryID:              1,
		AudioLanguage:          "en",
		SubtitleLanguage:       "es",
		SubtitleMode:           "auto",
		ShowForcedSubtitles:    false,
		HasShowForcedSubtitles: true,
		UpdatedAt:              now,
	}); err != nil {
		t.Fatalf("UpsertLibraryPlaybackPreference(update): %v", err)
	}
	pref, _ = store.GetLibraryPlaybackPreference(ctx, "p1", 1)
	if pref.AudioLanguage != "en" {
		t.Errorf("Updated AudioLanguage = %q, want %q", pref.AudioLanguage, "en")
	}
	if pref.SubtitleLanguage != "es" {
		t.Errorf("Updated SubtitleLanguage = %q, want %q", pref.SubtitleLanguage, "es")
	}
	if pref.SubtitleMode != "auto" {
		t.Errorf("Updated SubtitleMode = %q, want %q", pref.SubtitleMode, "auto")
	}
	if pref.ShowForcedSubtitles || !pref.HasShowForcedSubtitles {
		t.Errorf("Updated ShowForcedSubtitles = (%v, %v), want (false, true)", pref.ShowForcedSubtitles, pref.HasShowForcedSubtitles)
	}

	if err := store.UpsertLibraryPlaybackPreference(ctx, userstore.LibraryPlaybackPreference{
		ProfileID:        "p1",
		LibraryID:        2,
		AudioLanguage:    "de",
		SubtitleLanguage: "fr",
		SubtitleMode:     "off",
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("UpsertLibraryPlaybackPreference(second): %v", err)
	}

	list, err := store.ListLibraryPlaybackPreferences(ctx, "p1")
	if err != nil {
		t.Fatalf("ListLibraryPlaybackPreferences: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListLibraryPlaybackPreferences returned %d items, want 2", len(list))
	}

	if err := store.DeleteLibraryPlaybackPreference(ctx, "p1", 1); err != nil {
		t.Fatalf("DeleteLibraryPlaybackPreference: %v", err)
	}
	pref, _ = store.GetLibraryPlaybackPreference(ctx, "p1", 1)
	if pref != nil {
		t.Error("GetLibraryPlaybackPreference after delete returned non-nil")
	}

	if err := store.UpsertLibraryPlaybackPreference(ctx, userstore.LibraryPlaybackPreference{
		ProfileID:        "p1",
		LibraryID:        2,
		AudioLanguage:    "de",
		SubtitleLanguage: "fr",
		SubtitleMode:     "off",
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("UpsertLibraryPlaybackPreference(lifecycle): %v", err)
	}
	if err := store.DeleteProfile(ctx, "p1"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Recreated"}); err != nil {
		t.Fatalf("CreateProfile(recreate): %v", err)
	}
	pref, err = store.GetLibraryPlaybackPreference(ctx, "p1", 2)
	if err != nil {
		t.Fatalf("GetLibraryPlaybackPreference(recreated profile): %v", err)
	}
	if pref != nil {
		t.Fatal("GetLibraryPlaybackPreference after profile recreate returned stale preference")
	}

	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p2", Name: "Other"}); err != nil {
		t.Fatalf("CreateProfile(p2): %v", err)
	}
	if err := store.UpsertLibraryPlaybackPreference(ctx, userstore.LibraryPlaybackPreference{
		ProfileID:        "p2",
		LibraryID:        1,
		AudioLanguage:    "it",
		SubtitleLanguage: "pt",
		SubtitleMode:     "auto",
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("UpsertLibraryPlaybackPreference(p2): %v", err)
	}
	pref, err = store.GetLibraryPlaybackPreference(ctx, "p2", 1)
	if err != nil {
		t.Fatalf("GetLibraryPlaybackPreference(p2): %v", err)
	}
	if pref == nil {
		t.Fatal("GetLibraryPlaybackPreference(p2) returned nil")
	}
	if err := store.DeleteProfile(ctx, "p1"); err != nil {
		t.Fatalf("DeleteProfile(recreated): %v", err)
	}
	pref, err = store.GetLibraryPlaybackPreference(ctx, "p2", 1)
	if err != nil {
		t.Fatalf("GetLibraryPlaybackPreference(p2 after p1 delete): %v", err)
	}
	if pref == nil {
		t.Fatal("GetLibraryPlaybackPreference(p2 after p1 delete) returned nil")
	}
}

func testProgressHints(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	noThreshold := userstore.ProgressThresholds{}

	// Must have progress row first (hints are advisory — UPDATE only).
	if err := store.SetProgress(ctx, "p1", "movie-1", 500, 7200, noThreshold); err != nil {
		t.Fatalf("SetProgress: %v", err)
	}

	// Set hints.
	hints := userstore.VersionHints{
		FileID:     42,
		Resolution: "2160p",
		HDR:        true,
		CodecVideo: "hevc",
	}
	if err := store.UpdateProgressHints(ctx, "p1", "movie-1", hints); err != nil {
		t.Fatalf("UpdateProgressHints: %v", err)
	}

	// Verify hints are returned by GetProgress.
	wp, err := store.GetProgress(ctx, "p1", "movie-1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if wp == nil {
		t.Fatal("GetProgress returned nil")
	}
	if wp.LastFileID == nil || *wp.LastFileID != 42 {
		t.Errorf("LastFileID = %v, want 42", wp.LastFileID)
	}
	if wp.LastResolution == nil || *wp.LastResolution != "2160p" {
		t.Errorf("LastResolution = %v, want %q", wp.LastResolution, "2160p")
	}
	if wp.LastHDR == nil || *wp.LastHDR != true {
		t.Errorf("LastHDR = %v, want true", wp.LastHDR)
	}
	if wp.LastCodecVideo == nil || *wp.LastCodecVideo != "hevc" {
		t.Errorf("LastCodecVideo = %v, want %q", wp.LastCodecVideo, "hevc")
	}

	// Overwrite hints.
	hints2 := userstore.VersionHints{
		FileID:     99,
		Resolution: "1080p",
		HDR:        false,
		CodecVideo: "h264",
	}
	if err := store.UpdateProgressHints(ctx, "p1", "movie-1", hints2); err != nil {
		t.Fatalf("UpdateProgressHints(overwrite): %v", err)
	}
	wp, _ = store.GetProgress(ctx, "p1", "movie-1")
	if wp.LastFileID == nil || *wp.LastFileID != 99 {
		t.Errorf("LastFileID after overwrite = %v, want 99", wp.LastFileID)
	}
	if wp.LastResolution == nil || *wp.LastResolution != "1080p" {
		t.Errorf("LastResolution after overwrite = %v, want %q", wp.LastResolution, "1080p")
	}

	// Hints for nonexistent progress row should be a no-op (not an error).
	if err := store.UpdateProgressHints(ctx, "p1", "nonexistent", hints); err != nil {
		t.Fatalf("UpdateProgressHints(nonexistent): %v", err)
	}

	// Verify hints survive in ListProgress.
	all, err := store.ListProgress(ctx, "p1", "all", 10, 0)
	if err != nil {
		t.Fatalf("ListProgress: %v", err)
	}
	found := false
	for _, wp := range all {
		if wp.MediaItemID == "movie-1" {
			found = true
			if wp.LastFileID == nil || *wp.LastFileID != 99 {
				t.Errorf("ListProgress LastFileID = %v, want 99", wp.LastFileID)
			}
		}
	}
	if !found {
		t.Error("movie-1 not found in ListProgress results")
	}

	// Verify hints survive UpdateProgress (position-only update).
	if err := store.UpdateProgress(ctx, "p1", "movie-1", 600, 7200, noThreshold); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}
	wp, _ = store.GetProgress(ctx, "p1", "movie-1")
	if wp.LastFileID == nil || *wp.LastFileID != 99 {
		t.Errorf("LastFileID after UpdateProgress = %v, want 99 (preserved)", wp.LastFileID)
	}

	// Verify hints survive SetProgress (unconditional position update).
	if err := store.SetProgress(ctx, "p1", "movie-1", 400, 7200, noThreshold); err != nil {
		t.Fatalf("SetProgress: %v", err)
	}
	wp, _ = store.GetProgress(ctx, "p1", "movie-1")
	if wp.LastFileID == nil || *wp.LastFileID != 99 {
		t.Errorf("LastFileID after SetProgress = %v, want 99 (preserved)", wp.LastFileID)
	}

	// Verify pre-migration rows return nil hints.
	if err := store.SetProgress(ctx, "p1", "movie-no-hints", 400, 3600, noThreshold); err != nil {
		t.Fatalf("SetProgress: %v", err)
	}
	wp, err = store.GetProgress(ctx, "p1", "movie-no-hints")
	if err != nil {
		t.Fatalf("GetProgress(movie-no-hints): %v", err)
	}
	if wp == nil {
		t.Fatal("GetProgress(movie-no-hints) returned nil")
	}
	if wp.LastFileID != nil {
		t.Errorf("Expected nil LastFileID for row without hints, got %v", wp.LastFileID)
	}
	if wp.LastHDR != nil {
		t.Errorf("Expected nil LastHDR for row without hints, got %v", wp.LastHDR)
	}
}

func testSectionOverrides(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	if err := store.SaveSectionOverrides(ctx, "profile-1", "home", "", []userstore.SectionOverride{
		{
			SectionID: "section-1",
			Removed:   true,
			Title:     "Recently Added",
		},
	}); err != nil {
		t.Fatalf("SaveSectionOverrides: %v", err)
	}

	got, err := store.ListSectionOverrides(ctx, "profile-1", "home", "")
	if err != nil {
		t.Fatalf("ListSectionOverrides: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListSectionOverrides returned %d overrides, want 1", len(got))
	}
	if !got[0].Removed {
		t.Fatalf("Removed = %v, want true", got[0].Removed)
	}
	if got[0].SectionID != "section-1" {
		t.Fatalf("SectionID = %q, want %q", got[0].SectionID, "section-1")
	}

	if err := store.SaveSectionOverrides(ctx, "profile-2", "home", "", []userstore.SectionOverride{
		{
			SectionID: "section-2",
			Hidden:    true,
			Title:     "Continue Watching",
		},
	}); err != nil {
		t.Fatalf("SaveSectionOverrides(profile-2): %v", err)
	}

	if err := store.SaveSectionOverrides(ctx, "profile-1", "home", "", []userstore.SectionOverride{
		{
			SectionID: "section-3",
			Removed:   true,
			Title:     "Top Picks",
		},
	}); err != nil {
		t.Fatalf("SaveSectionOverrides(profile-1 replacement): %v", err)
	}

	got, err = store.ListSectionOverrides(ctx, "profile-1", "home", "")
	if err != nil {
		t.Fatalf("ListSectionOverrides(profile-1 after replacement): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListSectionOverrides(profile-1 after replacement) returned %d overrides, want 1", len(got))
	}
	if got[0].SectionID != "section-3" {
		t.Fatalf("profile-1 SectionID after replacement = %q, want %q", got[0].SectionID, "section-3")
	}

	got, err = store.ListSectionOverrides(ctx, "profile-2", "home", "")
	if err != nil {
		t.Fatalf("ListSectionOverrides(profile-2): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListSectionOverrides(profile-2) returned %d overrides, want 1", len(got))
	}
	if got[0].SectionID != "section-2" {
		t.Fatalf("profile-2 SectionID = %q, want %q", got[0].SectionID, "section-2")
	}

	if err := store.ResetSectionOverrides(ctx, "profile-1", "home", ""); err != nil {
		t.Fatalf("ResetSectionOverrides(profile-1): %v", err)
	}

	got, err = store.ListSectionOverrides(ctx, "profile-1", "home", "")
	if err != nil {
		t.Fatalf("ListSectionOverrides(profile-1 after reset): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListSectionOverrides(profile-1 after reset) returned %d overrides, want 0", len(got))
	}

	got, err = store.ListSectionOverrides(ctx, "profile-2", "home", "")
	if err != nil {
		t.Fatalf("ListSectionOverrides(profile-2 after profile-1 reset): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListSectionOverrides(profile-2 after profile-1 reset) returned %d overrides, want 1", len(got))
	}
	if got[0].SectionID != "section-2" {
		t.Fatalf("profile-2 SectionID after profile-1 reset = %q, want %q", got[0].SectionID, "section-2")
	}
}

func testSectionOverridesUserAddedFields(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	input := userstore.SectionOverride{
		SectionID:       "",
		IsUserAdded:     true,
		UserSectionType: "hidden_gems",
		UserConfig:      `{"min_rating":7.5}`,
		UserTitle:       "Hidden Gems",
	}

	if err := store.SaveSectionOverrides(ctx, "profile-ua", "home", "", []userstore.SectionOverride{input}); err != nil {
		t.Fatalf("SaveSectionOverrides: %v", err)
	}

	got, err := store.ListSectionOverrides(ctx, "profile-ua", "home", "")
	if err != nil {
		t.Fatalf("ListSectionOverrides: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListSectionOverrides returned %d overrides, want 1", len(got))
	}

	o := got[0]
	if !o.IsUserAdded {
		t.Error("IsUserAdded = false, want true")
	}
	if o.UserSectionType != "hidden_gems" {
		t.Errorf("UserSectionType = %q, want %q", o.UserSectionType, "hidden_gems")
	}
	if o.UserConfig != `{"min_rating":7.5}` {
		t.Errorf("UserConfig = %q, want %q", o.UserConfig, `{"min_rating":7.5}`)
	}
	if o.UserTitle != "Hidden Gems" {
		t.Errorf("UserTitle = %q, want %q", o.UserTitle, "Hidden Gems")
	}
}
