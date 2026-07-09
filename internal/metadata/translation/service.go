package translation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
	aitranslate "github.com/Silo-Server/silo-server/internal/ai/translate"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// Config holds the runtime configuration for metadata translation. The shared
// endpoint connection lives in the llm client this service is wired with.
type Config struct {
	Enabled    bool
	Configured bool   // shared AI endpoint has a base URL
	ChatModel  string // for job provenance and idempotency
	// OnView controls viewer-triggered translation: "off" | "button" | "auto".
	OnView string
}

// Ready reports whether metadata translation can currently run.
func (c Config) Ready() bool { return c.Enabled && c.Configured && c.ChatModel != "" }

// OnViewMode reports the viewer-triggered translation mode ("off" when the
// feature itself is not ready).
func (c Config) OnViewMode() string {
	if !c.Ready() || c.OnView == "" {
		return "off"
	}
	return c.OnView
}

// onViewFailureCooldown suppresses re-enqueueing after a failed job for the
// same target+language, so a broken endpoint is not retried on every page
// view. Admin-triggered jobs are unaffected.
const onViewFailureCooldown = 15 * time.Minute

// Service owns the metadata translation job semantics: enqueue validation,
// field collection with skip-if-filled, batched translation, and
// provenance-aware persistence. Lifecycle mechanics are delegated to the
// shared jobrunner.
type Service struct {
	// cfg is behind an atomic pointer so admin settings changes apply to
	// subsequent enqueues/view checks without rebuilding the service.
	cfg     atomic.Pointer[Config]
	repo    JobRepository
	content ContentReader
	locs    LocalizationStore
	chat    aitranslate.ChatFn
	runner  *jobrunner.Runner
	logger  *slog.Logger
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

// NewService wires a metadata translation service. sem is the dispatch
// semaphore shared with the other AI job services.
func NewService(
	appCtx context.Context,
	cfg Config,
	repo JobRepository,
	content ContentReader,
	locs LocalizationStore,
	chat aitranslate.ChatFn,
	sem chan struct{},
	logger *slog.Logger,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		repo:    repo,
		content: content,
		locs:    locs,
		chat:    chat,
		runner:  jobrunner.New(appCtx, sem, repo, "metadata translation", logger),
		logger:  logger,
	}
	s.UpdateConfig(cfg)
	return s
}

// Enabled reports whether metadata translation can currently run.
func (s *Service) Enabled() bool { return s.config().Ready() }

// Recover clears jobs orphaned by a crashed worker and starts a background
// reaper that keeps doing so. Call once at startup.
func (s *Service) Recover() { s.runner.Recover() }

