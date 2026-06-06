package autoscan

import (
	"context"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/requests/arrclient"
)

// rootFolderTimeout is generous: Radarr/Sonarr compute unmappedFolders by
// scanning every root folder (often slow network shares), so a large library's
// /api/v3/rootfolder can take 20-30s+ — well past arrclient's 30s default.
const rootFolderTimeout = 2 * time.Minute

type arrRootFolderClient struct{ httpClient *http.Client }

// NewArrRootFolderClient returns a RootFolderClient backed by the shared arrclient.
// When no client is given it uses a long timeout, since the root-folder scan is slow.
func NewArrRootFolderClient(httpClient *http.Client) RootFolderClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: rootFolderTimeout}
	}
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

// statusTimeout bounds the connection-test probe: /api/v3/system/status is a
// cheap call, so a short timeout keeps a misconfigured/unreachable target from
// hanging the request.
const statusTimeout = 10 * time.Second

// SystemStatus probes the arr /api/v3/system/status endpoint and returns the
// reported version. It satisfies ArrStatusProbe. A dedicated short-timeout
// client is used (the suggester's long root-folder timeout is inappropriate for
// a liveness probe).
func (c *arrRootFolderClient) SystemStatus(ctx context.Context, baseURL, apiKey string) (string, error) {
	httpClient := &http.Client{Timeout: statusTimeout}
	client := arrclient.New(baseURL, apiKey, httpClient)
	var status struct {
		Version string `json:"version"`
	}
	if err := client.GetJSON(ctx, "/api/v3/system/status", &status); err != nil {
		return "", err
	}
	return status.Version, nil
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
