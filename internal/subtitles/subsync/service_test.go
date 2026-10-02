package subsync

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"

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
	hasJob   bool
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
func (f *fakeJobs) Latest(context.Context, int) (*Job, error) { return nil, nil }
func (f *fakeJobs) LatestForSubtitles(context.Context, []int) (map[int]*Job, error) {
	return nil, nil
}
func (f *fakeJobs) HasJob(context.Context, int) (bool, error) {
	return f.hasJob, nil
}
func (f *fakeJobs) MarkRunning(context.Context, int64) error { return nil }
func (f *fakeJobs) Finish(_ context.Context, id int64, o Outcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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

type fakeNotifier struct{ calls int }

func (f *fakeNotifier) SubtitleTimingChanged(context.Context, int, int) { f.calls++ }

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

	// The same file's speech comes from the cache the second time.
	job = f.run(t, TriggerManual)
	if f.decodes != sampledWindows {
		t.Fatalf("second sync decoded again: %d", f.decodes)
	}
	if got := f.jobs.finished[job.ID].ExecutedOn; got != "cache" {
		t.Fatalf("executed on %q", got)
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

func TestExecuteLosesToConcurrentEdit(t *testing.T) {
	f := newFixture(t, subtitles.Timing{OffsetMS: 3000}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	f.jobs.applyErr = ErrSubtitleChanged
	job := f.run(t, TriggerManual)
	if job.Status != JobFailed || f.notifier.calls != 0 {
		t.Fatalf("status %s notifier %d", job.Status, f.notifier.calls)
	}
}

func TestExecuteEndsJobWhenApplyFails(t *testing.T) {
	f := newFixture(t, subtitles.Timing{OffsetMS: 3000}, settingsMap{SettingExecution: ExecutionLocal}, "stereo")
	f.jobs.applyErr = errors.New("connection reset")
	if job := f.run(t, TriggerManual); job.Status != JobFailed {
		t.Fatalf("status %s, want failed so a new sync can start", job.Status)
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
		if _, where, err := s.run(context.Background(), reqs); err != nil || where != "node:gpu-1" {
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
		results, where, err := s.run(context.Background(), reqs)
		if err != nil || where != ExecutionLocal || len(results) != 2 {
			t.Fatalf("%q %d %v", where, len(results), err)
		}
	})
	t.Run("node refusing the path falls back", func(t *testing.T) {
		s := &sampler{settings: settings, nodes: nodeList{node}, reservations: &nodepool.Reservations{}, local: ok,
			remote: fakeRemote(func(mediasample.Request) (mediasample.Result, error) {
				return mediasample.Result{}, &mediasample.RemoteError{Status: http.StatusBadRequest, Reason: mediasample.ReasonNodeUnavailable}
			})}
		if _, where, err := s.run(context.Background(), reqs); err != nil || where != ExecutionLocal {
			t.Fatalf("%q %v", where, err)
		}
	})
	t.Run("file failure does not fall back", func(t *testing.T) {
		s := &sampler{settings: settings, nodes: nodeList{node}, reservations: &nodepool.Reservations{}, local: ok,
			remote: fakeRemote(func(mediasample.Request) (mediasample.Result, error) {
				return mediasample.Result{}, &mediasample.RemoteError{Status: http.StatusUnprocessableEntity, Reason: mediasample.ReasonInvalidData}
			})}
		if _, _, err := s.run(context.Background(), reqs); err == nil {
			t.Fatal("invalid data fell back to local")
		}
	})
	t.Run("transcode only does not fall back from a busy node", func(t *testing.T) {
		s := &sampler{settings: settingsMap{SettingExecution: ExecutionTranscodeOnly, settingJWTSecret: "secret"},
			nodes: nodeList{node}, reservations: &nodepool.Reservations{}, local: ok,
			remote: fakeRemote(func(mediasample.Request) (mediasample.Result, error) {
				return mediasample.Result{}, &mediasample.RemoteError{Status: http.StatusServiceUnavailable, Reason: mediasample.ReasonNodeUnavailable}
			})}
		if _, _, err := s.run(context.Background(), reqs); err == nil {
			t.Fatal("transcode_nodes_only ran locally")
		}
	})
	t.Run("transcode only without node", func(t *testing.T) {
		s := &sampler{settings: settingsMap{SettingExecution: ExecutionTranscodeOnly, settingJWTSecret: "secret"},
			nodes: nodeList{}, reservations: &nodepool.Reservations{}, local: ok}
		if _, _, err := s.run(context.Background(), reqs); !errors.Is(err, errNoNode) {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("prefer without node runs locally", func(t *testing.T) {
		s := &sampler{settings: settingsMap{}, nodes: nodeList{}, reservations: &nodepool.Reservations{}, local: ok}
		if _, where, err := s.run(context.Background(), reqs); err != nil || where != ExecutionLocal {
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
