package autoscan

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Silo-Server/silo-server/internal/requests/arrclient"
)

// HistoryClient reads recently-changed file paths from a Radarr/Sonarr instance:
// newly-imported files plus renamed files (both their old and new paths), so the
// affected library folders can be rescanned.
type HistoryClient interface {
	ChangedPaths(ctx context.Context, baseURL, apiKey string, since time.Time) ([]string, error)
}

type historyRecord struct {
	EventType string `json:"eventType"`
	Data      struct {
		ImportedPath string `json:"importedPath"` // downloadFolderImported: new file
		Path         string `json:"path"`         // *FileRenamed: new path
		SourcePath   string `json:"sourcePath"`   // *FileRenamed: old path
	} `json:"data"`
}

const (
	eventImported       = "downloadFolderImported"
	eventEpisodeRenamed = "episodeFileRenamed"
	eventMovieRenamed   = "movieFileRenamed"
)

type arrHistoryClient struct {
	httpClient *http.Client
}

// NewArrHistoryClient returns a HistoryClient backed by the shared arrclient.
func NewArrHistoryClient(httpClient *http.Client) HistoryClient {
	return &arrHistoryClient{httpClient: httpClient}
}

// ChangedPaths returns file paths whose library folder should be rescanned:
// imported files, and for renames both the new and old paths (a rename can move
// a file between folders, so both parents may need scanning). Delete events are
// intentionally not handled — upgrade-deletes are already covered by the paired
// import, and standalone deletes carry no file path in arr history.
func (c *arrHistoryClient) ChangedPaths(ctx context.Context, baseURL, apiKey string, since time.Time) ([]string, error) {
	client := arrclient.New(baseURL, apiKey, c.httpClient)
	q := url.Values{}
	q.Set("date", since.UTC().Format(time.RFC3339))
	var records []historyRecord
	if err := client.GetJSON(ctx, "/api/v3/history/since?"+q.Encode(), &records); err != nil {
		return nil, fmt.Errorf("autoscan: poll history: %w", err)
	}
	var paths []string
	for _, rec := range records {
		switch rec.EventType {
		case eventImported:
			if rec.Data.ImportedPath != "" {
				paths = append(paths, rec.Data.ImportedPath)
			}
		case eventEpisodeRenamed, eventMovieRenamed:
			if rec.Data.Path != "" {
				paths = append(paths, rec.Data.Path)
			}
			if rec.Data.SourcePath != "" {
				paths = append(paths, rec.Data.SourcePath)
			}
		}
	}
	return paths, nil
}
