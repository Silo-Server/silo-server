package pgstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func TestJellycompatProgressExplicitHistoricalEdit(t *testing.T) {
	pool, userID := newConstraintTestUser(t)
	store := newStore(pool, userID)
	date := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
	var previousSequence int64
	var previousWrite time.Time
	for _, played := range []bool{true, false} {
		if err := store.ApplyJellycompatProgress(t.Context(), "compat-profile", userstore.JellycompatProgressEdit{MediaItemID: "compat-item", PositionSeconds: 123, DurationSeconds: 600, Completed: played, EventAt: date}); err != nil {
			t.Fatalf("set explicit progress played=%v: %v", played, err)
		}
		progress, err := store.GetProgress(t.Context(), "compat-profile", "compat-item")
		if err != nil || progress == nil || progress.PositionSeconds != 123 || progress.Completed != played {
			t.Fatalf("progress=%+v err=%v", progress, err)
		}
		var sequence int64
		var writeTime time.Time
		if err := pool.QueryRow(t.Context(), "SELECT synced_seq, updated_at FROM user_watch_progress WHERE user_id=$1 AND profile_id=$2 AND media_item_id=$3", userID, "compat-profile", "compat-item").Scan(&sequence, &writeTime); err != nil {
			t.Fatal(err)
		}
		if sequence <= previousSequence {
			t.Fatalf("explicit edit did not advance sync cursor: previous=%d next=%d", previousSequence, sequence)
		}
		previousSequence = sequence
		if !writeTime.After(previousWrite) {
			t.Fatalf("edit timestamp did not advance: previous=%v next=%v", previousWrite, writeTime)
		}
		previousWrite = writeTime
		dates, err := store.ListJellycompatProgressDates(t.Context(), "compat-profile", []string{"compat-item"})
		if err != nil || dates["compat-item"] != date.Format(time.RFC3339Nano) {
			t.Fatalf("dates=%+v err=%v", dates, err)
		}
	}
	// Keep a progress row behind a later visibility watermark, then explicitly
	// edit it again using the identical historical event time.
	if _, err := pool.Exec(t.Context(), `INSERT INTO user_history_hidden_items(user_id,profile_id,media_item_id,hidden_before,updated_at) VALUES($1,'compat-profile','compat-item',$2,$2)`, userID, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if progress, err := store.GetProgress(t.Context(), "compat-profile", "compat-item"); err != nil || progress != nil {
		t.Fatalf("expected hidden progress: %+v %v", progress, err)
	}
	if err := store.ApplyJellycompatProgress(t.Context(), "compat-profile", userstore.JellycompatProgressEdit{MediaItemID: "compat-item", PositionSeconds: 222, DurationSeconds: 600, EventAt: date}); err != nil {
		t.Fatal(err)
	}
	if progress, err := store.GetProgress(t.Context(), "compat-profile", "compat-item"); err != nil || progress == nil || progress.PositionSeconds != 222 {
		t.Fatalf("explicit edit remained hidden: %+v %v", progress, err)
	}
	dates, err := store.ListJellycompatProgressDates(t.Context(), "compat-profile", []string{"compat-item"})
	if err != nil || dates["compat-item"] != date.Format(time.RFC3339Nano) {
		t.Fatalf("historical date lost: %+v %v", dates, err)
	}
	// A subsequent unmarked legacy write still advances event_at. The marker
	// must not leak through the connection pool beyond the committed transaction.
	if _, err := pool.Exec(t.Context(), `UPDATE user_watch_progress SET updated_at=updated_at+interval '1 second' WHERE user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	var matches bool
	if err := pool.QueryRow(t.Context(), `SELECT event_at=updated_at FROM user_watch_progress WHERE user_id=$1`, userID).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if !matches {
		t.Fatal("explicit-date marker leaked into legacy write")
	}

}

func TestDatedMarkWatchedBatchAtomic(t *testing.T) {
	pool, userID := newConstraintTestUser(t)
	storetest.RunDatedMarkWatchedBatch(t, newStore(pool, userID))
}

func TestAtomicJellycompatProgressHistoryRollback(t *testing.T) {
	pool, userID := newConstraintTestUser(t)
	constraint := fmt.Sprintf("test_progress_failure_%d", userID)
	if _, err := pool.Exec(t.Context(), fmt.Sprintf("ALTER TABLE user_watch_progress ADD CONSTRAINT %s CHECK (user_id <> %d OR position_seconds <> 321) NOT VALID", constraint, userID)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "ALTER TABLE user_watch_progress DROP CONSTRAINT "+constraint)
	})
	storetest.RunAtomicJellycompatProgress(t, newStore(pool, userID))
}
