package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
	aitranslate "github.com/Silo-Server/silo-server/internal/ai/translate"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

const (
	providerTranslated  = "translated"
	providerTranscribed = "transcribed"
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

// Service owns the AI subtitle job semantics: enqueue validation, source
// resolution, translation, and result storage. Lifecycle mechanics (bounded
// dispatch, heartbeat, stale-job reaping, cancellation) are delegated to the
// shared jobrunner so they stay identical across Silo's AI job services.
type Service struct {
	// cfg is behind an atomic pointer so admin settings changes apply to
	// subsequent enqueues/quota checks without rebuilding the service.
	cfg         atomic.Pointer[Config]
	repo        JobRepository
	translator  Translator
	transcriber Transcriber // optional; nil disables ASR kinds
	store       SubtitleStore
	lister      SubtitleLister
	files       MediaFileResolver
	notifier    Notifier // optional
	ffmpegPath  string
	logger      *slog.Logger
	runner      *jobrunner.Runner
}

// UpdateConfig swaps the service config. Safe for concurrent use; running
// jobs finish with the config they started with.
func (s *Service) UpdateConfig(cfg Config) {
	s.cfg.Store(&cfg)
}

// config returns a snapshot of the current service config.
func (s *Service) config() Config {
	return *s.cfg.Load()
}

// NewService wires a translation service. notifier may be nil. appCtx is the
// application lifecycle context; jobs and the reaper derive from it so they stop
// on shutdown. A nil appCtx falls back to context.Background(). sem is the
// dispatch semaphore, normally shared with the other AI job services so the
// configured endpoint sees one global concurrency bound; nil gets a private
// default-size semaphore.
func NewService(
	appCtx context.Context,
	cfg Config,
	repo JobRepository,
	translator Translator,
	transcriber Transcriber,
	store SubtitleStore,
	lister SubtitleLister,
	files MediaFileResolver,
	notifier Notifier,
	ffmpegPath string,
	logger *slog.Logger,
	sem chan struct{},
) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		repo:        repo,
		translator:  translator,
		transcriber: transcriber,
		store:       store,
		lister:      lister,
		files:       files,
		notifier:    notifier,
		ffmpegPath:  ffmpegPath,
		logger:      logger,
		runner:      jobrunner.New(appCtx, sem, repo, "subtitle ai", logger),
	}
	s.UpdateConfig(cfg)
	return s
}

// Enabled reports whether translation can currently run.
func (s *Service) Enabled() bool { return s.config().TranslateReady() }

// TranscribeEnabled reports whether ASR subtitle generation can currently run.
func (s *Service) TranscribeEnabled() bool {
	return s.config().TranscribeReady() && s.transcriber != nil
}

// Recover clears jobs orphaned by a crashed worker and starts a background
// reaper that keeps doing so. Call once at startup.
func (s *Service) Recover() {
	s.runner.Recover()
}

