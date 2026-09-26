package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// Trakt rates on the integer 1 to 10 scale watchsync uses, so ratings pass
// through unchanged. Only movie and show ratings are read and written: Silo
// rates movies and series, not seasons or episodes.

type traktRatedMovie struct {
	RatedAt time.Time  `json:"rated_at"`
	Rating  int        `json:"rating"`
	Movie   traktMovie `json:"movie"`
}

type traktRatedShow struct {
	RatedAt time.Time `json:"rated_at"`
	Rating  int       `json:"rating"`
	Show    traktShow `json:"show"`
}

type traktRatingPayload struct {
	Rating  int        `json:"rating"`
	RatedAt *time.Time `json:"rated_at,omitempty"`
	IDs     traktIDs   `json:"ids"`
}

type traktRatingsPayload struct {
	Movies []traktRatingPayload `json:"movies,omitempty"`
	Shows  []traktRatingPayload `json:"shows,omitempty"`
}

// FetchRatings reads every movie and show rating. Both lists are complete, so a
// title absent from them is unrated on Trakt.
func (p *Provider) FetchRatings(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
) (watchsync.RatingImportBatch, error) {
	movies, err := fetchTraktPages[traktRatedMovie](ctx, p, cfg, conn, "/sync/ratings/movies", nil)
	if err != nil {
		return watchsync.RatingImportBatch{}, err
	}
	shows, err := fetchTraktPages[traktRatedShow](ctx, p, cfg, conn, "/sync/ratings/shows", nil)
	if err != nil {
		return watchsync.RatingImportBatch{}, err
	}
	rows := make([]watchsync.RemoteRating, 0, len(movies)+len(shows))
	for _, item := range movies {
		rows = append(rows, watchsync.RemoteRating{
			RemoteFavorite: watchsync.RemoteFavorite{
				Provider:        p.Key(),
				ProviderItemKey: movieKey(item.Movie.IDs),
				Kind:            historyimport.KindMovie,
				Title:           item.Movie.Title,
				Year:            item.Movie.Year,
				IMDbID:          item.Movie.IDs.IMDb,
				TMDBID:          intString(item.Movie.IDs.TMDB),
				TVDBID:          intString(item.Movie.IDs.TVDB),
			},
			Rating:  item.Rating,
			RatedAt: item.RatedAt,
		})
	}
	for _, item := range shows {
		rows = append(rows, watchsync.RemoteRating{
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
			Rating:  item.Rating,
			RatedAt: item.RatedAt,
		})
	}
	return watchsync.RatingImportBatch{
		Rows:          rows,
		SnapshotKinds: []string{historyimport.KindMovie, historyimport.KindSeries},
	}, nil
}

// ExportRatings sets movie and show ratings. Trakt replaces an existing rating,
// so resending one is harmless.
func (p *Provider) ExportRatings(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	items []watchsync.LocalRating,
) (watchsync.ExportResult, error) {
	var payload traktRatingsPayload
	favorites := make([]watchsync.LocalFavorite, 0, len(items))
	for _, item := range items {
		ids := favoriteIDs(item.LocalFavorite)
		if !sendableIDs(ids) || item.Rating < 1 || item.Rating > 10 {
			continue
		}
		entry := traktRatingPayload{Rating: item.Rating, IDs: ids}
		if !item.RatedAt.IsZero() {
			ratedAt := item.RatedAt.UTC()
			entry.RatedAt = &ratedAt
		}
		switch item.Kind {
		case historyimport.KindMovie:
			payload.Movies = append(payload.Movies, entry)
		case historyimport.KindSeries:
			payload.Shows = append(payload.Shows, entry)
		default:
			continue
		}
		favorites = append(favorites, item.LocalFavorite)
	}
	if len(favorites) == 0 {
		return watchsync.ExportResult{}, nil
	}
	return p.sendRatings(ctx, "/sync/ratings", cfg, conn, payload, favorites)
}

// RemoveRatings clears movie and show ratings. Trakt reports a title it does
// not know in not_found, which the caller treats as already cleared.
func (p *Provider) RemoveRatings(
	ctx context.Context,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	items []watchsync.LocalFavorite,
) (watchsync.ExportResult, error) {
	ids := buildFavoritesPayload(items)
	if len(ids.Movies) == 0 && len(ids.Shows) == 0 {
		return watchsync.ExportResult{}, nil
	}
	return p.sendRatings(ctx, "/sync/ratings/remove", cfg, conn, ids, items)
}

func (p *Provider) sendRatings(
	ctx context.Context,
	path string,
	cfg watchsync.ServerConfig,
	conn watchsync.Connection,
	payload any,
	items []watchsync.LocalFavorite,
) (watchsync.ExportResult, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return watchsync.ExportResult{}, fmt.Errorf("encode trakt ratings payload: %w", err)
	}
	// The not_found lists echo {ids} (plus the rating on a set), the same
	// shape favorites use.
	var response traktFavoritesResponse
	if err := p.do(ctx, http.MethodPost, path, cfg, conn.AccessToken, &body, &response); err != nil {
		return watchsync.ExportResult{}, err
	}
	return favoriteExportResult(items, response.NotFound), nil
}
