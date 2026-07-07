package scanner

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestInferGroupAssignments_FlatLooseMovieEditionsCollapse(t *testing.T) {
	t.Parallel()

	filePaths := []string{
		"/Media/Movies/Blade Runner (1982) {edition-Director's Cut}.mp4",
		"/Media/Movies/Blade Runner (1982) {edition-Final Cut}.mp4",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	groupInference := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, nil)

	if got, want := len(groupInference.ScannedGroups), 1; got != want {
		t.Fatalf("len(ScannedGroups) = %d, want %d", got, want)
	}

	first := groupInference.Assignments[filePaths[0]]
	second := groupInference.Assignments[filePaths[1]]
	if first.ContentGroupKey == "" || second.ContentGroupKey == "" {
		t.Fatal("content group key should not be empty")
	}
	if first.ContentGroupKey != second.ContentGroupKey {
		t.Fatalf("group keys differ: %q != %q", first.ContentGroupKey, second.ContentGroupKey)
	}
	if got, want := groupInference.ScannedGroups[0].BaseTitle, "Blade Runner"; got != want {
		t.Fatalf("BaseTitle = %q, want %q", got, want)
	}
	if got, want := groupInference.ScannedGroups[0].BaseYear, 1982; got != want {
		t.Fatalf("BaseYear = %d, want %d", got, want)
	}
	if got, want := groupInference.ScannedGroups[0].State, "resolved"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
}

func TestInferGroupAssignments_AnchormanEditionNoiseStaysResolved(t *testing.T) {
	t.Parallel()

	filePaths := []string{
		"/srv/media/movies/alt-cuts/1080p/Anchorman 2 The Legend Continues (2013)/Anchorman.2.The.Legend.Continues.Super-Sized.R-Rated.Version.2013.Bluray.Remux.1080p.AVC.DTS-HD.MA.5.1-HiFi.mkv",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	groupInference := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, nil)

	if got, want := len(groupInference.ScannedGroups), 1; got != want {
		t.Fatalf("len(ScannedGroups) = %d, want %d", got, want)
	}
	group := groupInference.ScannedGroups[0]
	if got, want := group.State, "resolved"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := group.BaseTitle, "Anchorman 2 The Legend Continues"; got != want {
		t.Fatalf("BaseTitle = %q, want %q", got, want)
	}
}

func TestInferGroupAssignments_AVPAliasStaysResolved(t *testing.T) {
	t.Parallel()

	filePaths := []string{
		"/srv/media/movies/alt-cuts/1080p/AVP Alien vs. Predator (2004)/Alien.vs.Predator.2004.Unrated.Bluray.Remux.1080p.AVC.DTS-HD.MA.5.1-HiFi.mkv",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	groupInference := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, nil)

	group := groupInference.ScannedGroups[0]
	if got, want := group.State, "resolved"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := group.BaseYear, 2004; got != want {
		t.Fatalf("BaseYear = %d, want %d", got, want)
	}
}

func TestInferGroupAssignments_UnrelatedMovieTitleBecomesAmbiguous(t *testing.T) {
	t.Parallel()

	filePaths := []string{
		"/srv/media/movies/alt-cuts/1080p/Green Lantern (2011)/Green.Lantern.Emerald.Knights.2011.1080p.BluRay.x264.mkv",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	groupInference := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, nil)

	group := groupInference.ScannedGroups[0]
	if got, want := group.State, "ambiguous"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
}

func TestInferGroupAssignments_SeriesSeasonDirsCollapse(t *testing.T) {
	t.Parallel()

	filePaths := []string{
		"/tv/Breaking Bad (2008)/Season 01/Breaking.Bad.S01E01.mkv",
		"/tv/Breaking Bad (2008)/Season 02/Breaking.Bad.S02E01.mkv",
	}

	rootInference := inferRootAssignments(filePaths, "series", 1, nil)
	groupInference := inferGroupAssignments(filePaths, "series", 1, rootInference.Assignments, nil)

	if got, want := len(groupInference.ScannedGroups), 1; got != want {
		t.Fatalf("len(ScannedGroups) = %d, want %d", got, want)
	}
	group := groupInference.ScannedGroups[0]
	if got, want := group.InferredType, "series"; got != want {
		t.Fatalf("InferredType = %q, want %q", got, want)
	}
	if got, want := group.BaseTitle, "Breaking Bad"; got != want {
		t.Fatalf("BaseTitle = %q, want %q", got, want)
	}
	if got, want := group.BaseYear, 2008; got != want {
		t.Fatalf("BaseYear = %d, want %d", got, want)
	}
}

