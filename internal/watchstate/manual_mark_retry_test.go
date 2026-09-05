package watchstate

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// staleCompletionRead models two requests whose preflight ran before either
// transaction committed. The real store remains responsible for the decision.
type staleCompletionRead struct{ userstore.UserStore }

func (s staleCompletionRead) ListProgressByMediaItems(context.Context, string, []string) (map[string]userstore.WatchProgress, error) {
	return map[string]userstore.WatchProgress{}, nil
}

func (s staleCompletionRead) MarkWatchedBatch(ctx context.Context, profileID string, targets []userstore.MarkWatchedTarget, entries []userstore.WatchHistoryEntry) ([]userstore.WatchHistoryEntry, error) {
	return userstore.MarkWatchedBatch(ctx, s.UserStore, profileID, targets, entries)
}

type completionRecorder struct{ ids []string }

func (r *completionRecorder) HandleWatchedCompleted(_ context.Context, _ int, _ string, ids []string) {
	r.ids = append(r.ids, ids...)
}

func TestManualMarkRetryWithStalePreflightEmitsOnce(t *testing.T) {
	store, db := newTestUserStore(t)
	defer func() { _ = db.Close() }()
	observer := &completionRecorder{}
	service := NewService(testStoreProvider{store: staleCompletionRead{store}}).WithCompletionObserver(observer)
	emitted := 0
	for range 2 {
		result, err := service.RecordManualMarkWatchedWithResult(t.Context(), 1, "profile-1", []LeafWatchTarget{{MediaItemID: "movie-1", DurationSeconds: 120}}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		emitted += len(result.Entries)
	}
	if emitted != 1 || len(observer.ids) != 1 || observer.ids[0] != "movie-1" {
		t.Fatalf("provider entries = %d, completion notifications = %v; want one each", emitted, observer.ids)
	}
	history, err := store.ListHistory(t.Context(), "profile-1", 10, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %+v %v", history, err)
	}
}
