package jellycompat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type compatEpisodeTarget struct {
	Item         upstreamListItem
	SeriesImages seriesImageSet
}

type libraryMembershipChecker interface {
	GetItemsInLibrary(ctx context.Context, contentIDs []string, libraryID int) (map[string]bool, error)
}

// itemRepoForBatchLoader is the subset of *catalog.ItemRepository used by
// ItemsHandler. Defined as an interface so tests can substitute a counting
// fake without standing up a Postgres pool. The compatPool() == nil fallback
// uses GetByIDsWithAccess to push library/rating gating into a single SQL
// statement instead of a per-item EnsureAccessible loop (audit 2026-05-01 §3.3).
type itemRepoForBatchLoader interface {
	GetByIDs(ctx context.Context, contentIDs []string) ([]*models.MediaItem, error)
	GetByIDsWithAccess(ctx context.Context, contentIDs []string, access catalog.AccessFilter) ([]*models.MediaItem, error)
	GetItemsInLibrary(ctx context.Context, contentIDs []string, libraryID int) (map[string]bool, error)
}

// episodeRepoForBatchLoader is the subset of *catalog.EpisodeRepository used
// by ItemsHandler. Episode access in the compatPool() == nil fallback is
// gated through the parent series item via GetByIDsWithAccess on media_items.
type episodeRepoForBatchLoader interface {
	GetByIDs(ctx context.Context, contentIDs []string) ([]*models.Episode, error)
	ListBySeason(ctx context.Context, seriesID string, seasonNum int) ([]*models.Episode, error)
	ListBySeries(ctx context.Context, seriesID string) ([]*models.Episode, error)
}

func (h *ItemsHandler) fetchCompatItemsByContentIDs(ctx context.Context, session *Session, contentIDs []string, libraryID *int) (map[string]upstreamListItem, error) {
	normalized := normalizeContentIDs(contentIDs)
	if len(normalized) == 0 {
		return map[string]upstreamListItem{}, nil
	}

	pool := h.compatPool()
	if pool == nil {
		return h.fetchCompatItemsByContentIDsFallback(ctx, session, normalized, libraryID)
	}

	access := h.resolveAccessFilter(ctx, session)
	fromClause := "media_items mi"
	conditions := []string{"mi.content_id = ANY($1)"}
	args := []any{normalized}
	argIdx := 2

	if libraryID != nil || access.AllowedLibraryIDs != nil || len(access.DisabledLibraryIDs) > 0 {
		fromClause = "media_items mi JOIN media_item_libraries mil ON mi.content_id = mil.content_id"
	}
	if libraryID != nil {
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = $%d", argIdx))
		args = append(args, *libraryID)
		argIdx++
	}
	if access.AllowedLibraryIDs != nil {
		if len(access.AllowedLibraryIDs) == 0 {
			return map[string]upstreamListItem{}, nil
		}
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = ANY($%d)", argIdx))
		args = append(args, access.AllowedLibraryIDs)
		argIdx++
	}
	if len(access.DisabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("NOT (mil.media_folder_id = ANY($%d))", argIdx))
		args = append(args, access.DisabledLibraryIDs)
		argIdx++
	}
	catalog.ApplySectionAccessFilter("mi", access, &conditions, &args, &argIdx)

	query := fmt.Sprintf(
		`SELECT DISTINCT ON (mi.content_id) %s FROM %s WHERE %s ORDER BY mi.content_id`,
		compatItemColumns("mi"), fromClause, strings.Join(conditions, " AND "),
	)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items, err := scanCompatMediaItems(rows)
	if err != nil {
		return nil, err
	}

	result := make(map[string]upstreamListItem, len(items))
	for _, item := range items {
		listItem := mediaItemToListItem(item)
		h.presignCompatListItem(ctx, &listItem)
		result[item.ContentID] = listItem
	}
	return result, nil
}

func (h *ItemsHandler) fetchCompatItemsByContentIDsFallback(ctx context.Context, session *Session, contentIDs []string, libraryID *int) (map[string]upstreamListItem, error) {
	result := make(map[string]upstreamListItem, len(contentIDs))
	if h.itemRepo == nil {
		return result, nil
	}

	access := h.resolveAccessFilter(ctx, session)
	if libraryID != nil && *libraryID > 0 {
		if !narrowAccessToLibrary(&access, *libraryID) {
			return result, nil
		}
	}

	items, err := h.itemRepo.GetByIDsWithAccess(ctx, contentIDs, access)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		listItem := mediaItemToListItem(item)
		h.presignCompatListItem(ctx, &listItem)
		result[item.ContentID] = listItem
	}
	return result, nil
}

