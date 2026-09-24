package metadata

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// unseasonedEpisodeIndex resolves episode-only names against metadata already
// loaded for the matched series. Episode zero, specials, and scanner fallback
// rows cannot establish the season of an otherwise unseasoned filename.
type unseasonedEpisodeIndex struct {
	byNumber map[int][]*models.Episode
	byTitle  map[string][]*models.Episode
}

func newUnseasonedEpisodeIndex(episodes []*models.Episode) unseasonedEpisodeIndex {
	index := unseasonedEpisodeIndex{
		byNumber: make(map[int][]*models.Episode),
		byTitle:  make(map[string][]*models.Episode),
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
