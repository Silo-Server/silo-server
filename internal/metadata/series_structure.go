package metadata

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// providerStructure is what one provider returned for a series' seasons and
// episodes in one refresh, in chain order.
type providerStructure struct {
	slug string
	// local marks a provider that reads the library's own files (NFO). Its
	// numbers are the files' numbers, so it merges by number with any order.
	local bool
	// failed marks a provider whose seasons or episodes call failed for a
	// reason other than not knowing the series.
	failed   bool
	seasons  []SeasonResult
	episodes []EpisodeResult
}

// mergeSeriesStructure builds a series' seasons and episodes from the
// providers' results. One remote provider, the structure provider, supplies
// the seasons and the episode numbering; preferred names it. Other remote
// providers number episodes in their own order, so they only fill empty
// fields of episodes they share a provider ID with. Local providers merge by
// number, as the library's files do.
//
// It also returns the structure provider's slug, empty when no remote
// provider numbered the series.
func mergeSeriesStructure(providers []providerStructure, preferred, language string) ([]SeasonResult, []EpisodeResult, string) {
	structure := structureProvider(providers, preferred)
	structureSlug := ""
	if structure != nil {
		structureSlug = structure.slug
	}

	seasonResults := make(map[int]*SeasonResult)
	episodeResults := make(map[episodeResultKey]*EpisodeResult)
	for i := range providers {
		p := &providers[i]
		if !p.local && p != structure {
			continue
		}
		accumulateSeasonResults(seasonResults, p.seasons, language)
		accumulateEpisodeResults(episodeResults, p.episodes, language)
	}

	structureEpisodes := flattenEpisodePointers(episodeResults)
	for i := range providers {
		p := &providers[i]
		if p.local || p == structure {
			continue
		}
		for j, target := range bridgeEpisodes(structureEpisodes, p.episodes) {
			mergeEpisodeInto(target, p.episodes[j], language)
			lendProviderIDs(target, p.episodes[j].ProviderIDs)
		}
	}
	return flattenSeasonResults(seasonResults), flattenEpisodeResults(episodeResults), structureSlug
}

type episodeStructureKey struct{}

// withEpisodeStructure marks a full series refresh whose episodes the
// provider slug numbered, so persisting them renumbers the stored rows to
// match (renumberSeriesEpisodes). Partial episode refetches never carry it.
func withEpisodeStructure(ctx context.Context, slug string) context.Context {
	return context.WithValue(ctx, episodeStructureKey{}, slug)
}

func episodeStructureOf(ctx context.Context) string {
	slug, _ := ctx.Value(episodeStructureKey{}).(string)
	return slug
}

// structureProvider picks the remote provider that numbers the series: the
// preferred one when it has episodes, else the next remote provider in chain
// order that has any. Only providers enabled for the library's episodes are
// in the chain, so a disabled provider is never a fallback. A provider that
// failed stops the search: falling past it would renumber the show to
// another order for as long as it is down, so the refresh takes no remote
// structure and the stored episodes stay.
func structureProvider(providers []providerStructure, preferred string) *providerStructure {
	candidates := make([]*providerStructure, 0, len(providers))
	for i := range providers {
		if !providers[i].local && providers[i].slug == preferred {
			candidates = append(candidates, &providers[i])
		}
	}
	for i := range providers {
		if !providers[i].local && providers[i].slug != preferred {
			candidates = append(candidates, &providers[i])
		}
	}
	for _, candidate := range candidates {
		if candidate.failed {
			return nil
		}
		if len(candidate.episodes) > 0 {
			return candidate
		}
	}
	return nil
}

