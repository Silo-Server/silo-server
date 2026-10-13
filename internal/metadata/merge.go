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
	mergeAdvisory(target, source, mode)
	if !isLocked(FieldRating) {
		mergeFloat(&target.Ratings.IMDB, source.Ratings.IMDB, mode)
		mergeFloat(&target.Ratings.TMDB, source.Ratings.TMDB, mode)
		mergeFloat(&target.Ratings.RTCritic, source.Ratings.RTCritic, mode)
		mergeFloat(&target.Ratings.RTAudience, source.Ratings.RTAudience, mode)
		mergeRatingSources(&target.RatingSources, source.RatingSources, mode)
	}

	// Year and release/air dates lock together under FieldReleaseDates.
	if !isLocked(FieldReleaseDates) {
		mergeInt(&target.Year, source.Year, mode)
		mergeScalar(&target.ReleaseDate, source.ReleaseDate, mode)
		mergeScalar(&target.FirstAirDate, source.FirstAirDate, mode)
		mergeScalar(&target.LastAirDate, source.LastAirDate, mode)
	}
	mergeScalar(&target.OriginalLanguage, source.OriginalLanguage, mode)

	// Series fields (not lockable — structural)
	mergeInt(&target.SeasonCount, source.SeasonCount, mode)
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

	// Smart merge people (unified cast/crew); each role group has its own lock.
	mergePeopleUnlocked(&target.People, source.People, mode, isLocked(FieldCast), isLocked(FieldCrew))

	// Images
	if !isLocked(FieldImages) {
		mergeScalar(&target.PosterPath, source.PosterPath, mode)
		mergeScalar(&target.PosterThumbhash, source.PosterThumbhash, mode)
		mergeScalar(&target.BackdropPath, source.BackdropPath, mode)
		mergeScalar(&target.BackdropThumbhash, source.BackdropThumbhash, mode)
		mergeScalar(&target.LogoPath, source.LogoPath, mode)
	}

	// Remote videos accumulate across providers (TMDB trailers + another
	// provider's clips can coexist), deduped by provider identity.
	if !isLocked(FieldVideos) {
		mergeVideos(&target.Videos, source.Videos)
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
	mergeAdvisory(target, source, mode)
	if !isLocked(FieldRating) {
		mergeFloat(&target.Ratings.IMDB, source.Ratings.IMDB, mode)
		mergeFloat(&target.Ratings.TMDB, source.Ratings.TMDB, mode)
		mergeFloat(&target.Ratings.RTCritic, source.Ratings.RTCritic, mode)
		mergeFloat(&target.Ratings.RTAudience, source.Ratings.RTAudience, mode)
		mergeRatingSources(&target.RatingSources, source.RatingSources, mode)
	}

	if !isLocked(FieldReleaseDates) {
		mergeInt(&target.Year, source.Year, mode)
		mergeScalar(&target.ReleaseDate, source.ReleaseDate, mode)
		mergeScalar(&target.FirstAirDate, source.FirstAirDate, mode)
		mergeScalar(&target.LastAirDate, source.LastAirDate, mode)
	}
	mergeScalar(&target.OriginalTitle, source.OriginalTitle, mode)
	mergeScalar(&target.OriginalLanguage, source.OriginalLanguage, mode)
	mergeInt(&target.SeasonCount, source.SeasonCount, mode)
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
	mergePeopleUnlocked(&target.People, source.People, mode, isLocked(FieldCast), isLocked(FieldCrew))
	if !isLocked(FieldVideos) {
		mergeVideos(&target.Videos, source.Videos)
	}

	mergeProviderIDs(target, source)
}

