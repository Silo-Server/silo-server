package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

const providerTranslated = "translated"

const (
	// A running job refreshes its heartbeat every heartbeatInterval; one whose
	// heartbeat has not advanced for staleJobThreshold is treated as orphaned by
	// a crashed worker and reaped. The margin over heartbeatInterval avoids
	// reaping a job that is merely mid–LLM-call.
	heartbeatInterval = 30 * time.Second
	staleJobThreshold = 2 * time.Minute
	// How often the background reaper scans for orphaned jobs.
	reaperInterval = time.Minute
)

var (
	// ErrEngineNotConfigured is returned when translation is requested but the
	// engine is disabled or missing required settings.
	ErrEngineNotConfigured = errors.New("subtitle AI engine is not configured")
	// ErrInvalidRequest wraps caller-input validation failures (e.g. an invalid
	// target language) so handlers can map them to 400 rather than 500.
	ErrInvalidRequest = errors.New("invalid translation request")
	// ErrJobNotFound is returned for unknown job IDs.
	ErrJobNotFound = errors.New("subtitle ai job not found")
	// ErrSourceUnsupported is returned when the chosen source track cannot be
	// translated (bitmap track, or a styled/unsupported format in this version).
	ErrSourceUnsupported = errors.New("subtitle source is not supported for translation")
)

// MediaFileResolver loads a media file (path + subtitle metadata) by ID.
type MediaFileResolver interface {
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
}

// SubtitleStore stores a generated subtitle and reads existing stored ones.
// Satisfied by *subtitles.Manager.
type SubtitleStore interface {
	StoreSubtitle(ctx context.Context, req subtitles.StoreSubtitleRequest) (*subtitles.DownloadedSubtitle, error)
	GetSubtitleContent(ctx context.Context, id int) (*subtitles.DownloadedSubtitle, []byte, error)
}

// SubtitleLister lists stored subtitles for a media file, used to resolve a
// downloaded-source track by its position. Satisfied by subtitles.Repository.
type SubtitleLister interface {
	ListDownloadedSubtitles(ctx context.Context, mediaFileID int) ([]subtitles.DownloadedSubtitle, error)
}

// Service owns the AI subtitle job lifecycle: enqueue, bounded concurrent
// execution, progress/heartbeat, cancellation, and restart recovery.
type Service struct {
	// baseCtx is the application context; dispatched jobs and the reaper derive
	// from it so they stop when the server shuts down.
	baseCtx    context.Context
	cfg        Config
	repo       JobRepository
	translator Translator
	store      SubtitleStore
	lister     SubtitleLister
	files      MediaFileResolver
	notifier   Notifier // optional
	ffmpegPath string
	logger     *slog.Logger

	sem     chan struct{}
	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
	wg      sync.WaitGroup
}

// NewService wires a translation service. notifier may be nil. appCtx is the
// application lifecycle context; jobs and the reaper derive from it so they stop
// on shutdown. A nil appCtx falls back to context.Background().
func NewService(
	appCtx context.Context,
	cfg Config,
	repo JobRepository,
	translator Translator,
	store SubtitleStore,
	lister SubtitleLister,
	files MediaFileResolver,
	notifier Notifier,
	ffmpegPath string,
	logger *slog.Logger,
) *Service {
	maxConcurrent := cfg.MaxConcurrentJobs
	if maxConcurrent <= 0 {
		maxConcurrent = 2
	}
	if logger == nil {
		logger = slog.Default()
	}
	if appCtx == nil {
		appCtx = context.Background()
	}
	return &Service{
		baseCtx:    appCtx,
		cfg:        cfg,
		repo:       repo,
		translator: translator,
		store:      store,
		lister:     lister,
		files:      files,
		notifier:   notifier,
		ffmpegPath: ffmpegPath,
		logger:     logger,
		sem:        make(chan struct{}, maxConcurrent),
		cancels:    make(map[int64]context.CancelFunc),
	}
}

// Enabled reports whether translation can currently run.
func (s *Service) Enabled() bool { return s.cfg.Ready() }

// Recover clears jobs orphaned by a crashed worker and starts a background
// reaper that keeps doing so. Reaping is heartbeat-based (not "every active
// job"), so it is safe when multiple instances share one database: a job still
// being heartbeat-updated by a live worker is never reset. Call once at startup;
// jobs and the reaper derive from the application context passed to NewService,
// so they stop on shutdown.
func (s *Service) Recover() {
	s.reapStaleJobs()
	go s.reaperLoop()
}

// reaperLoop periodically reaps orphaned jobs until the application context is
// cancelled (server shutdown).
func (s *Service) reaperLoop() {
	ticker := time.NewTicker(reaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.baseCtx.Done():
			return
		case <-ticker.C:
			s.reapStaleJobs()
		}
	}
}

