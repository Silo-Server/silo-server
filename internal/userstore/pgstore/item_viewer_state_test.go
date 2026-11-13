package pgstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestQueueItemViewerStateMatchesSingleReads pins the batched item-detail
// read to the single-item reads it stands in for: progress with completed
// history folded in (including hidden history, which the batch filters with
// its own statement), the version choice, the three marks and the stored
// preference rows. Skips unless SILO_TEST_DATABASE_URL names a migrated
// database.
func TestQueueItemViewerStateMatchesSingleReads(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := seedTestUser(t, pool, "item-viewer-state")
	store := newStore(pool, userID)
	const profileID = "viewer"
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	seedItemViewerStates(ctx, t, pool, store, profileID, base)

	for _, item := range []string{"in-progress", "completed", "history-only", "rewatch-newer-history", "rewatch-older-history",
		"partial-history", "hidden-history", "hidden-progress", "other-profile", "marked", "nothing"} {
		for _, withProgress := range []bool{true, false} {
			q := userstore.ItemViewerStateQuery{ProfileID: profileID, MediaItemID: item,
				Progress: withProgress, VersionChoice: true, Marks: true, PlaybackPreferences: true}
			got, want := batchedItemViewerState(ctx, t, pool, store, q), singleReadItemViewerState(ctx, t, store, q)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s (progress %v):\n got %+v\nwant %+v", item, withProgress, got, want)
			}
		}
	}

	// The cases above must reach every shape, or the comparison proves little.
	state := batchedItemViewerState(ctx, t, pool, store, userstore.ItemViewerStateQuery{ProfileID: profileID, MediaItemID: "hidden-progress", Progress: true})
	if p := state.Progress; p == nil || !p.Completed || p.PositionSeconds != 0 || p.UpdatedAt != base.Add(2*time.Hour).Format(time.RFC3339) {
		t.Fatalf("hidden-progress = %+v, want only the later completed watch", p)
	}
}

// seedItemViewerStates stores, for profileID, one item per progress and
// completed-history shape (named after it), an item with every mark and
// preference row ("marked"), and rows another profile holds.
func seedItemViewerStates(ctx context.Context, t *testing.T, pool *pgxpool.Pool, store *PostgresUserStore, profileID string, base time.Time) {
	t.Helper()
	const otherProfileID = "other"
	var errs []error
	for _, id := range []string{profileID, otherProfileID} {
		errs = append(errs, store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}))
	}
	progress := func(profile, item string, position float64, completed bool, at time.Time) {
		errs = append(errs, store.SetProgressAt(ctx, profile, item, position, 7200, completed, at))
	}
	history := func(profile, item string, completed bool, at time.Time) {
		errs = append(errs, store.AddHistory(ctx, userstore.WatchHistoryEntry{
			ProfileID: profile, MediaItemID: item, WatchedAt: at.Format(time.RFC3339),
			DurationSeconds: 7200, Completed: completed, Source: userstore.WatchHistorySourceTrakt,
		}))
	}
	hide := func(item string, before time.Time) {
		_, err := pool.Exec(ctx, `INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before)
			VALUES ($1, $2, $3, $4)`, store.userID, profileID, item, before)
		errs = append(errs, err)
	}
	added := func(_ bool, err error) { errs = append(errs, err) }

	progress(profileID, "in-progress", 600, false, base)
	progress(profileID, "completed", 0, true, base)
	history(profileID, "completed", true, base.Add(time.Hour))
	history(profileID, "history-only", true, base)
	history(profileID, "history-only", true, base.Add(2*time.Hour))
	progress(profileID, "rewatch-newer-history", 900, false, base)
	history(profileID, "rewatch-newer-history", true, base.Add(time.Hour))
	progress(profileID, "rewatch-older-history", 900, false, base.Add(3*time.Hour))
	history(profileID, "rewatch-older-history", true, base)
	history(profileID, "partial-history", false, base)
	history(profileID, "hidden-history", true, base)
	hide("hidden-history", base.Add(30*time.Minute))
	progress(profileID, "hidden-progress", 300, false, base)
	history(profileID, "hidden-progress", true, base.Add(2*time.Hour))
	hide("hidden-progress", base.Add(time.Hour))
	progress(otherProfileID, "other-profile", 600, true, base)
	history(otherProfileID, "other-profile", true, base)
	added(store.AddFavorite(ctx, otherProfileID, "other-profile"))

	const marked = "marked"
	added(store.AddFavorite(ctx, profileID, marked))
	added(store.AddToWatchlist(ctx, profileID, marked))
	added(store.MarkNotInterested(ctx, profileID, marked))
	errs = append(errs,
		store.SetSeriesPlaybackPreference(ctx, userstore.SeriesPlaybackPreference{
			ProfileID: profileID, SeriesID: marked, Resolution: "2160p", HDR: true, CodecVideo: "hevc", FileID: 42, EditionKey: "directors-cut",
		}),
		store.SetAudioPreference(ctx, userstore.AudioPreference{
			ProfileID: profileID, SeriesID: marked, AudioTrackIndex: 2, AudioLanguage: "ja",
			TrackSignature: &userstore.AudioTrackSignature{Language: "ja", Codec: "aac", Channels: 2},
		}),
		store.SetSubtitlePreference(ctx, userstore.SubtitlePreference{
			ProfileID: profileID, SeriesID: marked, SubtitleLanguage: "en", SubtitleMode: "always",
			TrackSignature:      &userstore.SubtitleTrackSignature{Source: "embedded", Language: "en", Codec: "subrip", Forced: true},
			ShowForcedSubtitles: true, HasShowForcedSubtitles: true,
		}),
	)
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("seeding viewer state: %v", err)
	}
}