// Enqueue validates and queues a job, returning immediately. If an identical
// job is already pending or running, that job is returned instead of a new one.
func (s *Service) Enqueue(ctx context.Context, req JobRequest) (*Job, error) {
	if !s.config().Ready() {
		return nil, ErrNotConfigured
	}
	if req.TargetKind == "" {
		req.TargetKind = TargetItem
	}
	switch req.TargetKind {
	case TargetItem, TargetSeason, TargetEpisode:
	default:
		return nil, fmt.Errorf("%w: unsupported target kind %q", ErrInvalidRequest, req.TargetKind)
	}
	if strings.TrimSpace(req.ContentID) == "" {
		return nil, fmt.Errorf("%w: content id is required", ErrInvalidRequest)
	}
	target, err := subtitles.NormalizeLanguageCode(req.TargetLanguage)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid target language %q", ErrInvalidRequest, req.TargetLanguage)
	}
	req.TargetLanguage = target

	key := idempotencyKey(req.TargetKind, req.ContentID, req.TargetLanguage, s.config().ChatModel)
	if existing, err := s.repo.GetActiveJobByIdempotencyKey(ctx, key); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	job := &Job{
		TargetKind:      req.TargetKind,
		ContentID:       req.ContentID,
		IncludeChildren: req.IncludeChildren,
		TargetLanguage:  req.TargetLanguage,
		Engine:          "openai",
		Model:           s.config().ChatModel,
		Status:          jobrunner.StatusPending,
		ProgressMessage: "Queued",
		Force:           req.Force,
		IdempotencyKey:  key,
		RequestedBy:     req.RequestedBy,
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

// AutoEnqueue is the metadata-refresh fallback hook: when the library opted
// in and the providers left translatable fields without a localization for
// language, it queues a non-force item job covering the children. Cheap on
// repeat refreshes — a missing-field count guards the enqueue, and the run
// loop re-checks per field, so fully translated items never reach the model.
// Errors are logged, never returned: a refresh must not fail on this.
func (s *Service) AutoEnqueue(ctx context.Context, itemContentID, language string) {
	if !s.config().Ready() {
		return
	}
	target, err := subtitles.NormalizeLanguageCode(language)
	if err != nil {
		return
	}
	missing, err := s.content.CountMissingFields(ctx, itemContentID, target)
	if err != nil {
		s.logger.WarnContext(ctx, "metadata translation: missing-field count failed",
			"content_id", itemContentID, "language", target, "error", err)
		return
	}
	if missing == 0 {
		return
	}
	if _, err := s.Enqueue(ctx, JobRequest{
		TargetKind:      TargetItem,
		ContentID:       itemContentID,
		TargetLanguage:  target,
		IncludeChildren: true,
	}); err != nil {
		s.logger.WarnContext(ctx, "metadata translation: auto-enqueue failed",
			"content_id", itemContentID, "language", target, "error", err)
	}
}

// OnViewMode exposes the viewer-triggered translation mode for status probes.
func (s *Service) OnViewMode() string { return s.config().OnViewMode() }

// RequestOnView is the viewer-triggered enqueue path (detail-page button or
// auto-on-view). On top of the normal pipeline it suppresses retries for a
// cooldown window after a failed job for the same target+language: ordinary
// page views must never hammer a broken endpoint. Returns the resulting job
// (which may be the in-flight or recently failed one).
func (s *Service) RequestOnView(ctx context.Context, targetKind TargetKind, contentID, targetLanguage string, requestedBy *int) (*Job, error) {
	if s.config().OnViewMode() == "off" {
		return nil, ErrNotConfigured
	}
	switch targetKind {
	case "":
		targetKind = TargetItem
	case TargetItem, TargetSeason, TargetEpisode:
	default:
		return nil, fmt.Errorf("%w: unsupported target kind %q", ErrInvalidRequest, targetKind)
	}
	target, err := subtitles.NormalizeLanguageCode(targetLanguage)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid target language %q", ErrInvalidRequest, targetLanguage)
	}

	jobs, err := s.repo.ListJobsByContent(ctx, contentID)
	if err != nil {
		return nil, err
	}
	for _, job := range jobs {
		if job.TargetKind != targetKind {
			continue
		}
		if job.TargetLanguage != target {
			continue
		}
		if job.Status == jobrunner.StatusFailed && time.Since(job.UpdatedAt) < onViewFailureCooldown {
			cooled := job
			return &cooled, nil
		}
		break // most recent job for this language decides; older history is irrelevant
	}

	return s.Enqueue(ctx, JobRequest{
		TargetKind:      targetKind,
		ContentID:       contentID,
		TargetLanguage:  target,
		IncludeChildren: targetKind == TargetItem,
		RequestedBy:     requestedBy,
	})
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

// ListJobs returns recent jobs for a content ID.
func (s *Service) ListJobs(ctx context.Context, contentID string) ([]Job, error) {
	return s.repo.ListJobsByContent(ctx, contentID)
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
	if !job.Status.Terminal() {
		return s.repo.FailJob(ctx, id, jobrunner.StatusCancelled, "cancelled")
	}
	return nil
}

func (s *Service) dispatch(job Job) {
	s.runner.Dispatch(job.ID, func(ctx context.Context) {
		s.run(ctx, &job)
	}, func(ctx context.Context) {
		_ = s.repo.FailJob(ctx, job.ID, jobrunner.StatusCancelled, "cancelled before start")
	})
}

// field is one translatable unit of a job.
type field struct {
	kind      TargetKind
	contentID string
	isTagline bool
	text      string
}

func (f field) segmentID() string {
	if f.kind == TargetItem && f.isTagline {
		return "item:tagline:" + f.contentID
	}
	return string(f.kind) + ":overview:" + f.contentID
}

func (s *Service) run(ctx context.Context, job *Job) {
	if err := s.repo.UpdateProgress(ctx, job.ID, jobrunner.StatusRunning, 0, "Loading content", 0, 0); err != nil {
		s.logger.WarnContext(ctx, "failed to mark metadata translation job running", "job", job.ID, "error", err)
	}

	fields, meta, err := s.collectFields(ctx, job)
	if err != nil {
		s.finishWithError(ctx, job, err)
		return
	}
	if len(fields) == 0 {
		if err := s.repo.CompleteJob(context.WithoutCancel(ctx), job.ID, "Nothing to translate", 0, 0); err != nil {
			s.logger.WarnContext(ctx, "failed to complete metadata translation job", "job", job.ID, "error", err)
		}
		return
	}

	total := len(fields)
	if err := s.repo.UpdateProgress(ctx, job.ID, jobrunner.StatusRunning, 0.05, "Translating", 0, total); err != nil {
		s.logger.WarnContext(ctx, "failed to update metadata translation job", "job", job.ID, "error", err)
	}

	segments := make([]aitranslate.Segment, total)
	byID := make(map[string]field, total)
	for i, f := range fields {
		segments[i] = aitranslate.Segment{ID: f.segmentID(), Text: f.text}
		byID[f.segmentID()] = f
	}

	srcName := aitranslate.LanguageDisplayName(meta.srcLanguage)
	tgtName := aitranslate.LanguageDisplayName(job.TargetLanguage)

	// Persist per batch so a cancellation keeps completed fields. A persist
	// failure cancels the remaining batches via persistCtx instead of burning
	// model calls whose output cannot be stored.
	persistCtx, cancelPersist := context.WithCancel(ctx)
	defer cancelPersist()
	var persistMu sync.Mutex
	var persistErr error
	done := 0

	_, translateErr := aitranslate.Translate(persistCtx, s.chat, aitranslate.Request{
		Segments:         segments,
		SystemPrompt:     systemPrompt(srcName, tgtName, meta.title, meta.year),
		TargetName:       tgtName,
		EntryNoun:        "descriptions",
		BatchSize:        metadataBatchSize,
		ContextNeighbors: 0,
	}, func(batch []aitranslate.Segment, batchDone, batchTotal int) {
		// Finished translations should not be thrown away by a late cancel.
		storeCtx := context.WithoutCancel(ctx)
		for _, seg := range batch {
			f, ok := byID[seg.ID]
			if !ok {
				continue
			}
			if err := s.persistField(storeCtx, job, f, seg.Text); err != nil {
				persistMu.Lock()
				if persistErr == nil {
					persistErr = err
				}
				persistMu.Unlock()
				cancelPersist()
				return
			}
			done++
		}
		progress := 0.05 + 0.9*float64(batchDone)/float64(batchTotal)
		_ = s.repo.UpdateProgress(storeCtx, job.ID, jobrunner.StatusRunning, progress, "Translating", done, total)
	})

	persistMu.Lock()
	failure := persistErr
	persistMu.Unlock()
	if failure == nil && translateErr != nil {
		failure = translateErr
	}
	if failure != nil {
		s.finishWithError(ctx, job, failure)
		return
	}

	if err := s.repo.CompleteJob(context.WithoutCancel(ctx), job.ID, "", done, total); err != nil {
		s.logger.WarnContext(ctx, "failed to complete metadata translation job", "job", job.ID, "error", err)
	}
}

func (s *Service) persistField(ctx context.Context, job *Job, f field, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("model returned an empty translation for %s", f.segmentID())
	}
	switch f.kind {
	case TargetItem:
		if f.isTagline {
			return s.locs.UpsertItemAI(ctx, f.contentID, job.TargetLanguage, nil, &text, job.Force)
		}
		return s.locs.UpsertItemAI(ctx, f.contentID, job.TargetLanguage, &text, nil, job.Force)
	case TargetSeason:
		return s.locs.UpsertSeasonAI(ctx, f.contentID, job.TargetLanguage, text, job.Force)
	case TargetEpisode:
		return s.locs.UpsertEpisodeAI(ctx, f.contentID, job.TargetLanguage, text, job.Force)
	default:
		return fmt.Errorf("unknown field kind %q", f.kind)
	}
}

