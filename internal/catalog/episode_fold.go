package catalog

import "github.com/Silo-Server/silo-server/internal/models"

// FoldSpannedEpisodes finds the episodes a season list shows inside the row of
// a multi-episode file (S01E02-E03) rather than as rows of their own. It maps
// each such file's first episode to the later episodes folded into it, in
// list order, and reports every folded episode ID.
//
// An episode folds into an earlier one only when the two always play the same
// video: every present file serving it is a multi-episode file linked to that
// earlier episode, and every file of the earlier episode spans it. An episode
// with a file of its own, or whose first episode also has a version that does
// not reach it, keeps its row. served holds every present file per episode,
// before access filtering, since playback reaches every version. Folding
// stops at the first episode in the range that keeps its row, so a row's
// range and its folded episodes always agree. episodes is in episode order.
func FoldSpannedEpisodes(episodes []*models.Episode, served map[string][]*models.MediaFile) (map[string][]*models.Episode, map[string]bool) {
	byID := make(map[string]*models.Episode, len(episodes))
	for _, ep := range episodes {
		if ep != nil {
			byID[ep.ContentID] = ep
		}
	}
	var parts map[string][]*models.Episode
	folded := map[string]bool{}
	reached := map[string]int{} // the last episode number each row plays so far
	for _, ep := range episodes {
		if ep == nil {
			continue
		}
		first := byID[spanningFirstEpisode(ep.ContentID, served[ep.ContentID])]
		if first == nil || folded[first.ContentID] || first.SeasonNumber != ep.SeasonNumber ||
			!everyFileSpans(first.ContentID, served[first.ContentID], ep.EpisodeNumber) {
			continue
		}
		last, ok := reached[first.ContentID]
		if !ok {
			last = first.EpisodeNumber
		}
		if ep.EpisodeNumber != last+1 {
			continue
		}
		reached[first.ContentID] = ep.EpisodeNumber
		if parts == nil {
			parts = map[string][]*models.Episode{}
		}
		parts[first.ContentID] = append(parts[first.ContentID], ep)
		folded[ep.ContentID] = true
	}
	return parts, folded
}

// spanningFirstEpisode returns the episode every one of files is linked to
// when that is an episode other than episodeID, or "".
func spanningFirstEpisode(episodeID string, files []*models.MediaFile) string {
	first := ""
	for _, f := range files {
		if f == nil || f.EpisodeID == "" || f.EpisodeID == episodeID || (first != "" && f.EpisodeID != first) {
			return ""
		}
		first = f.EpisodeID
	}
	return first
}

// everyFileSpans reports whether files is non-empty and every one is a
// multi-episode file linked to firstID whose range holds episodeNumber.
func everyFileSpans(firstID string, files []*models.MediaFile, episodeNumber int) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if f == nil || f.EpisodeID != firstID || f.MultiEpisodeEnd <= f.MultiEpisodeStart ||
			episodeNumber < f.MultiEpisodeStart || episodeNumber > f.MultiEpisodeEnd {
			return false
		}
	}
	return true
}
