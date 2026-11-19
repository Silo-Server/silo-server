package subsync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeJobs struct {
	mu       sync.Mutex
	jobs     []*Job
	finished map[int64]Outcome
	applied  map[int64]subtitles.Timing
	applyErr error
	// finishErr and progressErr are what Finish and Progress answer, as for
	// a job reaped meanwhile.
	finishErr   error
	progressErr error
	hasJob      bool
	progress    []string
}

func (f *fakeJobs) Heartbeat(context.Context, int64) error { return nil }
func (f *fakeJobs) ResetStaleJobs(context.Context, time.Time, string) (int64, error) {
	return 0, nil
}
func (f *fakeJobs) Create(_ context.Context, sub *subtitles.DownloadedSubtitle, trigger string, by *int) (*Job, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.jobs {
		if j.SubtitleID == sub.ID && j.Active() {
			return j, false, nil
		}
	}
	j := &Job{ID: int64(len(f.jobs) + 1), SubtitleID: sub.ID, MediaFileID: sub.MediaFileID, Trigger: trigger,
		RequestedBy: by, BaseRevision: sub.Revision, Status: JobPending}
	f.jobs = append(f.jobs, j)
	return j, true, nil
}
func (f *fakeJobs) CreateExternal(_ context.Context, timing *subtitles.ExternalTiming, trigger string, by *int) (*Job, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.jobs {
		if j.ExternalTimingID == timing.ID && j.Active() {
			return j, false, nil
		}
	}
	j := &Job{ID: int64(len(f.jobs) + 1), ExternalTimingID: timing.ID, MediaFileID: timing.MediaFileID, Trigger: trigger,
		RequestedBy: by, BaseRevision: timing.Revision, Status: JobPending}
	f.jobs = append(f.jobs, j)
	return j, true, nil
}
func (f *fakeJobs) Latest(context.Context, int) (*Job, error)           { return nil, nil }
func (f *fakeJobs) LatestExternal(context.Context, int64) (*Job, error) { return nil, nil }
func (f *fakeJobs) LatestForSubtitles(context.Context, []int) (map[int]*Job, error) {
	return nil, nil
}
func (f *fakeJobs) LatestForExternal(context.Context, []int64) (map[int64]*Job, error) {
	return nil, nil
}
func (f *fakeJobs) HasJob(context.Context, int) (bool, error) {
	return f.hasJob, nil
}
func (f *fakeJobs) HasExternalJob(context.Context, int64) (bool, error) {
	return f.hasJob, nil
}
func (f *fakeJobs) MarkRunning(context.Context, int64) error { return nil }
func (f *fakeJobs) Progress(_ context.Context, _ int64, phase string, progress float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.progressErr != nil {
		return f.progressErr
	}
	f.progress = append(f.progress, fmt.Sprintf("%s %.2f", phase, progress))
	return nil
}
func (f *fakeJobs) FinishUnchanged(ctx context.Context, job *Job, o Outcome) error {
	return f.Finish(ctx, job.ID, o)
}
func (f *fakeJobs) Finish(_ context.Context, id int64, o Outcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finishErr != nil {
		return f.finishErr
	}
	if f.finished == nil {
		f.finished = map[int64]Outcome{}
	}
	f.finished[id] = o
	return nil
}
func (f *fakeJobs) Apply(_ context.Context, job *Job, timing subtitles.Timing, o Outcome) (int64, error) {
	if f.applyErr != nil {
		return 0, f.applyErr
	}
	if f.applied == nil {
		f.applied = map[int64]subtitles.Timing{}
	}
	f.applied[job.ID] = timing
	_ = f.Finish(context.Background(), job.ID, o)
	return job.BaseRevision + 1, nil
}

type fakeSubtitles struct {
	sub  *subtitles.DownloadedSubtitle
	data []byte
}

func (f *fakeSubtitles) GetDownloadedSubtitle(context.Context, int) (*subtitles.DownloadedSubtitle, error) {
	return f.sub, nil
}
func (f *fakeSubtitles) GetSubtitleContent(context.Context, int) (*subtitles.DownloadedSubtitle, []byte, error) {
	return f.sub, f.data, nil
}

