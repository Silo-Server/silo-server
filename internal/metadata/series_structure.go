package metadata

import "sort"

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
func mergeSeriesStructure(providers []providerStructure, preferred, language string) ([]SeasonResult, []EpisodeResult) {
	structure := structureProvider(providers, preferred)

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

	byID := make(map[string]*EpisodeResult)
	for _, episode := range episodeResults {
		for key, value := range episode.ProviderIDs {
			if value != "" {
				byID[key+"\x00"+value] = episode
			}
		}
	}
	for i := range providers {
		p := &providers[i]
		if p.local || p == structure {
			continue
		}
		for _, episode := range p.episodes {
			if target := sharedEpisode(byID, episode); target != nil {
				mergeEpisodeInto(target, episode, language)
			}
		}
	}
	return flattenSeasonResults(seasonResults), flattenEpisodeResults(episodeResults)
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

// sharedEpisode finds the structure episode that carries one of episode's
// provider IDs.
func sharedEpisode(byID map[string]*EpisodeResult, episode EpisodeResult) *EpisodeResult {
	for key, value := range episode.ProviderIDs {
		if value == "" {
			continue
		}
		if target := byID[key+"\x00"+value]; target != nil {
			return target
		}
	}
	return nil
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
