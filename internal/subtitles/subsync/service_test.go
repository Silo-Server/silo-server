package subsync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
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

func (n nodeList) Nodes() []*nodepool.Node { return n }

type fakeRemote func(req mediasample.Request) (mediasample.Result, error)

func (f fakeRemote) Run(_ context.Context, _, _ string, req mediasample.Request) (mediasample.Result, error) {
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
	f.svc.sampler = newSampler(settings, nil, func() string { return "ffmpeg" })
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

func TestExecuteAppliesSyncAndCachesSpeech(t *testing.T) {
	truth := subtitles.Timing{Scale: 25 / 23.976, OffsetMS: 2500}
	f := newFixture(t, truth, settingsMap{SettingExecution: ExecutionLocal}, "5.1(side)")

	job := f.run(t, TriggerManual)
	if job.Status != string(StatusSynced) {
		t.Fatalf("status %s: %+v", job.Status, f.jobs.finished[job.ID])
	}
	assertTiming(t, f.jobs.applied[job.ID], truth, 7200)
	if f.notifier.calls != 1 {
		t.Fatalf("notifier calls %d", f.notifier.calls)
	}
	if f.decodes != sampledWindows || !f.center[0] {
		t.Fatalf("decodes %d, center %v", f.decodes, f.center)
	}
	if got := f.jobs.finished[job.ID].ExecutedOn; got != ExecutionLocal {
		t.Fatalf("executed on %q", got)
	}

	// The next speech request reuses the artifact written by execute.
	windows, _, executedOn, err := f.svc.speech(t.Context(), f.svc.files.(fakeFiles).file, "en", false, nil)
	if err != nil || len(windows) != sampledWindows || executedOn != "cache" {
		t.Fatalf("cached speech: windows=%d executed_on=%q err=%v", len(windows), executedOn, err)
	}
	if f.decodes != sampledWindows {
		t.Fatalf("cached speech decoded again: %d", f.decodes)
	}
}

func TestExecuteFallsBackFromCenterChannel(t *testing.T) {
	f := newFixture(t, subtitles.Timing{OffsetMS: -4000}, settingsMap{SettingExecution: ExecutionLocal}, "5.1")
	local := f.svc.sampler.local
	f.svc.sampler.local = func(ctx context.Context, req mediasample.Request) (mediasample.Result, error) {
		if req.Audio.Speech.CenterChannel {
			f.decodes++
			return mediasample.Result{}, &mediasample.Error{Reason: mediasample.ReasonExit,
				Attempts: []mediasample.AttemptError{{Reason: mediasample.ReasonExit, Err: errors.New("exit status 1")}}}
		}
		return local(ctx, req)
	}
	job := f.run(t, TriggerManual)
	if job.Status != string(StatusSynced) {
		t.Fatalf("status %s: %+v", job.Status, f.jobs.finished[job.ID])
	}
	unusable := 0
	for _, a := range f.artifacts.rows {
		if a.Status == mediaartifact.StatusUnusable {
			unusable++
		}
	}
	if unusable != 1 {
		t.Fatalf("center plan not recorded unusable: %+v", f.artifacts.rows)
	}
}

func TestExecuteReportsNoMatch(t *testing.T) {
	f := newFixture(t, subtitles.Timing{}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	other := dialogCues(77, 7200)
	f.svc.content = &fakeSubtitles{sub: f.svc.rows.(*fakeSubtitles).sub, data: subtitles.SerializeSRT(other)}
	job := f.run(t, TriggerAuto)
	if job.Status != string(StatusNoMatch) || len(f.jobs.applied) != 0 || f.notifier.calls != 0 {
		t.Fatalf("status %s applied %v", job.Status, f.jobs.applied)
	}
}

func TestExecuteEndsJobWhenApplyFails(t *testing.T) {
	f := newFixture(t, subtitles.Timing{OffsetMS: 3000}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	f.jobs.applyErr = errors.New("connection reset")
	if job := f.run(t, TriggerManual); job.Status != JobFailed || f.notifier.calls != 0 {
		t.Fatalf("status %s, notifier calls %d; want failed without notifying players", job.Status, f.notifier.calls)
	}
}

func TestRequestAutoSkips(t *testing.T) {
	f := newFixture(t, subtitles.Timing{}, settingsMap{SettingAutoSync: "false"}, "")
	if job, err := f.svc.Request(context.Background(), 5, TriggerAuto, nil); job != nil || err != nil {
		t.Fatalf("auto sync off: %v %v", job, err)
	}
	f.svc.settings = settingsMap{}
	// Identical content re-added returns the existing row: a subtitle synced
	// before, or one whose timing someone set, is not synced again.
	f.jobs.hasJob = true
	if job, err := f.svc.Request(context.Background(), 5, TriggerAuto, nil); job != nil || err != nil {
		t.Fatalf("synced before: %v %v", job, err)
	}
	f.jobs.hasJob = false
	f.svc.rows.(*fakeSubtitles).sub.Timing = subtitles.Timing{OffsetMS: 2000}
	if job, err := f.svc.Request(context.Background(), 5, TriggerAuto, nil); job != nil || err != nil {
		t.Fatalf("timing set by hand: %v %v", job, err)
	}
	f.svc.rows.(*fakeSubtitles).sub.Timing = subtitles.Timing{}
	f.svc.rows.(*fakeSubtitles).sub.Format = subtitles.FormatSUB
	if job, err := f.svc.Request(context.Background(), 5, TriggerAuto, nil); job != nil || err != nil {
		t.Fatalf("unsupported auto: %v %v", job, err)
	}
	if _, err := f.svc.Request(context.Background(), 5, TriggerManual, nil); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("unsupported manual: %v", err)
	}
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

func TestSubtitlePlayedSyncsAStoredSubtitleNeverSynced(t *testing.T) {
	f := newFixture(t, subtitles.Timing{}, settingsMap{}, "stereo")
	f.svc.runner = idleRunner(f.jobs)
	f.svc.SubtitlePlayed(context.Background(), subtitles.SyncTarget{MediaFileID: 9, StoredID: 5})
	if len(f.jobs.jobs) != 1 || f.jobs.jobs[0].Trigger != TriggerAuto || f.jobs.jobs[0].RequestedBy != nil {
		t.Fatalf("jobs %+v", f.jobs.jobs)
	}
}

func TestSubtitlePlayedSyncsASidecarNeverSynced(t *testing.T) {
	for name, tc := range map[string]struct {
		prepare func(f *fixture, external *fakeExternal)
		want    int
	}{
		"never synced":    {func(*fixture, *fakeExternal) {}, 1},
		"synced before":   {func(f *fixture, _ *fakeExternal) { f.jobs.hasJob = true }, 0},
		"retimed by hand": {func(_ *fixture, e *fakeExternal) { e.row.Timing = subtitles.Timing{Scale: 1, OffsetMS: 400} }, 0},
		"auto sync off":   {func(f *fixture, _ *fakeExternal) { f.svc.settings = settingsMap{SettingAutoSync: "false"} }, 0},
		"cannot be retimed": {func(f *fixture, _ *fakeExternal) {
			f.svc.files.(fakeFiles).file.ExternalSubtitles[0].Format = "sub"
		}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, subtitles.Timing{}, settingsMap{}, "stereo")
			f.svc.runner = idleRunner(f.jobs)
			external, sidecar := sidecarFixture(t, f)
			tc.prepare(f, external)
			f.svc.SubtitlePlayed(context.Background(), subtitles.SyncTarget{MediaFileID: 9, ExternalPath: sidecar.Path})
			if len(f.jobs.jobs) != tc.want {
				t.Fatalf("jobs %+v", f.jobs.jobs)
			}
			if tc.want == 1 && (f.jobs.jobs[0].Trigger != TriggerAuto || f.jobs.jobs[0].ExternalTimingID != external.row.ID) {
				t.Fatalf("job %+v", f.jobs.jobs[0])
			}
		})
	}
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

// Players fetch a subtitle in windows, many times a session: each played
// subtitle is considered once per playedTTL on a server.
func TestFirstPlayRemembersForATime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := &Service{now: func() time.Time { return now }}
	target := subtitles.SyncTarget{MediaFileID: 9, StoredID: 5}
	if !s.firstPlay(target) || s.firstPlay(target) {
		t.Fatal("a second play within the window was considered again")
	}
	if !s.firstPlay(subtitles.SyncTarget{MediaFileID: 9, ExternalPath: "/media/film.en.srt"}) {
		t.Fatal("another subtitle was not considered")
	}
	now = now.Add(playedTTL)
	if !s.firstPlay(target) {
		t.Fatal("a play after the window was not considered")
	}
}

func TestSamplerExecution(t *testing.T) {
	reqs := []mediasample.Request{{Input: "/m.mkv"}, {Input: "/m.mkv"}}
	ok := func(context.Context, mediasample.Request) (mediasample.Result, error) {
		return mediasample.Result{Decoder: "software"}, nil
	}
	node := &nodepool.Node{ID: 1, Name: "gpu-1", URL: "http://node", Enabled: true, Healthy: true}
	settings := settingsMap{settingJWTSecret: "secret"}

	t.Run("node", func(t *testing.T) {
		s := &sampler{settings: settings, nodes: nodeList{node}, reservations: &nodepool.Reservations{}, local: ok,
			remote: fakeRemote(func(mediasample.Request) (mediasample.Result, error) { return mediasample.Result{}, nil })}
		if _, where, err := s.run(context.Background(), reqs, nil); err != nil || where != "node:gpu-1" {
			t.Fatalf("%q %v", where, err)
		}
	})
	t.Run("node failure falls back", func(t *testing.T) {
		calls := 0
		s := &sampler{settings: settings, nodes: nodeList{node}, reservations: &nodepool.Reservations{}, local: ok,
			remote: fakeRemote(func(mediasample.Request) (mediasample.Result, error) {
				calls++
				if calls == 2 {
					return mediasample.Result{}, &mediasample.RemoteError{Status: http.StatusServiceUnavailable, Reason: mediasample.ReasonNodeUnavailable}
				}
				return mediasample.Result{}, nil
			})}
		var done []int
		results, where, err := s.run(context.Background(), reqs, func(n int) { done = append(done, n) })
		if err != nil || where != ExecutionLocal || len(results) != 2 {
			t.Fatalf("%q %d %v", where, len(results), err)
		}
		// Progress continues across the handoff to this server.
		if fmt.Sprint(done) != "[1 2]" {
			t.Fatalf("progress %v", done)
		}
	})
	t.Run("node refusing the path falls back", func(t *testing.T) {
		s := &sampler{settings: settings, nodes: nodeList{node}, reservations: &nodepool.Reservations{}, local: ok,
			remote: fakeRemote(func(mediasample.Request) (mediasample.Result, error) {
				return mediasample.Result{}, &mediasample.RemoteError{Status: http.StatusBadRequest, Reason: mediasample.ReasonNodeUnavailable}
			})}
		if _, where, err := s.run(context.Background(), reqs, nil); err != nil || where != ExecutionLocal {
			t.Fatalf("%q %v", where, err)
		}
	})
	t.Run("file failure does not fall back", func(t *testing.T) {
		s := &sampler{settings: settings, nodes: nodeList{node}, reservations: &nodepool.Reservations{}, local: ok,
			remote: fakeRemote(func(mediasample.Request) (mediasample.Result, error) {
				return mediasample.Result{}, &mediasample.RemoteError{Status: http.StatusUnprocessableEntity, Reason: mediasample.ReasonInvalidData}
			})}
		if _, _, err := s.run(context.Background(), reqs, nil); err == nil {
			t.Fatal("invalid data fell back to local")
		}
	})
	t.Run("transcode only does not fall back from a busy node", func(t *testing.T) {
		s := &sampler{settings: settingsMap{SettingExecution: ExecutionTranscodeOnly, settingJWTSecret: "secret"},
			nodes: nodeList{node}, reservations: &nodepool.Reservations{}, local: ok,
			remote: fakeRemote(func(mediasample.Request) (mediasample.Result, error) {
				return mediasample.Result{}, &mediasample.RemoteError{Status: http.StatusServiceUnavailable, Reason: mediasample.ReasonNodeUnavailable}
			})}
		if _, _, err := s.run(context.Background(), reqs, nil); err == nil {
			t.Fatal("transcode_nodes_only ran locally")
		}
	})
	t.Run("transcode only without node", func(t *testing.T) {
		s := &sampler{settings: settingsMap{SettingExecution: ExecutionTranscodeOnly, settingJWTSecret: "secret"},
			nodes: nodeList{}, reservations: &nodepool.Reservations{}, local: ok}
		if _, _, err := s.run(context.Background(), reqs, nil); !errors.Is(err, errNoNode) {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("prefer without node runs locally", func(t *testing.T) {
		s := &sampler{settings: settingsMap{}, nodes: nodeList{}, reservations: &nodepool.Reservations{}, local: ok}
		if _, where, err := s.run(context.Background(), reqs, nil); err != nil || where != ExecutionLocal {
			t.Fatalf("%q %v", where, err)
		}
	})
}

func TestPlanSpeech(t *testing.T) {
	file := &models.MediaFile{Duration: 1800, AudioTracks: []models.AudioTrack{
		{Language: "jpn", Default: true, Layout: "stereo"}, {Language: "eng", Layout: "5.1(side)"},
	}}
	plan, err := planSpeech(file, "en")
	if err != nil {
		t.Fatal(err)
	}
	if plan.AudioStream != 1 || !plan.CenterChannel || len(plan.Windows) != 15 {
		t.Fatalf("episode plan %+v", plan)
	}
	if last := plan.Windows[len(plan.Windows)-1]; last.StartSeconds+last.DurationSeconds != 1800 {
		t.Fatalf("last window %+v", last)
	}
	plan, _ = planSpeech(file, "fr")
	if plan.AudioStream != 0 || plan.CenterChannel {
		t.Fatalf("fallback plan %+v", plan)
	}
	file.Duration = 7000
	plan, _ = planSpeech(file, "en")
	if len(plan.Windows) != sampledWindows || plan.Windows[0].StartSeconds <= 0 ||
		math.Abs(plan.Windows[11].StartSeconds+windowSeconds-7000) > 400 {
		t.Fatalf("film plan %+v", plan.Windows)
	}
	if _, err := planSpeech(&models.MediaFile{}, "en"); err == nil {
		t.Fatal("no duration accepted")
	}
}

func TestSpeechCodecRoundTrip(t *testing.T) {
	windows := []mediasample.SpeechLevels{
		{StartSeconds: 12.5, FrameSeconds: mediasample.SpeechFrameSeconds, Levels: []byte{1, 2, 3}},
		{StartSeconds: 600, FrameSeconds: mediasample.SpeechFrameSeconds, Levels: []byte{}},
	}
	got, err := decodeSpeech(encodeSpeech(windows))
	if err != nil || len(got) != 2 || got[0].StartSeconds != 12.5 || string(got[0].Levels) != "\x01\x02\x03" || got[1].StartSeconds != 600 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := decodeSpeech([]byte{1, 2}); err == nil {
		t.Fatal("truncated payload decoded")
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

func TestExecuteSyncsSidecar(t *testing.T) {
	truth := subtitles.Timing{Scale: 1, OffsetMS: -3200}
	f := newFixture(t, truth, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	external, sidecar := sidecarFixture(t, f)
	job := f.runExternal(t, external.row)
	if job.Status != string(StatusSynced) {
		t.Fatalf("status %s: %+v", job.Status, f.jobs.finished[job.ID])
	}
	assertTiming(t, f.jobs.applied[job.ID], truth, 7200)
	if len(f.notifier.targets) != 1 || f.notifier.targets[0] != (subtitles.SyncTarget{MediaFileID: 9, ExternalPath: sidecar.Path}) {
		t.Fatalf("notified %+v", f.notifier.targets)
	}
}

func TestExecuteSidecarChangedOrGone(t *testing.T) {
	for name, change := range map[string]func(f *fixture, row *subtitles.ExternalTiming){
		"edited on disk": func(f *fixture, _ *subtitles.ExternalTiming) {
			f.svc.readFile = func(string) ([]byte, error) { return []byte("1\n00:00:01,000 --> 00:00:02,000\nEdited\n"), nil }
		},
		"deleted": func(f *fixture, _ *subtitles.ExternalTiming) {
			f.svc.readFile = func(string) ([]byte, error) { return nil, fs.ErrNotExist }
		},
		"no longer scanned": func(f *fixture, _ *subtitles.ExternalTiming) {
			f.svc.files.(fakeFiles).file.ExternalSubtitles = nil
		},
		"retimed meanwhile":       func(_ *fixture, row *subtitles.ExternalTiming) { row.Revision++ },
		"belongs to another file": func(_ *fixture, row *subtitles.ExternalTiming) { row.MediaFileID++ },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, subtitles.Timing{Scale: 1, OffsetMS: 2000}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
			external, _ := sidecarFixture(t, f)
			job, _, err := f.jobs.CreateExternal(context.Background(), external.row, TriggerManual, nil)
			if err != nil {
				t.Fatal(err)
			}
			change(f, external.row)
			f.svc.execute(context.Background(), job)
			outcome := f.jobs.finished[job.ID]
			if outcome.Status != JobFailed || outcome.Error != ErrSubtitleChanged.Error() || len(f.jobs.applied) != 0 {
				t.Fatalf("outcome %+v applied %v", outcome, f.jobs.applied)
			}
		})
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

func TestExecuteDoesNotAnnounceAJobThatEndedMeanwhile(t *testing.T) {
	f := newFixture(t, subtitles.Timing{Scale: 1, OffsetMS: 2000}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	sub := f.svc.rows.(*fakeSubtitles).sub
	job, _, err := f.jobs.Create(context.Background(), sub, TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.notifier.updates = nil
	sub.Revision++ // the job fails as changed
	f.jobs.finishErr = jobrunner.ErrJobTerminal
	f.svc.execute(context.Background(), job)
	if len(f.notifier.updates) != 0 {
		t.Fatalf("announced %+v", f.notifier.updates)
	}
}

func TestExecuteDoesNotAnnounceProgressOfAJobThatEndedMeanwhile(t *testing.T) {
	f := newFixture(t, subtitles.Timing{Scale: 1, OffsetMS: 2000}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	job, _, err := f.jobs.Create(context.Background(), f.svc.rows.(*fakeSubtitles).sub, TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.notifier.updates = nil
	f.jobs.progressErr = jobrunner.ErrJobTerminal
	f.svc.execute(context.Background(), job)
	// It stops at its first progress step: no audio decoded, nothing said.
	if len(f.notifier.updates) != 0 || f.decodes != 0 || len(f.jobs.finished) != 0 || len(f.jobs.applied) != 0 {
		t.Fatalf("announced %+v, decoded %d, finished %v, applied %v", f.notifier.updates, f.decodes, f.jobs.finished, f.jobs.applied)
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

// A sidecar edited, deleted, or dropped from the catalog while its audio is
// analyzed keeps the correction it had: the result would apply to bytes no
// longer served.
func TestExecuteSidecarChangedDuringAnalysis(t *testing.T) {
	for name, change := range map[string]func(f *fixture){
		"edited on disk": func(f *fixture) {
			f.svc.readFile = func(string) ([]byte, error) { return []byte("1\n00:00:01,000 --> 00:00:02,000\nEdited\n"), nil }
		},
		"deleted":           func(f *fixture) { f.svc.readFile = func(string) ([]byte, error) { return nil, fs.ErrNotExist } },
		"no longer scanned": func(f *fixture) { f.svc.files.(fakeFiles).file.ExternalSubtitles = nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, subtitles.Timing{Scale: 1, OffsetMS: 2000}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
			external, _ := sidecarFixture(t, f)
			job, _, err := f.jobs.CreateExternal(context.Background(), external.row, TriggerManual, nil)
			if err != nil {
				t.Fatal(err)
			}
			decode := f.svc.sampler.local
			f.svc.sampler.local = func(ctx context.Context, req mediasample.Request) (mediasample.Result, error) {
				change(f)
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

// Progress travels off the worker; the outcome still reaches players last.
func TestExecuteSendsProgressOffTheWorker(t *testing.T) {
	f := newFixture(t, subtitles.Timing{Scale: 1, OffsetMS: 1800}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	f.svc.inline = false
	job, _, err := f.jobs.Create(context.Background(), f.svc.rows.(*fakeSubtitles).sub, TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.notifier.updates = nil
	f.svc.execute(context.Background(), job)
	updates := f.notifier.updates
	if len(updates) < 2 || updates[len(updates)-1].Job.Status != string(StatusSynced) {
		t.Fatalf("updates %+v", updates)
	}
	for _, u := range updates[:len(updates)-1] {
		if u.Job.Status != JobRunning {
			t.Fatalf("step after the outcome or out of place: %+v", u.Job)
		}
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

func TestExecuteReportsProgressAndOutcome(t *testing.T) {
	truth := subtitles.Timing{Scale: 1, OffsetMS: 1800}
	f := newFixture(t, truth, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	job := f.run(t, TriggerManual)
	updates := f.notifier.updates
	if len(updates) != sampledWindows+3 {
		t.Fatalf("%d updates: %+v", len(updates), updates)
	}
	if u := updates[0]; u.Job.Status != JobRunning || u.Job.PublicPhase() != PhaseAnalyzing || *u.Job.Progress != 0 ||
		u.Target != (subtitles.SyncTarget{MediaFileID: 9, StoredID: 5}) {
		t.Fatalf("first update %+v", u)
	}
	last := 0.0
	for _, u := range updates[1 : sampledWindows+1] {
		if u.Job.Phase != PhaseAnalyzing || *u.Job.Progress <= last {
			t.Fatalf("decode update %+v after %v", u.Job, last)
		}
		last = *u.Job.Progress
	}
	if u := updates[sampledWindows+1]; u.Job.Phase != PhaseMatching || *u.Job.Progress <= last || *u.Job.Progress >= 1 {
		t.Fatalf("matching update %+v", u.Job)
	}
	final := updates[len(updates)-1]
	if final.Job.Status != string(StatusSynced) || final.Job.PublicPhase() != "" || final.Job.PublicProgress() != nil ||
		final.Timing != f.jobs.applied[job.ID] || final.Job.FinishedAt == nil {
		t.Fatalf("final update %+v", final)
	}
	if len(f.jobs.progress) != sampledWindows+2 {
		t.Fatalf("progress rows %v", f.jobs.progress)
	}

	// Cached speech skips straight to matching.
	f.notifier.updates = nil
	f.run(t, TriggerManual)
	if phases := len(f.notifier.updates); phases != 3 || f.notifier.updates[1].Job.Phase != PhaseMatching {
		t.Fatalf("cached run updates %+v", f.notifier.updates)
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