type fakeFiles struct{ file *models.MediaFile }

func (f fakeFiles) GetByID(context.Context, int) (*models.MediaFile, error) { return f.file, nil }

type fakeArtifacts struct {
	rows map[string]mediaartifact.Artifact
}

func (f *fakeArtifacts) Load(_ context.Context, _ int, key mediaartifact.Key) (*mediaartifact.Artifact, error) {
	if a, ok := f.rows[key.ConfigHash]; ok {
		return &a, nil
	}
	return nil, nil
}
func (f *fakeArtifacts) Upsert(_ context.Context, a mediaartifact.Artifact) error {
	if f.rows == nil {
		f.rows = map[string]mediaartifact.Artifact{}
	}
	f.rows[a.ConfigHash] = a
	return nil
}
func (f *fakeArtifacts) RecordFailure(_ context.Context, failure mediaartifact.Failure) error {
	return f.Upsert(context.Background(), mediaartifact.Artifact{Key: failure.Key, Identity: failure.Identity,
		Status: mediaartifact.StatusFailed, LastError: failure.Error, RecordedBy: failure.RecordedBy})
}

type fakeNotifier struct {
	mu      sync.Mutex
	calls   int
	targets []subtitles.SyncTarget
	updates []Update
}

func (f *fakeNotifier) SubtitleSyncUpdated(_ context.Context, update Update) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, update)
}

func (f *fakeNotifier) SubtitleTimingChanged(_ context.Context, target subtitles.SyncTarget) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.targets = append(f.targets, target)
}

type settingsMap map[string]string

func (s settingsMap) Get(_ context.Context, key string) (string, error) { return s[key], nil }

type nodeList []*nodepool.Node

func (n nodeList) KeyedNodes() []*nodepool.Node { return n }

type fakeRemote func(req mediasample.Request) (mediasample.Result, error)

func (f fakeRemote) Run(_ context.Context, _ string, _ nodeauth.Keyring, req mediasample.Request) (mediasample.Result, error) {
	return f(req)
}

// fixture is a two-hour film whose provider subtitle runs `truth` off.
type fixture struct {
	svc       *Service
	jobs      *fakeJobs
	artifacts *fakeArtifacts
	notifier  *fakeNotifier
	decodes   int
	center    []bool
}

func newFixture(t *testing.T, truth subtitles.Timing, settings settingsMap, layout string) *fixture {
	t.Helper()
	const runtime = 7200
	cues := dialogCues(21, runtime)
	f := &fixture{jobs: &fakeJobs{}, artifacts: &fakeArtifacts{}, notifier: &fakeNotifier{}}
	sub := &subtitles.DownloadedSubtitle{ID: 5, MediaFileID: 9, Language: "en", Format: subtitles.FormatSRT, Revision: 3}
	file := &models.MediaFile{ID: 9, FilePath: "/media/film.mkv", Duration: runtime, FileSize: 1 << 34, FileHash: "abc",
		AudioTracks: []models.AudioTrack{{Language: "eng", Layout: layout, Default: true}}}
	f.svc = &Service{
		jobs: f.jobs, rows: &fakeSubtitles{sub: sub}, content: &fakeSubtitles{sub: sub, data: subtitles.SerializeSRT(cues)},
		files: fakeFiles{file}, artifacts: f.artifacts, settings: settings, notifier: f.notifier, node: "test", now: time.Now,
		inline: true,
	}
	f.svc.sampler = newSampler(settings, nil, nil, func() string { return "ffmpeg" })
	f.svc.sampler.local = func(_ context.Context, req mediasample.Request) (mediasample.Result, error) {
		f.decodes++
		f.center = append(f.center, req.Audio.Speech.CenterChannel)
		w := speechFor(cues, truth, []float64{req.Window.StartSeconds}, req.Window.DurationSeconds, uint64(req.Window.StartSeconds))[0]
		return mediasample.Result{Speech: &w}, nil
	}
	return f
}

func (f *fixture) run(t *testing.T, trigger string) *Job {
	t.Helper()
	job, created, err := f.jobs.Create(context.Background(), f.svc.rows.(*fakeSubtitles).sub, trigger, nil)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	f.svc.execute(context.Background(), job)
	job.Status = f.jobs.finished[job.ID].Status
	return job
}

