package handlers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ceilingTestFiles serves fixed episode file lists to buildEpisodeResponses.
type ceilingTestFiles struct {
	byEpisode map[string][]*models.MediaFile
}

func (f ceilingTestFiles) GetByContentID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (f ceilingTestFiles) GetByEpisodeID(_ context.Context, id string) ([]*models.MediaFile, error) {
	return f.byEpisode[id], nil
}

func (f ceilingTestFiles) ListByContentIDs(context.Context, []string) (map[string][]*models.MediaFile, error) {
	return nil, nil
}

func (f ceilingTestFiles) ListEpisodeListFilesByEpisodeIDs(context.Context, []string) (map[string][]*models.MediaFile, error) {
	return f.byEpisode, nil
}

// A season list leaves out an episode whose every version exceeds the
// viewer's playback-quality ceiling, since none of them can play. A version
// in another library still counts: playback reaches every version even when
// the page shows only the browsed library's.
func TestSeasonListHidesEpisodesAboveQualityCeiling(t *testing.T) {
	browsed, other := 1, 2
	h := &ItemsHandler{fileRepo: ceilingTestFiles{byEpisode: map[string][]*models.MediaFile{
		"e1": {{ID: 1, MediaFolderID: browsed, Resolution: "1080p"}},
		"e2": {{ID: 2, MediaFolderID: browsed, Resolution: "2160p"}},
		"e3": {{ID: 3, MediaFolderID: browsed, Resolution: "2160p"}, {ID: 4, MediaFolderID: other, Resolution: "1080p"}},
	}}}
	episodes := []*models.Episode{{ContentID: "e1"}, {ContentID: "e2"}, {ContentID: "e3"}}
	ids := func(access catalog.AccessFilter) string {
		var got []string
		for _, ep := range h.buildEpisodeResponses(t.Context(), ItemViewer{Access: access}, episodes) {
			got = append(got, ep.ContentID)
		}
		return strings.Join(got, ",")
	}
	if got := ids(catalog.AccessFilter{}); got != "e1,e2,e3" {
		t.Fatalf("no ceiling: episodes = %s, want e1,e2,e3", got)
	}
	capped := catalog.AccessFilter{MaxPlaybackQuality: "1080p", PresentationLibraryID: &browsed, ScopeFilesToLibrary: true}
	if got := ids(capped); got != "e1,e3" {
		t.Fatalf("1080p ceiling: episodes = %s, want e1,e3", got)
	}
}

// A season list shows a multi-episode file once: an episode the file spans
// leaves the list and its number range and parts move onto the file's first
// episode. It folds only when nothing else plays it: an episode with a file of
// its own stays, and so does one the first episode has a version without.
func TestSeasonListFoldsEpisodesOfMultiEpisodeFiles(t *testing.T) {
	multi := func(id int, first string, start, end int) *models.MediaFile {
		return &models.MediaFile{ID: id, EpisodeID: first, MediaFolderID: 1, Resolution: "1080p", MultiEpisodeStart: start, MultiEpisodeEnd: end}
	}
	single := func(id int, episode string) *models.MediaFile {
		return &models.MediaFile{ID: id, EpisodeID: episode, MediaFolderID: 1, Resolution: "1080p"}
	}
	h := &ItemsHandler{fileRepo: ceilingTestFiles{byEpisode: map[string][]*models.MediaFile{
		"e1": {single(1, "e1")},
		"e2": {multi(2, "e2", 2, 3)},
		"e3": {multi(2, "e2", 2, 3)},
		// E4 has a single-episode version too, which does not play E5.
		"e4": {multi(4, "e4", 4, 5), single(5, "e4")},
		"e5": {multi(4, "e4", 4, 5)},
		// E7 has a file of its own besides the E6-E7 file.
		"e6": {multi(6, "e6", 6, 7)},
		"e7": {multi(6, "e6", 6, 7), single(7, "e7")},
	}}}
	var episodes []*models.Episode
	for n := 1; n <= 7; n++ {
		id := fmt.Sprintf("e%d", n)
		episodes = append(episodes, &models.Episode{ContentID: id, SeasonNumber: 1, EpisodeNumber: n, Title: "Title " + id, Overview: "Overview " + id})
	}

	got := h.buildEpisodeResponses(t.Context(), ItemViewer{}, episodes)
	var ids []string
	for _, ep := range got {
		ids = append(ids, ep.ContentID)
	}
	if strings.Join(ids, ",") != "e1,e2,e4,e5,e6,e7" {
		t.Fatalf("episodes = %v, want e3 folded into e2 only", ids)
	}
	for _, ep := range got {
		wantEnd := 0
		if ep.ContentID == "e2" {
			wantEnd = 3
		}
		if ep.EpisodeNumberEnd != wantEnd {
			t.Errorf("%s episode_number_end = %d, want %d", ep.ContentID, ep.EpisodeNumberEnd, wantEnd)
		}
	}
	parts := got[1].SpannedEpisodes
	if len(parts) != 1 || parts[0].ContentID != "e3" || parts[0].EpisodeNumber != 3 || parts[0].Title != "Title e3" || parts[0].Overview != "Overview e3" {
		t.Fatalf("e2 spanned episodes = %+v, want e3 with its title and overview", parts)
	}
}
