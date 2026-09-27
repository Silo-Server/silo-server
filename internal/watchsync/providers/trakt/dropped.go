package trakt

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// Trakt's "drop" hides a show from Up Next, progress, and the calendar without
// touching its history, and Trakt undrops a show on its own when the user
// watches it again. Dropped shows live in the hidden-items section "dropped";
// the older hidden sections (progress_watched, calendar) are not drops and
// are not read.

type traktDroppedShow struct {
	HiddenAt time.Time `json:"hidden_at"`
	Type     string    `json:"type"`
	Show     traktShow `json:"show"`
}

// FetchDropped reads every show the account dropped. The paged read is
// verified by fetchTraktPages, so a successful read is complete.
func (p *Provider) FetchDropped(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
) (watchsync.DroppedImportBatch, error) {
	shows, err := fetchTraktPages[traktDroppedShow](ctx, p, cfg, conn, "/users/hidden/dropped", nil)
	if err != nil {
		return watchsync.DroppedImportBatch{}, err
	}
	rows := make([]watchsync.RemoteDropped, 0, len(shows))
	for _, item := range shows {
		if item.Type != "" && item.Type != "show" {
			continue
		}
		rows = append(rows, watchsync.RemoteDropped{
			RemoteFavorite: watchsync.RemoteFavorite{
				Provider:        p.Key(),
				ProviderItemKey: showKey(item.Show.IDs),
				Kind:            historyimport.KindSeries,
				Title:           item.Show.Title,
				Year:            item.Show.Year,
				IMDbID:          item.Show.IDs.IMDb,
				TMDBID:          intString(item.Show.IDs.TMDB),
				TVDBID:          intString(item.Show.IDs.TVDB),
			},
			DroppedAt: item.HiddenAt,
		})
	}
	return watchsync.DroppedImportBatch{Rows: rows, Complete: true}, nil
}

// ExportDropped drops shows on Trakt.
func (p *Provider) ExportDropped(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	items []watchsync.LocalFavorite,
) (watchsync.ExportResult, error) {
	return p.sendIDList(ctx, "/users/hidden/dropped", "dropped shows", cfg, conn, seriesOnly(items))
}

// RemoveDropped undrops shows on Trakt.
func (p *Provider) RemoveDropped(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	items []watchsync.LocalFavorite,
) (watchsync.ExportResult, error) {
	return p.sendIDList(ctx, "/users/hidden/dropped/remove", "dropped shows", cfg, conn, seriesOnly(items))
}

func seriesOnly(items []watchsync.LocalFavorite) []watchsync.LocalFavorite {
	shows := make([]watchsync.LocalFavorite, 0, len(items))
	for _, item := range items {
		if item.Kind == historyimport.KindSeries {
			shows = append(shows, item)
		}
	}
	return shows
}
