package markers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ExternalIDs is the resolved identity for a media file at marker-fetch
// time. It bundles the external IDs needed by online providers (TMDB,
// IMDB, TVDB) with the kind-specific extras (season/episode for
// episodes). The zero value carries no IDs and signals "unresolvable".
type ExternalIDs struct {
	Kind          ItemKind
	TmdbID        string
	ImdbID        string
	TvdbID        string
	SeasonNumber  int
	EpisodeNumber int
}

// HasAnyID reports whether at least one usable external identifier is
// present. Providers should refuse to issue requests when this is false.
func (e ExternalIDs) HasAnyID() bool {
	return e.TmdbID != "" || e.ImdbID != "" || e.TvdbID != ""
}

// AsRequestMap exposes the external IDs in the Request.ExternalIDs shape.
func (e ExternalIDs) AsRequestMap() map[string]string {
	out := make(map[string]string, 3)
	if e.TmdbID != "" {
		out[ExternalIDKeyTMDB] = e.TmdbID
	}
	if e.ImdbID != "" {
		out[ExternalIDKeyIMDB] = e.ImdbID
	}
	if e.TvdbID != "" {
		out[ExternalIDKeyTVDB] = e.TvdbID
	}
	return out
}

// ExternalIDResolver maps an internal media file to the external IDs an
// online marker provider can query against. Implementations must handle
// both episodes (joined through episodes -> series) and movies (joined
// through media_items directly).
type ExternalIDResolver interface {
	ResolveForFile(ctx context.Context, file *models.MediaFile) (ExternalIDs, error)
}

// DBExternalIDResolver issues a single query per file against the postgres
// pool. Episodes pull from `episodes`; movies fall back to `media_items`
// via the file's content_id.
type DBExternalIDResolver struct {
	pool *pgxpool.Pool
}

// NewDBExternalIDResolver constructs a resolver backed by the supplied pool.
func NewDBExternalIDResolver(pool *pgxpool.Pool) *DBExternalIDResolver {
	return &DBExternalIDResolver{pool: pool}
}

// ResolveForFile fetches the external IDs for the given file. Returns the
// zero ExternalIDs and a nil error when the file cannot be resolved (e.g.
// unmatched media); callers should treat that as "no online lookup possible".
func (r *DBExternalIDResolver) ResolveForFile(ctx context.Context, file *models.MediaFile) (ExternalIDs, error) {
	if r == nil || r.pool == nil || file == nil {
		return ExternalIDs{}, nil
	}

	episodeID := strings.TrimSpace(file.EpisodeID)
	contentID := strings.TrimSpace(file.ContentID)

	if episodeID != "" {
		// TheIntroDB indexes episode markers by show + season/episode, so we
		// prefer the series-level external IDs and fall back to the episode
		// row only if the series isn't matched yet. The episode row's
		// season/episode numbers are authoritative; the media_files copy
		// can drift during multi-version moves.
		var epTmdb, epImdb, epTvdb, showTmdb, showImdb, showTvdb string
		var season, episode int
		err := r.pool.QueryRow(ctx, `
			SELECT COALESCE(NULLIF(e.tmdb_id, ''), ''),
			       COALESCE(NULLIF(e.imdb_id, ''), ''),
			       COALESCE(NULLIF(e.tvdb_id, ''), ''),
			       COALESCE(e.season_number, 0),
			       COALESCE(e.episode_number, 0),
			       COALESCE(NULLIF(mi.tmdb_id, ''), ''),
			       COALESCE(NULLIF(mi.imdb_id, ''), ''),
			       COALESCE(NULLIF(mi.tvdb_id, ''), '')
			FROM episodes e
			LEFT JOIN media_items mi ON mi.content_id = e.series_id
			WHERE e.content_id = $1`, episodeID).Scan(
			&epTmdb, &epImdb, &epTvdb, &season, &episode,
			&showTmdb, &showImdb, &showTvdb,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ExternalIDs{}, nil
			}
			return ExternalIDs{}, fmt.Errorf("resolve episode external ids: %w", err)
		}
		tmdb := showTmdb
		if tmdb == "" {
			tmdb = epTmdb
		}
		imdb := showImdb
		if imdb == "" {
			imdb = epImdb
		}
		tvdb := showTvdb
		if tvdb == "" {
			tvdb = epTvdb
		}
		if season <= 0 {
			season = file.SeasonNumber
		}
		if episode <= 0 {
			episode = file.EpisodeNumber
		}
		return ExternalIDs{
			Kind:          ItemKindEpisode,
			TmdbID:        tmdb,
			ImdbID:        imdb,
			TvdbID:        tvdb,
			SeasonNumber:  season,
			EpisodeNumber: episode,
		}, nil
	}

	if contentID == "" {
		return ExternalIDs{}, nil
	}

	var tmdb, imdb, tvdb, itemType string
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(tmdb_id, ''), ''),
		       COALESCE(NULLIF(imdb_id, ''), ''),
		       COALESCE(NULLIF(tvdb_id, ''), ''),
		       COALESCE(type, '')
		FROM media_items WHERE content_id = $1`, contentID).Scan(&tmdb, &imdb, &tvdb, &itemType)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ExternalIDs{}, nil
		}
		return ExternalIDs{}, fmt.Errorf("resolve movie external ids: %w", err)
	}
	if itemType != "movie" {
		return ExternalIDs{}, nil
	}
	return ExternalIDs{
		Kind:   ItemKindMovie,
		TmdbID: tmdb,
		ImdbID: imdb,
		TvdbID: tvdb,
	}, nil
}
