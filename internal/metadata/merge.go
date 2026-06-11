package metadata

import (
	"slices"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// MergeMetadata merges source into target respecting locked fields and merge mode.
func MergeMetadata(source, target *MetadataResult, locked []MetadataField, mode MergeMode) {
	if source == nil {
		return
	}
	isLocked := func(f MetadataField) bool {
		return slices.Contains(locked, f)
	}

	// Scalar fields
	if !isLocked(FieldName) {
		mergeScalar(&target.Title, source.Title, mode)
		mergeScalar(&target.OriginalTitle, source.OriginalTitle, mode)
		mergeScalar(&target.SortTitle, source.SortTitle, mode)
	}
	if !isLocked(FieldOverview) {
		mergeScalar(&target.Overview, source.Overview, mode)
		mergeScalar(&target.Tagline, source.Tagline, mode)
	}
	if !isLocked(FieldRuntime) {
		mergeInt(&target.Runtime, source.Runtime, mode)
	}
	if !isLocked(FieldContentRating) {
		mergeScalar(&target.ContentRating, source.ContentRating, mode)
	}
	if !isLocked(FieldRating) {
		mergeFloat(&target.Ratings.IMDB, source.Ratings.IMDB, mode)
		mergeFloat(&target.Ratings.TMDB, source.Ratings.TMDB, mode)
		mergeFloat(&target.Ratings.RTCritic, source.Ratings.RTCritic, mode)
		mergeFloat(&target.Ratings.RTAudience, source.Ratings.RTAudience, mode)
	}

	// Always fill year (not lockable — identity field)
	mergeInt(&target.Year, source.Year, mode)
	mergeScalar(&target.OriginalLanguage, source.OriginalLanguage, mode)
	mergeScalar(&target.ReleaseDate, source.ReleaseDate, mode)

	// Series fields (not lockable — structural)
	mergeInt(&target.SeasonCount, source.SeasonCount, mode)
	mergeScalar(&target.FirstAirDate, source.FirstAirDate, mode)
	mergeScalar(&target.LastAirDate, source.LastAirDate, mode)
	mergeScalar(&target.ShowStatus, source.ShowStatus, mode)
	if !isLocked(FieldAirSchedule) {
		mergeScalar(&target.AirTime, source.AirTime, mode)
		mergeScalar(&target.AirTimezone, source.AirTimezone, mode)
	}

	// Genres follow provider priority during FillEmpty instead of unioning tags
	// from later providers, which can create noisy hybrid classifications.
	if !isLocked(FieldGenres) {
		mergePrioritizedStringSlice(&target.Genres, source.Genres, mode)
	}
	// Accumulate other arrays
	if !isLocked(FieldStudios) {
		mergeStringSlice(&target.Studios, source.Studios, mode)
		mergeStringSlice(&target.Networks, source.Networks, mode)
	}
	if !isLocked(FieldTags) {
		mergeStringSlice(&target.Countries, source.Countries, mode)
		mergeStringSlice(&target.Keywords, source.Keywords, mode)
	}

	// Smart merge people (unified cast/crew)
	if !isLocked(FieldCast) || !isLocked(FieldCrew) {
		mergePeople(&target.People, source.People, mode)
	}

	// Images
	if !isLocked(FieldImages) {
		mergeScalar(&target.PosterPath, source.PosterPath, mode)
		mergeScalar(&target.PosterThumbhash, source.PosterThumbhash, mode)
		mergeScalar(&target.BackdropPath, source.BackdropPath, mode)
		mergeScalar(&target.BackdropThumbhash, source.BackdropThumbhash, mode)
		mergeScalar(&target.LogoPath, source.LogoPath, mode)
	}

	// Provider IDs always accumulate, never overwrite
	mergeProviderIDs(target, source)
}

// MergeGlobalMetadata merges only provider-invariant fields, preserving any
// language-specific presentation fields already stored on the target.
func MergeGlobalMetadata(source, target *MetadataResult, locked []MetadataField, mode MergeMode) {
	if source == nil {
		return
	}
	isLocked := func(f MetadataField) bool {
		return slices.Contains(locked, f)
	}

	if !isLocked(FieldRuntime) {
		mergeInt(&target.Runtime, source.Runtime, mode)
	}
	if !isLocked(FieldContentRating) {
		mergeScalar(&target.ContentRating, source.ContentRating, mode)
	}
	if !isLocked(FieldRating) {
		mergeFloat(&target.Ratings.IMDB, source.Ratings.IMDB, mode)
		mergeFloat(&target.Ratings.TMDB, source.Ratings.TMDB, mode)
		mergeFloat(&target.Ratings.RTCritic, source.Ratings.RTCritic, mode)
		mergeFloat(&target.Ratings.RTAudience, source.Ratings.RTAudience, mode)
	}

	mergeInt(&target.Year, source.Year, mode)
	mergeScalar(&target.OriginalTitle, source.OriginalTitle, mode)
	mergeScalar(&target.OriginalLanguage, source.OriginalLanguage, mode)
	mergeScalar(&target.ReleaseDate, source.ReleaseDate, mode)
	mergeInt(&target.SeasonCount, source.SeasonCount, mode)
	mergeScalar(&target.FirstAirDate, source.FirstAirDate, mode)
	mergeScalar(&target.LastAirDate, source.LastAirDate, mode)
	mergeScalar(&target.ShowStatus, source.ShowStatus, mode)
	if !isLocked(FieldAirSchedule) {
		mergeScalar(&target.AirTime, source.AirTime, mode)
		mergeScalar(&target.AirTimezone, source.AirTimezone, mode)
	}

	if !isLocked(FieldGenres) {
		mergePrioritizedStringSlice(&target.Genres, source.Genres, mode)
	}
	if !isLocked(FieldStudios) {
		mergeStringSlice(&target.Studios, source.Studios, mode)
		mergeStringSlice(&target.Networks, source.Networks, mode)
	}
	if !isLocked(FieldTags) {
		mergeStringSlice(&target.Countries, source.Countries, mode)
		mergeStringSlice(&target.Keywords, source.Keywords, mode)
	}
	if !isLocked(FieldCast) || !isLocked(FieldCrew) {
		mergePeople(&target.People, source.People, mode)
	}

	mergeProviderIDs(target, source)
}

// MergeSeasonResult merges source into target using the standard metadata
// fallback contract for season-level fields.
func MergeSeasonResult(source, target *SeasonResult, mode MergeMode) {
	if source == nil {
		return
	}

	mergeScalar(&target.ContentID, source.ContentID, mode)
	mergeInt(&target.SeasonNumber, source.SeasonNumber, mode)
	mergeScalar(&target.Title, source.Title, mode)
	mergeScalar(&target.Overview, source.Overview, mode)
	mergeScalar(&target.AirDate, source.AirDate, mode)
	mergeScalar(&target.PosterPath, source.PosterPath, mode)
	mergeScalar(&target.PosterThumbhash, source.PosterThumbhash, mode)
}

// MergeEpisodeResult merges source into target using the standard metadata
// fallback contract for episode-level fields.
func MergeEpisodeResult(source, target *EpisodeResult, mode MergeMode) {
	if source == nil {
		return
	}

	mergeScalar(&target.ContentID, source.ContentID, mode)
	mergeInt(&target.SeasonNumber, source.SeasonNumber, mode)
	mergeInt(&target.EpisodeNumber, source.EpisodeNumber, mode)
	mergeScalar(&target.Title, source.Title, mode)
	mergeScalar(&target.Overview, source.Overview, mode)
	mergeScalar(&target.AirDate, source.AirDate, mode)
	mergeInt(&target.Runtime, source.Runtime, mode)
	mergeFloat(&target.Ratings.IMDB, source.Ratings.IMDB, mode)
	mergeFloat(&target.Ratings.TMDB, source.Ratings.TMDB, mode)
	mergeFloat(&target.Ratings.RTCritic, source.Ratings.RTCritic, mode)
	mergeFloat(&target.Ratings.RTAudience, source.Ratings.RTAudience, mode)
	mergeScalar(&target.StillPath, source.StillPath, mode)
	mergeScalar(&target.StillThumbhash, source.StillThumbhash, mode)
	mergeProviderIDMap(&target.ProviderIDs, source.ProviderIDs)
}

// MergePersonDetail merges source into target using the standard metadata
// fallback contract for person-level fields.
func MergePersonDetail(source, target *PersonDetailResult, mode MergeMode) {
	if source == nil {
		return
	}

	mergeScalar(&target.Name, source.Name, mode)
	mergeScalar(&target.SortName, source.SortName, mode)
	mergeScalar(&target.Bio, source.Bio, mode)
	mergeScalar(&target.BirthDate, source.BirthDate, mode)
	mergeScalar(&target.DeathDate, source.DeathDate, mode)
	mergeScalar(&target.Birthplace, source.Birthplace, mode)
	mergeScalar(&target.Homepage, source.Homepage, mode)
	mergeScalar(&target.PhotoPath, source.PhotoPath, mode)
	mergeScalar(&target.PhotoThumbhash, source.PhotoThumbhash, mode)
	mergeProviderIDMap(&target.ProviderIDs, source.ProviderIDs)
}

// mergeProviderIDs adds new IDs without overwriting existing ones.
func mergeProviderIDs(target, source *MetadataResult) {
	if len(source.ProviderIDs) == 0 {
		return
	}
	if target.ProviderIDs == nil {
		target.ProviderIDs = make(map[string]string)
	}
	for k, v := range source.ProviderIDs {
		if v == "" {
			continue
		}
		if _, exists := target.ProviderIDs[k]; !exists {
			target.ProviderIDs[k] = v
		}
	}
}

func mergeProviderIDMap(target *map[string]string, source map[string]string) {
	if len(source) == 0 {
		return
	}
	if *target == nil {
		*target = make(map[string]string)
	}
	for key, value := range source {
		if value == "" {
			continue
		}
		if (*target)[key] == "" {
			(*target)[key] = value
		}
	}
}

func mergeScalar(target *string, source string, mode MergeMode) {
	if source == "" {
		return
	}
	if mode == MergeReplaceUnlocked || *target == "" {
		*target = source
	}
}

func mergeInt(target *int, source int, mode MergeMode) {
	if source == 0 {
		return
	}
	if mode == MergeReplaceUnlocked || *target == 0 {
		*target = source
	}
}

func mergeFloat(target *float64, source float64, mode MergeMode) {
	if source == 0 {
		return
	}
	if mode == MergeReplaceUnlocked || *target == 0 {
		*target = source
	}
}

func mergeStringSlice(target *[]string, source []string, mode MergeMode) {
	if len(source) == 0 {
		return
	}
	if mode == MergeReplaceUnlocked {
		*target = source
		return
	}
	// FillEmpty: accumulate unique
	if len(*target) == 0 {
		*target = source
		return
	}
	seen := make(map[string]bool, len(*target))
	for _, s := range *target {
		seen[strings.ToLower(s)] = true
	}
	for _, s := range source {
		if !seen[strings.ToLower(s)] {
			*target = append(*target, s)
			seen[strings.ToLower(s)] = true
		}
	}
}

func mergePrioritizedStringSlice(target *[]string, source []string, mode MergeMode) {
	if len(source) == 0 {
		return
	}
	if mode == MergeReplaceUnlocked || len(*target) == 0 {
		*target = source
	}
}

func mergePeople(target *[]models.ItemPerson, source []models.ItemPerson, mode MergeMode) {
	if len(source) == 0 {
		return
	}
	if mode == MergeReplaceUnlocked {
		*target = source
		return
	}
	if len(*target) == 0 {
		*target = source
		return
	}
	byTmdbID := indexPeopleBy(*target, func(p models.ItemPerson) string { return p.TmdbID })
	byTvdbID := indexPeopleBy(*target, func(p models.ItemPerson) string { return p.TvdbID })
	byImdbID := indexPeopleBy(*target, func(p models.ItemPerson) string { return p.ImdbID })
	byPlexGUID := indexPeopleBy(*target, func(p models.ItemPerson) string { return p.PlexGUID })
	byNameKind := indexPeopleBy(*target, func(p models.ItemPerson) string {
		return strings.ToLower(p.Name) + "|" + strconv.Itoa(int(p.Kind))
	})

	for _, sp := range source {
		if idx, ok := findPersonMatch(sp, byTmdbID, byTvdbID, byImdbID, byPlexGUID, byNameKind); ok {
			mergePersonFields(&(*target)[idx], sp)
		} else {
			*target = append(*target, sp)
		}
	}
}

func indexPeopleBy(people []models.ItemPerson, keyFn func(models.ItemPerson) string) map[string]int {
	m := make(map[string]int, len(people))
	for i, p := range people {
		if key := keyFn(p); key != "" {
			m[key] = i
		}
	}
	return m
}

func findPersonMatch(sp models.ItemPerson, maps ...map[string]int) (int, bool) {
	keys := []string{sp.TmdbID, sp.TvdbID, sp.ImdbID, sp.PlexGUID,
		strings.ToLower(sp.Name) + "|" + strconv.Itoa(int(sp.Kind))}
	for i, m := range maps {
		if i < len(keys) {
			if idx, ok := m[keys[i]]; ok && keys[i] != "" {
				return idx, true
			}
		}
	}
	return 0, false
}

func mergePersonFields(dst *models.ItemPerson, src models.ItemPerson) {
	if dst.TmdbID == "" {
		dst.TmdbID = src.TmdbID
	}
	if dst.ImdbID == "" {
		dst.ImdbID = src.ImdbID
	}
	if dst.TvdbID == "" {
		dst.TvdbID = src.TvdbID
	}
	if dst.PlexGUID == "" {
		dst.PlexGUID = src.PlexGUID
	}
	if dst.PhotoPath == "" {
		dst.PhotoPath = src.PhotoPath
	}
	if dst.PhotoThumbhash == "" {
		dst.PhotoThumbhash = src.PhotoThumbhash
	}
	if dst.Character == "" {
		dst.Character = src.Character
	}
}
