package catalog

import (
	"context"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/titleutil"
)

// MetadataUpdate contains the fields that can be updated on a media item,
// season, or episode. Nil pointer fields are skipped (not updated).
type MetadataUpdate struct {
	Title             *string
	SortTitle         *string
	OriginalTitle     *string
	Overview          *string
	Tagline           *string
	ContentRating     *string
	Year              *int
	Runtime           *int
	Genres            *[]string
	Studios           *[]string
	Networks          *[]string
	Countries         *[]string
	ReleaseDate       *string
	FirstAirDate      *string
	LastAirDate       *string
	AirTime           *string
	AirTimezone       *string
	AirDate           *string
	Status            *string
	ShowStatus        *string
	RatingIMDB        *float64
	RatingTMDB        *float64
	RatingRTCritic    *int
	RatingRTAudience  *int
	ImdbID            *string
	TmdbID            *string
	TvdbID            *string
	SeasonNumber      *int
	EpisodeNumber     *int
	LockedFields      *[]int
	PosterPath        *string
	PosterThumbhash   *string
	BackdropPath      *string
	BackdropThumbhash *string
	LogoPath          *string
	StillPath         *string
	StillThumbhash    *string
}

// UpdateMediaItemMetadata updates specific metadata fields on a media_items row.
func (s *DetailService) UpdateMediaItemMetadata(ctx context.Context, contentID string, upd *MetadataUpdate) error {
	if err := applyDefaultSortTitleOnAdminUpdate(ctx, s.itemRepo, contentID, upd); err != nil {
		return err
	}
	return s.itemRepo.UpdateMetadata(ctx, contentID, upd)
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
