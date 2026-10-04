package scanner

import "testing"

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
	if !observation.HasFolderIDs {
		t.Fatal("expected folder ids to be detected")
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
