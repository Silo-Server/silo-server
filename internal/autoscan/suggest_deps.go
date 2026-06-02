package autoscan

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/requests/arrclient"
)

// RootFolderClient lists a Radarr/Sonarr instance's configured root folder paths.
type RootFolderClient interface {
	RootFolders(ctx context.Context, baseURL, apiKey string) ([]string, error)
}

// FolderLister lists every Silo media-folder path.
type FolderLister interface {
	ListFolderPaths(ctx context.Context) ([]string, error)
}

type arrRootFolderClient struct{ httpClient *http.Client }

// NewArrRootFolderClient returns a RootFolderClient backed by the shared arrclient.
func NewArrRootFolderClient(httpClient *http.Client) RootFolderClient {
	return &arrRootFolderClient{httpClient: httpClient}
}

func (c *arrRootFolderClient) RootFolders(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	client := arrclient.New(baseURL, apiKey, c.httpClient)
	folders, err := arrclient.ListRootFolders(ctx, client)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(folders))
	for _, f := range folders {
		if f.Path != "" {
			paths = append(paths, f.Path)
		}
	}
	return paths, nil
}

type catalogFolderLister struct{ repo *catalog.FolderRepository }

// NewCatalogFolderLister adapts catalog.FolderRepository to FolderLister.
func NewCatalogFolderLister(repo *catalog.FolderRepository) FolderLister {
	return &catalogFolderLister{repo: repo}
}

func (l *catalogFolderLister) ListFolderPaths(ctx context.Context) ([]string, error) {
	folders, err := l.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, f := range folders {
		paths = append(paths, f.Paths...)
	}
	return paths, nil
}
