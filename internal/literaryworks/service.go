package literaryworks

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) GetWork(ctx context.Context, workID string, filter catalog.AccessFilter) (*DetailResponse, error) {
	return nil, ErrWorkNotFound
}
