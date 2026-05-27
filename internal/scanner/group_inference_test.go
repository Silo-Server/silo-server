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
	groupInference := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments)

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
	groupInference := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments)

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
	groupInference := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments)

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
	groupInference := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments)

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
	groupInference := inferGroupAssignments(filePaths, "series", 1, rootInference.Assignments)

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
	groupInference := inferGroupAssignments(filePaths, "mixed", 7, rootInference.Assignments)

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
	groupInference := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments)

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
	groupInference := inferGroupAssignments(filePaths, "movies", 1, rootInference.Assignments)
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
