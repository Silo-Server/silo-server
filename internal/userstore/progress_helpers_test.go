package userstore_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestGetProgressWithCompletedHistoryCarriesHistoryTimestamp(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Profile"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if err := store.AddHistory(context.Background(), userstore.WatchHistoryEntry{
		ProfileID:       "profile-1",
		MediaItemID:     "movie-history-only",
		WatchedAt:       "2026-05-04T12:00:00Z",
		DurationSeconds: 7200,
		Completed:       true,
		Source:          userstore.WatchHistorySourceTrakt,
	}); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}

	progress, err := userstore.GetProgressWithCompletedHistory(context.Background(), store, "profile-1", "movie-history-only")
	if err != nil {
		t.Fatalf("GetProgressWithCompletedHistory: %v", err)
	}
	if progress == nil || !progress.Completed {
		t.Fatalf("progress = %+v, want synthetic completed progress", progress)
	}
	if progress.UpdatedAt != "2026-05-04T12:00:00Z" {
		t.Fatalf("UpdatedAt = %q, want history watched_at", progress.UpdatedAt)
	}
}

// priorReadCountingStore counts the prior-row reads a progress write makes.
type priorReadCountingStore struct {
	userstore.UserStore
	reads    int
	readFail error
}

func (s *priorReadCountingStore) GetProgress(ctx context.Context, profileID, mediaItemID string) (*userstore.WatchProgress, error) {
	s.reads++
	if s.readFail != nil {
		return nil, s.readFail
	}
	return s.UserStore.GetProgress(ctx, profileID, mediaItemID)
}

// UpdateProgressReportingCompletion reports only the write that flips an item
// to watched, and reads the prior row only for samples past the threshold.
func TestUpdateProgressReportingCompletion(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	base := userdb.NewSQLiteUserStore(db)
	ctx := context.Background()
	if err := base.CreateProfile(ctx, userstore.Profile{ID: "profile-1", Name: "Profile"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	store := &priorReadCountingStore{UserStore: base}

	// 3600s items with the default thresholds: 5% min-resume (180s), 90%
	// watched (3240s).
	steps := []struct {
		name          string
		item          string
		position      float64
		wantCompleted bool
		wantReads     int
	}{
		{"below the min-resume floor", "movie-1", 100, false, 0},
		{"in progress", "movie-1", 600, false, 0},
		{"crossing the watched threshold", "movie-1", 3250, true, 1},
		{"credits of a watched item", "movie-1", 3260, false, 2},
		{"rewatch in progress", "movie-1", 600, false, 2},
		{"rewatch past the threshold", "movie-1", 3300, false, 3},
		{"first sample already past the threshold", "movie-2", 3500, true, 4},
	}
	for _, step := range steps {
		completed, err := userstore.UpdateProgressReportingCompletion(ctx, store, "profile-1", step.item, step.position, 3600, userstore.ProgressThresholds{})
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if completed != step.wantCompleted || store.reads != step.wantReads {
			t.Fatalf("%s: completed = %v, prior reads = %d; want %v and %d", step.name, completed, store.reads, step.wantCompleted, step.wantReads)
		}
	}

	// Marking the item unwatched releases the latch, so the next crossing
	// completes it again.
	if err := base.ClearProgress(ctx, "profile-1", "movie-1"); err != nil {
		t.Fatalf("ClearProgress: %v", err)
	}
	completed, err := userstore.UpdateProgressReportingCompletion(ctx, store, "profile-1", "movie-1", 3300, 3600, userstore.ProgressThresholds{})
	if err != nil || !completed {
		t.Fatalf("crossing after mark unwatched: completed = %v, err = %v; want true", completed, err)
	}

	// A failed prior read still writes the sample and reports completion.
	store.readFail = errors.New("read failed")
	completed, err = userstore.UpdateProgressReportingCompletion(ctx, store, "profile-1", "movie-3", 3400, 3600, userstore.ProgressThresholds{})
	if err != nil || !completed {
		t.Fatalf("failed prior read: completed = %v, err = %v; want true and no error", completed, err)
	}
	if progress, err := base.GetProgress(ctx, "profile-1", "movie-3"); err != nil || progress == nil || !progress.Completed {
		t.Fatalf("failed prior read: stored progress = %+v, err = %v; want the completed row", progress, err)
	}
}