// stalledNotifier stands for a player connection that does not take writes
// until released.
type stalledNotifier struct{ release chan struct{} }

func (n stalledNotifier) SubtitleSyncUpdated(context.Context, Update)                 { <-n.release }
func (n stalledNotifier) SubtitleTimingChanged(context.Context, subtitles.SyncTarget) {}

func TestRequestDoesNotWaitForPlayers(t *testing.T) {
	f := newFixture(t, subtitles.Timing{Scale: 1, OffsetMS: 1500}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	f.svc.inline = false
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		cancel()
	})
	f.svc.runner = jobrunner.New(ctx, jobrunner.NewSemaphore(1), f.jobs, "test", nil)
	f.svc.notifier = stalledNotifier{release: release}
	// The request returns, with its job started, while the update that says
	// it is queued is stuck on the player connection.
	job, err := f.svc.Request(context.Background(), 5, TriggerManual, nil)
	if err != nil || job == nil {
		t.Fatalf("request: %v %v", job, err)
	}
}

// idleRunner accepts jobs without running them: its context is already
// done, so each job is aborted instead of executed.
func idleRunner(store jobrunner.Store) *jobrunner.Runner {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return jobrunner.New(ctx, jobrunner.NewSemaphore(1), store, "test", nil)
}

func TestSubtitlePlayedTriesAgainAfterAFailureToAsk(t *testing.T) {
	f := newFixture(t, subtitles.Timing{}, settingsMap{}, "stereo")
	f.svc.runner = idleRunner(f.jobs)
	_, sidecar := sidecarFixture(t, f)
	read := f.svc.readFile
	f.svc.readFile = func(string) ([]byte, error) { return nil, errors.New("mount not ready") }
	target := subtitles.SyncTarget{MediaFileID: 9, ExternalPath: sidecar.Path}
	f.svc.SubtitlePlayed(context.Background(), target)
	if len(f.jobs.jobs) != 0 {
		t.Fatalf("jobs %+v", f.jobs.jobs)
	}
	// The next window of the same track asks again, and this time it can.
	f.svc.readFile = read
	f.svc.SubtitlePlayed(context.Background(), target)
	if len(f.jobs.jobs) != 1 || f.jobs.jobs[0].Trigger != TriggerAuto {
		t.Fatalf("jobs %+v", f.jobs.jobs)
	}
}

type fakeExternal struct {
	row     *subtitles.ExternalTiming
	ensured int
}

func (f *fakeExternal) ExternalTimingByID(context.Context, int64) (*subtitles.ExternalTiming, error) {
	return f.row, nil
}
func (f *fakeExternal) ExternalTiming(_ context.Context, _ int, sha string) (*subtitles.ExternalTiming, error) {
	if f.row == nil || f.row.ContentSHA256 != sha {
		return nil, nil
	}
	return f.row, nil
}
func (f *fakeExternal) EnsureExternalTiming(_ context.Context, fileID int, sha, path string, format subtitles.SubtitleFormat) (*subtitles.ExternalTiming, error) {
	f.ensured++
	if f.row == nil || f.row.ContentSHA256 != sha {
		f.row = &subtitles.ExternalTiming{ID: 11, MediaFileID: fileID, ContentSHA256: sha, Format: format, Timing: subtitles.Timing{Scale: 1}, Revision: 1}
	}
	f.row.Path = path
	return f.row, nil
}

// sidecarFixture turns a fixture's subtitle into a sidecar on disk.
func sidecarFixture(t *testing.T, f *fixture) (*fakeExternal, *models.ExternalSubtitle) {
	t.Helper()
	data := f.svc.content.(*fakeSubtitles).data
	sidecar := models.ExternalSubtitle{Path: "/media/film.en.srt", Language: "en", Format: "srt"}
	f.svc.files.(fakeFiles).file.ExternalSubtitles = []models.ExternalSubtitle{sidecar}
	disk := map[string][]byte{sidecar.Path: data}
	f.svc.readFile = func(path string) ([]byte, error) {
		if b, ok := disk[path]; ok {
			return b, nil
		}
		return nil, fs.ErrNotExist
	}
	external := &fakeExternal{}
	f.svc.external = external
	if _, err := external.EnsureExternalTiming(context.Background(), 9, subtitles.ContentSHA256(data), sidecar.Path, subtitles.FormatSRT); err != nil {
		t.Fatal(err)
	}
	return external, &f.svc.files.(fakeFiles).file.ExternalSubtitles[0]
}

