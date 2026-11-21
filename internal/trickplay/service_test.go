package trickplay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
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
	// 5 h at 10 s: one geometry sheet, then batches of 11 sheets, the most
	// that fit maxChunkPixels.
	job := testJob(42, 18000)
	job.HDR = true
	s.process(t.Context(), job)

	if len(extractor.requests) != 3 || len(extractor.requests[0].Samples.Seconds) != 80 || len(extractor.requests[1].Samples.Seconds) != 880 || len(extractor.requests[2].Samples.Seconds) != 840 {
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

// fakeChecker serves every key except those in missing, through an endpoint
// that fails with err.
type fakeChecker struct {
	missing map[string]bool
	err     error
}

func (c fakeChecker) ObjectAvailable(_ context.Context, key string) (bool, error) {
	return !c.missing[key], c.err
}

// fakeDeliveryRecords holds claimable checks and records confirmations.
type fakeDeliveryRecords struct {
	due       [][]DeliveryCheck
	confirmed []int
}

func (r *fakeDeliveryRecords) ClaimDeliveryChecks(context.Context, string, string, time.Duration, int) ([]DeliveryCheck, error) {
	if len(r.due) == 0 {
		return nil, nil
	}
	batch := r.due[0]
	r.due = r.due[1:]
	return batch, nil
}

func (r *fakeDeliveryRecords) RecordDelivery(_ context.Context, fileID int, _ int64, _, _ string) error {
	r.confirmed = append(r.confirmed, fileID)
	return nil
}

func TestConfirmDeliveryPassStopsOnEndpointTrouble(t *testing.T) {
	full := func(first int) []DeliveryCheck {
		batch := make([]DeliveryCheck, deliveryBatch)
		for i := range batch {
			batch[i] = DeliveryCheck{FileID: first + i, Revision: 1, SheetCount: 1}
		}
		return batch
	}
	lagging := map[string]bool{SheetKey(1, 1, 0): true}
	everything := map[string]bool{}
	for _, check := range full(1) {
		everything[SheetKey(check.FileID, 1, 0)] = true
	}
	tests := map[string]struct {
		checker fakeChecker
		want    int
		wantErr bool
	}{
		// One lagging revision waits its recheck; the pass goes on.
		"one revision lags": {fakeChecker{missing: lagging}, 2*deliveryBatch - 1, false},
		// A batch that confirms nothing ends the pass before the backlog.
		"nothing served": {fakeChecker{missing: everything}, 0, false},
		// So does a probe error.
		"endpoint fails": {fakeChecker{err: errors.New("status 403")}, 0, true},
	}
	for name, tt := range tests {
		records := &fakeDeliveryRecords{due: [][]DeliveryCheck{full(1), full(1000)}}
		s := testService(newFakeQueue(), &fakeStore{}, &fakeExtractor{}, fakeSettings{})
		s.delivery = &externalDelivery{scope: "scope-a", checker: tt.checker, records: records}
		err := s.confirmDeliveryPass(t.Context())
		if len(records.confirmed) != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("%s: confirmed %d with error %v, want %d", name, len(records.confirmed), err, tt.want)
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

// TestSheetsToneMapNamesTheSourceSignal hands sheets the base signal playback
// classifies a file's catalog facts as, and keeps the best-effort chains for
// facts it cannot classify.
func TestSheetsToneMapNamesTheSourceSignal(t *testing.T) {
	profile5 := models.VideoTrack{DVProfile: 5, DVConfigPresent: true, DVBLCompatIDPresent: true, DVBLPresent: true, DVRPUPresent: true, DolbyVision: "Dolby Vision"}
	tests := map[string]struct {
		file *models.MediaFile
		want *mediasample.ToneMap
	}{
		"SDR": {&models.MediaFile{VideoTracks: []models.VideoTrack{{ColorTransfer: "bt709"}}}, nil},
		"HLG": {&models.MediaFile{HDR: true, VideoTracks: []models.VideoTrack{{ColorTransfer: "arib-std-b67", ColorPrimaries: "bt2020", ColorSpace: "bt2020nc", ColorRange: "tv"}}},
			&mediasample.ToneMap{AllowSoftware: true, Source: tonemap.SourceHLG}},
		"Dolby Vision profile 5": {&models.MediaFile{HDR: true, VideoTracks: []models.VideoTrack{profile5}},
			&mediasample.ToneMap{AllowSoftware: true, Source: tonemap.SourceDolbyVision}},
		"legacy Dolby Vision row": {&models.MediaFile{VideoTracks: []models.VideoTrack{{DolbyVision: "Dolby Vision", DVProfile: 8}}},
			&mediasample.ToneMap{AllowSoftware: true}},
		"legacy Dolby Vision profile 5 row": {&models.MediaFile{VideoTracks: []models.VideoTrack{{DolbyVision: "Dolby Vision", DVProfile: 5}}},
			&mediasample.ToneMap{AllowSoftware: true, Source: tonemap.SourceDolbyVision}},
	}
	for name, tt := range tests {
		got := sheetsToneMap(tt.file)
		if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
			t.Errorf("%s: tone map %+v, want %+v", name, got, tt.want)
		}
	}
}

// A full artwork store is not the file's fault: the file waits an hour and
// keeps its failure count, so previews resume soon after space is freed.
func TestClassifyReleasesFileWhenStorageIsFull(t *testing.T) {
	err := fmt.Errorf("store trickplay/1/1/0.jpg: %w", syscall.ENOSPC)
	if outcome, delay := (&Service{}).classify(t.Context(), err); outcome != Released || delay != releaseDelay {
		t.Fatalf("a full store classifies as %v after %v, want Released after %v", outcome, delay, releaseDelay)
	}
}

// After a sheet write finds the store full, the service claims no more files
// for a while: each would be decoded only for its first write to be refused.
func TestDispatchPausesAfterStorageIsFull(t *testing.T) {
	q := newFakeQueue(testJob(1, 600), testJob(2, 600))
	extractor := &fakeExtractor{}
	s := testService(q, &fakeStore{err: fmt.Errorf("blobstore: %w", syscall.ENOSPC)}, extractor, fakeSettings{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.dispatch(ctx)
	}()
	for deadline := time.Now().Add(5 * time.Second); ; {
		q.mu.Lock()
		_, finished := q.finished[1]
		q.mu.Unlock()
		if finished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first file was never released")
		}
		runtime.Gosched()
	}
	// A negative check: no second claim may come within the window.
	time.Sleep(100 * time.Millisecond) //nolint:forbidigo // waits out a window in which nothing may happen
	cancel()
	<-done
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claims != 1 || q.finished[1] != (finishCall{Released, releaseDelay}) {
		t.Fatalf("claims = %d, file 1 %+v; want one claim, released for %v", q.claims, q.finished[1], releaseDelay)
	}
}