func TestInferGroupAssignments_SeriesEpisodeTitleNumberDoesNotBecomeTVDBID(t *testing.T) {
	t.Parallel()

	filePaths := []string{
		"/sports/television/WWE SmackDown (1999)/Season 01/WWE SmackDown (1999) - S01E01 - SmackDown 01.mkv",
	}

	rootInference := inferRootAssignments(filePaths, "mixed", 7, nil)
	groupInference := inferGroupAssignments(filePaths, "mixed", 7, rootInference.Assignments, nil)

	if got, want := len(groupInference.ScannedGroups), 1; got != want {
		t.Fatalf("len(ScannedGroups) = %d, want %d", got, want)
	}
	group := groupInference.ScannedGroups[0]
	if got, want := group.InferredType, "series"; got != want {
		t.Fatalf("InferredType = %q, want %q", got, want)
	}
	if group.TvdbID != "" {
		t.Fatalf("TvdbID = %q, want empty", group.TvdbID)
	}
}

func TestInferGroupAssignments_MovieEpisodeTokenFolderStaysResolved(t *testing.T) {
	t.Parallel()

	filePaths := []string{
		"/movies/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	groupInference := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, nil)

	if got, want := len(groupInference.ScannedGroups), 1; got != want {
		t.Fatalf("len(ScannedGroups) = %d, want %d", got, want)
	}
	group := groupInference.ScannedGroups[0]
	if got, want := group.State, "resolved"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := group.BaseTitle, "s01e03"; got != want {
		t.Fatalf("BaseTitle = %q, want %q", got, want)
	}
	if got, want := group.InferredType, "movie"; got != want {
		t.Fatalf("InferredType = %q, want %q", got, want)
	}
}

func TestApplyGroupOverrides_ForcesResolvedIdentity(t *testing.T) {
	t.Parallel()

	filePaths := []string{
		"/srv/media/movies/alt-cuts/1080p/AVP Alien vs. Predator (2004)/Alien.vs.Predator.2004.Unrated.Bluray.Remux.1080p.AVC.DTS-HD.MA.5.1-HiFi.mkv",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	groupInference := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, nil)
	group := groupInference.ScannedGroups[0]

	applyGroupOverrides(&groupInference, map[string]models.MediaGroupOverride{
		groupOverrideKey(group.GroupKeyVersion, group.ContentGroupKey): {
			MediaFolderID:   1,
			GroupKeyVersion: group.GroupKeyVersion,
			ContentGroupKey: group.ContentGroupKey,
			ForcedTitle:     "Alien vs. Predator",
			ForcedYear:      2004,
			ForcedTmdbID:    "395",
		},
	})

	updated := groupInference.ScannedGroups[0]
	if got, want := updated.State, "resolved"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := updated.BaseTitle, "Alien vs. Predator"; got != want {
		t.Fatalf("BaseTitle = %q, want %q", got, want)
	}
	if got, want := updated.TmdbID, "395"; got != want {
		t.Fatalf("TmdbID = %q, want %q", got, want)
	}
	if got, want := updated.OverrideSource, "manual"; got != want {
		t.Fatalf("OverrideSource = %q, want %q", got, want)
	}

	assignment := groupInference.Assignments[filePaths[0]]
	if got, want := assignment.BaseTitle, "AVP Alien vs. Predator"; got != want {
		t.Fatalf("raw assignment BaseTitle = %q, want %q", got, want)
	}
	if got, want := assignment.TmdbID, ""; got != want {
		t.Fatalf("raw assignment TmdbID = %q, want %q", got, want)
	}
}

func TestInferGroupAssignments_FileIdentityOverrideSplitsCollidingKey(t *testing.T) {
	t.Parallel()

	// Two different films whose names parse to the same title+year: without an
	// override they merge into one group as fake "versions".
	filePaths := []string{
		"/Media/Movies/The Grudge (2004)/The Grudge (2004).mkv",
		"/Media/Movies/The Grudge (2004)/The Grudge (2004) JP Original.mkv",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	merged := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, nil)
	if got, want := len(merged.ScannedGroups), 1; got != want {
		t.Fatalf("pre-override len(ScannedGroups) = %d, want %d", got, want)
	}

	overrides := newIdentityOverrideSet([]models.MediaIdentityOverride{{
		MediaFolderID: 1,
		Scope:         "file",
		FilePath:      filePaths[1],
		ForcedTitle:   "Ju-on: The Grudge",
		ForcedTmdbID:  "11838",
	}})
	split := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, overrides)

	if got, want := len(split.ScannedGroups), 2; got != want {
		t.Fatalf("post-override len(ScannedGroups) = %d, want %d", got, want)
	}
	kept := split.Assignments[filePaths[0]]
	moved := split.Assignments[filePaths[1]]
	if kept.ContentGroupKey == moved.ContentGroupKey {
		t.Fatalf("override did not split group key: %q", kept.ContentGroupKey)
	}
	if moved.TmdbID != "11838" {
		t.Fatalf("moved TmdbID = %q, want %q", moved.TmdbID, "11838")
	}
	if moved.State != "resolved" || moved.Confidence != "high" {
		t.Fatalf("moved state/confidence = %q/%q, want resolved/high", moved.State, moved.Confidence)
	}
	if !moved.Overridden || kept.Overridden {
		t.Fatalf("Overridden flags = moved:%v kept:%v, want true/false", moved.Overridden, kept.Overridden)
	}

	// The shared folder now intentionally hosts two groups; the location must
	// not stay flagged ambiguous forever.
	if got, want := len(split.Locations), 1; got != want {
		t.Fatalf("len(Locations) = %d, want %d", got, want)
	}
	if got, want := split.Locations[0].State, "resolved"; got != want {
		t.Fatalf("location State = %q, want %q", got, want)
	}

	// Overridden groups surface as manual for admin visibility.
	overrideSources := map[string]bool{}
	for _, group := range split.ScannedGroups {
		overrideSources[group.OverrideSource] = true
	}
	if !overrideSources["manual"] || !overrideSources["none"] {
		t.Fatalf("override sources = %v, want both manual and none", overrideSources)
	}
}

