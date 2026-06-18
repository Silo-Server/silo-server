package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// recordingRepo is a JobRepository that records ResetStaleJobs calls and
// insert/quota-count activity and no-ops everything else, so service behavior
// can be tested in isolation.
type recordingRepo struct {
	mu         sync.Mutex
	resets     int
	lastBefore time.Time
	inserts    int
	quotaUsed  int // returned by CountTranscribeJobsByUserSince
	lastJob    Job
	completed  []int
	failures   []recordedFailure
	progress   []recordedProgress
}

type recordedFailure struct {
	id      int64
	status  JobStatus
	message string
}

type recordedProgress struct {
	progress float64
	message  string
}

// InsertJob mirrors the Postgres repo's atomic quota guard: a non-nil quota
// is checked against quotaUsed before the insert, so the tests exercise the
// same enforcement path production uses.
func (r *recordingRepo) InsertJob(_ context.Context, job *Job, quota *JobQuota) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if quota != nil && r.quotaUsed >= quota.Limit {
		return &QuotaExceededError{Limit: quota.Limit, Used: r.quotaUsed, Period: quota.Period}
	}
	r.inserts++
	job.ID = int64(r.inserts)
	r.lastJob = *job
	return nil
}
func (r *recordingRepo) GetJob(context.Context, int64) (*Job, error) { return nil, nil }
func (r *recordingRepo) GetActiveJobByIdempotencyKey(context.Context, string) (*Job, error) {
	return nil, nil
}
func (r *recordingRepo) ListJobsByMediaFile(context.Context, int) ([]Job, error) { return nil, nil }
func (r *recordingRepo) CountTranscribeJobsByUserSince(context.Context, int, time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.quotaUsed, nil
}
func (r *recordingRepo) UpdateProgress(_ context.Context, _ int64, _ JobStatus, progress float64, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, recordedProgress{progress: progress, message: message})
	return nil
}
func (r *recordingRepo) CompleteJob(_ context.Context, _ int64, subtitleID int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completed = append(r.completed, subtitleID)
	return nil
}
func (r *recordingRepo) FailJob(_ context.Context, id int64, status JobStatus, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, recordedFailure{id: id, status: status, message: message})
	return nil
}
func (r *recordingRepo) Heartbeat(context.Context, int64) error { return nil }

func (r *recordingRepo) ResetStaleJobs(_ context.Context, before time.Time, _ string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resets++
	r.lastBefore = before
	return 0, nil
}

func (r *recordingRepo) snapshot() (int, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resets, r.lastBefore
}

// stubTranscriber satisfies Transcriber so TranscribeEnabled() is true in
// tests; it returns no cues.
type stubTranscriber struct{}

func (stubTranscriber) Transcribe(context.Context, TranscribeJobRequest,
	TranscribeChunkCallback) ([]SubtitleCue, string, error) {
	return nil, "", nil
}

// nilFileResolver reports every media file as missing, so dispatched test jobs
// fail fast instead of touching the filesystem.
type nilFileResolver struct{}

func (nilFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) { return nil, nil }

// newQuotaTestService builds a transcribe-ready service whose repo reports
// `used` transcription jobs in the current quota window.
func newQuotaTestService(t *testing.T, used, limit int) (*Service, *recordingRepo) {
	t.Helper()
	repo := &recordingRepo{quotaUsed: used}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := Config{
		Configured:            true,
		TranscribeEnabled:     true,
		ASRModel:              "whisper-1",
		TranscribeQuotaJobs:   limit,
		TranscribeQuotaPeriod: QuotaPeriodDay,
	}
	svc := NewService(ctx, cfg, repo, nil, stubTranscriber{}, nil, nil, nilFileResolver{}, nil, "", nil, nil)
	return svc, repo
}

func transcribeRequest(userID int, exempt bool) JobRequest {
	return JobRequest{
		MediaFileID: 1,
		Kind:        JobKindTranscribe,
		SourceIndex: -1,
		RequestedBy: &userID,
		QuotaExempt: exempt,
	}
}

func TestEnqueueRejectsTranscribeOverQuota(t *testing.T) {
	svc, repo := newQuotaTestService(t, 3, 3)

	_, err := svc.Enqueue(context.Background(), transcribeRequest(42, false))
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("Enqueue error = %v, want ErrQuotaExceeded", err)
	}
	var qe *QuotaExceededError
	if !errors.As(err, &qe) {
		t.Fatalf("error %v does not unwrap to *QuotaExceededError", err)
	}
	if qe.Limit != 3 || qe.Used != 3 || qe.Period != QuotaPeriodDay {
		t.Errorf("quota error = %+v, want limit=3 used=3 period=day", qe)
	}
	if repo.inserts != 0 {
		t.Errorf("job was inserted despite exceeded quota (inserts=%d)", repo.inserts)
	}
}

