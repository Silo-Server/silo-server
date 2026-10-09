package scanner

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestObserveRoot_ReportedMovieFolderStaysMovie(t *testing.T) {
	observation, ok := ObserveRoot(
		"/mixed/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
		"mixed",
	)
	if !ok {
		t.Fatal("expected observation")
	}
	if observation.RootPath != "/mixed/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}" {
		t.Fatalf("RootPath = %q, want movie folder root", observation.RootPath)
	}
	if !observation.HasProviderIDs {
		t.Fatal("expected provider ids to be detected")
	}
	if observation.Reason != RootObservationReasonMatchable {
		t.Fatalf("Reason = %q, want %q", observation.Reason, RootObservationReasonMatchable)
	}
}

func TestObserveRootUnknownLibraryKeepsAudioRoot(t *testing.T) {
	path := "/media/Show/theme.mp3"
	if _, ok := ObserveRoot(path, "future-audio-kind", "/media"); !ok {
		t.Fatal("unknown library type discarded its audio root as a video theme")
	}
	if _, ok := ObserveRoot(path, "movies", "/media"); ok {
		t.Fatal("movie theme became a media root")
	}
}

func TestInferRootAssignments_CollapsesWrapperFolderToTaggedParent(t *testing.T) {
	result := inferRootAssignments([]string{
		"/movies/The Bay (2019) {tvdbid-368807}/The Bay (2019)/The Bay (2019) S01E01.mkv",
	}, "mixed", 7, nil)

	assignment := result.Assignments["/movies/The Bay (2019) {tvdbid-368807}/The Bay (2019)/The Bay (2019) S01E01.mkv"]
	if got, want := assignment.RootPath, "/movies/The Bay (2019) {tvdbid-368807}"; got != want {
		t.Fatalf("RootPath = %q, want %q", got, want)
	}
	if !assignment.WrapperCollapsed {
		t.Fatal("expected wrapper collapse to be recorded")
	}
}

func TestCollectScannedRoots_ProviderTaggedParentBeatsSyntheticChild(t *testing.T) {
	roots := inferRootAssignments([]string{
		"/movies/Bagman {tmdb-814889}/Bagman.2024.2160p.WEB-DL.mkv",
	}, "movies", 12, nil).Snapshots
	if len(roots) != 1 {
		t.Fatalf("len(roots) = %d, want 1", len(roots))
	}
	if got, want := roots[0].RootPath, "/movies/Bagman {tmdb-814889}"; got != want {
		t.Fatalf("RootPath = %q, want %q", got, want)
	}
}

func TestCollectScannedRoots_UFCEventWithoutFolderIDsStillResolves(t *testing.T) {
	roots := inferRootAssignments([]string{
		"/events/UFC 300/UFC.300.2024.1080p.WEB-DL.mkv",
	}, "mixed", 14, nil).Snapshots
	if len(roots) != 1 {
		t.Fatalf("len(roots) = %d, want 1", len(roots))
	}
	if got := roots[0].State; got != "resolved" {
		t.Fatalf("State = %q, want resolved", got)
	}
}

func TestCollectScannedRoots_AltCutReleaseNameUsesMovieFolderRoot(t *testing.T) {
	roots := inferRootAssignments([]string{
		"/movies/alt-cuts/1080p/Borderland (2007)/Borderland.2007.Unrated.Directors.Cut.BluRay.1080p.DTS-HD.MA.5.1.AVC.REMUX-FraMeSToR.mkv",
	}, "movies", 21, nil).Snapshots
	if len(roots) != 1 {
		t.Fatalf("len(roots) = %d, want 1", len(roots))
	}
	if got, want := roots[0].RootPath, "/movies/alt-cuts/1080p/Borderland (2007)"; got != want {
		t.Fatalf("RootPath = %q, want %q", got, want)
	}
	if got, want := roots[0].Title, "Borderland"; got != want {
		t.Fatalf("Title = %q, want %q", got, want)
	}
	if got, want := roots[0].Year, 2007; got != want {
		t.Fatalf("Year = %d, want %d", got, want)
	}
	if got := roots[0].State; got != "resolved" {
		t.Fatalf("State = %q, want resolved", got)
	}
}

