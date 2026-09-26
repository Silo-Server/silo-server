package scanner

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestFlatSeriesFilesKeepSeparateObservedRootsAndSharedShowGroups(t *testing.T) {
	paths := []string{
		"/tv/downloads/Show.One.S01E01.mkv",
		"/tv/downloads/Show.One.S01E02.mkv",
		"/tv/downloads/Show.Two.S01E01.mkv",
	}
	roots := inferRootAssignments(paths, "series", 1, nil, "/tv/downloads")
	if len(roots.Snapshots) != 3 {
		t.Fatalf("flat file roots = %d; want isolated roots for root-based matching", len(roots.Snapshots))
	}
	for _, path := range paths {
		if roots.Assignments[path].RootPath != path {
			t.Fatalf("file %q shares an unsafe observed root %q", path, roots.Assignments[path].RootPath)
		}
	}
	groups := inferGroupAssignments(paths, "series", 1, roots.Assignments, nil)
	if len(groups.ScannedGroups) != 2 {
		t.Fatalf("show groups = %d; want 2", len(groups.ScannedGroups))
	}
	for _, group := range groups.ScannedGroups {
		if group.State != "resolved" || (group.BaseTitle != "Show One" && group.BaseTitle != "Show Two") {
			t.Fatalf("unexpected show group: %+v", group)
		}
	}
}

func TestSeriesFolderKeepsEpisodeTitlePrefixOutOfShowIdentity(t *testing.T) {
	paths := []string{
		"/tv/The X-Files/Pilot - S01E01.mkv",
		"/tv/The X-Files/Deep Throat - S01E02.mkv",
	}
	roots := inferRootAssignments(paths, "series", 1, nil, "/tv")
	groups := inferGroupAssignments(paths, "series", 1, roots.Assignments, nil)
	if len(roots.Snapshots) != 1 || len(groups.ScannedGroups) != 1 {
		t.Fatalf("episode titles split a show: %d roots, %d groups", len(roots.Snapshots), len(groups.ScannedGroups))
	}
	for index, path := range paths {
		var file models.MediaFile
		populateScanIdentity(&file, path, "series", roots.Assignments[path], groups.Assignments[path], nil)
		if file.BaseTitle != "The X-Files" || file.ObservedRootPath != "/tv/The X-Files" || file.SeasonNumber != 1 || file.EpisodeNumber != index+1 {
			t.Fatalf("episode title replaced show identity: %+v", file)
		}
	}
}

func TestFlatSeriesRangeUsesConfiguredLibraryBoundary(t *testing.T) {
	const root = "/media/Season 21"
	const path = root + "/Example.Show.301-E05.mkv"
	roots := inferRootAssignments([]string{path}, "series", 1, nil, root)
	groups := inferGroupAssignments([]string{path}, "series", 1, roots.Assignments, nil)
	var file models.MediaFile
	populateScanIdentity(&file, path, "series", roots.Assignments[path], groups.Assignments[path], nil)
	if file.BaseTitle != "Example Show" || file.SeasonNumber != 3 || file.EpisodeNumber != 1 || file.MultiEpisodeStart != 1 || file.MultiEpisodeEnd != 5 {
		t.Fatalf("library directory changed episode or range coordinates: %+v", file)
	}
}
