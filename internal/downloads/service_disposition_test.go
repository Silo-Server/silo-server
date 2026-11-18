package downloads

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
)

type namingItems map[string]*models.MediaItem

func (n namingItems) GetByID(_ context.Context, id string) (*models.MediaItem, error) {
	return n[id], nil
}

type namingEpisodes struct {
	EpisodeResolver
	episode *models.Episode
}

func (n namingEpisodes) GetByID(context.Context, string) (*models.Episode, error) {
	return n.episode, nil
}
