package api

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/apiv2"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/watchlist"
)

// watchlistSyncRequests applies watchlist requests to the entries watch
// provider sync adds and removes, through the same request service calls as
// the watchlist title add and remove operations. The request service applies
// its own gates: requests on, watchlist requests on, the profile's opt-out,
// quotas and blocks.
type watchlistSyncRequests struct {
	requests syncRequestService
}

// syncRequestService is the slice of *requests.Service the sync uses.
type syncRequestService interface {
	apiv2.WatchlistRequestService
	RequestQueuedWatchlistTitles(ctx context.Context, viewer mediarequests.Viewer, titles []mediarequests.WatchlistTitle) error
}

var _ watchlist.SyncRequests = watchlistSyncRequests{}

func (r watchlistSyncRequests) RequestWatchlistTitle(ctx context.Context, userID int, profileID string, title watchlist.Title) {
	viewer := mediarequests.Viewer{UserID: userID, ProfileID: profileID}
	_, err := r.requests.RequestFromWatchlist(ctx, viewer, syncRequestTitle(title))
	if err != nil && !errors.Is(err, mediarequests.ErrRequestsDisabled) {
		slog.WarnContext(ctx, "watchlist request for a synced title failed", "component", "watchsync",
			"user_id", userID, "profile_id", profileID, "media_type", title.MediaType, "tmdb_id", title.TMDBID, "error", err)
	}
}

func (r watchlistSyncRequests) WithdrawWatchlistTitle(ctx context.Context, userID int, profileID string, mediaType string, tmdbIDs []int) {
	viewer := mediarequests.Viewer{UserID: userID, ProfileID: profileID}
	for _, id := range tmdbIDs {
		if err := r.requests.WithdrawWatchlistRequest(ctx, viewer, mediarequests.MediaType(mediaType), id); err != nil {
			slog.WarnContext(ctx, "withdrawing the watchlist request of a synced title failed", "component", "watchsync",
				"user_id", userID, "profile_id", profileID, "media_type", mediaType, "tmdb_id", id, "error", err)
		}
	}
}

func (r watchlistSyncRequests) RequestQueuedWatchlistTitles(ctx context.Context, userID int, profileID string, titles []watchlist.Title) {
	queued := make([]mediarequests.WatchlistTitle, len(titles))
	for i, title := range titles {
		queued[i] = syncRequestTitle(title)
	}
	viewer := mediarequests.Viewer{UserID: userID, ProfileID: profileID}
	if err := r.requests.RequestQueuedWatchlistTitles(ctx, viewer, queued); err != nil && !errors.Is(err, mediarequests.ErrRequestsDisabled) {
		slog.WarnContext(ctx, "watchlist requests for queued synced titles failed", "component", "watchsync",
			"user_id", userID, "profile_id", profileID, "error", err)
	}
}

func syncRequestTitle(title watchlist.Title) mediarequests.WatchlistTitle {
	return mediarequests.WatchlistTitle{
		MediaType: mediarequests.MediaType(title.MediaType), TMDBID: title.TMDBID, IMDbID: title.IMDbID, TVDBID: title.TVDBID,
		Title: title.Title, Year: title.Year, PosterPath: title.PosterPath, FormerTMDBIDs: title.FormerTMDBIDs,
	}
}
