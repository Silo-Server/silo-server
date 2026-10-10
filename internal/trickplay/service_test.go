package trickplay

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeSettings map[string]string

func (f fakeSettings) Get(_ context.Context, key string) (string, error) {
	return f[key], nil
}

type finishCall struct {
	outcome Outcome
	delay   time.Duration
}

// fakeQueue is an in-memory queue of jobs.
type fakeQueue struct {
	mu          sync.Mutex
	jobs        []*Job
	heartbeatOK bool
	publishOK   bool
	beginOK     bool
	claims      int
	published   map[int]Published
	finished    map[int]finishCall
	revisions   int64
}

func newFakeQueue(jobs ...*Job) *fakeQueue {
	return &fakeQueue{jobs: jobs, heartbeatOK: true, publishOK: true, beginOK: true, published: map[int]Published{}, finished: map[int]finishCall{}}
}

func (q *fakeQueue) Claim(context.Context, string, time.Duration) (*Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) == 0 {
		return nil, nil
	}
	q.claims++
	job := q.jobs[0]
	q.jobs = q.jobs[1:]
	return job, nil
}

func (q *fakeQueue) Heartbeat(context.Context, int, string, time.Duration) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.heartbeatOK, nil
}

func (q *fakeQueue) BeginUpload(context.Context, int, string) (int64, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.revisions++
	return 7000 + q.revisions, q.beginOK, nil
}

func (q *fakeQueue) Publish(_ context.Context, fileID int, _ string, _ int64, p Published) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.publishOK {
		q.published[fileID] = p
	}
	return q.publishOK, nil
}

func (q *fakeQueue) Finish(_ context.Context, fileID int, _ string, outcome Outcome, _ string, delay time.Duration) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.finished[fileID] = finishCall{outcome, delay}
	return true, nil
}

func (q *fakeQueue) Reconcile(context.Context, Recipe, string, int) (ReconcileStats, error) {
	return ReconcileStats{}, nil
}

type fakeStore struct {
	mu   sync.Mutex
	keys []string
	err  error
}

func (s *fakeStore) Put(_ context.Context, key string, _ []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.keys = append(s.keys, key)
	return nil
}

func (s *fakeStore) Identity() string { return "local|/test" }

// fakeExtractor returns one sheet per full or partial grid of samples, and
// records the requests.
type fakeExtractor struct {
	mu       sync.Mutex
	requests []mediasample.Request
	err      error
	block    chan struct{}
}

func (e *fakeExtractor) Extract(ctx context.Context, _ *Job, req mediasample.Request) (mediasample.Result, error) {
	e.mu.Lock()
	e.requests = append(e.requests, req)
	e.mu.Unlock()
	if e.block != nil {
		select {
		case <-e.block:
		case <-ctx.Done():
			return mediasample.Result{}, &mediasample.Error{Reason: mediasample.ReasonCanceled, Attempts: []mediasample.AttemptError{{Reason: mediasample.ReasonCanceled, Err: ctx.Err()}}}
		}
	}
	if e.err != nil {
		return mediasample.Result{}, e.err
	}
	perSheet := req.Sheets.Columns * req.Sheets.Rows
	var result mediasample.Result
	for i := 0; i*perSheet < len(req.Samples.Seconds); i++ {
		result.Sheets = append(result.Sheets, mediasample.Sheet{Index: i, Thumbnails: min(perSheet, len(req.Samples.Seconds)-i*perSheet), JPEG: make([]byte, 1000+i)})
	}
	result.SheetFrames.Decoded = len(req.Samples.Seconds)
	result.Decoder = "software"
	return result, nil
}

func testService(q *fakeQueue, store *fakeStore, extractor Extractor, settings fakeSettings) *Service {
	s := newService(q, store, settings, extractor, "node-a")
	s.logger = slog.New(slog.DiscardHandler)
	return s
}

func testJob(fileID, seconds int) *Job {
	return &Job{FileID: fileID, FilePath: "/media/a.mkv", DurationSeconds: seconds,
		VideoTracks: []models.VideoTrack{{Width: 1920, Height: 800, AspectRatio: "12:5", BitDepth: 10}}}
}

