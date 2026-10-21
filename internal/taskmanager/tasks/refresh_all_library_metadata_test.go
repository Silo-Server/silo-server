package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeEnabledFolders struct {
	folders []*models.MediaFolder
	err     error
}

func (f fakeEnabledFolders) GetEnabled(context.Context) ([]*models.MediaFolder, error) {
	return f.folders, f.err
}

type fakeActiveLibraryRefreshes struct {
	active map[int]bool
	errs   map[int]error
}

func (f fakeActiveLibraryRefreshes) GetActiveLibraryRefreshByLibraryID(_ context.Context, libraryID int) (*models.AdminJob, error) {
	if err := f.errs[libraryID]; err != nil {
		return nil, err
	}
	if f.active[libraryID] {
		return &models.AdminJob{ID: "active", JobType: adminjob.JobTypeLibraryRefresh}, nil
	}
	return nil, adminjob.ErrJobNotFound
}

type fakeLibraryRefreshRunner struct {
	requests []adminjob.LibraryRefreshRequest
	results  map[int]*adminjob.LibraryRefreshResult
	errs     map[int]error
	onRun    func(req adminjob.LibraryRefreshRequest)
}

func (f *fakeLibraryRefreshRunner) Execute(_ context.Context, req adminjob.LibraryRefreshRequest, progress func(current, total int, message string)) (*adminjob.LibraryRefreshResult, error) {
	f.requests = append(f.requests, req)
	if f.onRun != nil {
		f.onRun(req)
	}
	progress(1, 2, "Refreshing items with external IDs")
	return f.results[req.LibraryID], f.errs[req.LibraryID]
}

type fakeClusterLock struct {
	acquired bool
	err      error
	released int
}

func (f *fakeClusterLock) TryAcquire(context.Context) (func(), bool, error) {
	if f.err != nil || !f.acquired {
		return nil, false, f.err
	}
	return func() { f.released++ }, true, nil
}

func newTestRefreshAllTask(folders ScanFolderRepository, active ActiveLibraryRefreshFinder, runner LibraryRefreshRunner) *RefreshAllLibraryMetadataTask {
	task := NewRefreshAllLibraryMetadataTask(nil, folders, active, runner)
	task.lock = &fakeClusterLock{acquired: true}
	return task
}

type resultRecordingProgress struct {
	recordingProgress
	result json.RawMessage
}

func (r *resultRecordingProgress) SetResultData(data json.RawMessage) { r.result = data }

func (r *resultRecordingProgress) summary(t *testing.T) refreshAllLibraryMetadataSummary {
	t.Helper()
	var summary refreshAllLibraryMetadataSummary
	if err := json.Unmarshal(r.result, &summary); err != nil {
		t.Fatalf("result data %q: %v", r.result, err)
	}
	return summary
}

func TestRefreshAllLibraryMetadataTaskSkipsActiveAndContinuesAfterFailures(t *testing.T) {
	runner := &fakeLibraryRefreshRunner{errs: map[int]error{2: errors.New("provider unavailable")}}
	task := newTestRefreshAllTask(fakeEnabledFolders{folders: []*models.MediaFolder{
		{ID: 1, Name: "Movies"}, {ID: 2, Name: "TV Shows"}, {ID: 3, Name: "Anime"}, {ID: 4, Name: "Sports"},
	}}, fakeActiveLibraryRefreshes{
		active: map[int]bool{1: true},
		errs:   map[int]error{3: errors.New("database unavailable")},
	}, runner)
	progress := &resultRecordingProgress{}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var ran []int
	for _, req := range runner.requests {
		ran = append(ran, req.LibraryID)
	}
	if len(ran) != 2 || ran[0] != 2 || ran[1] != 4 {
		t.Fatalf("refreshed libraries = %v, want [2 4]", ran)
	}
	got := progress.summary(t)
	want := refreshAllLibraryMetadataSummary{Libraries: 4, Refreshed: 1, Skipped: 1, Failed: 2}
	if got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
}

func TestRefreshAllLibraryMetadataTaskStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeLibraryRefreshRunner{onRun: func(adminjob.LibraryRefreshRequest) { cancel() }}
	task := newTestRefreshAllTask(fakeEnabledFolders{folders: []*models.MediaFolder{
		{ID: 1, Name: "Movies"}, {ID: 2, Name: "TV Shows"},
	}}, fakeActiveLibraryRefreshes{}, runner)
	progress := &resultRecordingProgress{}

	if err := task.Execute(ctx, progress); !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("requests = %+v, want the run to stop after the first library", runner.requests)
	}
	if got := progress.summary(t); got.Libraries != 2 || got.Refreshed != 1 {
		t.Fatalf("summary = %+v, want the completed library recorded", got)
	}
}

func TestRefreshAllLibraryMetadataTaskRunsOnOneServerAtATime(t *testing.T) {
	runner := &fakeLibraryRefreshRunner{}
	task := newTestRefreshAllTask(fakeEnabledFolders{folders: []*models.MediaFolder{{ID: 1, Name: "Movies"}}}, fakeActiveLibraryRefreshes{}, runner)
	task.lock = &fakeClusterLock{}

	if err := task.Execute(context.Background(), &resultRecordingProgress{}); !errors.Is(err, errFullMetadataRefreshRunning) {
		t.Fatalf("Execute() error = %v, want errFullMetadataRefreshRunning so the run is not recorded as a refresh", err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("requests = %+v, want none while another server holds the lock", runner.requests)
	}
}

func TestRefreshAllLibraryMetadataTaskSkipsALibraryAnotherRefreshHolds(t *testing.T) {
	runner := &fakeLibraryRefreshRunner{errs: map[int]error{1: adminjob.ErrLibraryRefreshInProgress}}
	task := newTestRefreshAllTask(fakeEnabledFolders{folders: []*models.MediaFolder{
		{ID: 1, Name: "Movies"}, {ID: 2, Name: "TV Shows"},
	}}, fakeActiveLibraryRefreshes{}, runner)
	progress := &resultRecordingProgress{}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := progress.summary(t)
	want := refreshAllLibraryMetadataSummary{Libraries: 2, Refreshed: 1, Skipped: 1}
	if got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
}

func TestRefreshAllLibraryMetadataTaskCancelDuringLookupIsNotAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	active := cancelingActiveLookup{cancel: cancel}
	runner := &fakeLibraryRefreshRunner{}
	task := newTestRefreshAllTask(fakeEnabledFolders{folders: []*models.MediaFolder{{ID: 1, Name: "Movies"}}}, active, runner)
	progress := &resultRecordingProgress{}

	if err := task.Execute(ctx, progress); !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", err)
	}
	if got := progress.summary(t); got.Failed != 0 {
		t.Fatalf("summary = %+v, want a canceled lookup not counted as a failure", got)
	}
}

// cancelingActiveLookup cancels the run while the active-job lookup is in
// flight and fails the lookup the way a canceled query does.
type cancelingActiveLookup struct {
	cancel context.CancelFunc
}

func (c cancelingActiveLookup) GetActiveLibraryRefreshByLibraryID(ctx context.Context, _ int) (*models.AdminJob, error) {
	c.cancel()
	return nil, ctx.Err()
}
