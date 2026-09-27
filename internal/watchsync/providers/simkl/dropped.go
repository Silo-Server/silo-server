package simkl

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// Simkl keeps a dropped show in its "dropped" status list. Moving a show to
// another status undrops it; neither move touches its watch history. Simkl
// records no time for the move, so dropped rows carry no drop time.
const (
	simklCursorDroppedShows = "simkl.dropped.shows"
	simklCursorDroppedAnime = "simkl.dropped.anime"
)

// FetchDropped reads the dropped shows and anime. The read is skipped, and
// reported incomplete, while neither list's activity changed since the last
// read. The activity checked is each list's "all" stamp rather than its
// "dropped" one, so a show moved out of the dropped list is never missed.
func (p *Provider) FetchDropped(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection) (watchsync.DroppedImportBatch, error) {
	activities, err := p.fetchActivities(ctx, cfg, conn)
	if err != nil {
		return watchsync.DroppedImportBatch{}, err
	}
	cursors := map[string]string{
		simklCursorDroppedShows: activities.TVShows.All,
		simklCursorDroppedAnime: activities.Anime.All,
	}
	if shouldSkipSimklBucket(conn.SyncCursors[simklCursorDroppedShows], activities.TVShows.All) &&
		shouldSkipSimklBucket(conn.SyncCursors[simklCursorDroppedAnime], activities.Anime.All) {
		return watchsync.DroppedImportBatch{}, nil
	}

	var rows []watchsync.RemoteDropped
	for _, path := range []string{
		"/sync/all-items/shows/dropped?extended=full",
		"/sync/all-items/anime/dropped?extended=full",
	} {
		var payload simklAllItemsResponse
		if err := p.do(ctx, http.MethodGet, path, cfg, conn.AccessToken, nil, &payload); err != nil {
			return watchsync.DroppedImportBatch{}, err
		}
		for _, show := range append(payload.Shows, payload.Anime...) {
			key := showKey(show.Show.IDs)
			if key == "" {
				continue
			}
			rows = append(rows, watchsync.RemoteDropped{RemoteFavorite: watchsync.RemoteFavorite{
				Provider:        p.Key(),
				ProviderItemKey: key,
				Kind:            historyimport.KindSeries,
				Title:           show.Show.Title,
				Year:            show.Show.Year,
				IMDbID:          show.Show.IDs.IMDb,
				TMDBID:          intString(show.Show.IDs.TMDB),
				TVDBID:          intString(show.Show.IDs.TVDB),
			}})
		}
	}
	return watchsync.DroppedImportBatch{Rows: rows, Complete: true, UpdatedCursors: cursors}, nil
}

// ExportDropped moves shows to Simkl's dropped list.
func (p *Provider) ExportDropped(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, items []watchsync.LocalFavorite) (watchsync.ExportResult, error) {
	return p.sendListChange(ctx, "/sync/add-to-list", cfg, conn, seriesOnly(items), "dropped")
}

// RemoveDropped moves shows back to watching, which undrops them without
// changing their history.
func (p *Provider) RemoveDropped(ctx context.Context, cfg watchsync.ServerConfig, conn watchsync.Connection, items []watchsync.LocalFavorite) (watchsync.ExportResult, error) {
	return p.sendListChange(ctx, "/sync/add-to-list", cfg, conn, seriesOnly(items), "watching")
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
