package scanner

import (
	"context"

	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

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

type fakeObservedRootLister struct {
	files []*models.MediaFile
	err   error
	calls int
}

func (l *fakeObservedRootLister) ListByObservedRootPath(context.Context, int, string) ([]*models.MediaFile, error) {
	l.calls++
	return l.files, l.err
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

func TestObserveRoot_TaggedEpisodeLeavesSeriesFlagged(t *testing.T) {
	observation, ok := ObserveRoot("/tv/Severance (2022)/Season 01/Severance (2022) - S01E01 {tvdb-371980}.mkv", "tv")
	if !ok {
		t.Fatal("expected observation")
	}
	if observation.HasProviderIDs || observation.Reason != RootObservationReasonMissingFolderIDs {
		t.Fatalf("observation = %+v, want the series root flagged", observation)
	}
}