func TestInferGroupAssignments_RootIdentityOverrideRelinksFolder(t *testing.T) {
	t.Parallel()

	// Two folders that parse to the same identity; a root-scope override on the
	// second forces it to a different film.
	filePaths := []string{
		"/Media/Movies/Crash (2004)/Crash (2004).mkv",
		"/Media/Movies/Crash (2004) Cronenberg/Crash (2004).mkv",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	overrides := newIdentityOverrideSet([]models.MediaIdentityOverride{{
		MediaFolderID: 1,
		Scope:         "root",
		RootPath:      "/Media/Movies/Crash (2004) Cronenberg",
		ForcedTmdbID:  "10723",
	}})
	result := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, overrides)

	kept := result.Assignments[filePaths[0]]
	moved := result.Assignments[filePaths[1]]
	if kept.ContentGroupKey == moved.ContentGroupKey {
		t.Fatalf("root override did not split group key: %q", kept.ContentGroupKey)
	}
	if moved.TmdbID != "10723" {
		t.Fatalf("moved TmdbID = %q, want %q", moved.TmdbID, "10723")
	}
	if kept.TmdbID != "" {
		t.Fatalf("kept TmdbID = %q, want empty", kept.TmdbID)
	}
}

func TestInferGroupAssignments_FileOverrideBeatsRootOverride(t *testing.T) {
	t.Parallel()

	filePath := "/Media/Movies/Pack (2001)/Pack (2001) Part2.mkv"
	rootInference := inferRootAssignments([]string{filePath}, "movies", 1, nil)
	overrides := newIdentityOverrideSet([]models.MediaIdentityOverride{
		{MediaFolderID: 1, Scope: "root", RootPath: "/Media/Movies/Pack (2001)", ForcedTmdbID: "100"},
		{MediaFolderID: 1, Scope: "file", FilePath: filePath, ForcedTmdbID: "200"},
	})
	result := inferGroupAssignments([]string{filePath}, "movies", 1, rootInference.Assignments, overrides)

	got := result.Assignments[filePath]
	if got.TmdbID != "200" {
		t.Fatalf("TmdbID = %q, want file-scope override %q", got.TmdbID, "200")
	}
}

func TestInferGroupAssignments_OverridesConvergeAcrossRescans(t *testing.T) {
	t.Parallel()

	filePaths := []string{
		"/Media/Movies/The Thing (1982)/The Thing (1982).mkv",
		"/Media/Movies/The Thing (1982)/The Thing (1982) Remaster.mkv",
	}
	overrides := []models.MediaIdentityOverride{{
		MediaFolderID: 1,
		Scope:         "file",
		FilePath:      filePaths[1],
		ForcedTmdbID:  "1091",
	}}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	first := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, newIdentityOverrideSet(overrides))
	second := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, newIdentityOverrideSet(overrides))

	for _, path := range filePaths {
		if first.Assignments[path].ContentGroupKey != second.Assignments[path].ContentGroupKey {
			t.Fatalf("group key for %s not stable across rescans: %q != %q",
				path, first.Assignments[path].ContentGroupKey, second.Assignments[path].ContentGroupKey)
		}
	}
}