// jobMeta carries the prompt-grounding context for a job.
type jobMeta struct {
	title       string
	year        int
	srcLanguage string
}

// collectFields expands the job target into translatable fields, dropping any
// field whose localized value is already filled (unless force, which still
// respects manual provenance).
func (s *Service) collectFields(ctx context.Context, job *Job) ([]field, jobMeta, error) {
	var meta jobMeta

	switch job.TargetKind {
	case TargetItem:
		item, err := s.content.ItemText(ctx, job.ContentID)
		if err != nil {
			return nil, meta, fmt.Errorf("load item: %w", err)
		}
		if item == nil {
			return nil, meta, fmt.Errorf("%w: item not found", ErrInvalidRequest)
		}
		meta = jobMeta{title: item.Title, year: item.Year, srcLanguage: item.DefaultLanguage}
		job.SourceLanguage = item.DefaultLanguage

		var fields []field
		itemFields, err := s.itemFields(ctx, job, item)
		if err != nil {
			return nil, meta, err
		}
		fields = append(fields, itemFields...)

		if job.IncludeChildren {
			childFields, err := s.childFields(ctx, job, job.ContentID)
			if err != nil {
				return nil, meta, err
			}
			fields = append(fields, childFields...)
		}
		return fields, meta, nil

	case TargetSeason:
		season, seriesID, err := s.content.SeasonByID(ctx, job.ContentID)
		if err != nil {
			return nil, meta, fmt.Errorf("load season: %w", err)
		}
		if season == nil {
			return nil, meta, fmt.Errorf("%w: season not found", ErrInvalidRequest)
		}
		meta, err = s.parentMeta(ctx, job, seriesID)
		if err != nil {
			return nil, meta, err
		}
		fields, err := s.filterSeasons(ctx, job, []ChildText{*season})
		return fields, meta, err

	case TargetEpisode:
		episode, seriesID, err := s.content.EpisodeByID(ctx, job.ContentID)
		if err != nil {
			return nil, meta, fmt.Errorf("load episode: %w", err)
		}
		if episode == nil {
			return nil, meta, fmt.Errorf("%w: episode not found", ErrInvalidRequest)
		}
		meta, err = s.parentMeta(ctx, job, seriesID)
		if err != nil {
			return nil, meta, err
		}
		fields, err := s.filterEpisodes(ctx, job, []ChildText{*episode})
		return fields, meta, err

	default:
		return nil, meta, fmt.Errorf("%w: unsupported target kind %q", ErrInvalidRequest, job.TargetKind)
	}
}

