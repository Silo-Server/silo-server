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

	bridge := newEpisodeBridge(episodeResults)
	for i := range providers {
		p := &providers[i]
		if p.local || p == structure {
			continue
		}
		bridge.countOther(p.episodes)
		for _, episode := range p.episodes {
			target := bridge.match(episode)
			if target == nil {
				continue
			}
			mergeEpisodeInto(target, episode, language)
			lendProviderIDs(target, episode.ProviderIDs)
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

// episodeBridge finds the structure episode another provider's episode is.
// Providers rarely send each other's episode IDs, so after a shared ID it
// goes by air date: a date both providers give exactly one episode, else the
// same numbers on the same date. Anything else stays unmatched: a wrong match
// would put one episode's data, and later its watch history, on another.
type episodeBridge struct {
	byID         map[string]*EpisodeResult
	byDate       map[string][]*EpisodeResult
	byNumbers    map[episodeResultKey]*EpisodeResult
	otherPerDate map[string]int
}

func newEpisodeBridge(structure map[episodeResultKey]*EpisodeResult) *episodeBridge {
	b := &episodeBridge{
		byID:      make(map[string]*EpisodeResult),
		byDate:    make(map[string][]*EpisodeResult),
		byNumbers: make(map[episodeResultKey]*EpisodeResult, len(structure)),
	}
	for key, episode := range structure {
		b.byNumbers[key] = episode
		for idKey, value := range episode.ProviderIDs {
			if value != "" {
				b.byID[idKey+"\x00"+value] = episode
			}
		}
		if date := episodeAirDate(episode.AirDate); date != "" {
			b.byDate[date] = append(b.byDate[date], episode)
		}
	}
	return b
}

// countOther records how many episodes one other provider airs per date, so
// match can tell a unique date from a shared one.
func (b *episodeBridge) countOther(episodes []EpisodeResult) {
	b.otherPerDate = make(map[string]int)
	for _, episode := range episodes {
		if date := episodeAirDate(episode.AirDate); date != "" {
			b.otherPerDate[date]++
		}
	}
}

func (b *episodeBridge) match(episode EpisodeResult) *EpisodeResult {
	for idKey, value := range episode.ProviderIDs {
		if value == "" {
			continue
		}
		if target := b.byID[idKey+"\x00"+value]; target != nil {
			return target
		}
	}
	date := episodeAirDate(episode.AirDate)
	if date == "" {
		return nil
	}
	if candidates := b.byDate[date]; len(candidates) == 1 && b.otherPerDate[date] == 1 {
		return candidates[0]
	}
	target := b.byNumbers[episodeResultKey{seasonNumber: episode.SeasonNumber, episodeNumber: episode.EpisodeNumber}]
	if target != nil && episodeAirDate(target.AirDate) == date {
		return target
	}
	return nil
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