func TestGenerateChunksUploadsAndPublishes(t *testing.T) {
	q := newFakeQueue()
	store := &fakeStore{}
	extractor := &fakeExtractor{}
	s := testService(q, store, extractor, fakeSettings{IntervalSetting: "10"})
	// 5 h at 10 s: one geometry sheet, then batches of at most 16 sheets.
	job := testJob(42, 18000)
	job.HDR = true
	s.process(t.Context(), job)

	if len(extractor.requests) != 3 || len(extractor.requests[0].Samples.Seconds) != 80 || len(extractor.requests[1].Samples.Seconds) != 1280 || len(extractor.requests[2].Samples.Seconds) != 440 {
		t.Fatalf("requests of %d and %d samples", len(extractor.requests[0].Samples.Seconds), len(extractor.requests[len(extractor.requests)-1].Samples.Seconds))
	}
	first := extractor.requests[0]
	if first.Sheets.TileWidth != 300 || first.Sheets.TileHeight != 126 || first.Sheets.Columns != 10 || first.Sheets.ToneMap == nil ||
		!first.Background || first.VideoBitDepth != 10 || first.Samples.Seconds[0] != 5 || extractor.requests[1].Samples.Seconds[0] != 805 {
		t.Fatalf("request %+v %+v", first.Sheets, first.Samples.Seconds[:2])
	}
	if len(store.keys) != 23 || store.keys[0] != "trickplay/42/7001/0.7001.jpg" || store.keys[22] != "trickplay/42/7001/22.7001.jpg" {
		t.Fatalf("keys %v", store.keys)
	}
	published, ok := q.published[42]
	if !ok || published.Count != 1800 || len(published.SheetBytes) != 23 || published.Height != 126 || published.StoreIdentity != "local|/test" ||
		published.Recipe != (Recipe{Width: 300, IntervalMS: 10000}) {
		t.Fatalf("published %+v", published)
	}
	if q.revisions != 1 || len(q.finished) != 0 {
		t.Fatalf("revisions %d, finished %v", q.revisions, q.finished)
	}
}

type geometryExtractor struct {
	fakeExtractor
	heights []int
}

func (e *geometryExtractor) Extract(ctx context.Context, job *Job, req mediasample.Request) (mediasample.Result, error) {
	result, err := e.fakeExtractor.Extract(ctx, job, req)
	if err == nil {
		result.SheetTileHeight = e.heights[min(len(e.requests)-1, len(e.heights)-1)]
	}
	return result, err
}

func TestGeneratePublishesDecodedGeometryAndRejectsMixedChunks(t *testing.T) {
	for _, heights := range [][]int{{284, 284}, {284, 90}} {
		q, store := newFakeQueue(), &fakeStore{}
		extractor := &geometryExtractor{heights: heights}
		s := testService(q, store, extractor, fakeSettings{WidthSetting: "160"})
		published, err := s.generate(t.Context(), testJob(42, 18000))
		if heights[0] != heights[1] {
			if err == nil || len(q.published) != 0 {
				t.Fatalf("mixed geometry published: %+v, %v", published, err)
			}
		} else if err != nil || published.Height != 284 || !extractor.requests[0].Sheets.UseInputAspect {
			t.Fatalf("decoded geometry = %+v, %v", published, err)
		}
	}
}

func TestProcessOutcomes(t *testing.T) {
	unreadable := errors.New("stat: no such file")
	tests := map[string]struct {
		setup func(*fakeQueue, *fakeStore, *fakeExtractor, *Service)
		want  *finishCall
	}{
		"permanent": {func(_ *fakeQueue, _ *fakeStore, e *fakeExtractor, _ *Service) {
			e.err = &mediasample.Error{Reason: mediasample.ReasonExit, Attempts: []mediasample.AttemptError{{Reason: mediasample.ReasonExit, Err: errors.New("exit 1"), StderrTail: "Invalid data found when processing input"}}}
		}, &finishCall{Unusable, 0}},
		"transient": {func(_ *fakeQueue, _ *fakeStore, e *fakeExtractor, _ *Service) {
			e.err = &mediasample.Error{Reason: mediasample.ReasonTimeout, Attempts: []mediasample.AttemptError{{Reason: mediasample.ReasonTimeout}}}
		}, &finishCall{Failed, 0}},
		"storage": {func(_ *fakeQueue, st *fakeStore, _ *fakeExtractor, _ *Service) {
			st.err = errors.New("s3 down")
		}, &finishCall{Failed, 0}},
		"unreadable input": {func(_ *fakeQueue, _ *fakeStore, e *fakeExtractor, _ *Service) {
			e.err = &inputError{err: fmt.Errorf("%w: %w", unreadable, fs.ErrNotExist)}
		}, &finishCall{Released, releaseDelay}},
		"no node": {func(_ *fakeQueue, _ *fakeStore, e *fakeExtractor, _ *Service) {
			e.err = errNoNode
		}, &finishCall{Released, time.Minute}},
		"lease lost at upload": {func(q *fakeQueue, _ *fakeStore, _ *fakeExtractor, _ *Service) {
			q.beginOK = false
		}, nil},
		"lease lost at publish": {func(q *fakeQueue, _ *fakeStore, _ *fakeExtractor, _ *Service) {
			q.publishOK = false
		}, nil},
	}
	for name, tt := range tests {
		q, store, extractor := newFakeQueue(), &fakeStore{}, &fakeExtractor{}
		s := testService(q, store, extractor, fakeSettings{})
		tt.setup(q, store, extractor, s)
		s.process(t.Context(), testJob(1, 600))
		got, finished := q.finished[1]
		switch {
		case tt.want == nil && finished:
			t.Errorf("%s: finished %+v after losing the lease", name, got)
		case tt.want != nil && (!finished || got != *tt.want):
			t.Errorf("%s: finished %+v (%t), want %+v", name, got, finished, *tt.want)
		}
	}
}

