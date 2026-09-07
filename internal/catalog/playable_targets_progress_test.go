package catalog

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type recordingProgressStore struct {
	batchSizes []int
	ids        []string
	delegate   PlayableTargetProgressStore
}

func (s *recordingProgressStore) ListProgressByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]userstore.WatchProgress, error) {
	s.batchSizes = append(s.batchSizes, len(mediaItemIDs))
	s.ids = append(s.ids, mediaItemIDs...)
	if s.delegate != nil {
		return s.delegate.ListProgressByMediaItems(ctx, profileID, mediaItemIDs)
	}
	result := make(map[string]userstore.WatchProgress, len(mediaItemIDs))
	for _, id := range mediaItemIDs {
		result[id] = userstore.WatchProgress{MediaItemID: id, PositionSeconds: 1}
	}
	return result, nil
}

func TestPlayableTargetProgressIDsOnlyLoadsAmbiguousUnhintedCards(t *testing.T) {
	candidates := map[string][]string{
		"movie":        {"movie"},
		"empty":        nil,
		"one-episode":  {"episode-1"},
		"hinted":       {"episode-1", "episode-2", "hinted-only"},
		"series":       {"episode-1", "episode-2"},
		"same-series":  {"episode-1", "episode-2"},
		"invalid-hint": {"episode-3", "episode-4"},
	}
	ids := playableTargetProgressIDs(candidates, map[string]string{"hinted": "episode-2"})
	slices.Sort(ids)
	want := []string{"episode-1", "episode-2", "episode-3", "episode-4"}
	if !slices.Equal(ids, want) {
		t.Fatalf("progress IDs = %v, want %v", ids, want)
	}
}

// The PostgreSQL store binds one parameter per ID, so a page of long-running
// series must be split rather than sent as one oversized statement.
func TestListPlayableTargetProgressBatchesAndMerges(t *testing.T) {
	ids := make([]string, playableTargetProgressBatchSize*2+7)
	for i := range ids {
		ids[i] = fmt.Sprintf("episode-%d", i)
	}
	store := &recordingProgressStore{}

	progress, err := listPlayableTargetProgress(context.Background(), store, "profile", ids)
	if err != nil {
		t.Fatalf("list progress: %v", err)
	}
	if len(progress) != len(ids) {
		t.Fatalf("progress entries = %d, want %d", len(progress), len(ids))
	}
	wantBatches := []int{playableTargetProgressBatchSize, playableTargetProgressBatchSize, 7}
	if fmt.Sprint(store.batchSizes) != fmt.Sprint(wantBatches) {
		t.Fatalf("batch sizes = %v, want %v", store.batchSizes, wantBatches)
	}
	for _, id := range []string{ids[0], ids[playableTargetProgressBatchSize], ids[len(ids)-1]} {
		if progress[id].MediaItemID != id {
			t.Fatalf("missing merged progress for %s", id)
		}
	}
}