// reapStaleJobs fails any pending/running job whose heartbeat has not advanced
// within staleJobThreshold.
func (s *Service) reapStaleJobs() {
	before := time.Now().Add(-staleJobThreshold)
	n, err := s.repo.ResetStaleJobs(context.WithoutCancel(s.baseCtx), before, "interrupted by server restart")
	if err != nil {
		s.logger.Warn("failed to reset stale subtitle ai jobs", "error", err)
		return
	}
	if n > 0 {
		s.logger.Info("reset stale subtitle ai jobs", "count", n)
	}
}

// Enqueue validates and queues a job, returning immediately. If an identical
// job is already pending or running, that job is returned instead of a new one.
func (s *Service) Enqueue(ctx context.Context, req JobRequest) (*Job, error) {
	if !s.cfg.Ready() {
		return nil, ErrEngineNotConfigured
	}
	if req.Kind == "" {
		req.Kind = JobKindTranslate
	}

	target, err := subtitles.NormalizeLanguageCode(req.TargetLanguage)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid target language %q", ErrInvalidRequest, req.TargetLanguage)
	}
	req.TargetLanguage = target

	key := idempotencyKey(req.MediaFileID, req.Kind, req.SourceIndex, req.TargetLanguage, s.cfg.ChatModel)
	if existing, err := s.repo.GetActiveJobByIdempotencyKey(ctx, key); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	job := &Job{
		MediaFileID:     req.MediaFileID,
		Kind:            req.Kind,
		SourceIndex:     req.SourceIndex,
		SourceLanguage:  req.SourceLanguage,
		TargetLanguage:  req.TargetLanguage,
		Engine:          "openai",
		Model:           s.cfg.ChatModel,
		Status:          JobStatusPending,
		ProgressMessage: "Queued",
		IdempotencyKey:  key,
		RequestedBy:     req.RequestedBy,
		SessionID:       req.SessionID,
		StartPosition:   req.StartPosition,
	}
	if err := s.repo.InsertJob(ctx, job); err != nil {
		// A racing duplicate trips the partial unique index; return the winner.
		if existing, lookupErr := s.repo.GetActiveJobByIdempotencyKey(ctx, key); lookupErr == nil && existing != nil {
			return existing, nil
		}
		return nil, err
	}

	s.dispatch(*job)
	return job, nil
}

// GetJob returns a job by ID.
func (s *Service) GetJob(ctx context.Context, id int64) (*Job, error) {
	job, err := s.repo.GetJob(ctx, id)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, ErrJobNotFound
	}
	return job, nil
}

// ListJobs returns recent jobs for a media file.
func (s *Service) ListJobs(ctx context.Context, mediaFileID int) ([]Job, error) {
	return s.repo.ListJobsByMediaFile(ctx, mediaFileID)
}

// Cancel requests cancellation of a job.
func (s *Service) Cancel(ctx context.Context, id int64) error {
	job, err := s.repo.GetJob(ctx, id)
	if err != nil {
		return err
	}
	if job == nil {
		return ErrJobNotFound
	}

	s.mu.Lock()
	cancel := s.cancels[id]
	s.mu.Unlock()

	if cancel != nil {
		cancel()
		return nil
	}
	// No in-flight goroutine (e.g. another node, or never started): best-effort
	// terminal transition if it is still active.
	if !job.Status.Terminal() {
		return s.repo.FailJob(ctx, id, JobStatusCancelled, "cancelled")
	}
	return nil
}

// dispatch launches a bounded background goroutine to run the job.
func (s *Service) dispatch(job Job) {
	// Derive from the application context so a server shutdown cancels in-flight
	// translations (the per-job cancel still allows user-initiated cancellation).
	runCtx, cancel := context.WithCancel(s.baseCtx)
	s.mu.Lock()
	s.cancels[job.ID] = cancel
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.cancels, job.ID)
			s.mu.Unlock()
			cancel()
		}()

		// Bound concurrency so translation never starves transcodes.
		select {
		case s.sem <- struct{}{}:
		case <-runCtx.Done():
			_ = s.repo.FailJob(context.Background(), job.ID, JobStatusCancelled, "cancelled before start")
			return
		}
		defer func() { <-s.sem }()

		s.run(runCtx, &job)
	}()
}

