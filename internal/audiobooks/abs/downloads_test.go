package abs

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/models"
)

// downloadAccounts backs newDownloadService: every account is an enabled
// user who may download unless denied names it.
type downloadAccounts struct {
	denied map[int]bool
}

func (a *downloadAccounts) GetByID(_ context.Context, id int) (*models.User, error) {
	allowed := !a.denied[id]
	return &models.User{ID: id, Role: models.RoleUser, Enabled: true, DownloadAllowed: &allowed}, nil
}

type downloadFiles struct {
	downloads.FileResolver
	byID map[int]*models.MediaFile
}

func (f downloadFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	return f.byID[id], nil
}

type allowAllItems struct{}

func (allowAllItems) EnsureAccessible(context.Context, string, catalog.AccessFilter) error {
	return nil
}

func (allowAllItems) EnsureAccessibleIDs(_ context.Context, ids []string, _ catalog.AccessFilter) (map[string]bool, error) {
	ok := make(map[string]bool, len(ids))
	for _, id := range ids {
		ok[id] = true
	}
	return ok, nil
}

// newDownloadService builds the real download service, so ABS tests apply the
// native download rules rather than a fake's. enabled is the server's Allow
// downloads setting.
func newDownloadService(enabled bool, accounts *downloadAccounts, files ...*models.MediaFile) *downloads.Service {
	byID := map[int]*models.MediaFile{}
	for _, f := range files {
		byID[f.ID] = f
	}
	return downloads.NewService(nil, nil, nil, downloadFiles{byID: byID}, nil, nil, accounts, allowAllItems{}, nil,
		&config.DownloadConfig{Enabled: enabled})
}