func TestEnqueueAllowsTranscribeUnderQuota(t *testing.T) {
	svc, repo := newQuotaTestService(t, 2, 3)

	if _, err := svc.Enqueue(context.Background(), transcribeRequest(42, false)); err != nil {
		t.Fatalf("Enqueue under quota failed: %v", err)
	}
	if repo.inserts != 1 {
		t.Errorf("inserts = %d, want 1", repo.inserts)
	}
}

func TestEnqueueQuotaExemptions(t *testing.T) {
	t.Run("exempt", func(t *testing.T) {
		svc, repo := newQuotaTestService(t, 10, 1)
		if _, err := svc.Enqueue(context.Background(), transcribeRequest(42, true)); err != nil {
			t.Fatalf("exempt Enqueue blocked by quota: %v", err)
		}
		if repo.inserts != 1 {
			t.Errorf("inserts = %d, want 1", repo.inserts)
		}
	})
	t.Run("unlimited", func(t *testing.T) {
		svc, repo := newQuotaTestService(t, 10, 0)
		if _, err := svc.Enqueue(context.Background(), transcribeRequest(42, false)); err != nil {
			t.Fatalf("Enqueue with quota disabled failed: %v", err)
		}
		if repo.inserts != 1 {
			t.Errorf("inserts = %d, want 1", repo.inserts)
		}
	})
}