func (s *Service) parentMeta(ctx context.Context, job *Job, seriesID string) (jobMeta, error) {
	var meta jobMeta
	item, err := s.content.ItemText(ctx, seriesID)
	if err != nil {
		return meta, fmt.Errorf("load parent series: %w", err)
	}
	if item != nil {
		meta = jobMeta{title: item.Title, year: item.Year, srcLanguage: item.DefaultLanguage}
		job.SourceLanguage = item.DefaultLanguage
	}
	return meta, nil
}

// translatableField reports whether a field with base text and an existing
// localized value/source should be translated. Empty base text never
// translates; filled localizations are skipped unless force; manual values
// are skipped even with force (the SQL layer guards them too — skipping here
// saves the model call).
func translatableField(baseText, locValue, locSource string, force bool) bool {
	if strings.TrimSpace(baseText) == "" {
		return false
	}
	if locSource == "manual" {
		return false
	}
	if locValue != "" && !force {
		return false
	}
	return true
}

func (s *Service) itemFields(ctx context.Context, job *Job, item *ItemText) ([]field, error) {
	loc, err := s.locs.ItemLocalization(ctx, item.ContentID, job.TargetLanguage)
	if err != nil {
		return nil, fmt.Errorf("load item localization: %w", err)
	}
	var locOverview, locOverviewSrc, locTagline, locTaglineSrc string
	if loc != nil {
		locOverview, locOverviewSrc = loc.Overview, loc.OverviewSource
		locTagline, locTaglineSrc = loc.Tagline, loc.TaglineSource
	}

	var fields []field
	if translatableField(item.Overview, locOverview, locOverviewSrc, job.Force) {
		fields = append(fields, field{kind: TargetItem, contentID: item.ContentID, text: item.Overview})
	}
	if translatableField(item.Tagline, locTagline, locTaglineSrc, job.Force) {
		fields = append(fields, field{kind: TargetItem, contentID: item.ContentID, isTagline: true, text: item.Tagline})
	}
	return fields, nil
}

