package chatgpt

import (
	"context"
)

type cancelAfterCommitStore struct {
	*memoryStore
	cancel context.CancelFunc
}

func (s cancelAfterCommitStore) UpdateValueAndSettingsAtomic(ctx context.Context, key string, update func(map[string]string) (map[string]string, error)) error {
	err := s.memoryStore.UpdateValueAndSettingsAtomic(ctx, key, update)
	if err == nil {
		s.cancel()
	}
	return err
}