func (f *fixture) runExternal(t *testing.T, row *subtitles.ExternalTiming) *Job {
	t.Helper()
	job, created, err := f.jobs.CreateExternal(context.Background(), row, TriggerManual, nil)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	f.svc.execute(context.Background(), job)
	job.Status = f.jobs.finished[job.ID].Status
	return job
}

// A sidecar whose name is not UTF-8 is stored fspath-encoded; the service
// reads it through fspath, as NewService wires it.
func TestExecuteSyncsSidecarWithNonUTF8Name(t *testing.T) {
	truth := subtitles.Timing{Scale: 1, OffsetMS: -3200}
	f := newFixture(t, truth, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	data := f.svc.content.(*fakeSubtitles).data
	raw := filepath.Join(t.TempDir(), "B\xe5rd.en.srt")
	if err := os.WriteFile(raw, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sidecar := models.ExternalSubtitle{Path: fspath.Encode(raw), Language: "en", Format: "srt"}
	f.svc.files.(fakeFiles).file.ExternalSubtitles = []models.ExternalSubtitle{sidecar}
	f.svc.readFile = fspath.ReadFile
	external := &fakeExternal{}
	f.svc.external = external

	if _, err := external.EnsureExternalTiming(context.Background(), 9, subtitles.ContentSHA256(data), sidecar.Path, subtitles.FormatSRT); err != nil {
		t.Fatal(err)
	}
	job := f.runExternal(t, external.row)
	if job.Status != string(StatusSynced) {
		t.Fatalf("status %s: %+v", job.Status, f.jobs.finished[job.ID])
	}
}

func TestExecuteRefusesStoredSubtitleOfAnotherFile(t *testing.T) {
	f := newFixture(t, subtitles.Timing{Scale: 1, OffsetMS: 2000}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	sub := f.svc.rows.(*fakeSubtitles).sub
	job, _, err := f.jobs.Create(context.Background(), sub, TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	sub.MediaFileID++
	f.svc.execute(context.Background(), job)
	outcome := f.jobs.finished[job.ID]
	if outcome.Status != JobFailed || outcome.Error != ErrSubtitleChanged.Error() || len(f.jobs.applied) != 0 || f.decodes != 0 {
		t.Fatalf("outcome %+v applied %v decodes %d", outcome, f.jobs.applied, f.decodes)
	}
}

func TestUpdatesCarryTimingAnotherViewerSetMeanwhile(t *testing.T) {
	f := newFixture(t, subtitles.Timing{Scale: 1, OffsetMS: 2000}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	sub := f.svc.rows.(*fakeSubtitles).sub
	sub.Timing = subtitles.Timing{Scale: 1}
	manual := subtitles.Timing{Scale: 1, OffsetMS: 700}
	decode := f.svc.sampler.local
	f.svc.sampler.local = func(ctx context.Context, req mediasample.Request) (mediasample.Result, error) {
		sub.Timing = manual // another viewer sets the timing while this job decodes
		return decode(ctx, req)
	}
	f.jobs.applyErr = ErrSubtitleChanged
	job, _, err := f.jobs.Create(context.Background(), sub, TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.notifier.updates = nil
	f.svc.execute(context.Background(), job)

	updates := f.notifier.updates
	if len(updates) < 2 {
		t.Fatalf("updates %+v", updates)
	}
	for _, u := range updates[1:] {
		if u.Timing != manual {
			t.Fatalf("update %s/%s carries %+v, want %+v", u.Job.Status, u.Job.Phase, u.Timing, manual)
		}
	}
	if last := updates[len(updates)-1]; last.Job.Status != JobFailed || last.Job.Failure != FailureSubtitleChanged {
		t.Fatalf("last update %+v", last.Job)
	}
}

// A verdict reached about a subtitle another viewer retimed meanwhile is not
// recorded as if it described the new timing.
func TestExecuteUnchangedOutcomeAfterRetimeMeanwhile(t *testing.T) {
	for name, truth := range map[string]subtitles.Timing{
		"already synced": {Scale: 1},
		"synced":         {Scale: 1, OffsetMS: 1800},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, truth, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
			sub := f.svc.rows.(*fakeSubtitles).sub
			job, _, err := f.jobs.Create(context.Background(), sub, TriggerManual, nil)
			if err != nil {
				t.Fatal(err)
			}
			decode := f.svc.sampler.local
			f.svc.sampler.local = func(ctx context.Context, req mediasample.Request) (mediasample.Result, error) {
				sub.Revision = job.BaseRevision + 1 // another viewer sets the timing
				return decode(ctx, req)
			}
			f.svc.execute(context.Background(), job)
			outcome := f.jobs.finished[job.ID]
			if outcome.Status != JobFailed || outcome.Failure != FailureSubtitleChanged || len(f.jobs.applied) != 0 {
				t.Fatalf("outcome %+v applied %v", outcome, f.jobs.applied)
			}
		})
	}
}

func TestRequestExternalRefusesFormatsItCannotRetime(t *testing.T) {
	f := newFixture(t, subtitles.Timing{}, settingsMap{}, "stereo")
	external, _ := sidecarFixture(t, f)
	external.ensured = 0
	_, err := f.svc.RequestExternal(context.Background(), 9, models.ExternalSubtitle{Path: "/media/film.sub", Format: "sub"}, TriggerManual, nil)
	if !errors.Is(err, ErrUnsupportedFormat) || external.ensured != 0 || len(f.jobs.jobs) != 0 {
		t.Fatalf("err %v ensured %d jobs %d", err, external.ensured, len(f.jobs.jobs))
	}
}

func TestExecuteNamesFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(f *fixture)
		want   string
	}{
		"subtitle changed": {func(f *fixture) {
			edited := *f.svc.content.(*fakeSubtitles).sub
			edited.Revision++
			f.svc.content = &fakeSubtitles{sub: &edited, data: f.svc.content.(*fakeSubtitles).data}
		}, FailureSubtitleChanged},
		"no audio": {func(f *fixture) { f.svc.files.(fakeFiles).file.Duration = 0 }, FailureNoAudio},
		"no node":  {func(f *fixture) { f.svc.settings = settingsMap{SettingExecution: ExecutionTranscodeOnly} }, FailureUnavailable},
		"unreadable audio": {func(f *fixture) {
			f.svc.sampler.local = func(context.Context, mediasample.Request) (mediasample.Result, error) {
				return mediasample.Result{}, &mediasample.Error{Reason: mediasample.ReasonExit,
					Attempts: []mediasample.AttemptError{{Reason: mediasample.ReasonExit, Err: errors.New("exit status 1"),
						StderrTail: "Stream map '0:a:0' matches no streams."}}}
			}
		}, FailureNoAudio},
		"busy host": {func(f *fixture) {
			f.svc.sampler.local = func(context.Context, mediasample.Request) (mediasample.Result, error) {
				return mediasample.Result{}, &mediasample.Error{Reason: mediasample.ReasonTimeout,
					Attempts: []mediasample.AttemptError{{Reason: mediasample.ReasonTimeout, Err: errors.New("timed out")}}}
			}
		}, FailureUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, subtitles.Timing{Scale: 1, OffsetMS: 1000}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
			tc.change(f)
			f.svc.sampler.settings = f.svc.settings
			job := f.run(t, TriggerManual)
			outcome := f.jobs.finished[job.ID]
			if outcome.Status != JobFailed || outcome.Failure != tc.want {
				t.Fatalf("outcome %+v, want failure %s", outcome, tc.want)
			}
			final := f.notifier.updates[len(f.notifier.updates)-1]
			if final.Job.PublicFailure() != tc.want || final.Target.StoredID != 5 {
				t.Fatalf("final update %+v", final)
			}
		})
	}
}
