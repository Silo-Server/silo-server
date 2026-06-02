package autoscan

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Silo-Server/silo-server/internal/requests/arrclient"
)

// HistoryClient reads recently-imported file paths from a Radarr/Sonarr instance.
type HistoryClient interface {
	ImportedPaths(ctx context.Context, baseURL, apiKey string, since time.Time) ([]string, error)
}

type historyRecord struct {
	EventType string `json:"eventType"`
	Data      struct {
		ImportedPath string `json:"importedPath"`
	} `json:"data"`
}

const importedEventType = "downloadFolderImported"

type arrHistoryClient struct {
	httpClient *http.Client
}

// NewArrHistoryClient returns a HistoryClient backed by the shared arrclient.
func NewArrHistoryClient(httpClient *http.Client) HistoryClient {
	return &arrHistoryClient{httpClient: httpClient}
}

func (c *arrHistoryClient) ImportedPaths(ctx context.Context, baseURL, apiKey string, since time.Time) ([]string, error) {
	client := arrclient.New(baseURL, apiKey, c.httpClient)
	q := url.Values{}
	q.Set("date", since.UTC().Format(time.RFC3339))
	var records []historyRecord
	if err := client.GetJSON(ctx, "/api/v3/history/since?"+q.Encode(), &records); err != nil {
		return nil, fmt.Errorf("autoscan: poll history: %w", err)
	}
	var paths []string
	for _, rec := range records {
		if rec.EventType != importedEventType {
			continue
		}
		if rec.Data.ImportedPath != "" {
			paths = append(paths, rec.Data.ImportedPath)
		}
	}
	return paths, nil
}
