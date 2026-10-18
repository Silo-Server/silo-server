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

// A series plays from its latest progress: that episode while unfinished, else
// the next one, else the first.
func TestPreferredPlayableTargetPlaysFromLatestProgress(t *testing.T) {
	episodes := []string{"e1", "e2", "e3", "e4", "e5"}
	done := func(at string) userstore.WatchProgress {
		return userstore.WatchProgress{Completed: true, UpdatedAt: at}
	}
	started := func(at string) userstore.WatchProgress {
		return userstore.WatchProgress{PositionSeconds: 300, UpdatedAt: at}
	}
	for _, tc := range []struct {
		name     string
		progress map[string]userstore.WatchProgress
		want     string
	}{
		{"no progress", nil, "e1"},
		{"latest completed plays the next", map[string]userstore.WatchProgress{
			"e1": done("2026-10-01T00:00:00Z"), "e3": done("2026-10-02T00:00:00Z")}, "e4"},
		{"latest unfinished resumes", map[string]userstore.WatchProgress{
			"e2": done("2026-10-01T00:00:00Z"), "e3": started("2026-10-02T00:00:00Z")}, "e3"},
		{"an older unfinished episode does not win", map[string]userstore.WatchProgress{
			"e1": started("2026-09-01T00:00:00Z"), "e3": done("2026-10-02T00:00:00Z")}, "e4"},
		{"finished series starts over", map[string]userstore.WatchProgress{
			"e5": done("2026-10-02T00:00:00Z")}, "e1"},
		{"a bulk mark-watched tie anchors on the last episode", map[string]userstore.WatchProgress{
			"e1": done("2026-10-02T00:00:00Z"), "e2": done("2026-10-02T00:00:00Z"), "e3": done("2026-10-02T00:00:00Z")}, "e4"},
		{"a zero-position unfinished row is not progress", map[string]userstore.WatchProgress{
			"e4": {UpdatedAt: "2026-10-03T00:00:00Z"}, "e1": done("2026-10-01T00:00:00Z")}, "e2"},
	} {
		if got := preferredPlayableTarget(episodes, tc.progress); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}
