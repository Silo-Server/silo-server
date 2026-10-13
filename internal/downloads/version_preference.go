package downloads

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// versionPreference chooses the file for a download that names none. It
// follows what the profile watches rather than the largest file: the file last
// played for the item itself, then, for episodes, the version closest to the
// one last played anywhere in the series, then the highest resolution. A
// request that sends media_file_id never reaches it.
type versionPreference struct {
	// lastFile maps an item's content ID to the file the profile last played.
	lastFile map[string]int
	// series is the most recently updated progress row in the series that
	// carries version hints; nil for movies or an unwatched series.
	series *userstore.WatchProgress
}

func (p versionPreference) pick(itemID string, files []*models.MediaFile) *models.MediaFile {
	if len(files) == 0 {
		return nil
	}
	if id, ok := p.lastFile[itemID]; ok {
		for _, f := range files {
			if f.ID == id {
				return f
			}
		}
	}
	best, bestScore := files[0], p.seriesScore(files[0])
	for _, f := range files[1:] {
		score := p.seriesScore(f)
		if score > bestScore || (score == bestScore && preferredByDefault(f, best)) {
			best, bestScore = f, score
		}
	}
	return best
}

// seriesScore weighs how closely f matches the version last played in the
// series. Each episode is a different file, so the match is on the recorded
// traits, edition first, then resolution, HDR and video codec.
func (p versionPreference) seriesScore(f *models.MediaFile) int {
	h := p.series
	if h == nil {
		return 0
	}
	score := 0
	if nonEmpty(h.LastEditionKey) && f.EditionKey == *h.LastEditionKey {
		score += 8
	}
	if nonEmpty(h.LastResolution) && strings.EqualFold(f.Resolution, *h.LastResolution) {
		score += 4
	}
	if h.LastHDR != nil && f.HDR == *h.LastHDR {
		score += 2
	}
	if nonEmpty(h.LastCodecVideo) && strings.EqualFold(f.CodecVideo, *h.LastCodecVideo) {
		score++
	}
	return score
}

// preferredByDefault orders files no preference separates: highest resolution
// first (access.CompareQuality is the ordering playback uses), then lowest ID
// so the choice never depends on query order.
func preferredByDefault(a, b *models.MediaFile) bool {
	if c := access.CompareQuality(a.Resolution, b.Resolution); c != 0 {
		return c > 0
	}
	return a.ID < b.ID
}

// allowedCandidates narrows files to those the profile may play, so an
// automatic pick never registers a version its access rules would refuse to
// serve. When none qualify the full list is kept, and serving reports the
// refusal as before.
func allowedCandidates(files []*models.MediaFile, filter catalog.AccessFilter) []*models.MediaFile {
	if allowed := catalog.FilterMediaFilesByAccess(files, filter); len(allowed) > 0 {
		return allowed
	}
	return files
}

func nonEmpty(s *string) bool { return s != nil && *s != "" }

func hasVersionHints(p userstore.WatchProgress) bool {
	return nonEmpty(p.LastEditionKey) || nonEmpty(p.LastResolution) || p.LastHDR != nil || nonEmpty(p.LastCodecVideo)
}

// seriesHistoryWindow bounds the series lookup to the profile's most recent
// progress rows. A series' latest play almost always sits inside it; an older
// one falls back to the highest-resolution default. Either way each request
// costs the same whatever the series length, so paging a long series stays
// linear.
const seriesHistoryWindow = 200

// loadVersionPreference reads the profile's progress for itemIDs and, when
// seriesID is set, finds the series' most recently played version among the
// profile's latest progress rows. It is best effort: with no profile, no
// progress store, or a failed read, downloads fall back to the highest
// resolution instead of failing.
func (s *Service) loadVersionPreference(ctx context.Context, userID int, profileID, seriesID string, itemIDs []string) versionPreference {
	if s.progressStores == nil || profileID == "" || len(itemIDs) == 0 {
		return versionPreference{}
	}
	store, err := s.progressStores.ForUser(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "download version preference: opening progress store", "component", "downloads", "error", err)
		return versionPreference{}
	}
	pref := versionPreference{lastFile: map[string]int{}}
	for start := 0; start < len(itemIDs); start += watchedLookupChunk {
		progress, err := store.ListProgressByMediaItems(ctx, profileID, itemIDs[start:min(start+watchedLookupChunk, len(itemIDs))])
		if err != nil {
			slog.WarnContext(ctx, "download version preference: listing progress", "component", "downloads", "error", err)
			return versionPreference{}
		}
		for id, row := range progress {
			if row.LastFileID != nil {
				pref.lastFile[id] = *row.LastFileID
			}
		}
	}
	if seriesID != "" {
		pref.series = s.latestSeriesPlay(ctx, store, profileID, seriesID)
	}
	return pref
}

// latestSeriesPlay returns the newest progress row with version hints that
// belongs to an episode of seriesID, or nil.
func (s *Service) latestSeriesPlay(ctx context.Context, store userstore.UserStore, profileID, seriesID string) *userstore.WatchProgress {
	recent, err := store.ListProgressPage(ctx, profileID, "", nil, seriesHistoryWindow)
	if err != nil {
		slog.WarnContext(ctx, "download version preference: listing recent progress", "component", "downloads", "error", err)
		return nil
	}
	hinted := make([]userstore.WatchProgress, 0, len(recent))
	ids := make([]string, 0, len(recent))
	for _, row := range recent {
		if hasVersionHints(row) {
			hinted = append(hinted, row)
			ids = append(ids, row.MediaItemID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// Episode files carry their series' content ID, so one lookup tells
	// which recent rows are episodes of this series.
	files, err := s.fileRepo.ListByEpisodeIDs(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "download version preference: resolving recent episodes", "component", "downloads", "error", err)
		return nil
	}
	var latest *userstore.WatchProgress
	for _, row := range hinted {
		episodeFiles := files[row.MediaItemID]
		if len(episodeFiles) == 0 || episodeFiles[0].ContentID != seriesID {
			continue
		}
		if latest == nil || newerProgress(row, *latest) {
			latest = &row
		}
	}
	return latest
}

// newerProgress compares UpdatedAt in the store's own form, which every store
// writes as a fixed-width UTC timestamp that sorts lexically; the content ID
// breaks ties so the choice is stable.
func newerProgress(a, b userstore.WatchProgress) bool {
	if a.UpdatedAt != b.UpdatedAt {
		return a.UpdatedAt > b.UpdatedAt
	}
	return a.MediaItemID > b.MediaItemID
}