func TestEnqueueNormalizesSourceLanguageHint(t *testing.T) {
	svc, repo := newQuotaTestService(t, 0, 0)

	_, err := svc.Enqueue(context.Background(), JobRequest{
		MediaFileID:    1,
		Kind:           JobKindTranscribe,
		SourceIndex:    -1,
		SourceLanguage: "eng",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if repo.lastJob.SourceLanguage != "en" {
		t.Errorf("source language = %q, want en", repo.lastJob.SourceLanguage)
	}
}

func TestTranscribeQuotaStatus(t *testing.T) {
	svc, _ := newQuotaTestService(t, 2, 5)

	got, err := svc.TranscribeQuota(context.Background(), 42, false)
	if err != nil {
		t.Fatalf("TranscribeQuota: %v", err)
	}
	want := QuotaStatus{Limited: true, Limit: 5, Used: 2, Remaining: 3, Period: QuotaPeriodDay}
	if got != want {
		t.Errorf("TranscribeQuota = %+v, want %+v", got, want)
	}

	exempt, err := svc.TranscribeQuota(context.Background(), 42, true)
	if err != nil {
		t.Fatalf("TranscribeQuota (exempt): %v", err)
	}
	if exempt.Limited {
		t.Errorf("exempt quota = %+v, want unlimited", exempt)
	}
}

// Recover reaps immediately using a heartbeat cutoff of now-staleJobThreshold,
// not "every active job", so a live worker's jobs survive a peer's startup.
func TestRecoverReapsStaleJobsImmediately(t *testing.T) {
	repo := &recordingRepo{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the reaper goroutine started by Recover

	svc := NewService(ctx, Config{}, repo, nil, nil, nil, nil, nil, nil, "", nil, nil)

	approxNow := time.Now()
	svc.Recover()

	resets, before := repo.snapshot()
	if resets < 1 {
		t.Fatalf("Recover did not reap immediately: resets=%d", resets)
	}
	want := approxNow.Add(-jobrunner.StaleJobThreshold)
	if diff := before.Sub(want); diff > 2*time.Second || diff < -2*time.Second {
		t.Errorf("stale cutoff = %v, want ~%v (now-staleJobThreshold)", before, want)
	}
}

type runTranscribeFileResolver struct {
	file *models.MediaFile
}

func (r runTranscribeFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	return r.file, nil
}

type chunkedTranscriber struct {
	chunks   [][]SubtitleCue
	detected string
	trace    *[]string
	lastReq  TranscribeJobRequest
}

func (t *chunkedTranscriber) Transcribe(_ context.Context, req TranscribeJobRequest,
	onChunk TranscribeChunkCallback) ([]SubtitleCue, string, error) {
	t.lastReq = req
	var all []SubtitleCue
	for i, chunk := range t.chunks {
		appendTrace(t.trace, fmt.Sprintf("transcribe:%d", i))
		if onChunk != nil {
			total := len(t.chunks)
			if req.Incremental {
				total = progressTotal(i+1, estimatedChunkTotal(req.DurationSeconds, req.ChunkSeconds))
			}
			onChunk(chunk, t.detected, i+1, total)
		}
		all = append(all, chunk...)
	}
	return all, t.detected, nil
}

type recordingTranslator struct {
	calls     []TranslateRequest
	errOnCall int
	trace     *[]string
}

func (t *recordingTranslator) Translate(_ context.Context, req TranslateRequest,
	onBatch func([]SubtitleCue, int, int)) ([]SubtitleCue, error) {
	t.calls = append(t.calls, TranslateRequest{
		Cues:           cloneTestCues(req.Cues),
		SourceLanguage: req.SourceLanguage,
		TargetLanguage: req.TargetLanguage,
	})
	call := len(t.calls)
	if t.errOnCall == call {
		return nil, errors.New("translator chunk failed")
	}

	out := cloneTestCues(req.Cues)
	for i := range out {
		for j, line := range out[i].Lines {
			out[i].Lines[j] = req.TargetLanguage + ":" + line
		}
	}
	if len(out) > 0 {
		appendTrace(t.trace, "translate:"+strings.Join(out[0].Lines, " "))
	}
	if onBatch != nil {
		onBatch(out, len(out), len(out))
	}
	return out, nil
}

type recordingSubtitleStore struct {
	stored []subtitles.StoreSubtitleRequest
}

func (s *recordingSubtitleStore) StoreSubtitle(_ context.Context, req subtitles.StoreSubtitleRequest) (*subtitles.DownloadedSubtitle, error) {
	s.stored = append(s.stored, req)
	return &subtitles.DownloadedSubtitle{
		ID:          len(s.stored),
		MediaFileID: req.MediaFileID,
		Provider:    req.Provider,
		Language:    req.Language,
		Format:      req.Format,
		ReleaseName: req.ReleaseName,
	}, nil
}

func (s *recordingSubtitleStore) GetSubtitleContent(context.Context, int) (*subtitles.DownloadedSubtitle, []byte, error) {
	return nil, nil, errors.New("not implemented")
}

type notifierEvent struct {
	kind    string
	cues    []playback.StreamCue
	done    int
	total   int
	message string
}

type recordingNotifier struct {
	events []notifierEvent
	trace  *[]string
}

func (n *recordingNotifier) SubtitleReady(_ context.Context, _ int, subtitleID int, language, _ string) {
	n.events = append(n.events, notifierEvent{kind: fmt.Sprintf("ready:%d:%s", subtitleID, language)})
}

func (n *recordingNotifier) TranslationStarted(context.Context, string, int, int64, string, string, string, int) {
	n.events = append(n.events, notifierEvent{kind: "started"})
}

func (n *recordingNotifier) TranslationCues(_ context.Context, _ string, _ int, _ int64, _ string,
	cues []playback.StreamCue, done, total int) {
	n.events = append(n.events, notifierEvent{kind: "cues", cues: cues, done: done, total: total})
	if len(cues) > 0 {
		appendTrace(n.trace, "stream:"+cues[0].Text)
	}
}

func (n *recordingNotifier) TranslationCompleted(context.Context, string, int, int64, string, int, string, string) {
	n.events = append(n.events, notifierEvent{kind: "completed"})
}

func (n *recordingNotifier) TranslationFailed(_ context.Context, _ string, _ int, _ int64, _, message string) {
	n.events = append(n.events, notifierEvent{kind: "failed", message: message})
}

func TestRunTranscribeTranslateStreamingInterleavesTranslationPerChunk(t *testing.T) {
	var trace []string
	transcriber := &chunkedTranscriber{
		detected: "en",
		trace:    &trace,
		chunks: [][]SubtitleCue{
			{testCue(60, "second")},
			{},
			{testCue(0, "first")},
		},
	}
	translator := &recordingTranslator{trace: &trace}
	store := &recordingSubtitleStore{}
	notifier := &recordingNotifier{trace: &trace}
	repo := &recordingRepo{}
	svc := newRunTranscribeService(repo, translator, transcriber, store, notifier, Config{LiveASRChunkSeconds: 30})

	svc.runTranscribe(context.Background(), &Job{
		ID:             7,
		MediaFileID:    10,
		Kind:           JobKindTranscribeTranslate,
		SourceIndex:    -1,
		TargetLanguage: "es",
		SessionID:      "session-1",
		StartPosition:  60,
	})

	if !transcriber.lastReq.Incremental {
		t.Fatalf("streaming request did not enable incremental extraction")
	}
	if transcriber.lastReq.ChunkSeconds != 30 {
		t.Fatalf("chunk override = %d, want 30", transcriber.lastReq.ChunkSeconds)
	}
	if got := len(translator.calls); got != 2 {
		t.Fatalf("Translate calls = %d, want one per non-empty chunk", got)
	}
	if strings.Join(translator.calls[0].Cues[0].Lines, " ") != "second" {
		t.Errorf("first translated chunk = %q, want playhead chunk", translator.calls[0].Cues[0].Lines)
	}
	if streamIdx, lastChunkIdx := traceIndex(trace, "stream:es:second"), traceIndex(trace, "transcribe:2"); streamIdx < 0 || lastChunkIdx < 0 || streamIdx > lastChunkIdx {
		t.Fatalf("first translated stream did not precede final ASR chunk: %v", trace)
	}

	cueEvents := cueNotifierEvents(notifier.events)
	if got := len(cueEvents); got != 2 {
		t.Fatalf("TranslationCues events = %d, want 2", got)
	}
	if cueEvents[0].done != 1 || cueEvents[0].total != 6 || cueEvents[1].done != 3 || cueEvents[1].total != 6 {
		t.Fatalf("chunk progress = (%d/%d, %d/%d), want 1/6 then 3/6",
			cueEvents[0].done, cueEvents[0].total, cueEvents[1].done, cueEvents[1].total)
	}
	if len(store.stored) != 2 {
		t.Fatalf("stored subtitles = %d, want transcript and translation", len(store.stored))
	}
	if store.stored[0].Provider != providerTranscribed || store.stored[1].Provider != providerTranslated {
		t.Fatalf("stored providers = %q, %q", store.stored[0].Provider, store.stored[1].Provider)
	}
	translated, err := ParseCues(store.stored[1].Data)
	if err != nil {
		t.Fatalf("parse stored translation: %v", err)
	}
	if len(translated) != 2 || strings.Join(translated[0].Lines, " ") != "es:first" ||
		strings.Join(translated[1].Lines, " ") != "es:second" {
		t.Fatalf("stored translation not sorted/complete: %#v", translated)
	}
	if got := fmt.Sprint(repo.completed); got != "[2]" {
		t.Fatalf("completed subtitle IDs = %s, want [2]", got)
	}
}

func TestRunTranscribeTranslateStreamingFailureStoresTranscript(t *testing.T) {
	var trace []string
	transcriber := &chunkedTranscriber{
		detected: "en",
		trace:    &trace,
		chunks: [][]SubtitleCue{
			{testCue(0, "one")},
			{testCue(30, "two")},
			{testCue(60, "three")},
		},
	}
	translator := &recordingTranslator{errOnCall: 2, trace: &trace}
	store := &recordingSubtitleStore{}
	notifier := &recordingNotifier{trace: &trace}
	repo := &recordingRepo{}
	svc := newRunTranscribeService(repo, translator, transcriber, store, notifier, Config{LiveASRChunkSeconds: 30})

	svc.runTranscribe(context.Background(), &Job{
		ID:             8,
		MediaFileID:    10,
		Kind:           JobKindTranscribeTranslate,
		SourceIndex:    -1,
		TargetLanguage: "es",
		SessionID:      "session-1",
	})

	if got := len(translator.calls); got != 2 {
		t.Fatalf("Translate calls = %d, want calls stop after failure", got)
	}
	if traceIndex(trace, "transcribe:2") < 0 {
		t.Fatalf("ASR did not continue through final chunk after translate failure: %v", trace)
	}
	if len(store.stored) != 1 || store.stored[0].Provider != providerTranscribed {
		t.Fatalf("stored subtitles = %#v, want transcript only", store.stored)
	}
	if len(repo.failures) != 1 || repo.failures[0].status != JobStatusFailed {
		t.Fatalf("failures = %#v, want one failed job", repo.failures)
	}
	if got := countNotifierKind(notifier.events, "failed"); got != 1 {
		t.Fatalf("TranslationFailed events = %d, want 1", got)
	}
	if len(repo.completed) != 0 {
		t.Fatalf("completed despite translation failure: %v", repo.completed)
	}
}

func TestRunTranscribeTranslateNonStreamingKeepsWholeFileTranslation(t *testing.T) {
	transcriber := &chunkedTranscriber{
		detected: "en",
		chunks: [][]SubtitleCue{
			{testCue(30, "later")},
			{testCue(0, "earlier")},
		},
	}
	translator := &recordingTranslator{}
	store := &recordingSubtitleStore{}
	notifier := &recordingNotifier{}
	repo := &recordingRepo{}
	svc := newRunTranscribeService(repo, translator, transcriber, store, notifier, Config{LiveASRChunkSeconds: 30})

	svc.runTranscribe(context.Background(), &Job{
		ID:             9,
		MediaFileID:    10,
		Kind:           JobKindTranscribeTranslate,
		SourceIndex:    -1,
		TargetLanguage: "es",
	})

	if transcriber.lastReq.Incremental {
		t.Fatalf("background request unexpectedly enabled incremental extraction")
	}
	if transcriber.lastReq.ChunkSeconds != 0 {
		t.Fatalf("background chunk override = %d, want 0", transcriber.lastReq.ChunkSeconds)
	}
	if got := len(translator.calls); got != 1 {
		t.Fatalf("Translate calls = %d, want one whole-file call", got)
	}
	if got := len(translator.calls[0].Cues); got != 2 {
		t.Fatalf("whole-file call cues = %d, want 2", got)
	}
	if got := countNotifierKind(notifier.events, "cues"); got != 0 {
		t.Fatalf("non-streaming TranslationCues events = %d, want 0", got)
	}
	if len(store.stored) != 2 || store.stored[1].Provider != providerTranslated {
		t.Fatalf("stored subtitles = %#v, want transcript and translated track", store.stored)
	}
}

func TestRunTranscribeTranslateStreamingUsesDetectedLanguageForLiveChunks(t *testing.T) {
	transcriber := &chunkedTranscriber{
		detected: "ja",
		chunks: [][]SubtitleCue{
			{testCue(0, "konnichiwa")},
		},
	}
	translator := &recordingTranslator{}
	store := &recordingSubtitleStore{}
	notifier := &recordingNotifier{}
	repo := &recordingRepo{}
	file := &models.MediaFile{
		ID:          10,
		FilePath:    "/media/movie.mkv",
		Duration:    180,
		AudioTracks: []models.AudioTrack{{Default: true}},
	}
	svc := newRunTranscribeServiceWithFile(repo, translator, transcriber, store, notifier,
		Config{LiveASRChunkSeconds: 30}, file)

	svc.runTranscribe(context.Background(), &Job{
		ID:             10,
		MediaFileID:    10,
		Kind:           JobKindTranscribeTranslate,
		SourceIndex:    -1,
		TargetLanguage: "es",
		SessionID:      "session-1",
	})

	if got := len(translator.calls); got != 1 {
		t.Fatalf("Translate calls = %d, want one live chunk call", got)
	}
	if got := translator.calls[0].SourceLanguage; got != "ja" {
		t.Fatalf("live chunk source language = %q, want detected ja", got)
	}
}

func newRunTranscribeService(
	repo *recordingRepo,
	translator Translator,
	transcriber Transcriber,
	store *recordingSubtitleStore,
	notifier Notifier,
	cfg Config,
) *Service {
	file := &models.MediaFile{
		ID:          10,
		FilePath:    "/media/movie.mkv",
		Duration:    180,
		AudioTracks: []models.AudioTrack{{Language: "eng", Default: true}},
	}
	return newRunTranscribeServiceWithFile(repo, translator, transcriber, store, notifier, cfg, file)
}

func newRunTranscribeServiceWithFile(
	repo *recordingRepo,
	translator Translator,
	transcriber Transcriber,
	store *recordingSubtitleStore,
	notifier Notifier,
	cfg Config,
	file *models.MediaFile,
) *Service {
	return NewService(context.Background(), cfg, repo, translator, transcriber, store, nil,
		runTranscribeFileResolver{file: file}, notifier, "", nil, nil)
}

func testCue(startSeconds float64, text string) SubtitleCue {
	start := time.Duration(startSeconds * float64(time.Second))
	return SubtitleCue{
		Start: start,
		End:   start + 2*time.Second,
		Lines: []string{text},
	}
}

func cloneTestCues(cues []SubtitleCue) []SubtitleCue {
	out := make([]SubtitleCue, len(cues))
	for i, cue := range cues {
		out[i] = cue
		out[i].Lines = append([]string(nil), cue.Lines...)
	}
	return out
}

func appendTrace(trace *[]string, value string) {
	if trace != nil {
		*trace = append(*trace, value)
	}
}

func traceIndex(trace []string, value string) int {
	for i, got := range trace {
		if got == value {
			return i
		}
	}
	return -1
}

func cueNotifierEvents(events []notifierEvent) []notifierEvent {
	var out []notifierEvent
	for _, event := range events {
		if event.kind == "cues" {
			out = append(out, event)
		}
	}
	return out
}

func countNotifierKind(events []notifierEvent, kind string) int {
	count := 0
	for _, event := range events {
		if event.kind == kind {
			count++
		}
	}
	return count
}
