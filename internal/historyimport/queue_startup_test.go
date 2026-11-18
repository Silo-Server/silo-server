package historyimport

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type startupHistoryStore struct {
	userstore.UserStore
	entries chan userstore.WatchHistoryEntry
}

func (s startupHistoryStore) SetProgressIfNewer(context.Context, string, string, float64, float64, bool, time.Time) (bool, error) {
	return true, nil
}
func (s startupHistoryStore) AddHistoryIfMissing(_ context.Context, entry userstore.WatchHistoryEntry) (bool, error) {
	s.entries <- entry
	return true, nil
}

type startupIdentityItems struct{}

func (startupIdentityItems) GetByID(context.Context, string) (*models.MediaItem, error) {
	return &models.MediaItem{ContentID: "movie", Type: "movie"}, nil
}

type startupIdentityProviders struct{}

func (startupIdentityProviders) GetByContentID(context.Context, string) ([]*models.MediaItemProviderID, error) {
	return []*models.MediaItemProviderID{{ContentID: "movie", ItemType: "movie", Provider: "imdb", ProviderID: "tt1234567"}}, nil
}
func (startupIdentityProviders) FindContentIDByProviderIDs(context.Context, map[string]string, string, string) (string, error) {
	return "movie", nil
}