func (s *Service) run(ctx context.Context, job *Job) {
	// Keep heartbeat_at fresh while the job runs (progress updates also bump it),
	// so the stale-job reaper never resets a job that is still alive during a long
	// single LLM call.
	stopHeartbeat := make(chan struct{})
	defer close(stopHeartbeat)
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.repo.Heartbeat(context.WithoutCancel(ctx), job.ID)
			}
		}
	}()

	if err := s.repo.UpdateProgress(ctx, job.ID, JobStatusRunning, 0, "Loading subtitle"); err != nil {
		s.logger.Warn("failed to mark subtitle ai job running", "job", job.ID, "error", err)
	}

	cues, sourceLang, err := s.loadSource(ctx, job)
	if err != nil {
		s.finishWithError(ctx, job, err)
		return
	}
	if job.SourceLanguage == "" {
		job.SourceLanguage = sourceLang
	}

	releaseName := translatedReleaseName(job.SourceLanguage, job.TargetLanguage)
	streaming := job.SessionID != "" && s.notifier != nil
	trackKey := liveTrackKey(job.ID)
	if streaming {
		s.notifier.TranslationStarted(ctx, job.SessionID, job.MediaFileID, job.ID, trackKey,
			job.TargetLanguage, releaseName, len(cues))
	}

	// Translate from the viewer's playhead forward (then wrap to the start), so
	// the region they're watching fills first. Cues carry absolute timing, so
	// the player places streamed cues correctly regardless of arrival order.
	ordered := reorderFromPosition(cues, job.StartPosition)

	translated, err := s.translator.Translate(ctx, TranslateRequest{
		Cues:           ordered,
		SourceLanguage: job.SourceLanguage,
		TargetLanguage: job.TargetLanguage,
	}, func(batch []SubtitleCue, done, total int) {
		// Map cue progress into the 5%..95% band; push the batch live.
		_ = s.repo.UpdateProgress(ctx, job.ID, JobStatusRunning, 0.05+0.9*float64(done)/float64(total), "Translating")
		if streaming {
			s.notifier.TranslationCues(ctx, job.SessionID, job.MediaFileID, job.ID, trackKey,
				toStreamCues(batch), done, total)
		}
	})
	if err != nil {
		s.finishWithError(ctx, job, err)
		return
	}

	// A finished translation should not be thrown away by a last-moment cancel.
	storeCtx := context.WithoutCancel(ctx)

	// Persist in chronological order regardless of the playhead-first order used
	// for translation/streaming.
	sortCuesByStart(translated)
	sub, err := s.store.StoreSubtitle(storeCtx, subtitles.StoreSubtitleRequest{
		MediaFileID: job.MediaFileID,
		UserID:      job.RequestedBy,
		Provider:    providerTranslated,
		Language:    job.TargetLanguage,
		Format:      subtitles.FormatSRT,
		ReleaseName: releaseName,
		Data:        SerializeSRT(translated),
	})
	if err != nil {
		s.finishWithError(ctx, job, fmt.Errorf("store translated subtitle: %w", err))
		return
	}

	if err := s.repo.CompleteJob(storeCtx, job.ID, sub.ID); err != nil {
		s.logger.Warn("failed to mark subtitle ai job complete", "job", job.ID, "error", err)
	}

	if s.notifier != nil {
		if streaming {
			s.notifier.TranslationCompleted(storeCtx, job.SessionID, job.MediaFileID, job.ID, trackKey,
				sub.ID, job.TargetLanguage, releaseName)
		}
		// Broadcast to other sessions watching this file (a no-op for the
		// streaming requester, which already tracks its live track).
		s.notifier.SubtitleReady(storeCtx, job.MediaFileID, sub.ID, job.TargetLanguage, releaseName)
	}
}

func (s *Service) finishWithError(ctx context.Context, job *Job, err error) {
	status := JobStatusFailed
	msg := truncate(err.Error(), 500)
	// Only a genuine cancellation (user cancel, or shutdown via cancel) becomes
	// "cancelled". A deadline/timeout (context.DeadlineExceeded) stays "failed".
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		status = JobStatusCancelled
		msg = "cancelled"
	}
	if dbErr := s.repo.FailJob(context.WithoutCancel(ctx), job.ID, status, msg); dbErr != nil {
		s.logger.Warn("failed to record subtitle ai job failure", "job", job.ID, "error", dbErr)
	}
	if job.SessionID != "" && s.notifier != nil {
		s.notifier.TranslationFailed(context.WithoutCancel(ctx), job.SessionID, job.MediaFileID, job.ID,
			liveTrackKey(job.ID), msg)
	}
	if status == JobStatusFailed {
		s.logger.Warn("subtitle ai job failed", "job", job.ID, "media_file", job.MediaFileID, "error", err)
	}
}