func TestProcessReleasesOnShutdown(t *testing.T) {
	q, extractor := newFakeQueue(), &fakeExtractor{block: make(chan struct{})}
	s := testService(q, &fakeStore{}, extractor, fakeSettings{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.process(ctx, testJob(3, 600))
	}()
	cancel()
	<-done
	if got := q.finished[3]; got != (finishCall{Released, 0}) {
		t.Fatalf("finished %+v, want an immediate release", got)
	}
}

// TestHeartbeatLossCancelsWork refuses the lease renewal while a run is in
// progress: the run must stop, and the service must not record an outcome
// on a row another server now holds.
func TestHeartbeatLossCancelsWork(t *testing.T) {
	q, extractor := newFakeQueue(), &fakeExtractor{block: make(chan struct{})}
	q.heartbeatOK = false
	s := testService(q, &fakeStore{}, extractor, fakeSettings{})
	s.heartbeatEvery = 5 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.process(t.Context(), testJob(5, 600))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run kept going after its lease was lost")
	}
	if _, finished := q.finished[5]; finished || len(q.published) != 0 {
		t.Fatalf("recorded an outcome after losing the lease: %+v", q.finished)
	}
}

func TestDispatchRunsQueuedJobs(t *testing.T) {
	jobs := []*Job{testJob(10, 600), testJob(11, 600), testJob(12, 600)}
	q := newFakeQueue(jobs...)
	s := testService(q, &fakeStore{}, &fakeExtractor{}, fakeSettings{WorkersSetting: "2"})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.dispatch(ctx)
	}()
	deadline := time.After(5 * time.Second)
	for {
		q.mu.Lock()
		n := len(q.published)
		q.mu.Unlock()
		if n == 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("published %d of 3", n)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
	var ids []int
	for id := range q.published {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []int{10, 11, 12}) {
		t.Fatalf("published %v", ids)
	}
}

func TestReadThroughAVI(t *testing.T) {
	for container, want := range map[string]bool{"avi": true, "AVI": true, "mkv": false, "mp4": false, "ts": false, "": false} {
		if got := readThrough(&Job{Container: container}); got != want {
			t.Errorf("%q: %t", container, got)
		}
	}
	q, extractor := newFakeQueue(), &fakeExtractor{}
	s := testService(q, &fakeStore{}, extractor, fakeSettings{})
	job := testJob(9, 600)
	job.Container = "avi"
	s.process(t.Context(), job)
	if len(extractor.requests) != 1 || !extractor.requests[0].Samples.ReadThrough {
		t.Fatalf("an AVI was not read through: %+v", extractor.requests)
	}
}

func TestAttemptPlan(t *testing.T) {
	if plan := AttemptPlan(false, 720); len(plan) != 1 || plan[0].Hardware || plan[0].TimeoutSeconds != 120+2*720 {
		t.Fatalf("software plan %+v", plan)
	}
	plan := AttemptPlan(true, 1600)
	if len(plan) != 2 || !plan[0].Hardware || plan[0].TimeoutSeconds != 120+0.5*1600 || plan[1].Hardware {
		t.Fatalf("hardware plan %+v", plan)
	}
	if long := AttemptPlan(false, 1_000_000); long[0].TimeoutSeconds != maxAttemptTimeoutSeconds {
		t.Fatalf("timeout not capped: %+v", long)
	}
}

func TestDisplayAspect(t *testing.T) {
	for _, tt := range []struct {
		track models.VideoTrack
		want  float64
	}{
		{models.VideoTrack{Width: 720, Height: 480, AspectRatio: "16:9"}, 16.0 / 9},
		{models.VideoTrack{Width: 1920, Height: 800, AspectRatio: "0:1"}, 2.4},
		{models.VideoTrack{}, 0},
	} {
		if got := displayAspect(tt.track); got != tt.want {
			t.Errorf("%+v: %v, want %v", tt.track, got, tt.want)
		}
	}
}

type unreadableSettings struct{}

func (unreadableSettings) Get(context.Context, string) (string, error) {
	return "", errors.New("statement timeout")
}

