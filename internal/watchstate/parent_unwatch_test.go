package watchstate

import (
	"context"

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