// Enqueue validates and queues a job, returning immediately. If an identical
// job is already pending or running, that job is returned instead of a new one.
func (s *Service) Enqueue(ctx context.Context, req JobRequest) (*Job, error) {
	if req.Kind == "" {
		req.Kind = JobKindTranslate
	}
	// Snapshot once so the job's model provenance is internally consistent
	// even if the config reloads mid-enqueue.
	cfg := s.config()
	jobModel := cfg.ChatModel
	switch req.Kind {
	case JobKindTranslate:
		if !cfg.TranslateReady() {
			return nil, ErrEngineNotConfigured
		}
	case JobKindTranscribe:
		if !cfg.TranscribeReady() || s.transcriber == nil {
			return nil, ErrEngineNotConfigured
		}
		jobModel = cfg.ASRModel
	case JobKindTranscribeTranslate:
		if !cfg.TranscribeReady() || s.transcriber == nil || !cfg.TranslateReady() {
			return nil, ErrEngineNotConfigured
		}
		jobModel = cfg.ASRModel + "+" + cfg.ChatModel
	default:
		return nil, fmt.Errorf("%w: unsupported job kind %q", ErrInvalidRequest, req.Kind)
	}

	// A plain transcribe has no target language (the track comes out in the
	// spoken language); when provided it acts as a language hint. Every other
	// kind requires a valid target.
	if req.Kind == JobKindTranscribe && strings.TrimSpace(req.TargetLanguage) == "" {
		req.TargetLanguage = ""
	} else {
		target, err := subtitles.NormalizeLanguageCode(req.TargetLanguage)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid target language %q", ErrInvalidRequest, req.TargetLanguage)
		}
		req.TargetLanguage = target
	}
	req.SourceLanguage = normalizeOptionalLanguageCode(req.SourceLanguage)

	key := idempotencyKey(req.MediaFileID, req.Kind, req.SourceIndex, req.TargetLanguage, jobModel)
	if existing, err := s.repo.GetActiveJobByIdempotencyKey(ctx, key); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	// Quota gates only the creation of a new job: collapsing onto an existing
	// in-flight job above costs nothing, so it stays allowed at the limit.
	// Enforcement happens inside InsertJob, atomically with the insert, so
	// concurrent requests cannot race past the limit.
	quota := s.transcribeQuotaSpec(req)

	job := &Job{
		MediaFileID:     req.MediaFileID,
		Kind:            req.Kind,
		SourceIndex:     req.SourceIndex,
		SourceLanguage:  req.SourceLanguage,
		TargetLanguage:  req.TargetLanguage,
		Engine:          "openai",
		Model:           jobModel,
		Status:          JobStatusPending,
		ProgressMessage: "Queued",
		IdempotencyKey:  key,
		RequestedBy:     req.RequestedBy,
		SessionID:       req.SessionID,
		StartPosition:   req.StartPosition,
	}
	if err := s.repo.InsertJob(ctx, job, quota); err != nil {
		// A racing duplicate trips the partial unique index; return the winner.
		// The same lookup also covers a quota rejection that raced an identical
		// request: collapsing onto the winner is free, so it stays allowed.
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

	if s.runner.Cancel(id) {
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
	s.runner.Dispatch(job.ID, func(ctx context.Context) {
		s.run(ctx, &job)
	}, func(ctx context.Context) {
		_ = s.repo.FailJob(ctx, job.ID, JobStatusCancelled, "cancelled before start")
	})
}

func (s *Service) run(ctx context.Context, job *Job) {
	if job.Kind.IsTranscribe() {
		s.runTranscribe(ctx, job)
		return
	}
	if err := s.repo.UpdateProgress(ctx, job.ID, JobStatusRunning, 0, "Loading subtitle"); err != nil {
		s.logger.Warn("failed to mark subtitle ai job running", "job", job.ID, "error", err)
	}

	cues, sourceLang, err := s.loadSource(ctx, job)
	if err != nil {
		s.finishWithError(ctx, job, err)
		return
	}
	if job.SourceLanguage == "" {
		job.SourceLanguage = normalizeOptionalLanguageCode(sourceLang)
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

// runTranscribe executes transcribe / transcribe_translate jobs: extract the
// audio track, transcribe it chunk by chunk (playhead-first), store the
// transcript as an ordinary downloaded subtitle, and for the chained kind run
// the regular translator over the transcript and store the translated track
// too. The transcript doubles as a cache: a later translation to another
// language can use it as a plain text source without re-running ASR.
func (s *Service) runTranscribe(ctx context.Context, job *Job) {
	if err := s.repo.UpdateProgress(ctx, job.ID, JobStatusRunning, 0, "Preparing audio"); err != nil {
		s.logger.Warn("failed to mark subtitle ai job running", "job", job.ID, "error", err)
	}

	file, err := s.files.GetByID(ctx, job.MediaFileID)
	if err != nil {
		s.finishWithError(ctx, job, fmt.Errorf("load media file: %w", err))
		return
	}
	if file == nil {
		s.finishWithError(ctx, job, fmt.Errorf("media file not found"))
		return
	}
	if len(file.AudioTracks) == 0 {
		s.finishWithError(ctx, job, fmt.Errorf("%w: file has no audio tracks", ErrSourceUnsupported))
		return
	}

	audioIdx := job.SourceIndex
	if audioIdx < 0 {
		audioIdx = defaultAudioTrackIndex(file.AudioTracks)
	}
	if audioIdx >= len(file.AudioTracks) {
		s.finishWithError(ctx, job, fmt.Errorf("%w: audio track index out of range", ErrInvalidRequest))
		return
	}

	// Language hint: an explicit source language wins; otherwise the track's
	// tagged language, when it normalizes to an ISO code.
	hint := normalizeOptionalLanguageCode(job.SourceLanguage)
	if hint == "" {
		hint = normalizeOptionalLanguageCode(file.AudioTracks[audioIdx].Language)
	}
	if job.Kind == JobKindTranscribe && hint == "" && job.TargetLanguage != "" {
		hint = normalizeOptionalLanguageCode(job.TargetLanguage)
	}

	streaming := job.SessionID != "" && s.notifier != nil
	trackKey := liveTrackKey(job.ID)
	if streaming {
		liveLang, liveLabel := job.TargetLanguage, transcribedReleaseName(hint)
		if job.Kind == JobKindTranscribeTranslate {
			liveLabel = translatedReleaseName(hint, job.TargetLanguage)
		} else {
			liveLang = hint
		}
		// Cue total is unknown before transcription; 0 means indeterminate.
		s.notifier.TranslationStarted(ctx, job.SessionID, job.MediaFileID, job.ID, trackKey, liveLang, liveLabel, 0)
	}

	// Only a plain transcribe streams transcript cues — the chained kind
	// streams the translated cues from its translation stage instead.
	streamTranscript := streaming && job.Kind == JobKindTranscribe
	cues, detected, err := s.transcriber.Transcribe(ctx, TranscribeJobRequest{
		FilePath:        file.FilePath,
		AudioTrackIndex: audioIdx,
		LanguageHint:    hint,
		StartPosition:   job.StartPosition,
	}, func(chunk []SubtitleCue, done, total int) {
		// Transcription occupies the 5%..70% progress band (chunk granularity).
		_ = s.repo.UpdateProgress(ctx, job.ID, JobStatusRunning, 0.05+0.65*float64(done)/float64(total), "Transcribing")
		if streamTranscript {
			s.notifier.TranslationCues(ctx, job.SessionID, job.MediaFileID, job.ID, trackKey,
				toStreamCues(chunk), done, total)
		}
	})
	if err != nil {
		s.finishWithError(ctx, job, err)
		return
	}

	language := hint
	if language == "" {
		language = detected
	}
	if job.SourceLanguage == "" {
		job.SourceLanguage = language
	}

	// A finished transcription should not be thrown away by a last-moment cancel.
	storeCtx := context.WithoutCancel(ctx)

	transcriptCues := make([]SubtitleCue, len(cues))
	copy(transcriptCues, cues)
	sortCuesByStart(transcriptCues)
	transcriptLabel := transcribedReleaseName(language)
	transcript, err := s.store.StoreSubtitle(storeCtx, subtitles.StoreSubtitleRequest{
		MediaFileID: job.MediaFileID,
		UserID:      job.RequestedBy,
		Provider:    providerTranscribed,
		Language:    language,
		Format:      subtitles.FormatSRT,
		ReleaseName: transcriptLabel,
		Data:        SerializeSRT(transcriptCues),
	})
	if err != nil {
		s.finishWithError(ctx, job, fmt.Errorf("store transcribed subtitle: %w", err))
		return
	}
	if s.notifier != nil {
		s.notifier.SubtitleReady(storeCtx, job.MediaFileID, transcript.ID, language, transcriptLabel)
	}

	if job.Kind == JobKindTranscribe {
		if err := s.repo.CompleteJob(storeCtx, job.ID, transcript.ID); err != nil {
			s.logger.Warn("failed to mark subtitle ai job complete", "job", job.ID, "error", err)
		}
		if streaming {
			s.notifier.TranslationCompleted(storeCtx, job.SessionID, job.MediaFileID, job.ID, trackKey,
				transcript.ID, language, transcriptLabel)
		}
		return
	}

	// transcribe_translate: run the transcript through the regular translator.
	// Cues already arrive playhead-first from chunk ordering, so the viewer's
	// region translates (and streams) first here too.
	translated, err := s.translator.Translate(ctx, TranslateRequest{
		Cues:           cues,
		SourceLanguage: language,
		TargetLanguage: job.TargetLanguage,
	}, func(batch []SubtitleCue, done, total int) {
		// Translation occupies the 70%..95% band.
		_ = s.repo.UpdateProgress(ctx, job.ID, JobStatusRunning, 0.7+0.25*float64(done)/float64(total), "Translating")
		if streaming {
			s.notifier.TranslationCues(ctx, job.SessionID, job.MediaFileID, job.ID, trackKey,
				toStreamCues(batch), done, total)
		}
	})
	if err != nil {
		s.finishWithError(ctx, job, err)
		return
	}

	storeCtx = context.WithoutCancel(ctx)
	sortCuesByStart(translated)
	translatedLabel := translatedReleaseName(language, job.TargetLanguage)
	sub, err := s.store.StoreSubtitle(storeCtx, subtitles.StoreSubtitleRequest{
		MediaFileID: job.MediaFileID,
		UserID:      job.RequestedBy,
		Provider:    providerTranslated,
		Language:    job.TargetLanguage,
		Format:      subtitles.FormatSRT,
		ReleaseName: translatedLabel,
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
				sub.ID, job.TargetLanguage, translatedLabel)
		}
		s.notifier.SubtitleReady(storeCtx, job.MediaFileID, sub.ID, job.TargetLanguage, translatedLabel)
	}
}

// defaultAudioTrackIndex returns the index of the default-flagged audio
// track, falling back to the first track.
func defaultAudioTrackIndex(tracks []models.AudioTrack) int {
	for i, t := range tracks {
		if t.Default {
			return i
		}
	}
	return 0
}

func transcribedReleaseName(lang string) string {
	name := aitranslate.LanguageDisplayName(lang)
	if name == "" {
		name = "Audio"
	}
	return fmt.Sprintf("%s (AI transcribed)", name)
}

func (s *Service) finishWithError(ctx context.Context, job *Job, err error) {
	status := JobStatusFailed
	msg := llm.Truncate(err.Error(), 500)
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

func normalizeOptionalLanguageCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	code, err := subtitles.NormalizeLanguageCode(value)
	if err != nil {
		return ""
	}
	return code
}

func translatedReleaseName(sourceLang, targetLang string) string {
	src := aitranslate.LanguageDisplayName(sourceLang)
	if src == "" {
		src = "Original"
	}
	tgt := aitranslate.LanguageDisplayName(targetLang)
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
