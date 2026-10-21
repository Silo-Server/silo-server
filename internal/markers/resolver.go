package markers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
	// Episode names the provider episode records the catalog matched at
	// SeasonNumber/EpisodeNumber. It is part of the lookup identity because
	// it can change which provider record a lookup addresses; it is omitted
	// when empty, so a movie's identity predates the field.
	Episode EpisodeIDs `json:",omitzero"`
	// EpisodeNumbering is the external ID key ("tvdb" or "tmdb") of the
	// provider whose numbering SeasonNumber/EpisodeNumber follow: the first in
	// the library's episode chain that supplied this episode. Empty when no
	// such provider supplied it.
	EpisodeNumbering string `json:",omitempty"`
	// EpisodeTitle is a hint providers may use to corroborate the episode.
	// A retitled episode is still the same record, so it is not part of the
	// lookup identity.
	EpisodeTitle string `json:"-"`
	// Released is the episode air date or movie release date, when known.
	// It sets cache freshness and is not part of the lookup identity.
	Released time.Time `json:"-"`
}

// EpisodeIDs holds an episode's own provider IDs, as opposed to its series'.
type EpisodeIDs struct {
	TmdbID string `json:",omitempty"`
	ImdbID string `json:",omitempty"`
	TvdbID string `json:",omitempty"`
}

// HasAnyID reports whether at least one usable external identifier is
// present. Providers should refuse to issue requests when this is false.
func (e ExternalIDs) HasAnyID() bool {
	return e.TmdbID != "" || e.ImdbID != "" || e.TvdbID != ""
}

// AsRequestMap exposes the external IDs in the Request.ExternalIDs shape.
func (e ExternalIDs) AsRequestMap() map[string]string {
	return requestIDMap(e.TmdbID, e.ImdbID, e.TvdbID)
}

// AsRequestMap exposes the episode IDs in the Request.EpisodeExternalIDs
// shape.
func (e EpisodeIDs) AsRequestMap() map[string]string {
	return requestIDMap(e.TmdbID, e.ImdbID, e.TvdbID)
}

func requestIDMap(tmdbID, imdbID, tvdbID string) map[string]string {
	out := make(map[string]string, 3)
	if tmdbID != "" {
		out[ExternalIDKeyTMDB] = tmdbID
	}
	if imdbID != "" {
		out[ExternalIDKeyIMDB] = imdbID
	}
	if tvdbID != "" {
		out[ExternalIDKeyTVDB] = tvdbID
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
		// Marker providers index episodes by show + season/episode, so the
		// series IDs are the identity. The episode's own IDs and title only
		// refine it: they cannot substitute for an unmatched show. The episode
		// row's season/episode numbers are authoritative; the media_files copy
		// can drift during multi-version moves.
		//
		// Episode rows merge metadata providers' episodes by number, in the
		// order of the library's episode chain, so the first provider in that
		// chain whose ID the row carries is the one whose numbering it
		// follows. The legacy flat chain applies when the library has no
		// per-level chain, as metadata.ChainRepository.GetChain does.
		var showTmdb, showImdb, showTvdb string
		var episodeIDs EpisodeIDs
		var title, numbering string
		var season, episode int
		var aired *time.Time
		err := r.pool.QueryRow(ctx, `
			SELECT COALESCE(e.season_number, 0),
			       COALESCE(e.episode_number, 0),
			       COALESCE(NULLIF(mi.tmdb_id, ''), ''),
			       COALESCE(NULLIF(mi.imdb_id, ''), ''),
			       COALESCE(NULLIF(mi.tvdb_id, ''), ''),
			       COALESCE(BTRIM(e.tmdb_id), ''),
			       COALESCE(BTRIM(e.imdb_id), ''),
			       COALESCE(BTRIM(e.tvdb_id), ''),
			       COALESCE(BTRIM(e.title), ''),
			       e.air_date,
			       COALESCE((
			           SELECT c.capability_id
			           FROM library_provider_chains c
			           JOIN plugin_installations pi ON pi.id = c.plugin_installation_id
			           WHERE c.media_folder_id = (SELECT media_folder_id FROM media_files WHERE id = $2)
			             AND c.capability_type = 'metadata_provider.v1'
			             AND c.enabled AND pi.enabled
			             AND c.content_level = CASE WHEN EXISTS (
			                     SELECT 1 FROM library_provider_chains l
			                     WHERE l.media_folder_id = c.media_folder_id AND l.content_level = 'episode'
			                 ) THEN 'episode' ELSE '' END
			             AND ((c.capability_id = 'tvdb' AND BTRIM(e.tvdb_id) <> '')
			               OR (c.capability_id = 'tmdb' AND BTRIM(e.tmdb_id) <> ''))
			           ORDER BY c.priority
			           LIMIT 1
			       ), '')
			FROM episodes e
			LEFT JOIN media_items mi ON mi.content_id = e.series_id
			WHERE e.content_id = $1`, episodeID, file.ID).Scan(
			&season, &episode,
			&showTmdb, &showImdb, &showTvdb,
			&episodeIDs.TmdbID, &episodeIDs.ImdbID, &episodeIDs.TvdbID, &title,
			&aired, &numbering,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ExternalIDs{}, nil
			}
			return ExternalIDs{}, fmt.Errorf("resolve episode external ids: %w", err)
		}
		if season < 0 {
			season = file.SeasonNumber
		}
		if episode <= 0 {
			episode = file.EpisodeNumber
		}
		return ExternalIDs{
			Kind:             ItemKindEpisode,
			TmdbID:           showTmdb,
			ImdbID:           showImdb,
			TvdbID:           showTvdb,
			SeasonNumber:     season,
			EpisodeNumber:    episode,
			Episode:          episodeIDs,
			EpisodeNumbering: numbering,
			EpisodeTitle:     title,
			Released:         dateOrZero(aired),
		}, nil
	}

	if contentID == "" {
		return ExternalIDs{}, nil
	}

	var tmdb, imdb, tvdb, itemType string
	var released *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(tmdb_id, ''), ''),
		       COALESCE(NULLIF(imdb_id, ''), ''),
		       COALESCE(NULLIF(tvdb_id, ''), ''),
		       COALESCE(type, ''),
		       release_date
		FROM media_items WHERE content_id = $1`, contentID).Scan(&tmdb, &imdb, &tvdb, &itemType, &released)
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
		Kind:     ItemKindMovie,
		TmdbID:   tmdb,
		ImdbID:   imdb,
		TvdbID:   tvdb,
		Released: dateOrZero(released),
	}, nil
}

func dateOrZero(date *time.Time) time.Time {
	if date == nil {
		return time.Time{}
	}
	return *date
}