func TestInferGroupAssignments_StructuredTagsAnchorGroupKeys(t *testing.T) {
	t.Parallel()

	// Real-world wrong merge: two different 2026 films whose titles normalize
	// identically once the leading article is stripped. Both folders carry
	// explicit provider tags; those must anchor the group key so the films
	// never merge as fake "versions" of one item.
	filePaths := []string{
		"/media/movies/Passenger (2026) [imdb-tt33763941][tmdb-1368314]/Passenger (2026) {imdb-tt33763941} {tmdb-1368314} [WEBDL-2160p][EAC3 5.1][h265].mkv",
		"/media/movies/Passenger (2026) [imdb-tt33763941][tmdb-1368314]/Passenger (2026) {imdb-tt33763941} {tmdb-1368314} [WEBDL-1080p][EAC3 5.1][h264].mkv",
		"/media/movies/The Passenger (2026) [imdb-tt32298956][tmdb-1285959]/The Passenger (2026) {imdb-tt32298956} {tmdb-1285959} [WEBDL-2160p][EAC3 5.1][h265].mkv",
		"/media/movies/The Passenger (2026) [imdb-tt32298956][tmdb-1285959]/The Passenger (2026) {imdb-tt32298956} {tmdb-1285959} [WEBDL-1080p][EAC3 5.1][h264].mkv",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	result := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, nil)

	if got, want := len(result.ScannedGroups), 2; got != want {
		t.Fatalf("len(ScannedGroups) = %d, want %d (distinct tags must not merge)", got, want)
	}
	byTmdb := map[string]models.ScannedMediaGroup{}
	for _, group := range result.ScannedGroups {
		byTmdb[group.TmdbID] = group
	}
	passenger, ok1 := byTmdb["1368314"]
	thePassenger, ok2 := byTmdb["1285959"]
	if !ok1 || !ok2 {
		t.Fatalf("groups carry wrong provider ids: %v", byTmdb)
	}
	if passenger.ObservedFileCount != 2 || thePassenger.ObservedFileCount != 2 {
		t.Fatalf("file counts = %d/%d, want 2/2", passenger.ObservedFileCount, thePassenger.ObservedFileCount)
	}
	if passenger.BaseTitle != "Passenger" || thePassenger.BaseTitle != "The Passenger" {
		t.Fatalf("titles = %q/%q", passenger.BaseTitle, thePassenger.BaseTitle)
	}
	if passenger.ImdbID != "tt33763941" || thePassenger.ImdbID != "tt32298956" {
		t.Fatalf("imdb ids = %q/%q", passenger.ImdbID, thePassenger.ImdbID)
	}
}

func TestInferGroupAssignments_SameTagAcrossFoldersStillGroups(t *testing.T) {
	t.Parallel()

	// Cross-folder versions of ONE film, both folders tagged with the same
	// tmdb id, must keep sharing a group under the anchored key.
	filePaths := []string{
		"/media/movies/Blade Runner (1982) {tmdb-78}/Blade Runner (1982) [WEBDL-2160p].mkv",
		"/media/movies-4k/Blade Runner (1982) Final Cut {tmdb-78}/Blade Runner (1982) [Remux-2160p].mkv",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	result := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, nil)

	if got, want := len(result.ScannedGroups), 1; got != want {
		t.Fatalf("len(ScannedGroups) = %d, want %d (same tag must group)", got, want)
	}
	if got, want := result.ScannedGroups[0].ObservedFileCount, 2; got != want {
		t.Fatalf("ObservedFileCount = %d, want %d", got, want)
	}
	if got, want := len(result.GroupLocations), 2; got != want {
		t.Fatalf("len(GroupLocations) = %d, want %d (two roots, one group)", got, want)
	}
}

func TestInferGroupAssignments_UntaggedFilesKeepTitleKeys(t *testing.T) {
	t.Parallel()

	// No structured tags: grouping stays title+year based, so untagged
	// libraries keep today's group keys (no churn).
	filePaths := []string{
		"/media/movies/Heat (1995)/Heat (1995) 1080p.mkv",
		"/media/movies/Heat (1995)/Heat (1995) 2160p.mkv",
	}

	rootInference := inferRootAssignments(filePaths, "movies", 1, nil)
	result := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments, nil)

	if got, want := len(result.ScannedGroups), 1; got != want {
		t.Fatalf("len(ScannedGroups) = %d, want %d", got, want)
	}
	if got, want := result.ScannedGroups[0].ContentGroupKey, "v1|movie|heat|1995"; got != want {
		t.Fatalf("ContentGroupKey = %q, want %q", got, want)
	}
}
