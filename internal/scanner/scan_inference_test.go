package scanner

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// batchedInference runs paths through the batch-at-a-time inference a full
// scan uses and returns its snapshots with every batch's assignments.
func batchedInference(
	paths []string,
	batchSize int,
	libraryType string,
	folderID int,
	rootOverrides map[string]models.MediaRootOverride,
	identityOverrides *identityOverrideSet,
	libraryRoots ...string,
) (rootInferenceResult, groupInferenceResult) {
	roots := newScanRootInference(libraryType, folderID, rootOverrides, libraryRoots...)
	groups := newScanGroupInference(libraryType, folderID, identityOverrides)
	rootAssignments := map[string]fileRootAssignment{}
	groupAssignments := map[string]fileGroupAssignment{}
	for batch := range slices.Chunk(paths, batchSize) {
		batchRoots := roots.add(batch)
		maps.Copy(rootAssignments, batchRoots)
		maps.Copy(groupAssignments, groups.add(batch, batchRoots))
	}
	rootResult := roots.result()
	rootResult.Assignments = rootAssignments
	groupResult := groups.result()
	groupResult.Assignments = groupAssignments
	return rootResult, groupResult
}

// A full scan infers roots and groups a batch of files at a time. Groups,
// observed locations and library-root series span batches, so whatever the
// batch size the snapshots must equal the ones a single pass over every file
// produces: file counts, the first file's sample and identity, ambiguity, the
// primary location and the evidence.
//
// Groups are compared with inferGroupAssignments, which still tallies a whole
// list itself. Roots have one implementation, so for them this checks only
// that the batch size does not matter; internal/naming pins what a root
// snapshot holds.
func TestBatchedInferenceMatchesSinglePass(t *testing.T) {
	t.Parallel()

	const libraryRoot = "/lib"
	for _, tc := range []struct {
		name        string
		libraryType string
		paths       []string
	}{
		{
			name:        "series",
			libraryType: "series",
			paths: []string{
				// One show split across two top-level folders by the same tag.
				"/lib/Alpha (2019) [tvdb-81189]/Season 01/Alpha - S01E01.mkv",
				"/lib/Alpha (2019) [tvdb-81189]/Season 01/Alpha - S01E02.mkv",
				"/lib/Beta/Season 01/Beta - S01E01.mkv",
				"/lib/Beta/Season 01/Beta - S01E02-E03.mkv",
				"/lib/Beta/random.mkv",
				"/lib/Alpha S2 [tvdb-81189]/Season 02/Alpha - S02E01.mkv",
				"/lib/Alpha S2 [tvdb-81189]/Season 02/Alpha - S02E02.mkv",
				// Season folders directly under the library root make the
				// library root itself a series root.
				"/lib/Season 01/Loose - S01E01.mkv",
				"/lib/Gamma (2020)/Gamma (2020) - S01E01.mkv",
				"/lib/Season 02/Loose - S02E01.mkv",
				"/lib/Loose - S03E01.mkv",
				"/lib/Zeta/Zeta - 2026-04-24.mkv",
				"/lib/Zeta/Specials/Zeta - S00E01.mkv",
			},
		},
		{
			name:        "movies",
			libraryType: "movies",
			paths: []string{
				"/lib/Blade Runner (1982) {edition-Director's Cut}.mp4",
				"/lib/Heat (1995)/Heat (1995) - part1.mkv",
				"/lib/Heat (1995)/Heat (1995) - part2.mkv",
				"/lib/Blade Runner (1982) {edition-Final Cut}.mp4",
				"/lib/Shelf A/Dune (2021) {tmdb-438631}/Dune (2021).mkv",
				"/lib/Mixed Bag/Alien (1979).mkv",
				"/lib/Mixed Bag/Aliens (1986).mkv",
				"/lib/Mixed Bag/Heat (1995) - 4K.mkv",
				"/lib/Shelf B/Dune (2021) {tmdb-438631}/Dune (2021) - 1080p.mkv",
				"/lib/Untitled/01.mkv",
				"/lib/Heat.1995.1080p.BluRay.x264-GRP/heat.mkv",
				"/lib/Dune (2021) [tmdb-438631].mkv",
			},
		},
		{
			name:        "mixed",
			libraryType: "mixed",
			paths: []string{
				"/lib/Alpha (2019)/Season 01/Alpha - S01E01.mkv",
				"/lib/Alpha (2019)/Alpha (2019).mkv",
				"/lib/Heat (1995)/Heat (1995).mkv",
				"/lib/Alpha (2019)/Season 02/Alpha - S02E01.mkv",
				"/lib/Heat (1995)/Heat - S01E01.mkv",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rootOverrides := map[string]models.MediaRootOverride{
				"/lib/Beta": {RootPath: "/lib/Beta", ForcedTitle: "Beta Forced", ForcedYear: 2001, ForcedTvdbID: "7"},
			}
			identityOverrides := newIdentityOverrideSet([]models.MediaIdentityOverride{
				{Scope: models.IdentityOverrideScopeFile, FilePath: "/lib/Mixed Bag/Aliens (1986).mkv", ForcedTitle: "Aliens Special", ForcedYear: 1990},
				{Scope: models.IdentityOverrideScopeRoot, RootPath: "/lib/Zeta", ForcedTitle: "Zeta Forced", ForcedTvdbID: "9"},
			})

			wantRoots := inferRootAssignments(tc.paths, tc.libraryType, 3, rootOverrides, libraryRoot)
			wantGroups := inferGroupAssignments(tc.paths, tc.libraryType, 3, wantRoots.Assignments, identityOverrides)
			spanning := false
			for _, group := range wantGroups.ScannedGroups {
				spanning = spanning || group.ObservedFileCount > 1
			}
			if !spanning {
				t.Fatal("fixture has no group with more than one file")
			}

			for batchSize := 1; batchSize <= len(tc.paths); batchSize++ {
				gotRoots, gotGroups := batchedInference(tc.paths, batchSize, tc.libraryType, 3, rootOverrides, identityOverrides, libraryRoot)
				if !reflect.DeepEqual(gotRoots, wantRoots) {
					t.Errorf("batch size %d: root inference differs\n got %+v\nwant %+v", batchSize, gotRoots, wantRoots)
				}
				if !reflect.DeepEqual(gotGroups, wantGroups) {
					t.Errorf("batch size %d: group inference differs\n got %+v\nwant %+v", batchSize, gotGroups, wantGroups)
				}
			}
		})
	}
}
