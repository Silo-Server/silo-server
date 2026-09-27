package downloads

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// missingFileRepo reports every lookup the way the real repository does for a
// row that does not exist.
type missingFileRepo struct{ err error }

func (r missingFileRepo) GetByID(context.Context, int) (*models.MediaFile, error) {
	return nil, r.err
}

func (r missingFileRepo) GetByContentID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, r.err
}

func (r missingFileRepo) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, r.err
}

func (r missingFileRepo) ListByEpisodeIDs(context.Context, []string) (map[string][]*models.MediaFile, error) {
	return nil, r.err
}

type stubUserRepo struct{ user *models.User }

func (r stubUserRepo) GetByID(context.Context, int) (*models.User, error) { return r.user, nil }

func serviceWithFileRepo(repo FileResolver) *Service {
	allowed := true
	return &Service{
		fileRepo:    repo,
		userRepo:    stubUserRepo{user: &models.User{ID: 7, DownloadAllowed: &allowed}},
		cfg:         config.DownloadConfig{Enabled: true},
		cfgLoadedAt: time.Now(),
	}
}

func TestResolveDirectFileTranslatesMissingFileToNotFound(t *testing.T) {
	svc := serviceWithFileRepo(missingFileRepo{err: scanner.ErrFileNotFound})

	_, err := svc.ResolveDirectFile(context.Background(), 7, 4242, "", catalog.AccessFilter{})
	if !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatalf("err = %v, want catalog.ErrItemNotFound", err)
	}
}

func TestResolveFileTranslatesMissingFileToNotFound(t *testing.T) {
	svc := serviceWithFileRepo(missingFileRepo{err: scanner.ErrFileNotFound})

	_, err := svc.resolveFile(context.Background(), CreateRequest{FileID: 4242})
	if !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatalf("err = %v, want catalog.ErrItemNotFound", err)
	}
}

func TestFileLookupKeepsRealFailuresOpaque(t *testing.T) {
	boom := errors.New("connection refused")
	svc := serviceWithFileRepo(missingFileRepo{err: boom})

	_, err := svc.ResolveDirectFile(context.Background(), 7, 4242, "", catalog.AccessFilter{})
	if errors.Is(err, catalog.ErrItemNotFound) || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the underlying transport failure", err)
	}
}