// bridgeEpisodes finds which structure episode each of another provider's
// episodes is, keyed by index into other. Providers rarely send each other's
// episode IDs, so it works in passes, each over what is still unmatched:
//
//  1. a shared provider ID;
//  2. an air date on which both providers list the same number of episodes,
//     each a consecutive run within one season, paired in episode order
//     (one each is the plain case; two each is a double bill);
//  3. the same numbers on the same date;
//  4. a gap between two matched neighbours: when the other provider has as
//     many unmatched regular episodes between them as the structure provider
//     has between the episodes they matched, pair those in order. This
//     catches air dates that differ by a day across time zones.
//
// Anything else stays unmatched: a wrong match would put one episode's data,
// and later its watch history, on another.
func bridgeEpisodes(structure []*EpisodeResult, other []EpisodeResult) map[int]*EpisodeResult {
	matched := make(map[int]*EpisodeResult)
	taken := make(map[*EpisodeResult]bool)
	pair := func(i int, target *EpisodeResult) {
		matched[i] = target
		taken[target] = true
	}

	byID := make(map[string]*EpisodeResult)
	for _, episode := range structure {
		for key, value := range episode.ProviderIDs {
			if value != "" {
				byID[key+"\x00"+value] = episode
			}
		}
	}
	for i, episode := range other {
		for key, value := range episode.ProviderIDs {
			if target := byID[key+"\x00"+value]; value != "" && target != nil && !taken[target] {
				pair(i, target)
				break
			}
		}
	}

	structureByDate := make(map[string][]*EpisodeResult)
	for _, episode := range structure {
		if date := episodeAirDate(episode.AirDate); date != "" && !taken[episode] {
			structureByDate[date] = append(structureByDate[date], episode)
		}
	}
	otherByDate := make(map[string][]int)
	for i, episode := range other {
		if _, done := matched[i]; done {
			continue
		}
		if date := episodeAirDate(episode.AirDate); date != "" {
			otherByDate[date] = append(otherByDate[date], i)
		}
	}
	for date, indexes := range otherByDate {
		targets := structureByDate[date]
		if len(targets) != len(indexes) {
			continue
		}
		sort.SliceStable(indexes, func(a, b int) bool { return episodeBefore(&other[indexes[a]], &other[indexes[b]]) })
		otherRun := make([]*EpisodeResult, len(indexes))
		for k, i := range indexes {
			otherRun[k] = &other[i]
		}
		if !consecutiveRun(otherRun) || !consecutiveRun(targets) {
			continue
		}
		for k, i := range indexes {
			pair(i, targets[k])
		}
	}

	byNumbers := make(map[episodeResultKey]*EpisodeResult, len(structure))
	for _, episode := range structure {
		byNumbers[episodeResultKey{seasonNumber: episode.SeasonNumber, episodeNumber: episode.EpisodeNumber}] = episode
	}
	for i, episode := range other {
		if _, done := matched[i]; done {
			continue
		}
		target := byNumbers[episodeResultKey{seasonNumber: episode.SeasonNumber, episodeNumber: episode.EpisodeNumber}]
		date := episodeAirDate(episode.AirDate)
		if target != nil && !taken[target] && date != "" && episodeAirDate(target.AirDate) == date {
			pair(i, target)
		}
	}

	bridgeGaps(structure, other, matched, taken, pair)
	return matched
}

// bridgeGaps is pass 4 of bridgeEpisodes, over regular episodes only.
func bridgeGaps(structure []*EpisodeResult, other []EpisodeResult, matched map[int]*EpisodeResult,
	taken map[*EpisodeResult]bool, pair func(int, *EpisodeResult)) {
	var regular []*EpisodeResult
	position := make(map[*EpisodeResult]int)
	for _, episode := range structure {
		if episode.SeasonNumber > 0 {
			position[episode] = len(regular)
			regular = append(regular, episode)
		}
	}
	var order []int
	for i := range other {
		if other[i].SeasonNumber > 0 {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return episodeBefore(&other[order[a]], &other[order[b]]) })

	// pairRun pairs a run of unmatched other episodes with the structure's
	// episodes regular[from:to] when the counts agree and none of those is
	// matched yet.
	pairRun := func(run []int, from, to int) {
		if len(run) == 0 || to-from != len(run) {
			return
		}
		for _, t := range regular[from:to] {
			if taken[t] {
				return
			}
		}
		for n, j := range run {
			pair(j, regular[from+n])
		}
	}

	first, last := -1, -1 // indexes into order of the first and last anchor
	for k, i := range order {
		target, ok := matched[i]
		if !ok {
			continue
		}
		to, inRegular := position[target]
		if !inRegular {
			continue
		}
		if last >= 0 {
			pairRun(order[last+1:k], position[matched[order[last]]]+1, to)
		} else {
			first = k
		}
		last = k
	}
	if first < 0 {
		return
	}
	// The runs before the first anchor and after the last: new episodes at the
	// end are the common case, a day apart across time zones.
	pairRun(order[:first], 0, position[matched[order[first]]])
	pairRun(order[last+1:], position[matched[order[last]]]+1, len(regular))
}

