package catalog

import "github.com/Silo-Server/silo-server/internal/models"

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
	if loc.Title != "" {
		localized.Title = loc.Title
	}
	if loc.SortTitle != "" {
		localized.SortTitle = loc.SortTitle
	}
	if loc.Overview != "" {
		localized.Overview = loc.Overview
	}
	if loc.Tagline != "" {
		localized.Tagline = loc.Tagline
	}
	if loc.PosterPath != "" {
		localized.PosterPath = loc.PosterPath
		localized.PosterSourcePath = loc.PosterSourcePath
		localized.PosterThumbhash = loc.PosterThumbhash
	}
	if loc.BackdropPath != "" {
		localized.BackdropPath = loc.BackdropPath
		localized.BackdropSourcePath = loc.BackdropSourcePath
		localized.BackdropThumbhash = loc.BackdropThumbhash
	}
	if loc.LogoPath != "" {
		localized.LogoPath = loc.LogoPath
		localized.LogoSourcePath = loc.LogoSourcePath
	}
	return localized
}

// applySeasonLocalization merges a localization onto a clone of season; see
// applyItemLocalization for the empty-field semantics.
func applySeasonLocalization(season *models.Season, loc *models.SeasonLocalization) *models.Season {
	localized := cloneSeason(season)
	if localized == nil || loc == nil {
		return localized
	}
	if loc.Title != "" {
		localized.Title = loc.Title
	}
	if loc.Overview != "" {
		localized.Overview = loc.Overview
	}
	if loc.PosterPath != "" {
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
	if loc.Title != "" {
		localized.Title = loc.Title
	}
	if loc.Overview != "" {
		localized.Overview = loc.Overview
	}
	return localized
}