func (s *Service) childFields(ctx context.Context, job *Job, seriesID string) ([]field, error) {
	seasons, err := s.content.SeasonTexts(ctx, seriesID)
	if err != nil {
		return nil, fmt.Errorf("load seasons: %w", err)
	}
	fields, err := s.filterSeasons(ctx, job, seasons)
	if err != nil {
		return nil, err
	}

	episodes, err := s.content.EpisodeTexts(ctx, seriesID)
	if err != nil {
		return nil, fmt.Errorf("load episodes: %w", err)
	}
	episodeFields, err := s.filterEpisodes(ctx, job, episodes)
	if err != nil {
		return nil, err
	}
	return append(fields, episodeFields...), nil
}

func (s *Service) filterSeasons(ctx context.Context, job *Job, seasons []ChildText) ([]field, error) {
	ids := make([]string, 0, len(seasons))
	for _, season := range seasons {
		ids = append(ids, season.ContentID)
	}
	locs, err := s.locs.SeasonLocalizations(ctx, ids, job.TargetLanguage)
	if err != nil {
		return nil, fmt.Errorf("load season localizations: %w", err)
	}
	var fields []field
	for _, season := range seasons {
		var locOverview, locSource string
		if loc := locs[season.ContentID]; loc != nil {
			locOverview, locSource = loc.Overview, loc.OverviewSource
		}
		if translatableField(season.Overview, locOverview, locSource, job.Force) {
			fields = append(fields, field{kind: TargetSeason, contentID: season.ContentID, text: season.Overview})
		}
	}
	return fields, nil
}

func (s *Service) filterEpisodes(ctx context.Context, job *Job, episodes []ChildText) ([]field, error) {
	ids := make([]string, 0, len(episodes))
	for _, episode := range episodes {
		ids = append(ids, episode.ContentID)
	}
	locs, err := s.locs.EpisodeLocalizations(ctx, ids, job.TargetLanguage)
	if err != nil {
		return nil, fmt.Errorf("load episode localizations: %w", err)
	}
	var fields []field
	for _, episode := range episodes {
		var locOverview, locSource string
		if loc := locs[episode.ContentID]; loc != nil {
			locOverview, locSource = loc.Overview, loc.OverviewSource
		}
		if translatableField(episode.Overview, locOverview, locSource, job.Force) {
			fields = append(fields, field{kind: TargetEpisode, contentID: episode.ContentID, text: episode.Overview})
		}
	}
	return fields, nil
}

func (s *Service) finishWithError(ctx context.Context, job *Job, err error) {
	status := jobrunner.StatusFailed
	msg := llm.Truncate(err.Error(), 500)
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		status = jobrunner.StatusCancelled
		msg = "cancelled"
	}
	if dbErr := s.repo.FailJob(context.WithoutCancel(ctx), job.ID, status, msg); dbErr != nil {
		s.logger.WarnContext(ctx, "failed to record metadata translation job failure", "job", job.ID, "error", dbErr)
	}
	if status == jobrunner.StatusFailed {
		s.logger.WarnContext(ctx, "metadata translation job failed", "job", job.ID, "content_id", job.ContentID, "error", err)
	}
}
