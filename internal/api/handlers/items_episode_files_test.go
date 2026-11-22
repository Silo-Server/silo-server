package handlers

import (
	"context"
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
