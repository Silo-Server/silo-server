package catalog

import (
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// fieldImagesLocked matches metadata.FieldImages. Catalog cannot import
// metadata because metadata already depends on catalog.
const fieldImagesLocked = 10

// applyItemLocalization merges a localization onto a clone of item. Only
// non-empty localized fields override the base — localization rows are
// legitimately partial (an AI translation carries only overview/tagline; a
// provider row may lack a tagline or logo), and an empty field must fall back
// to the base value rather than blank it. Base artwork copied from a local
// sidecar is language-neutral and outranks provider artwork, so a localized
// image never replaces it (see keepsLocalArtwork).
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
	imagesLocked := slices.Contains(item.LockedFields, fieldImagesLocked)
	if !imagesLocked && loc.PosterPath != "" && !keepsLocalArtwork(item.PosterPath, item.PosterSourcePath) {
		localized.PosterPath = loc.PosterPath
		localized.PosterSourcePath = loc.PosterSourcePath
		localized.PosterThumbhash = loc.PosterThumbhash
	}
	if !imagesLocked && loc.BackdropPath != "" && !keepsLocalArtwork(item.BackdropPath, item.BackdropSourcePath) {
		localized.BackdropPath = loc.BackdropPath
		localized.BackdropSourcePath = loc.BackdropSourcePath
		localized.BackdropThumbhash = loc.BackdropThumbhash
	}
	if !imagesLocked && loc.LogoPath != "" && !keepsLocalArtwork(item.LogoPath, item.LogoSourcePath) {
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
	if loc.Title != "" {
		localized.Title = loc.Title
	}
	if loc.Overview != "" {
		localized.Overview = loc.Overview
	}
	if !imagesLocked && loc.PosterPath != "" && !keepsLocalArtwork(season.PosterPath, season.PosterSourcePath) {
		localized.PosterPath = loc.PosterPath
		localized.PosterSourcePath = loc.PosterSourcePath
		localized.PosterThumbhash = loc.PosterThumbhash
	}
	return localized
}

// keepsLocalArtwork reports whether a base artwork slot shows a copy of a
// local sidecar (a file:// source, mirroring metadata.isLocalImageSourcePath).
// An empty path means the copy is not cached yet, so a localized image may
// still fill the slot.
func keepsLocalArtwork(path, sourcePath string) bool {
	return path != "" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(sourcePath)), "file://")
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