func batchedItemViewerState(ctx context.Context, t *testing.T, pool *pgxpool.Pool, store *PostgresUserStore, q userstore.ItemViewerStateQuery) userstore.ItemViewerState {
	t.Helper()
	var batch pgx.Batch
	var state userstore.ItemViewerState
	if !store.QueueItemViewerState(&batch, q, &state) {
		t.Fatal("QueueItemViewerState reported no support")
	}
	if err := pool.SendBatch(ctx, &batch).Close(); err != nil {
		t.Fatalf("%s: send batch: %v", q.MediaItemID, err)
	}
	return state
}

// singleReadItemViewerState reads what q asks for one method at a time.
func singleReadItemViewerState(ctx context.Context, t *testing.T, store *PostgresUserStore, q userstore.ItemViewerStateQuery) userstore.ItemViewerState {
	t.Helper()
	var want userstore.ItemViewerState
	var errs [7]error
	if q.Progress {
		want.Progress, errs[0] = userstore.GetProgressWithCompletedHistory(ctx, store, q.ProfileID, q.MediaItemID)
	}
	want.VersionChoice, errs[1] = store.GetSeriesPlaybackPreference(ctx, q.ProfileID, q.MediaItemID)
	want.IsFavorite, errs[2] = store.IsFavorite(ctx, q.ProfileID, q.MediaItemID)
	want.InWatchlist, errs[3] = store.InWatchlist(ctx, q.ProfileID, q.MediaItemID)
	notInterested, err := store.GetNotInterested(ctx, q.ProfileID, q.MediaItemID)
	want.NotInterested, errs[4] = notInterested != nil, err
	want.AudioPreference, errs[5] = store.GetAudioPreference(ctx, q.ProfileID, q.MediaItemID)
	want.SubtitlePreference, errs[6] = store.GetSubtitlePreference(ctx, q.ProfileID, q.MediaItemID)
	if err := errors.Join(errs[:]...); err != nil {
		t.Fatalf("%s: single reads: %v", q.MediaItemID, err)
	}
	return want
}
