package adminjob

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// itemRefreshTestRootWaker records WakeInScope, with whether the scope's links
// were cleared and how many scans had run when it was called.
type itemRefreshTestRootWaker struct {
	fileRepo *itemRefreshTestFileRepo
	ingester *itemRefreshTestIngester
	err      error
	calls    []itemRefreshTestWake
}

type itemRefreshTestWake struct {
	folderID      int
	scopePath     string
	linksCleared  bool
	scansBeforeIt int
}

func (w *itemRefreshTestRootWaker) WakeInScope(_ context.Context, folderID int, scopePath string) error {
	w.calls = append(w.calls, itemRefreshTestWake{folderID, scopePath, w.fileRepo.clearedPath != "", w.ingester.scans})
	return w.err
}

// A complete refresh of a series, season or episode wakes the scope's series
// root after it clears the links and before it scans, so the scan matches a
// root an earlier failure backed off (#5337). A quick refresh and a movie's
// complete refresh leave the TV match queue alone.
func TestItemRefreshExecutorCompleteRefreshWakesBackedOffSeriesRoot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		requestedType string
		mode          ItemRefreshMode
		wantWake      bool
	}{
		{name: "series complete", requestedType: "series", mode: ItemRefreshModeComplete, wantWake: true},
		{name: "episode complete", requestedType: "episode", mode: ItemRefreshModeComplete, wantWake: true},
		{name: "series quick", requestedType: "series", mode: ItemRefreshModeQuick},
		{name: "movie complete", requestedType: "movie", mode: ItemRefreshModeComplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ingester := &itemRefreshTestIngester{}
			fileRepo := &itemRefreshTestFileRepo{
				filesByPath: []*models.MediaFile{{ContentID: "rebuilt-id", FilePath: "/media/tv/Show/Season 01/Show S01E01.mkv"}},
			}
			waker := &itemRefreshTestRootWaker{fileRepo: fileRepo, ingester: ingester}
			executor := NewItemRefreshExecutor(
				&itemRefreshTestFolderRepo{folder: &models.MediaFolder{ID: 7, Enabled: true, Paths: []string{"/media/tv"}}},
				fileRepo,
				&itemRefreshTestRootClaimRepo{},
				&itemRefreshTestGroupClaimRepo{},
				newItemRefreshTestSkippedRootRepo(),
				nil,
				&itemRefreshTestEpisodeRepo{episode: &models.Episode{ContentID: "rebuilt-episode", SeriesID: "rebuilt-id", SeasonNumber: 1, EpisodeNumber: 1}},
				ingester,
				&itemRefreshTestScanRuns{},
				&itemRefreshTestRefresher{},
				nil,
				nil,
			)
			executor.SetSeriesRootWaker(waker)

			_, err := executor.Execute(context.Background(), ItemRefreshRequest{
				RequestedContentID:     "old-id",
				RequestedType:          tc.requestedType,
				RequestedSeasonNumber:  1,
				RequestedEpisodeNumber: 1,
				RefreshContentID:       "old-id",
				ScanFolderID:           7,
				ScanPath:               "/media/tv/Show",
				Mode:                   tc.mode,
				CanonicalRootPath:      "/media/tv/Show",
			}, nil)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if !tc.wantWake {
				if len(waker.calls) != 0 {
					t.Fatalf("WakeInScope calls = %+v, want none", waker.calls)
				}
				return
			}
			want := itemRefreshTestWake{folderID: 7, scopePath: "/media/tv/Show", linksCleared: true, scansBeforeIt: 0}
			if len(waker.calls) != 1 || waker.calls[0] != want {
				t.Fatalf("WakeInScope calls = %+v, want one %+v: after the links are cleared and before the scan", waker.calls, want)
			}
		})
	}
}

// A refresh that can't wake the scope's root still scans the scope it has
// cleared, as it did before the wake existed, so a queue error doesn't leave
// the scope's files unlinked.
func TestItemRefreshExecutorCompleteRefreshScansWhenTheWakeFails(t *testing.T) {
	t.Parallel()
	ingester := &itemRefreshTestIngester{}
	fileRepo := &itemRefreshTestFileRepo{
		filesByPath: []*models.MediaFile{{ContentID: "rebuilt-id", FilePath: "/media/tv/Show/Season 01/Show S01E01.mkv"}},
	}
	waker := &itemRefreshTestRootWaker{fileRepo: fileRepo, ingester: ingester, err: errors.New("queue unavailable")}
	executor := NewItemRefreshExecutor(
		&itemRefreshTestFolderRepo{folder: &models.MediaFolder{ID: 7, Enabled: true, Paths: []string{"/media/tv"}}},
		fileRepo,
		&itemRefreshTestRootClaimRepo{},
		&itemRefreshTestGroupClaimRepo{},
		newItemRefreshTestSkippedRootRepo(),
		nil,
		nil,
		ingester,
		&itemRefreshTestScanRuns{},
		&itemRefreshTestRefresher{},
		nil,
		nil,
	)
	executor.SetSeriesRootWaker(waker)

	_, err := executor.Execute(context.Background(), ItemRefreshRequest{
		RequestedContentID: "old-id",
		RequestedType:      "series",
		RefreshContentID:   "old-id",
		ScanFolderID:       7,
		ScanPath:           "/media/tv/Show",
		Mode:               ItemRefreshModeComplete,
		CanonicalRootPath:  "/media/tv/Show",
	}, nil)
	if err != nil {
		t.Fatalf("Execute() error = %v, want the refresh to go on after a failed wake", err)
	}
	if len(waker.calls) != 1 || ingester.scans != 1 {
		t.Fatalf("wakes = %d, scans = %d; want one of each", len(waker.calls), ingester.scans)
	}
}