func TestCollectScannedRoots_ContradictorySingleMovieFileBecomesAmbiguous(t *testing.T) {
	roots := inferRootAssignments([]string{
		"/movies/Puppet Master (1989)/Transformers.Armada.2002.1080p.BluRay.mkv",
	}, "movies", 22, nil).Snapshots
	if len(roots) != 1 {
		t.Fatalf("len(roots) = %d, want 1", len(roots))
	}
	if got, want := roots[0].RootPath, "/movies/Puppet Master (1989)"; got != want {
		t.Fatalf("RootPath = %q, want %q", got, want)
	}
	if got := roots[0].State; got != "ambiguous" {
		t.Fatalf("State = %q, want ambiguous", got)
	}
}

func TestShouldSkipMovieSupplementalFile(t *testing.T) {
	if !shouldSkipMovieSupplementalFile("/movies/Movie (2000)/Sample.mkv") {
		t.Fatal("expected Sample.mkv to be skipped")
	}
	if shouldSkipMovieSupplementalFile("/movies/Sample (2011)/Sample.2011.1080p.BluRay.mkv") {
		t.Fatal("did not expect a real movie named Sample to be skipped")
	}
}

func TestObserveRoot_MovieFileTagCountsWithoutFolderIDs(t *testing.T) {
	observation, ok := ObserveRoot(
		"/movies/Dune (2021)/Dune (2021) {tmdb-438631} [Bluray-1080p].mkv",
		"movies",
	)
	if !ok {
		t.Fatal("expected observation")
	}
	if observation.RootPath != "/movies/Dune (2021)" {
		t.Fatalf("RootPath = %q, want movie folder root", observation.RootPath)
	}
	if !observation.HasProviderIDs {
		t.Fatal("expected the file name's provider tag to count")
	}
	if observation.Reason != RootObservationReasonMatchable {
		t.Fatalf("Reason = %q, want %q", observation.Reason, RootObservationReasonMatchable)
	}
}