// narrowAccessToLibrary intersects the viewer's effective access policy with a
// caller-supplied libraryID. Returns false when the libraryID falls outside an
// existing AllowedLibraryIDs allowlist (caller should short-circuit with an
// empty result). Mutates access.AllowedLibraryIDs in place to the single ID.
func narrowAccessToLibrary(access *catalog.AccessFilter, libraryID int) bool {
	if access.AllowedLibraryIDs != nil {
		allowed := false
		for _, id := range access.AllowedLibraryIDs {
			if id == libraryID {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	access.AllowedLibraryIDs = []int{libraryID}
	return true
}

func (h *ItemsHandler) fetchCompatEpisodeTargetsByContentIDs(ctx context.Context, session *Session, contentIDs []string, libraryID *int) (map[string]compatEpisodeTarget, error) {
	normalized := normalizeContentIDs(contentIDs)
	if len(normalized) == 0 {
		return map[string]compatEpisodeTarget{}, nil
	}

	pool := h.compatPool()
	if pool == nil {
		return h.fetchCompatEpisodeTargetsByContentIDsFallback(ctx, session, normalized, libraryID)
	}

	access := h.resolveAccessFilter(ctx, session)
	fromClause := "episodes e JOIN media_items si ON e.series_id = si.content_id"
	conditions := []string{"e.content_id = ANY($1)"}
	args := []any{normalized}
	argIdx := 2

	if libraryID != nil || access.AllowedLibraryIDs != nil || len(access.DisabledLibraryIDs) > 0 {
		fromClause += " JOIN media_item_libraries mil ON si.content_id = mil.content_id"
	}
	if libraryID != nil {
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = $%d", argIdx))
		args = append(args, *libraryID)
		argIdx++
	}
	if access.AllowedLibraryIDs != nil {
		if len(access.AllowedLibraryIDs) == 0 {
			return map[string]compatEpisodeTarget{}, nil
		}
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = ANY($%d)", argIdx))
		args = append(args, access.AllowedLibraryIDs)
		argIdx++
	}
	if len(access.DisabledLibraryIDs) > 0 {
		conditions = append(conditions, fmt.Sprintf("NOT (mil.media_folder_id = ANY($%d))", argIdx))
		args = append(args, access.DisabledLibraryIDs)
		argIdx++
	}
	catalog.ApplySectionAccessFilter("si", access, &conditions, &args, &argIdx)

	query := fmt.Sprintf(`
		SELECT DISTINCT ON (e.content_id)
			e.content_id,
			e.title,
			e.overview,
			e.runtime,
			e.rating_imdb,
			e.rating_tmdb,
			e.air_date,
			e.still_path,
			COALESCE(e.still_thumbhash, ''),
			e.updated_at,
			e.season_number,
			e.episode_number,
			si.content_id,
			si.title,
			si.genres,
			si.content_rating,
			si.poster_path,
			COALESCE(si.poster_thumbhash, ''),
			si.backdrop_path,
			COALESCE(si.backdrop_thumbhash, ''),
			si.logo_path,
			si.status,
			si.updated_at
		FROM %s
		WHERE %s
		ORDER BY e.content_id
	`, fromClause, strings.Join(conditions, " AND "))

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]compatEpisodeTarget, len(normalized))
	for rows.Next() {
		var (
			contentID        string
			title            string
			overview         string
			runtime          int
			ratingIMDB       *float64
			ratingTMDB       *float64
			airDate          *time.Time
			stillPath        string
			stillThumbhash   string
			updatedAt        time.Time
			seasonNumber     int
			episodeNumber    int
			seriesID         string
			seriesTitle      string
			genres           []string
			contentRating    string
			seriesPosterPath string
			seriesPosterTH   string
			seriesBackdrop   string
			seriesBackdropTH string
			seriesLogoPath   string
			status           string
			seriesUpdatedAt  time.Time
		)
		if err := rows.Scan(
			&contentID,
			&title,
			&overview,
			&runtime,
			&ratingIMDB,
			&ratingTMDB,
			&airDate,
			&stillPath,
			&stillThumbhash,
			&updatedAt,
			&seasonNumber,
			&episodeNumber,
			&seriesID,
			&seriesTitle,
			&genres,
			&contentRating,
			&seriesPosterPath,
			&seriesPosterTH,
			&seriesBackdrop,
			&seriesBackdropTH,
			&seriesLogoPath,
			&status,
			&seriesUpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning compat episode target: %w", err)
		}

		listItem := upstreamListItem{
			ContentID:         contentID,
			Type:              "episode",
			Title:             title,
			Genres:            genres,
			ContentRating:     contentRating,
			Status:            status,
			RatingIMDB:        ratingIMDB,
			RatingTMDB:        ratingTMDB,
			Overview:          overview,
			PosterURL:         h.presignCompatImagePath(ctx, stillPath, "still"),
			BackdropURL:       h.presignCompatImagePath(ctx, seriesBackdrop, "backdrop"),
			LogoURL:           h.presignCompatImagePath(ctx, seriesLogoPath, "logo"),
			StillURL:          h.presignCompatImagePath(ctx, stillPath, "still"),
			PosterPath:        stillPath,
			BackdropPath:      seriesBackdrop,
			BackdropThumbhash: seriesBackdropTH,
			LogoPath:          seriesLogoPath,
			StillPath:         stillPath,
			StillThumbhash:    stillThumbhash,
			UpdatedAt:         updatedAt,
			SeriesID:          seriesID,
			SeriesTitle:       seriesTitle,
			SeasonNumber:      intPtr(seasonNumber),
			EpisodeNumber:     intPtr(episodeNumber),
			Runtime:           runtime,
		}
		if airDate != nil {
			listItem.AirDate = airDate.Format(time.DateOnly)
		}

		result[contentID] = compatEpisodeTarget{
			Item: listItem,
			SeriesImages: seriesImageSet{
				ContentID:         seriesID,
				PosterURL:         h.presignCompatImagePath(ctx, seriesPosterPath, "poster"),
				PosterPath:        seriesPosterPath,
				PosterThumbhash:   seriesPosterTH,
				BackdropURL:       h.presignCompatImagePath(ctx, seriesBackdrop, "backdrop"),
				BackdropPath:      seriesBackdrop,
				BackdropThumbhash: seriesBackdropTH,
				UpdatedAt:         seriesUpdatedAt,
			},
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating compat episode targets: %w", err)
	}

	return result, nil
}

func (h *ItemsHandler) fetchCompatEpisodeTargetsByContentIDsFallback(ctx context.Context, session *Session, contentIDs []string, libraryID *int) (map[string]compatEpisodeTarget, error) {
	result := make(map[string]compatEpisodeTarget, len(contentIDs))
	if h.episodeRepo == nil || h.itemRepo == nil {
		return result, nil
	}

	// Library filter for the episode IDs themselves (uses episode_libraries
	// for the membership check). Series-level access (allowed/disabled
	// libraries, rating ladder) is enforced below in a single batched query.
	filteredContentIDs, err := filterContentIDsForLibrary(ctx, h.itemRepo, contentIDs, libraryID)
	if err != nil {
		return nil, err
	}
	if len(filteredContentIDs) == 0 {
		return result, nil
	}

	episodes, err := h.episodeRepo.GetByIDs(ctx, filteredContentIDs)
	if err != nil {
		return nil, err
	}

	seriesIDs := make([]string, 0, len(episodes))
	seenSeries := make(map[string]struct{}, len(episodes))
	for _, episode := range episodes {
		if _, ok := seenSeries[episode.SeriesID]; ok {
			continue
		}
		seenSeries[episode.SeriesID] = struct{}{}
		seriesIDs = append(seriesIDs, episode.SeriesID)
	}

	access := h.resolveAccessFilter(ctx, session)
	if libraryID != nil && *libraryID > 0 {
		if !narrowAccessToLibrary(&access, *libraryID) {
			return result, nil
		}
	}

	// Single query that gates series-level access (libraries + rating) instead
	// of a per-series EnsureAccessible loop (audit 2026-05-01 §3.3).
	seriesItems, err := h.itemRepo.GetByIDsWithAccess(ctx, seriesIDs, access)
	if err != nil {
		return nil, err
	}
	seriesByID := make(map[string]*models.MediaItem, len(seriesItems))
	for _, item := range seriesItems {
		seriesByID[item.ContentID] = item
	}

	for _, episode := range episodes {
		series, ok := seriesByID[episode.SeriesID]
		if !ok {
			continue
		}
		listItem := upstreamListItem{
			ContentID:         episode.ContentID,
			Type:              "episode",
			Title:             episode.Title,
			Genres:            series.Genres,
			ContentRating:     series.ContentRating,
			Status:            series.Status,
			RatingIMDB:        episode.RatingIMDB,
			RatingTMDB:        episode.RatingTMDB,
			Overview:          episode.Overview,
			PosterURL:         h.presignCompatImagePath(ctx, episode.StillPath, "still"),
			BackdropURL:       h.presignCompatImagePath(ctx, series.BackdropPath, "backdrop"),
			LogoURL:           h.presignCompatImagePath(ctx, series.LogoPath, "logo"),
			StillURL:          h.presignCompatImagePath(ctx, episode.StillPath, "still"),
			PosterPath:        episode.StillPath,
			BackdropPath:      series.BackdropPath,
			BackdropThumbhash: series.BackdropThumbhash,
			LogoPath:          series.LogoPath,
			StillPath:         episode.StillPath,
			StillThumbhash:    episode.StillThumbhash,
			UpdatedAt:         episode.UpdatedAt,
			SeriesID:          episode.SeriesID,
			SeriesTitle:       series.Title,
			SeasonNumber:      intPtr(episode.SeasonNumber),
			EpisodeNumber:     intPtr(episode.EpisodeNumber),
			Runtime:           episode.Runtime,
		}
		if episode.AirDate != nil {
			listItem.AirDate = episode.AirDate.Format(time.DateOnly)
		}
		result[episode.ContentID] = compatEpisodeTarget{
			Item: listItem,
			SeriesImages: seriesImageSet{
				ContentID:         series.ContentID,
				PosterURL:         h.presignCompatImagePath(ctx, series.PosterPath, "poster"),
				PosterPath:        series.PosterPath,
				PosterThumbhash:   series.PosterThumbhash,
				BackdropURL:       h.presignCompatImagePath(ctx, series.BackdropPath, "backdrop"),
				BackdropPath:      series.BackdropPath,
				BackdropThumbhash: series.BackdropThumbhash,
				UpdatedAt:         series.UpdatedAt,
			},
		}
	}

	return result, nil
}

func (h *ItemsHandler) compatPool() *pgxpool.Pool {
	if h.browseRepo == nil {
		return nil
	}
	return h.browseRepo.Pool()
}

func compatItemColumns(alias string) string {
	cols := []string{
		"content_id", "type", "title", "sort_title", "original_title", "year", "genres",
		"content_rating", "runtime", "overview", "tagline",
		"rating_imdb", "rating_tmdb", "rating_rt_critic", "rating_rt_audience",
		"imdb_id", "tmdb_id", "tvdb_id",
		"poster_path", "poster_thumbhash", "backdrop_path", "backdrop_thumbhash", "logo_path",
		"metadata_s3_path", "metadata_etag", "season_count",
		"studios", "networks", "countries", "first_air_date", "last_air_date",
		"matched_at", "status", "created_at", "updated_at",
	}
	prefixed := make([]string, len(cols))
	for i, col := range cols {
		prefixed[i] = alias + "." + col
	}
	return strings.Join(prefixed, ", ")
}

func scanCompatMediaItems(rows pgx.Rows) ([]*models.MediaItem, error) {
	var items []*models.MediaItem
	for rows.Next() {
		var item models.MediaItem
		if err := rows.Scan(
			&item.ContentID, &item.Type, &item.Title, &item.SortTitle, &item.OriginalTitle,
			&item.Year, &item.Genres, &item.ContentRating, &item.Runtime, &item.Overview, &item.Tagline,
			&item.RatingIMDB, &item.RatingTMDB, &item.RatingRTCritic, &item.RatingRTAudience,
			&item.ImdbID, &item.TmdbID, &item.TvdbID,
			&item.PosterPath, &item.PosterThumbhash, &item.BackdropPath, &item.BackdropThumbhash, &item.LogoPath,
			&item.MetadataS3Path, &item.MetadataEtag, &item.SeasonCount,
			&item.Studios, &item.Networks, &item.Countries, &item.FirstAirDate, &item.LastAirDate,
			&item.MatchedAt, &item.Status, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning compat media item: %w", err)
		}
		items = append(items, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating compat media items: %w", err)
	}
	return items, nil
}

func filterContentIDsForLibrary(ctx context.Context, checker libraryMembershipChecker, contentIDs []string, libraryID *int) ([]string, error) {
	normalized := normalizeContentIDs(contentIDs)
	if libraryID == nil || *libraryID <= 0 || len(normalized) == 0 || checker == nil {
		return normalized, nil
	}

	membership, err := checker.GetItemsInLibrary(ctx, normalized, *libraryID)
	if err != nil {
		return nil, err
	}

	filtered := make([]string, 0, len(normalized))
	for _, contentID := range normalized {
		if membership[contentID] {
			filtered = append(filtered, contentID)
		}
	}
	return filtered, nil
}