// mergeVideos accumulates source videos into target, skipping entries the
// target already has from the same provider (same provider video id) or that
// point at the same hosted video (same site/site_key). Unlike scalar fields
// this ignores MergeMode: the set is replaced wholesale at persist time each
// refresh, so within one refresh pass accumulation is always correct.
func mergeVideos(target *[]RemoteVideo, source []RemoteVideo) {
	if len(source) == 0 {
		return
	}
	seen := make(map[string]bool, len(*target)*2)
	for _, v := range *target {
		seen["p|"+v.Provider+"|"+v.ProviderKey] = true
		seen["s|"+strings.ToLower(v.Site)+"|"+v.SiteKey] = true
	}
	for _, v := range source {
		providerKey := "p|" + v.Provider + "|" + v.ProviderKey
		siteKey := "s|" + strings.ToLower(v.Site) + "|" + v.SiteKey
		if seen[providerKey] || (v.SiteKey != "" && seen[siteKey]) {
			continue
		}
		seen[providerKey] = true
		seen[siteKey] = true
		*target = append(*target, v)
	}
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
	mergeScalar(&target.PhotoSourcePath, source.PhotoSourcePath, mode)
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

// mergeRatingSources merges per-source ratings one source at a time, with the
// scalar rating rules: fill-empty keeps a source the target already has, and
// replace-unlocked takes the incoming score for every source the source
// reports. Neither mode removes a source the incoming result lacks.
func mergeRatingSources(target *map[string]RatingSource, source map[string]RatingSource, mode MergeMode) {
	if len(source) == 0 {
		return
	}
	if *target == nil {
		*target = make(map[string]RatingSource, len(source))
	}
	for name, rating := range source {
		if _, exists := (*target)[name]; exists && mode != MergeReplaceUnlocked {
			continue
		}
		(*target)[name] = rating
	}
}

// mergeAdvisory merges the advisory age and its source as one unit.
//
// Merging them as separate scalars would let a new age land beside the
// previous provider's source and misattribute the recommendation. They move
// together or not at all.
//
// The advisory is deliberately not gated on FieldContentRating: that lock
// protects a manual certification correction, and the advisory is a different
// number from a different body that no one can edit by hand. It has no lock of
// its own for the same reason.
func mergeAdvisory(target, source *MetadataResult, mode MergeMode) {
	if source.AdvisoryAge <= 0 || source.AdvisorySource == "" {
		return
	}
	if mode == MergeReplaceUnlocked || target.AdvisoryAge <= 0 {
		target.AdvisoryAge = source.AdvisoryAge
		target.AdvisorySource = source.AdvisorySource
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

// isCastKind reports whether a credit belongs to the cast (FieldCast) rather
// than the crew (FieldCrew).
func isCastKind(kind models.PersonKind) bool {
	return kind == models.PersonKindActor || kind == models.PersonKindGuestStar
}

// splitPeopleByRole splits credits into cast and crew, keeping their order.
func splitPeopleByRole(people []models.ItemPerson) (cast, crew []models.ItemPerson) {
	for _, p := range people {
		if isCastKind(p.Kind) {
			cast = append(cast, p)
		} else {
			crew = append(crew, p)
		}
	}
	return cast, crew
}

// mergePeopleUnlocked merges source credits into target, leaving the target's
// cast alone under a Cast lock and its crew alone under a Crew lock. With one
// group locked, the other merges on its own and the result lists cast before
// crew.
func mergePeopleUnlocked(target *[]models.ItemPerson, source []models.ItemPerson, mode MergeMode, castLocked, crewLocked bool) {
	switch {
	case castLocked && crewLocked:
		return
	case !castLocked && !crewLocked:
		mergePeople(target, source, mode)
		return
	}
	targetCast, targetCrew := splitPeopleByRole(*target)
	sourceCast, sourceCrew := splitPeopleByRole(source)
	if castLocked {
		mergePeople(&targetCrew, sourceCrew, mode)
	} else {
		mergePeople(&targetCast, sourceCast, mode)
	}
	*target = append(targetCast, targetCrew...)
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
	indexes := personMatchIndexes(*target)
	for _, sp := range source {
		if idx, ok := findPersonMatch(sp, indexes...); ok {
			mergePersonFields(&(*target)[idx], sp)
		} else {
			*target = append(*target, sp)
		}
	}
}

// enrichPeople fills IDs, photos and roles on the target's cast (cast true)
// or crew from the source credits that match them by provider ID or by name
// in the same role group (findPersonMatch). Matching stays within cast or
// crew, so one person credited as both actor and director is enriched once
// per credit. Unlike mergePeople it never adds a credit: the target list is
// authoritative.
func enrichPeople(target []models.ItemPerson, source []models.ItemPerson, cast bool) {
	if len(target) == 0 || len(source) == 0 {
		return
	}
	var positions []int
	var group []models.ItemPerson
	for i, p := range target {
		if isCastKind(p.Kind) == cast {
			positions = append(positions, i)
			group = append(group, p)
		}
	}
	indexes := personMatchIndexes(group)
	for _, sp := range source {
		if idx, ok := findPersonMatch(sp, indexes...); ok {
			mergePersonFields(&target[positions[idx]], sp)
		}
	}
}

// personMatchIndexes indexes credits by each key findPersonMatch tries, in
// its order: TMDB, TVDB and IMDb IDs, Plex GUID, then name, each within the
// credit's role group.
func personMatchIndexes(people []models.ItemPerson) []map[string]int {
	return []map[string]int{
		indexPeopleBy(people, func(p models.ItemPerson) string { return creditKey(p.TmdbID, creditGroup(p.Kind)) }),
		indexPeopleBy(people, func(p models.ItemPerson) string { return creditKey(p.TvdbID, creditGroup(p.Kind)) }),
		indexPeopleBy(people, func(p models.ItemPerson) string { return creditKey(p.ImdbID, creditGroup(p.Kind)) }),
		indexPeopleBy(people, func(p models.ItemPerson) string { return creditKey(p.PlexGUID, creditGroup(p.Kind)) }),
		indexPeopleBy(people, func(p models.ItemPerson) string {
			return creditKey(strings.ToLower(p.Name), creditGroup(p.Kind))
		}),
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

// findPersonMatch finds sp's credit among the indexed target credits: the same
// person by provider ID, else by name, in the same role group. One person
// credited as both actor and director has two credits, so a match must not
// fold a director credit into the actor's; an actor one provider lists as a
// guest star is still one cast credit.
func findPersonMatch(sp models.ItemPerson, maps ...map[string]int) (int, bool) {
	group := creditGroup(sp.Kind)
	keys := []string{
		creditKey(sp.TmdbID, group), creditKey(sp.TvdbID, group), creditKey(sp.ImdbID, group),
		creditKey(sp.PlexGUID, group), creditKey(strings.ToLower(sp.Name), group),
	}
	for i, m := range maps {
		if i < len(keys) {
			if idx, ok := m[keys[i]]; ok && keys[i] != "" {
				return idx, true
			}
		}
	}
	return 0, false
}

// creditGroup is the role group a credit match stays within: the cast
// (actors and guest stars) as one, each crew kind on its own.
func creditGroup(kind models.PersonKind) models.PersonKind {
	if kind == models.PersonKindGuestStar {
		return models.PersonKindActor
	}
	return kind
}

// creditKey keys a credit by a person identifier and its kind, or "" when the
// identifier is empty.
func creditKey(identifier string, kind models.PersonKind) string {
	if identifier == "" {
		return ""
	}
	return identifier + "|" + strconv.Itoa(int(kind))
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
	if dst.PhotoSourcePath == "" {
		dst.PhotoSourcePath = src.PhotoSourcePath
	}
	if dst.PhotoThumbhash == "" {
		dst.PhotoThumbhash = src.PhotoThumbhash
	}
	if dst.LocalPhotoSource == "" {
		dst.LocalPhotoSource = src.LocalPhotoSource
	}
	if dst.Character == "" {
		dst.Character = src.Character
	}
}
