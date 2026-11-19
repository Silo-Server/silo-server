package metadata

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// stubPersonRepo resolves every credit to ids, or fails with err.
type stubPersonRepo struct {
	ids []int64
	err error
}

func (r stubPersonRepo) BatchFindOrCreate(_ context.Context, people []models.Person) ([]int64, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.ids[:len(people)], nil
}

func (stubPersonRepo) PhotoStates(context.Context, []int64) (map[int64]catalog.PersonPhotoState, error) {
	return nil, nil
}

func (stubPersonRepo) ClaimPhotoSources(context.Context, []catalog.PersonPhotoSourceClaim) ([]int64, error) {
	return nil, nil
}