// consecutiveRun reports whether episodes, in order, are consecutive
// episodes of one season.
func consecutiveRun(episodes []*EpisodeResult) bool {
	for k := 1; k < len(episodes); k++ {
		if episodes[k].SeasonNumber != episodes[0].SeasonNumber ||
			episodes[k].EpisodeNumber != episodes[k-1].EpisodeNumber+1 {
			return false
		}
	}
	return true
}

func episodeBefore(a, b *EpisodeResult) bool {
	if a.SeasonNumber != b.SeasonNumber {
		return a.SeasonNumber < b.SeasonNumber
	}
	return a.EpisodeNumber < b.EpisodeNumber
}

// flattenEpisodePointers lists the accumulated episodes in season and
// episode order, as pointers the bridge can fill in.
func flattenEpisodePointers(accumulator map[episodeResultKey]*EpisodeResult) []*EpisodeResult {
	out := make([]*EpisodeResult, 0, len(accumulator))
	for _, episode := range accumulator {
		out = append(out, episode)
	}
	sort.Slice(out, func(i, j int) bool { return episodeBefore(out[i], out[j]) })
	return out
}

// episodeAirDate is the date part of a provider air date, empty when unknown.
func episodeAirDate(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 10 {
		return value[:10]
	}
	return ""
}

// lendProviderIDs adds a bridged episode's provider IDs the structure
// episode lacks, so the stored row can be found by them after a later order
// change.
func lendProviderIDs(target *EpisodeResult, ids map[string]string) {
	for key, value := range ids {
		if value == "" || target.ProviderIDs[key] != "" {
			continue
		}
		if target.ProviderIDs == nil {
			target.ProviderIDs = make(map[string]string)
		}
		target.ProviderIDs[key] = value
	}
}

// providerStructures collects providerStructure values in the order providers
// first answer, which is chain order.
type providerStructures struct {
	list  []providerStructure
	index map[string]int
}

func newProviderStructures() *providerStructures {
	return &providerStructures{index: make(map[string]int)}
}

func (s *providerStructures) of(slug string, local bool) *providerStructure {
	if i, ok := s.index[slug]; ok {
		return &s.list[i]
	}
	s.index[slug] = len(s.list)
	s.list = append(s.list, providerStructure{slug: slug, local: local})
	return &s.list[len(s.list)-1]
}

// seasonNumbers lists every season any provider returned, sorted, so each
// provider is asked for the episodes of all of them.
func (s *providerStructures) seasonNumbers() []int {
	seen := make(map[int]bool)
	var out []int
	for _, p := range s.list {
		for _, season := range p.seasons {
			if !seen[season.SeasonNumber] {
				seen[season.SeasonNumber] = true
				out = append(out, season.SeasonNumber)
			}
		}
	}
	sort.Ints(out)
	return out
}

// seriesEpisodeOrderProvider returns the provider whose episode order a
// series follows: the setting of the first of its enabled libraries, in
// library order, that picks one. Every library holding the series shares its
// episode rows, so the answer must not depend on which library asked for the
// refresh. Empty means chain order.
func (s *MetadataService) seriesEpisodeOrderProvider(ctx context.Context, contentID string) (string, error) {
	if s == nil || s.libraryRepo == nil || s.folderRepo == nil || contentID == "" {
		return "", nil
	}
	folderIDs, err := s.libraryRepo.GetFolderIDsForItem(ctx, contentID)
	if err != nil {
		return "", fmt.Errorf("loading libraries for the episode order: %w", err)
	}
	if len(folderIDs) == 0 {
		return "", nil
	}
	folders, err := s.folderRepo.ListByIDs(ctx, folderIDs)
	if err != nil {
		return "", fmt.Errorf("loading libraries for the episode order: %w", err)
	}
	sort.SliceStable(folders, func(i, j int) bool {
		if folders[i].SortOrder != folders[j].SortOrder {
			return folders[i].SortOrder < folders[j].SortOrder
		}
		return folders[i].ID < folders[j].ID
	})
	for _, folder := range folders {
		if folder != nil && folder.Enabled && folder.EpisodeOrderProvider != "" {
			return folder.EpisodeOrderProvider, nil
		}
	}
	return "", nil
}
