package watchstate

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// batchRecordingStore answers the two reads WatchedItemIDs makes and
// records each batch size.
type batchRecordingStore struct {
	userstore.UserStore
	history, progress map[string]bool
	batches           []int
}

func (s *batchRecordingStore) LatestHistoryIDs(_ context.Context, _ string, groups map[string][]string) (map[string]string, error) {
	s.batches = append(s.batches, len(groups))
	result := map[string]string{}
	for id := range groups {
		if s.history[id] {
			result[id] = "history-" + id
		}
	}
	return result, nil
}

func (s *batchRecordingStore) ListProgressByMediaItems(_ context.Context, _ string, ids []string) (map[string]userstore.WatchProgress, error) {
	s.batches = append(s.batches, len(ids))
	result := map[string]userstore.WatchProgress{}
	for _, id := range ids {
		if s.progress[id] {
			result[id] = userstore.WatchProgress{MediaItemID: id}
		}
	}
	return result, nil
}

func TestWatchedItemIDsBatchesAndDeduplicates(t *testing.T) {
	ids := make([]string, 0, 2600)
	for i := range 2500 {
		ids = append(ids, fmt.Sprintf("ep-%d", i))
	}
	ids = append(ids, ids[:100]...) // overlapping targets repeat candidates
	store := &batchRecordingStore{
		history:  map[string]bool{"ep-5": true},
		progress: map[string]bool{"ep-2400": true},
	}

	watched, err := WatchedItemIDs(context.Background(), store, "profile-1", ids)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ep-5", "ep-2400"}; !slices.Equal(watched, want) {
		t.Fatalf("watched = %v, want %v", watched, want)
	}
	total := 0
	for _, n := range store.batches {
		if n > watchedItemIDBatch {
			t.Fatalf("batch of %d IDs, want at most %d", n, watchedItemIDBatch)
		}
		total += n
	}
	if total != 2*2500 {
		t.Fatalf("read %d IDs across both reads, want each of the 2500 unique IDs once per read", total)
	}
}

type fakeEpisodePartitioner struct {
	series, season map[string][2][]string
}

func (p fakeEpisodePartitioner) PartitionIDsBySeries(_ context.Context, id string) ([]string, []string, error) {
	sets := p.series[id]
	return sets[0], sets[1], nil
}

func (p fakeEpisodePartitioner) PartitionIDsBySeason(_ context.Context, id string) ([]string, []string, error) {
	sets := p.season[id]
	return sets[0], sets[1], nil
}

// A parent unwatch clears every episode with a file and only the file-less
// episodes the profile has history or progress on.
func TestParentUnwatchTargetsAddsWatchedFilelessEpisodes(t *testing.T) {
	store := &batchRecordingStore{
		history:  map[string]bool{"deleted-history": true},
		progress: map[string]bool{"deleted-progress": true},
	}
	service := NewService(testStoreProvider{store: store})
	sets := [2][]string{{"on-disk-1", "on-disk-2"}, {"deleted-history", "metadata-only", "deleted-progress"}}
	episodes := fakeEpisodePartitioner{
		series: map[string][2][]string{"series-1": sets},
		season: map[string][2][]string{"season-1": sets},
	}
	want := []string{"on-disk-1", "on-disk-2", "deleted-history", "deleted-progress"}
	for _, parent := range []struct{ kind, id string }{{ParentSeries, "series-1"}, {ParentSeason, "season-1"}} {
		got, err := service.ParentUnwatchTargets(context.Background(), 1, "profile-1", episodes, parent.kind, parent.id)
		if err != nil {
			t.Fatalf("%s: %v", parent.kind, err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s targets = %v, want %v", parent.kind, got, want)
		}
	}
	if _, err := service.ParentUnwatchTargets(context.Background(), 1, "profile-1", episodes, "movie", "movie-1"); err == nil {
		t.Fatal("a movie parent expanded; want an error")
	}
}