func TestInferRootAssignments_ProviderIDSources(t *testing.T) {
	tests := []struct {
		name        string
		libraryType string
		files       []string
		wantRoot    string
		wantReason  string
	}{
		{
			name:        "one tagged version marks the movie root",
			libraryType: "movies",
			files: []string{
				"/movies/Dune (2021)/Dune (2021) [Bluray-2160p].mkv",
				"/movies/Dune (2021)/Dune (2021) [imdbid-tt1160419] [Bluray-1080p].mkv",
			},
			wantRoot:   "/movies/Dune (2021)",
			wantReason: RootObservationReasonMatchable,
		},
		{
			name:        "untagged movie folder and file stay flagged",
			libraryType: "movies",
			files:       []string{"/movies/Dune (2021)/Dune (2021) [Bluray-1080p].mkv"},
			wantRoot:    "/movies/Dune (2021)",
			wantReason:  RootObservationReasonMissingFolderIDs,
		},
		{
			name:        "episode file tags do not vouch for the series",
			libraryType: "tv",
			files: []string{
				"/tv/Severance (2022)/Season 01/Severance (2022) - S01E01 {tvdb-371980}.mkv",
				"/tv/Severance (2022)/Season 01/Severance (2022) - S01E02 {tvdb-371980}.mkv",
			},
			wantRoot:   "/tv/Severance (2022)",
			wantReason: RootObservationReasonMissingFolderIDs,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			observations := inferRootAssignments(tc.files, tc.libraryType, 0, nil).Observations
			if len(observations) != 1 {
				t.Fatalf("len(observations) = %d, want 1: %+v", len(observations), observations)
			}
			got := observations[0]
			if got.RootPath != tc.wantRoot {
				t.Fatalf("RootPath = %q, want %q", got.RootPath, tc.wantRoot)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

type fakeObservedRootLister struct {
	files []*models.MediaFile
	err   error
	calls int
}

func (l *fakeObservedRootLister) ListByObservedRootPath(context.Context, int, string) ([]*models.MediaFile, error) {
	l.calls++
	return l.files, l.err
}

func TestObserveFileRoot_UntaggedVersionUsesTaggedSibling(t *testing.T) {
	const root = "/movies/Dune (2021)"
	lister := &fakeObservedRootLister{files: []*models.MediaFile{
		{FilePath: root + "/Dune (2021) [imdbid-tt1160419] [Bluray-1080p].mkv"},
		{FilePath: root + "/Dune (2021) [Bluray-2160p].mkv"},
	}}
	observation, ok, err := observeFileRoot(context.Background(), lister, 1, root+"/Dune (2021) [Bluray-2160p].mkv", "movies")
	if err != nil || !ok {
		t.Fatalf("observeFileRoot() = (%+v, %v, %v)", observation, ok, err)
	}
	if observation.RootPath != root {
		t.Fatalf("RootPath = %q, want %q", observation.RootPath, root)
	}
	if observation.Reason != RootObservationReasonMatchable {
		t.Fatalf("Reason = %q, want %q", observation.Reason, RootObservationReasonMatchable)
	}
}

func TestObserveFileRoot_StaysFlaggedWithoutTaggedSibling(t *testing.T) {
	const root = "/movies/Dune (2021)"
	lister := &fakeObservedRootLister{files: []*models.MediaFile{
		{FilePath: root + "/Dune (2021) [Bluray-1080p].mkv"},
	}}
	observation, ok, err := observeFileRoot(context.Background(), lister, 1, root+"/Dune (2021) [Bluray-2160p].mkv", "movies")
	if err != nil || !ok {
		t.Fatalf("observeFileRoot() = (%+v, %v, %v)", observation, ok, err)
	}
	if observation.Reason != RootObservationReasonMissingFolderIDs {
		t.Fatalf("Reason = %q, want %q", observation.Reason, RootObservationReasonMissingFolderIDs)
	}
}

func TestObserveFileRoot_SeriesIgnoresSiblingTags(t *testing.T) {
	const root = "/tv/Severance (2022)"
	lister := &fakeObservedRootLister{files: []*models.MediaFile{
		{FilePath: root + "/Season 01/Severance (2022) - S01E01 {tvdb-371980}.mkv"},
	}}
	observation, ok, err := observeFileRoot(context.Background(), lister, 1, root+"/Season 01/Severance (2022) - S01E02.mkv", "tv")
	if err != nil || !ok {
		t.Fatalf("observeFileRoot() = (%+v, %v, %v)", observation, ok, err)
	}
	if observation.Reason != RootObservationReasonMissingFolderIDs {
		t.Fatalf("Reason = %q, want %q", observation.Reason, RootObservationReasonMissingFolderIDs)
	}
	if lister.calls != 0 {
		t.Fatalf("series observation listed siblings %d times, want 0", lister.calls)
	}
}

func TestObserveFileRoot_TaggedFileSkipsSiblingLookup(t *testing.T) {
	lister := &fakeObservedRootLister{err: errors.New("unexpected lookup")}
	observation, ok, err := observeFileRoot(context.Background(), lister, 1, "/movies/Dune (2021)/Dune (2021) {tmdb-438631}.mkv", "movies")
	if err != nil || !ok {
		t.Fatalf("observeFileRoot() = (%+v, %v, %v)", observation, ok, err)
	}
	if observation.Reason != RootObservationReasonMatchable {
		t.Fatalf("Reason = %q, want %q", observation.Reason, RootObservationReasonMatchable)
	}
}

func TestObserveFileRoot_ReturnsListError(t *testing.T) {
	lister := &fakeObservedRootLister{err: errors.New("db down")}
	if _, _, err := observeFileRoot(context.Background(), lister, 1, "/movies/Dune (2021)/Dune (2021).mkv", "movies"); err == nil {
		t.Fatal("expected the sibling lookup error")
	}
}

func TestInferRootAssignments_ForcedMovieRootCountsFileTags(t *testing.T) {
	const root = "/movies/Planet Earth (2006)"
	files := []string{
		root + "/Planet Earth (2006) - S01E01 {tmdb-1044}.mkv",
		root + "/Planet Earth (2006) - S01E02 {tmdb-1044}.mkv",
	}
	overrides := map[string]models.MediaRootOverride{root: {RootPath: root, ForcedType: "movie"}}
	result := inferRootAssignments(files, "mixed", 1, overrides)
	if len(result.Observations) != 1 {
		t.Fatalf("len(observations) = %d, want 1: %+v", len(result.Observations), result.Observations)
	}
	if got := result.Observations[0]; got.RootPath != root || got.Reason != RootObservationReasonMatchable {
		t.Fatalf("observation = %+v, want matchable %q", got, root)
	}
}

func TestObserveRoot_TaggedEpisodeLeavesSeriesFlagged(t *testing.T) {
	observation, ok := ObserveRoot("/tv/Severance (2022)/Season 01/Severance (2022) - S01E01 {tvdb-371980}.mkv", "tv")
	if !ok {
		t.Fatal("expected observation")
	}
	if observation.HasProviderIDs || observation.Reason != RootObservationReasonMissingFolderIDs {
		t.Fatalf("observation = %+v, want the series root flagged", observation)
	}
}
