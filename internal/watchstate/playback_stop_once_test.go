package watchstate

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestRecordPlaybackStopOnceRecordsAPlayOnce covers a play several stops
// report: the first stop records it, a stop that completes it upgrades the
// same row, a further repeat changes nothing, and no stop writes progress.
func TestRecordPlaybackStopOnceRecordsAPlayOnce(t *testing.T) {
	const historyID = "4b1e9d2c-7a35-5f60-8c14-2d9e7f0b3a61"
	store, db := newTestUserStore(t)
	defer func() { _ = db.Close() }()
	createWatchstateProfile(t, store)
	observer := &completionRecorder{}
	service := NewService(testStoreProvider{store: store}).WithCompletionObserver(observer)
	stop := func(position float64) PlaybackStopResult {
		t.Helper()
		result, err := service.RecordPlaybackStopOnce(t.Context(), 1, "profile-1", "movie-1", 3600, position,
			time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC), userstore.VersionHints{}, userstore.ProgressThresholds{}, historyID)
		if err != nil {
			t.Fatalf("stop at %v: %v", position, err)
		}
		return result
	}

	if result := stop(1800); result.AlreadyRecorded || result.Completed {
		t.Fatalf("first stop = %+v, want a newly recorded, incomplete play", result)
	}
	if result := stop(3500); result.AlreadyRecorded || !result.Completed {
		t.Fatalf("completing stop = %+v, want the play completed", result)
	}
	if result := stop(3500); !result.AlreadyRecorded {
		t.Fatalf("repeated stop = %+v, want AlreadyRecorded", result)
	}

	history, err := store.ListHistory(t.Context(), "profile-1", 10, 0)
	if err != nil || len(history) != 1 || history[0].ID != historyID || !history[0].Completed {
		t.Fatalf("history = %+v, %v; want the one completed row %s", history, err, historyID)
	}
	if len(observer.ids) != 1 {
		t.Fatalf("completion notifications = %v, want one", observer.ids)
	}
	if progress, err := store.GetProgress(t.Context(), "profile-1", "movie-1"); err != nil || progress != nil {
		t.Fatalf("progress = %+v, %v; want none written by the stops", progress, err)
	}
}

// TestRecordPlaybackStopOnceLeavesANewerPlaysHints covers a stale copy of a
// recorded play finalized after the viewer started another version of the
// same title: it must not replace the newer play's version hints.
func TestRecordPlaybackStopOnceLeavesANewerPlaysHints(t *testing.T) {
	const historyID = "9c2d4e6f-1a3b-5c7d-8e9f-0a1b2c3d4e5f"
	store, db := newTestUserStore(t)
	defer func() { _ = db.Close() }()
	createWatchstateProfile(t, store)
	service := NewService(testStoreProvider{store: store})
	stop := func(fileID int) PlaybackStopResult {
		t.Helper()
		result, err := service.RecordPlaybackStopOnce(t.Context(), 1, "profile-1", "movie-1", 3600, 3500,
			time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC), userstore.VersionHints{FileID: fileID}, userstore.ProgressThresholds{}, historyID)
		if err != nil {
			t.Fatalf("stop with file %d: %v", fileID, err)
		}
		return result
	}

	if err := store.UpdateProgress(t.Context(), "profile-1", "movie-1", 3500, 3600, userstore.ProgressThresholds{}); err != nil {
		t.Fatal(err)
	}
	stop(1)
	// The viewer starts the title's other version.
	if err := store.UpdateProgressHints(t.Context(), "profile-1", "movie-1", userstore.VersionHints{FileID: 2}); err != nil {
		t.Fatal(err)
	}
	if result := stop(1); !result.AlreadyRecorded {
		t.Fatalf("stale stop = %+v, want AlreadyRecorded", result)
	}

	progress, err := store.GetProgress(t.Context(), "profile-1", "movie-1")
	if err != nil || progress == nil || progress.LastFileID == nil || *progress.LastFileID != 2 {
		t.Fatalf("progress = %+v, %v; want the newer play's file 2", progress, err)
	}
}
