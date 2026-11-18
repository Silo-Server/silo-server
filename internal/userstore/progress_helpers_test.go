package userstore_test

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

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
