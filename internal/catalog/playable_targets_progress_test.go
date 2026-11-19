package catalog

import (
	"context"

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