// TestProcessReleasesWhenSettingsAreUnreadable must not make sheets to the
// default recipe: a file published at another width would be made again.
func TestProcessReleasesWhenSettingsAreUnreadable(t *testing.T) {
	q, extractor := newFakeQueue(), &fakeExtractor{}
	s := newService(q, &fakeStore{}, unreadableSettings{}, extractor, "node-a")
	s.logger = slog.New(slog.DiscardHandler)

	s.process(t.Context(), testJob(8, 600))

	if got := q.finished[8]; got != (finishCall{Released, time.Minute}) {
		t.Fatalf("finished %+v, want a release that counts no failure", got)
	}
	if len(extractor.requests) != 0 || len(q.published) != 0 {
		t.Fatalf("made sheets without the settings: %d requests", len(extractor.requests))
	}
	if _, err := s.Recipe(t.Context()); !errors.Is(err, errSettingsUnreadable) {
		t.Fatalf("Recipe error %v", err)
	}
}

// lockedSettings is a settings reader a test can change while the service
// reads it.
type lockedSettings struct {
	mu     sync.Mutex
	values map[string]string
	reads  chan string
}

func (l *lockedSettings) Get(_ context.Context, key string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reads != nil {
		select {
		case l.reads <- key:
		default:
		}
	}
	return l.values[key], nil
}

func (l *lockedSettings) set(key, value string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.values[key] = value
}

// TestNewWidthReconcilesRightAway reconciles once the width changes, not at
// the first reading and not while it stays the same.
func TestNewWidthReconcilesRightAway(t *testing.T) {
	settings := &lockedSettings{values: map[string]string{WidthSetting: "300"}, reads: make(chan string, 32)}
	s := newService(newFakeQueue(), &fakeStore{}, settings, &fakeExtractor{}, "node-a")
	s.settingsEvery = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.followSettings(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// A second width read proves the first recipe became the baseline.
	reads := 0
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for reads < 2 {
		select {
		case key := <-settings.reads:
			if key == WidthSetting {
				reads++
			}
		case <-deadline.C:
			t.Fatal("initial recipe was not read")
		}
	}
	select {
	case <-s.reconcile:
		t.Fatal("reconciled without a settings change")
	default:
	}
	settings.set(WidthSetting, "320")
	select {
	case <-s.reconcile:
	case <-time.After(5 * time.Second):
		t.Fatal("a new width did not reconcile")
	}
}

// TestRequestedReconcileRetriesAfterLockContention keeps a requested pass
// that another server's lock skipped: the server tries again.
func TestRequestedReconcileRetriesAfterLockContention(t *testing.T) {
	s := newService(newFakeQueue(), &fakeStore{}, fakeSettings{}, &fakeExtractor{}, "node-a")
	s.reconcileRetry = 5 * time.Millisecond
	passes := make(chan bool, 4)
	calls := 0
	s.reconcilePass = func(context.Context) (ReconcileStats, bool, error) {
		// The first pass finds the lock taken.
		calls++
		ran := calls > 1
		passes <- ran
		return ReconcileStats{}, ran, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.reconcileOnRequest(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	s.ReconcileSoon()
	for _, want := range []bool{false, true} {
		select {
		case ran := <-passes:
			if ran != want {
				t.Fatalf("pass ran=%v, want %v", ran, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a pass skipped by lock contention was not retried")
		}
	}
}

// settingExtractor changes a setting while a run is in progress.
type settingExtractor struct {
	fakeExtractor
	settings *lockedSettings
}

func (e *settingExtractor) Extract(ctx context.Context, job *Job, req mediasample.Request) (mediasample.Result, error) {
	e.settings.set(WidthSetting, "320")
	return e.fakeExtractor.Extract(ctx, job, req)
}

// TestPublishAtOldRecipeRequestsReconcile requeues sheets made to settings
// that changed while the run held its lease, since that run's reconcile
// skipped the row.
func TestPublishAtOldRecipeRequestsReconcile(t *testing.T) {
	settings := &lockedSettings{values: map[string]string{WidthSetting: "300"}}
	q := newFakeQueue()
	s := newService(q, &fakeStore{}, settings, &settingExtractor{settings: settings}, "node-a")
	s.logger = slog.New(slog.DiscardHandler)

	s.process(t.Context(), testJob(9, 600))

	if q.published[9].Recipe.Width != 300 {
		t.Fatalf("published %+v, want the recipe in force at the start", q.published[9].Recipe)
	}
	select {
	case <-s.reconcile:
	default:
		t.Fatal("publishing an outdated recipe did not request a reconcile")
	}

	// Unchanged settings request nothing.
	s.process(t.Context(), testJob(10, 600))
	select {
	case <-s.reconcile:
		t.Fatal("a current recipe requested a reconcile")
	default:
	}
}
