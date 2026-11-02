package catalog

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/titleutil"
)

// The nullable columns an edit can clear. Each name is also the API member.
const (
	columnRatingIMDB       = "rating_imdb"
	columnRatingTMDB       = "rating_tmdb"
	columnRatingRTCritic   = "rating_rt_critic"
	columnRatingRTAudience = "rating_rt_audience"
	columnReleaseDate      = "release_date"
	columnFirstAirDate     = "first_air_date"
	columnLastAirDate      = "last_air_date"
	columnAirTime          = "air_time"
	columnAirTimezone      = "air_timezone"
	columnAirDate          = "air_date"
)

// The columns each table lets an edit clear.
var (
	itemClearableColumns = []string{
		columnRatingIMDB, columnRatingTMDB, columnRatingRTCritic, columnRatingRTAudience,
		columnReleaseDate, columnFirstAirDate, columnLastAirDate, columnAirTime, columnAirTimezone,
	}
	seasonClearableColumns  = []string{columnAirDate}
	episodeClearableColumns = []string{columnAirDate, columnRatingIMDB, columnRatingTMDB}
)

// ClearableMetadataFields names the fields an edit can clear through
// MetadataUpdate.Clear: every table's clearable columns. A table without the
// column ignores it, as it ignores a field it doesn't store.
var ClearableMetadataFields = func() []string {
	var fields []string
	for _, col := range slices.Concat(itemClearableColumns, seasonClearableColumns, episodeClearableColumns) {
		if !slices.Contains(fields, col) {
			fields = append(fields, col)
		}
	}
	return fields
}()

// ratingColumnSources pairs each rating column with the per-source row that
// reports the same rating. Clearing the column deletes that row too, since
// the title page falls back to it when the column is empty.
var ratingColumnSources = []struct{ column, source string }{
	{columnRatingIMDB, models.RatingSourceIMDB},
	{columnRatingTMDB, models.RatingSourceTMDB},
	{columnRatingRTCritic, models.RatingSourceRTCritic},
	{columnRatingRTAudience, models.RatingSourceRTAudience},
}

// MetadataUpdate contains the fields that can be updated on a media item,
// season, or episode. Nil pointer fields are skipped (not updated).
type MetadataUpdate struct {
	// Clear lists fields from ClearableMetadataFields to set to NULL. A
	// cleared field's pointer stays nil.
	Clear []string

	Title              *string
	SortTitle          *string
	OriginalTitle      *string
	Overview           *string
	Tagline            *string
	ContentRating      *string
	Year               *int
	Runtime            *int
	Genres             *[]string
	Studios            *[]string
	Networks           *[]string
	Countries          *[]string
	ReleaseDate        *string
	FirstAirDate       *string
	LastAirDate        *string
	AirTime            *string
	AirTimezone        *string
	AirDate            *string
	Status             *string
	ShowStatus         *string
	RatingIMDB         *float64
	RatingTMDB         *float64
	RatingRTCritic     *int
	RatingRTAudience   *int
	ImdbID             *string
	TmdbID             *string
	TvdbID             *string
	SeasonNumber       *int
	EpisodeNumber      *int
	LockedFields       *[]int
	PosterPath         *string
	PosterSourcePath   *string
	PosterThumbhash    *string
	BackdropPath       *string
	BackdropSourcePath *string
	BackdropThumbhash  *string
	LogoPath           *string
	LogoSourcePath     *string
	StillPath          *string
	StillSourcePath    *string
	StillThumbhash     *string
}

// clears reports whether the update sets field to NULL.
func (u *MetadataUpdate) clears(field string) bool {
	return slices.Contains(u.Clear, field)
}

// nullClauses returns a "column = NULL" assignment for each of columns the
// update clears. Callers pass their own table's clearable columns, so only
// those names reach the SQL.
func (u *MetadataUpdate) nullClauses(columns ...string) []string {
	var clauses []string
	for _, col := range columns {
		if u.clears(col) {
			clauses = append(clauses, col+" = NULL")
		}
	}
	return clauses
}

// clearedRatingSources returns the per-source rows to delete for the rating
// columns the update clears.
func (u *MetadataUpdate) clearedRatingSources() []string {
	var sources []string
	for _, rating := range ratingColumnSources {
		if u.clears(rating.column) {
			sources = append(sources, rating.source)
		}
	}
	return sources
}

// UpdateMediaItemMetadata updates specific metadata fields on a media_items row.
func (s *DetailService) UpdateMediaItemMetadata(ctx context.Context, contentID string, upd *MetadataUpdate) error {
	relinkWork := false
	if s.workLinker != nil && upd.Title != nil {
		item, err := s.itemRepo.GetByID(ctx, contentID)
		if err != nil {
			return err
		}
		relinkWork = item.Type == searchTypeEbook || item.Type == detailTypeAudiobook
	}
	if err := applyDefaultSortTitleOnAdminUpdate(ctx, s.itemRepo, contentID, upd); err != nil {
		return err
	}
	if err := s.itemRepo.UpdateMetadata(ctx, contentID, upd); err != nil {
		return err
	}
	// Every explicit book-title save retries best-effort linking, including
	// resubmitting a saved title after an earlier linking failure.
	if relinkWork {
		if _, _, err := s.workLinker.AutoLinkContent(ctx, contentID); err != nil {
			slog.WarnContext(ctx, "catalog: literary work auto-link after metadata update failed", "content_id", contentID, "error", err)
		}
	}
	return nil
}

// UpdateSeasonMetadata updates specific metadata fields on a seasons row.
func (s *DetailService) UpdateSeasonMetadata(ctx context.Context, contentID string, upd *MetadataUpdate) error {
	return s.seasonRepo.UpdateMetadata(ctx, contentID, upd)
}

// UpdateEpisodeMetadata updates specific metadata fields on an episodes row.
func (s *DetailService) UpdateEpisodeMetadata(ctx context.Context, contentID string, upd *MetadataUpdate) error {
	return s.episodeRepo.UpdateMetadata(ctx, contentID, upd)
}

func applyDefaultSortTitleOnAdminUpdate(
	ctx context.Context,
	itemRepo *ItemRepository,
	contentID string,
	upd *MetadataUpdate,
) error {
	if upd.Title == nil || upd.SortTitle != nil {
		return nil
	}

	item, err := itemRepo.GetByID(ctx, contentID)
	if err != nil {
		return err
	}
	if item == nil {
		return nil
	}
	if titleLockedForAdminUpdate(item, upd) {
		return nil
	}
	if strings.TrimSpace(item.SortTitle) != "" {
		return nil
	}

	derived := titleutil.DeriveDefaultSortTitle(*upd.Title)
	if derived == "" {
		return nil
	}
	upd.SortTitle = &derived
	return nil
}

// fieldNameLocked matches metadata.FieldName and EditMetadataDialog FIELD_NAME.
const fieldNameLocked = 0

func titleLockedForAdminUpdate(item *models.MediaItem, upd *MetadataUpdate) bool {
	if upd.LockedFields != nil {
		return slices.Contains(*upd.LockedFields, fieldNameLocked)
	}
	return slices.Contains(item.LockedFields, fieldNameLocked)
}
