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

// resolve finds the episode an unseasoned file names by its number and the
// episode title after it. showOnly reports that the file names no show title
// beyond its series' own (see namesOnlySeries).
func (index unseasonedEpisodeIndex) resolve(number int, title string, showOnly bool) (*models.Episode, bool) {
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
	// A file that names more than its show is not a plain season-1 episode:
	// a later season numbered per season (Show II - 05, Show Final Season -
	// 05) or an extra the parser read a number from (Show - NCOP 1, Show -
	// OVA - 01, the recap Show - 05.5). That holds even when only season 1
	// has the number, since a later season's metadata may not be stored yet.
	if !showOnly {
		return nil, false
	}
	// Only season 1 reads the same under absolute and per-season numbering,
	// so an absolute number (Show - 05) names its episode there. A number
	// found only in a later season exceeds every earlier season's length, so
	// an absolute reading would place it elsewhere. Requiring season 1 also
	// defers links while season 1 metadata has not been stored yet.
	var selected *models.Episode
	for _, match := range index.byNumber[number] {
		if match.SeasonNumber == 1 {
			selected = match
			break
		}
	}
	if selected == nil {
		return nil, false
	}
	if !isGenericEpisodeMatchTitle(title) && normalizeTitleForScoring(title) != normalizeTitleForScoring(selected.Title) {
		return nil, false
	}
	return selected, true
}

// namesOnlySeries reports whether showTitle, the show title an unseasoned
// file gives that its show folder doesn't (naming's EpisodeShowTitle), names
// nothing beyond series. An empty showTitle names nothing more.
func namesOnlySeries(showTitle string, series *models.MediaItem) bool {
	if showTitle == "" {
		return true
	}
	if series == nil {
		return false
	}
	want := normalizeTitleForScoring(showTitle)
	for _, title := range []string{series.Title, series.OriginalTitle} {
		if title != "" && normalizeTitleForScoring(title) == want {
			return true
		}
	}
	return false
}
