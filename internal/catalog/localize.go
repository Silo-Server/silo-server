package catalog

import (
	"slices"

	"github.com/Silo-Server/silo-server/internal/models"
)

// fieldImagesLocked matches metadata.FieldImages. Catalog cannot import
// metadata because metadata already depends on catalog.
const fieldImagesLocked = 10

// Machine-translated field names, as MachineTranslatedFields reports them.
const (
	MachineTranslatedOverview = "overview"
	MachineTranslatedTagline  = "tagline"
)

// localizationSourceAI is the provenance of an AI-written localized field.
const localizationSourceAI = "ai"

// applyItemLocalization merges a localization onto a clone of item. Only
// non-empty localized fields override the base — localization rows are
// legitimately partial (an AI translation carries only overview/tagline; a
// provider row may lack a tagline or logo), and an empty field must fall back
// to the base value rather than blank it.
func applyItemLocalization(item *models.MediaItem, loc *models.MediaItemLocalization) *models.MediaItem {
	localized := cloneMediaItem(item)
	if localized == nil || loc == nil {
		return localized
	}
	// The markers describe this localization only, never a previous one.
	localized.MachineTranslatedFields = nil
	if loc.Title != "" {
		localized.Title = loc.Title
	}
	if loc.SortTitle != "" {
		localized.SortTitle = loc.SortTitle
	}
	if loc.Overview != "" {
		localized.Overview = loc.Overview
		if loc.OverviewSource == localizationSourceAI {
			localized.MachineTranslatedFields = append(localized.MachineTranslatedFields, MachineTranslatedOverview)
		}
	}
	if loc.Tagline != "" {
		localized.Tagline = loc.Tagline
		if loc.TaglineSource == localizationSourceAI {
			localized.MachineTranslatedFields = append(localized.MachineTranslatedFields, MachineTranslatedTagline)
		}
	}
	imagesLocked := slices.Contains(item.LockedFields, fieldImagesLocked)
	if !imagesLocked && loc.PosterPath != "" {
		localized.PosterPath = loc.PosterPath
		localized.PosterSourcePath = loc.PosterSourcePath
		localized.PosterThumbhash = loc.PosterThumbhash
	}
	if !imagesLocked && loc.BackdropPath != "" {
		localized.BackdropPath = loc.BackdropPath
		localized.BackdropSourcePath = loc.BackdropSourcePath
		localized.BackdropThumbhash = loc.BackdropThumbhash
	}
	if !imagesLocked && loc.LogoPath != "" {
		localized.LogoPath = loc.LogoPath
		localized.LogoSourcePath = loc.LogoSourcePath
	}
	return localized
}

// applySeasonLocalization merges a localization onto a clone of season; see
// applyItemLocalization for the empty-field semantics.
func applySeasonLocalization(season *models.Season, loc *models.SeasonLocalization, imagesLocked bool) *models.Season {
	localized := cloneSeason(season)
	if localized == nil || loc == nil {
		return localized
	}
	// The markers describe this localization only, never a previous one.
	localized.MachineTranslatedFields = nil
	if loc.Title != "" {
		localized.Title = loc.Title
	}
	if loc.Overview != "" {
		localized.Overview = loc.Overview
		if loc.OverviewSource == localizationSourceAI {
			localized.MachineTranslatedFields = append(localized.MachineTranslatedFields, MachineTranslatedOverview)
		}
	}
	if !imagesLocked && loc.PosterPath != "" {
		localized.PosterPath = loc.PosterPath
		localized.PosterSourcePath = loc.PosterSourcePath
		localized.PosterThumbhash = loc.PosterThumbhash
	}
	return localized
}

// applyEpisodeLocalization merges a localization onto a clone of episode; see
// applyItemLocalization for the empty-field semantics.
func applyEpisodeLocalization(episode *models.Episode, loc *models.EpisodeLocalization) *models.Episode {
	localized := cloneEpisode(episode)
	if localized == nil || loc == nil {
		return localized
	}
	// The markers describe this localization only, never a previous one.
	localized.MachineTranslatedFields = nil
	if loc.Title != "" {
		localized.Title = loc.Title
	}
	if loc.Overview != "" {
		localized.Overview = loc.Overview
		if loc.OverviewSource == localizationSourceAI {
			localized.MachineTranslatedFields = append(localized.MachineTranslatedFields, MachineTranslatedOverview)
		}
	}
	return localized
}