// loadSource resolves the combined player subtitle index to translatable cues,
// mirroring the index space used by the playback subtitle endpoints
// (external → embedded → downloaded). Embedded tracks are extracted to SRT via
// ffmpeg (so any non-bitmap codec works); external/downloaded sources must be a
// text format (SRT/VTT) in this version.
func (s *Service) loadSource(ctx context.Context, job *Job) ([]SubtitleCue, string, error) {
	file, err := s.files.GetByID(ctx, job.MediaFileID)
	if err != nil {
		return nil, "", fmt.Errorf("load media file: %w", err)
	}
	if file == nil {
		return nil, "", fmt.Errorf("media file not found")
	}

	idx := job.SourceIndex
	externalCount := len(file.ExternalSubtitles)

	switch {
	case idx < 0:
		return nil, "", fmt.Errorf("invalid source subtitle index")

	case idx < externalCount:
		ext := file.ExternalSubtitles[idx]
		if !isParsableTextFormat(ext.Format) {
			return nil, "", fmt.Errorf("%w: external %s", ErrSourceUnsupported, ext.Format)
		}
		data, err := os.ReadFile(ext.Path)
		if err != nil {
			return nil, "", fmt.Errorf("read external subtitle: %w", err)
		}
		cues, err := ParseCues(data)
		if err != nil {
			return nil, "", err
		}
		return cues, ext.Language, nil

	case idx < externalCount+len(file.SubtitleTracks):
		embeddedIndex := idx - externalCount
		track := file.SubtitleTracks[embeddedIndex]
		if playback.NeedsBurnIn(track.Codec) {
			return nil, "", fmt.Errorf("%w: bitmap track", ErrSourceUnsupported)
		}
		data, _, err := playback.ExtractSubtitle(ctx, file.FilePath, embeddedIndex, s.ffmpegPath)
		if err != nil {
			return nil, "", fmt.Errorf("extract embedded subtitle: %w", err)
		}
		cues, err := ParseCues(data)
		if err != nil {
			return nil, "", err
		}
		return cues, track.Language, nil

	default:
		downloadedIndex := idx - externalCount - len(file.SubtitleTracks)
		list, err := s.lister.ListDownloadedSubtitles(ctx, file.ID)
		if err != nil {
			return nil, "", fmt.Errorf("list downloaded subtitles: %w", err)
		}
		if downloadedIndex < 0 || downloadedIndex >= len(list) {
			return nil, "", fmt.Errorf("source subtitle index out of range")
		}
		dl := list[downloadedIndex]
		if !isParsableTextFormat(string(dl.Format)) {
			return nil, "", fmt.Errorf("%w: downloaded %s", ErrSourceUnsupported, dl.Format)
		}
		_, data, err := s.store.GetSubtitleContent(ctx, dl.ID)
		if err != nil {
			return nil, "", fmt.Errorf("fetch source subtitle: %w", err)
		}
		cues, err := ParseCues(data)
		if err != nil {
			return nil, "", err
		}
		return cues, dl.Language, nil
	}
}

func isParsableTextFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "srt", "subrip", "vtt", "webvtt":
		return true
	default:
		return false
	}
}

func translatedReleaseName(sourceLang, targetLang string) string {
	src := languageDisplayName(sourceLang)
	if src == "" {
		src = "Original"
	}
	tgt := languageDisplayName(targetLang)
	if tgt == "" {
		tgt = targetLang
	}
	return fmt.Sprintf("%s → %s (AI)", src, tgt)
}

// liveTrackKey is the stable client-side identifier for a job's live track.
func liveTrackKey(jobID int64) string {
	return fmt.Sprintf("ai-%d", jobID)
}

// reorderFromPosition rotates chronological cues so the first cue still visible
// at startSeconds leads, with earlier cues appended after the end. This makes a
// live translation fill the viewer's current region first. Returns the input
// unchanged when there's no useful pivot.
func reorderFromPosition(cues []SubtitleCue, startSeconds float64) []SubtitleCue {
	if startSeconds <= 0 || len(cues) < 2 {
		return cues
	}
	start := time.Duration(startSeconds * float64(time.Second))
	pivot := -1
	for i, c := range cues {
		if c.End >= start {
			pivot = i
			break
		}
	}
	if pivot <= 0 {
		return cues
	}
	out := make([]SubtitleCue, 0, len(cues))
	out = append(out, cues[pivot:]...)
	out = append(out, cues[:pivot]...)
	return out
}

// toStreamCues converts cues to the realtime wire form (absolute seconds).
func toStreamCues(cues []SubtitleCue) []playback.StreamCue {
	out := make([]playback.StreamCue, 0, len(cues))
	for _, c := range cues {
		out = append(out, playback.StreamCue{
			Start: c.Start.Seconds(),
			End:   c.End.Seconds(),
			Text:  strings.Join(c.Lines, "\n"),
		})
	}
	return out
}

func sortCuesByStart(cues []SubtitleCue) {
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].Start < cues[j].Start })
}
