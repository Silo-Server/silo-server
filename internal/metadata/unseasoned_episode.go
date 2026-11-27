package metadata

import (
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// unseasonedEpisodeIndex resolves episode-only names against metadata already
// loaded for the matched series. Episode zero, specials, and scanner fallback
// rows cannot establish the season of an otherwise unseasoned filename.
//
// With absolute set (an admin chose the series' episode order), a number
// counts through the regular episodes of the provider's default order
// instead: Show - 045 is its 45th episode.
type unseasonedEpisodeIndex struct {
	byNumber   map[int][]*models.Episode
	byTitle    map[string][]*models.Episode
	byAbsolute map[int]*models.Episode
}

func newUnseasonedEpisodeIndex(episodes []*models.Episode, absolute bool) unseasonedEpisodeIndex {
	index := unseasonedEpisodeIndex{
		byNumber: make(map[int][]*models.Episode),
		byTitle:  make(map[string][]*models.Episode),
	}
	if absolute {
		index.byAbsolute = absoluteEpisodePositions(episodes)
	}
	for _, episode := range episodes {
		if episode == nil || episode.SeasonNumber <= 0 || episode.EpisodeNumber <= 0 {
			continue
		}
		source := strings.TrimSpace(episode.MetadataSource)
		if strings.EqualFold(source, "scanner_fallback") || source == "" && !episodeHasProviderMatch(episode) {
			continue
		}
		index.byNumber[episode.EpisodeNumber] = append(index.byNumber[episode.EpisodeNumber], episode)
		if !isGenericEpisodeMatchTitle(episode.Title) {
			title := normalizeTitleForScoring(episode.Title)
			index.byTitle[title] = append(index.byTitle[title], episode)
		}
	}
	return index
}

func (index unseasonedEpisodeIndex) resolve(number int, title string) (*models.Episode, bool) {
	if number <= 0 {
		return nil, false
	}
	if index.byAbsolute != nil {
		episode, ok := index.byAbsolute[number]
		return episode, ok
	}
	if isDistinctiveSingleEpisodeTitle(title) {
		// A distinctive exact title can resolve absolute numbering without
		// inventing a cumulative order from potentially incomplete seasons.
		matches := index.byTitle[normalizeTitleForScoring(title)]
		if len(matches) != 1 {
			return nil, false
		}
		if len(index.byNumber[number]) > 0 && matches[0].EpisodeNumber != number {
			return nil, false
		}
		return matches[0], true
	}
	matches := index.byNumber[number]
	// A number found only in a later season exceeds every earlier season's
	// length, so an absolute reading would place it elsewhere. Only season 1
	// reads the same under absolute and per-season numbering. Requiring it
	// also defers links while season 1 metadata has not been stored yet.
	if len(matches) != 1 || matches[0].SeasonNumber != 1 {
		return nil, false
	}
	if !isGenericEpisodeMatchTitle(title) && normalizeTitleForScoring(title) != normalizeTitleForScoring(matches[0].Title) {
		return nil, false
	}
	return matches[0], true
}

// absoluteEpisodePositions numbers the regular provider episodes 1…N in their
// provider's default order (ProviderSeasonNumber, else the shown numbers).
func absoluteEpisodePositions(episodes []*models.Episode) map[int]*models.Episode {
	type placed struct {
		season, number int
		episode        *models.Episode
	}
	var regular []placed
	for _, episode := range episodes {
		if episode == nil || strings.EqualFold(strings.TrimSpace(episode.MetadataSource), "scanner_fallback") {
			continue
		}
		season, number := episode.SeasonNumber, episode.EpisodeNumber
		if episode.ProviderSeasonNumber != nil && episode.ProviderEpisodeNumber != nil {
			season, number = *episode.ProviderSeasonNumber, *episode.ProviderEpisodeNumber
		}
		if season <= 0 || number <= 0 {
			continue
		}
		regular = append(regular, placed{season, number, episode})
	}
	sort.SliceStable(regular, func(i, j int) bool {
		if regular[i].season != regular[j].season {
			return regular[i].season < regular[j].season
		}
		return regular[i].number < regular[j].number
	})
	positions := make(map[int]*models.Episode, len(regular))
	for i, p := range regular {
		positions[i+1] = p.episode
	}
	return positions
}
