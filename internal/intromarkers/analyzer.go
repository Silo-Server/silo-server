package intromarkers

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type Analyzer struct {
	repo               introRepository
	extractor          fingerprintExtractor
	refiner            boundaryRefiner
	chromaprintRefiner chromaprintStartRefiner
	config             Config
	logger             *slog.Logger
	// node names this server in recorded silence refinement failures, which
	// only defer retries on the server that recorded them.
	node string
	// slotsMu guards workers and the ffmpegSlots pointer, which SetWorkers
	// sets when the markers.detection_workers setting changes.
	slotsMu sync.Mutex
	// workers is how many season groups, and silence backfill shares, run at
	// once. Zero falls back to config.MaxParallelFFmpeg.
	workers int
	// ffmpegSlots bounds the ffmpeg processes this analyzer runs at once across
	// the nightly run and its season workers. Nil means no shared bound.
	ffmpegSlots *slotLimiter
	// interactiveSlots is reserved for analysis started from playback (see
	// WithPlaybackPriority), which would otherwise queue behind every nightly
	// extraction waiting for a slot.
	interactiveSlots chan struct{}
	// lookupSlots bounds concurrent fingerprint cache reads across every group
	// being analyzed; see fingerprintLookupSlots.
	lookupOnce  sync.Once
	lookupSlots chan struct{}
}

// maxConcurrentFingerprintLookups caps the database connections fingerprint
// cache reads hold at once. Each candidate in a season looks up its cache
// entry concurrently, and several seasons run at once, so without a cap the
// nightly run could take the whole pool and starve API requests.
const maxConcurrentFingerprintLookups = 4

// interactiveAnalysisKey marks a context whose analysis a viewer is waiting on.
type interactiveAnalysisKey struct{}

// WithPlaybackPriority marks analysis a viewer is waiting on, letting it use
// the ffmpeg slot reserved for playback. Background callers, such as admin
// refreshes, must not use it or they would queue ahead of playback.
func WithPlaybackPriority(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveAnalysisKey{}, true)
}

type introRepository interface {
	CountEnabledLibraries(ctx context.Context) (int, error)
	ListEligibleCandidates(ctx context.Context) ([]Candidate, error)
	ListCandidatesForEpisode(ctx context.Context, episodeID string) ([]Candidate, error)
	ListCandidatesForGroup(ctx context.Context, mediaFolderID int, seasonID, analysisGroupKey string) ([]Candidate, error)
	ListChapterSilenceBackfillCandidates(ctx context.Context, limit int, cfg Config, node string) ([]Candidate, error)
	LoadSilenceRefinementAttempt(ctx context.Context, fileID int) (*SilenceRefinementAttempt, error)
	UpsertSilenceRefinementAttempt(ctx context.Context, attempt SilenceRefinementAttempt) error
	PatchIntroMarker(ctx context.Context, patch IntroMarkerPatch) (bool, error)
	LoadSeasonState(ctx context.Context, state SeasonState, cfg Config) (*SeasonState, error)
	UpsertSeasonState(ctx context.Context, state SeasonState, cfg Config) error
	LoadFingerprint(ctx context.Context, candidate Candidate, cfg Config) (*Fingerprint, error)
	UpsertFingerprint(ctx context.Context, fp Fingerprint) error
}

type fingerprintExtractor interface {
	Preflight(ctx context.Context) error
	Extract(ctx context.Context, candidate Candidate) (Fingerprint, bool, error)
}

func NewAnalyzer(repo *Repository, config Config, logger *slog.Logger) *Analyzer {
	config = config.normalized()
	if logger == nil {
		logger = slog.Default()
	}
	node, _ := os.Hostname()
	if node == "" {
		node = "silo"
	}
	return &Analyzer{
		repo:               repo,
		extractor:          NewChromaprintExtractor(config),
		refiner:            NewSilenceBoundaryRefiner(config),
		chromaprintRefiner: NewDialogueBoundaryRefiner(config),
		config:             config,
		logger:             logger,
		node:               node,
		workers:            config.MaxParallelFFmpeg,
		ffmpegSlots:        newSlotLimiter(config.MaxParallelFFmpeg),
		interactiveSlots:   make(chan struct{}, 1),
	}
}

// SetWorkers applies the markers.detection_workers setting: how many season
// groups are analyzed at once and how many ffmpeg processes they may run. Less
// than one means DefaultDetectionWorkers. The change applies to new work
// without waiting for a run to finish; after a decrease, extractions already
// running finish before new ones start.
func (a *Analyzer) SetWorkers(n int) {
	if n <= 0 {
		n = DefaultDetectionWorkers
	}
	a.slotsMu.Lock()
	defer a.slotsMu.Unlock()
	if a.workers == n && a.ffmpegSlots != nil {
		return
	}
	a.workers = n
	if a.ffmpegSlots == nil {
		a.ffmpegSlots = newSlotLimiter(n)
		return
	}
	a.ffmpegSlots.resize(n)
}

// workerCount is the current number of season workers.
func (a *Analyzer) workerCount() int {
	a.slotsMu.Lock()
	defer a.slotsMu.Unlock()
	if a.workers > 0 {
		return a.workers
	}
	return max(1, a.config.normalized().MaxParallelFFmpeg)
}

func (a *Analyzer) sharedSlots() *slotLimiter {
	a.slotsMu.Lock()
	defer a.slotsMu.Unlock()
	return a.ffmpegSlots
}

// acquireFFmpeg waits for an ffmpeg slot and returns its release. Interactive
// analysis may also take the reserved slot, so it never waits behind the
// nightly queue for more than one extraction.
func (a *Analyzer) acquireFFmpeg(ctx context.Context) (func(), error) {
	slots := a.sharedSlots()
	if slots == nil {
		return func() {}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A nil channel never receives, so non-interactive callers only wait on
	// the shared limit.
	var reserved chan struct{}
	if interactive, _ := ctx.Value(interactiveAnalysisKey{}).(bool); interactive {
		reserved = a.interactiveSlots
	}
	for {
		if reserved != nil {
			select {
			case reserved <- struct{}{}:
				return func() { <-reserved }, nil
			default:
			}
		}
		ok, changed := slots.tryAcquire()
		if ok {
			return slots.release, nil
		}
		select {
		case reserved <- struct{}{}:
			return func() { <-reserved }, nil
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// fingerprintLookupSlots returns the analyzer-wide bound on concurrent
// fingerprint cache reads, created on first use so analyzers built without
// NewAnalyzer share one too.
func (a *Analyzer) fingerprintLookupSlots() chan struct{} {
	a.lookupOnce.Do(func() {
		a.lookupSlots = make(chan struct{}, maxConcurrentFingerprintLookups)
	})
	return a.lookupSlots
}

// loadFingerprint reads a cached fingerprint within the lookup bound. The
// slot covers only the read, not the wait for ffmpeg that may follow a miss.
func (a *Analyzer) loadFingerprint(ctx context.Context, candidate Candidate) (*Fingerprint, error) {
	slots := a.fingerprintLookupSlots()
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-slots }()
	return a.repo.LoadFingerprint(ctx, candidate, a.config)
}

type ProgressFunc func(percent float64, message string)

func (a *Analyzer) Run(ctx context.Context, progress ProgressFunc) (RunSummary, error) {
	report := func(percent float64, message string) {
		if progress != nil {
			progress(percent, message)
		}
	}

	summary := RunSummary{}
	libraries, err := a.repo.CountEnabledLibraries(ctx)
	if err != nil {
		return summary, err
	}
	summary.LibrariesScanned = libraries
	if libraries == 0 {
		report(100, "No intro-enabled series libraries")
		return summary, nil
	}

	candidates, err := a.repo.ListEligibleCandidates(ctx)
	if err != nil {
		return summary, err
	}
	summary.FilesConsidered = len(candidates)
	if len(candidates) == 0 {
		report(100, "No eligible episode files")
		return summary, nil
	}

	report(10, fmt.Sprintf("Checking embedded chapters for %d files", len(candidates)))
	_, chapterSummary := a.processChapterCandidates(ctx, candidates, chapterProcessingOptions{
		allowEpisodeCopy: true,
		progress: func(i, total int) {
			if i%25 == 0 {
				report(10+float64(i)/float64(total)*20, fmt.Sprintf("Checked %d/%d files for chapter markers", i+1, total))
			}
		},
	})
	mergeRunSummary(&summary, chapterSummary)
	if err := ctx.Err(); err != nil {
		return summary, err
	}

	remaining := ownDetectionCandidates(candidates)
	if len(remaining) == 0 {
		backfillSummary, err := a.runSilenceBackfill(ctx)
		mergeRunSummary(&summary, backfillSummary)
		if err != nil {
			return summary, err
		}
		report(100, "Intro chapter detection complete")
		return summary, nil
	}

	groups := groupCandidates(remaining)
	summary.SeasonGroupsConsidered = len(groups)
	if len(groups) == 0 {
		backfillSummary, err := a.runSilenceBackfill(ctx)
		mergeRunSummary(&summary, backfillSummary)
		if err != nil {
			return summary, err
		}
		report(100, "No season groups eligible for Chromaprint")
		return summary, nil
	}

	report(35, "Checking FFmpeg Chromaprint support")
	if err := a.extractor.Preflight(ctx); err != nil {
		summary.ChromaprintSupported = false
		summary.ChromaprintSupportMessage = err.Error()
		backfillSummary, backfillErr := a.runSilenceBackfill(ctx)
		mergeRunSummary(&summary, backfillSummary)
		if backfillErr != nil {
			return summary, backfillErr
		}
		report(100, "Chromaprint unsupported; chapter detection completed")
		return summary, nil
	}
	summary.ChromaprintSupported = true

	groupSummary := a.analyzeGroups(ctx, groups, func(done int) {
		report(40+float64(done)/float64(len(groups))*55, fmt.Sprintf("Analyzed intro group %d/%d", done, len(groups)))
	})
	mergeRunSummary(&summary, groupSummary)
	if err := ctx.Err(); err != nil {
		return summary, err
	}

	backfillSummary, err := a.runSilenceBackfill(ctx)
	mergeRunSummary(&summary, backfillSummary)
	if err != nil {
		return summary, err
	}

	report(100, "Intro marker detection completed")
	return summary, nil
}

func (a *Analyzer) AnalyzeEpisode(ctx context.Context, episodeID string) (RunSummary, error) {
	summary := RunSummary{}
	candidates, err := a.repo.ListCandidatesForEpisode(ctx, episodeID)
	if err != nil {
		return summary, err
	}
	summary.FilesConsidered = len(candidates)
	if len(candidates) == 0 {
		return summary, nil
	}

	_, chapterSummary := a.processChapterCandidates(ctx, candidates, chapterProcessingOptions{
		forceExistingScanner: true,
		allowEpisodeCopy:     true,
	})
	mergeRunSummary(&summary, chapterSummary)
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	remaining := ownDetectionCandidates(candidates)
	if len(remaining) == 0 {
		return summary, nil
	}
	targetFileIDs := candidateFileIDs(remaining)

	groupsByKey := map[string]candidateGroup{}
	for _, candidate := range remaining {
		key := fmt.Sprintf("%d:%s:%s", candidate.MediaFolderID, candidate.SeasonID, candidate.AnalysisGroupKey())
		if _, ok := groupsByKey[key]; ok {
			continue
		}
		groupCandidates, err := a.repo.ListCandidatesForGroup(ctx, candidate.MediaFolderID, candidate.SeasonID, candidate.AnalysisGroupKey())
		if err != nil {
			return summary, err
		}
		// Compare the same files the scheduled run would, so a file's result
		// and confidence do not depend on how its analysis started.
		groupCandidates = ownDetectionCandidates(groupCandidates)
		if distinctEpisodeCount(groupCandidates) < 2 {
			continue
		}
		groupsByKey[key] = candidateGroup{
			SeasonID:         candidate.SeasonID,
			MediaFolderID:    candidate.MediaFolderID,
			AnalysisGroupKey: candidate.AnalysisGroupKey(),
			Candidates:       groupCandidates,
		}
	}

	if len(groupsByKey) == 0 {
		return summary, nil
	}

	if err := a.extractor.Preflight(ctx); err != nil {
		summary.ChromaprintSupported = false
		summary.ChromaprintSupportMessage = err.Error()
		return summary, nil
	}
	summary.ChromaprintSupported = true

	groups := make([]candidateGroup, 0, len(groupsByKey))
	for _, group := range groupsByKey {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].MediaFolderID != groups[j].MediaFolderID {
			return groups[i].MediaFolderID < groups[j].MediaFolderID
		}
		if groups[i].SeasonID != groups[j].SeasonID {
			return groups[i].SeasonID < groups[j].SeasonID
		}
		return groups[i].AnalysisGroupKey < groups[j].AnalysisGroupKey
	})

	summary.SeasonGroupsConsidered = len(groups)
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		groupSummary, err := a.analyzeGroup(ctx, group, analyzeGroupOptions{
			force:        true,
			patchFileIDs: targetFileIDs,
		})
		summary.FingerprintsComputed += groupSummary.FingerprintsComputed
		summary.FingerprintCacheHits += groupSummary.FingerprintCacheHits
		summary.FingerprintExtractionErrors += groupSummary.FingerprintExtractionErrors
		summary.ChromaprintMarkersWritten += groupSummary.ChromaprintMarkersWritten
		summary.DialogueRefinementsAttempted += groupSummary.DialogueRefinementsAttempted
		summary.DialogueRefinementsApplied += groupSummary.DialogueRefinementsApplied
		summary.DialogueRefinementErrors += groupSummary.DialogueRefinementErrors
		summary.GroupsNotFound += groupSummary.GroupsNotFound
		summary.GroupsSkipped += groupSummary.GroupsSkipped
		summary.Errors = append(summary.Errors, groupSummary.Errors...)
		if err != nil {
			a.logger.WarnContext(ctx, "intro marker episode group analysis failed",
				"episode_id", episodeID,
				"season_id", group.SeasonID,
				"media_folder_id", group.MediaFolderID,
				"group_key", group.AnalysisGroupKey,
				"error", err)
		}
	}

	return summary, nil
}

type chapterProcessingOptions struct {
	forceExistingScanner bool
	allowEpisodeCopy     bool
	deadline             time.Time
	progress             func(index, total int)
}

type chapterSourceMarker struct {
	candidate Candidate
	segment   Segment
}

const episodeVersionCopyDurationToleranceSeconds = 3.0

func (a *Analyzer) processChapterCandidates(ctx context.Context, candidates []Candidate, opts chapterProcessingOptions) ([]Candidate, RunSummary) {
	summary := RunSummary{}
	remaining := make([]Candidate, 0, len(candidates))
	directByEpisode := map[string]chapterSourceMarker{}
	directFileIDs := map[int]struct{}{}

	for i, candidate := range candidates {
		if opts.progress != nil {
			opts.progress(i, len(candidates))
		}
		if err := ctx.Err(); err != nil {
			summary.Errors = append(summary.Errors, err.Error())
			return remaining, summary
		}
		if !opts.deadline.IsZero() && time.Now().After(opts.deadline) {
			return remaining, summary
		}
		if candidate.HasHigherPriorityIntro(models.MarkerSourceScanner) {
			continue
		}

		hasIntro := candidate.IntroStart != nil && candidate.IntroEnd != nil
		effectiveSource := candidate.EffectiveIntroSource()
		if hasIntro && effectiveSource != "" && effectiveSource != models.MarkerSourceScanner {
			continue
		}
		if hasIntro && !opts.forceExistingScanner {
			continue
		}

		segment, ok := DetectChapterIntro(candidate.Chapters)
		if !ok {
			remaining = append(remaining, candidate)
			continue
		}

		segment, refined := a.refineChapterSegment(ctx, candidate, segment, opts.deadline, &summary)
		if !refined {
			// The run's time budget ran out while waiting for an ffmpeg slot.
			remaining = append(remaining, candidate)
			continue
		}
		applied, patchErr := a.repo.PatchIntroMarker(ctx, IntroMarkerPatch{
			ExpectedFile: candidate.expectedFile(),
			FileID:       candidate.FileID,
			Start:        segment.Start,
			End:          segment.End,
			Source:       models.MarkerSourceScanner,
			Confidence:   segment.Confidence,
			Algorithm:    segment.Algorithm,
			DetectedAt:   time.Now().UTC(),
		})
		if patchErr != nil {
			summary.Errors = append(summary.Errors, patchErr.Error())
			a.logger.WarnContext(ctx, "intro marker chapter patch failed", "file_id", candidate.FileID, "error", patchErr)
			remaining = append(remaining, candidate)
			continue
		}
		if applied {
			summary.ChapterMarkersWritten++
		}
		directFileIDs[candidate.FileID] = struct{}{}
		setBestChapterSource(directByEpisode, candidate, segment)
	}

	if !opts.allowEpisodeCopy || len(directByEpisode) == 0 || len(remaining) == 0 {
		return remaining, summary
	}

	unresolved := remaining[:0]
	for _, candidate := range remaining {
		if err := ctx.Err(); err != nil {
			summary.Errors = append(summary.Errors, err.Error())
			unresolved = append(unresolved, candidate)
			continue
		}
		if !opts.deadline.IsZero() && time.Now().After(opts.deadline) {
			unresolved = append(unresolved, candidate)
			continue
		}
		if _, ok := directFileIDs[candidate.FileID]; ok {
			continue
		}
		source, ok := directByEpisode[candidate.EpisodeID]
		if !ok {
			unresolved = append(unresolved, candidate)
			continue
		}
		if candidate.HasHigherPriorityIntro(models.MarkerSourceScanner) || !compatibleEpisodeVersionDuration(source.candidate, candidate) {
			unresolved = append(unresolved, candidate)
			continue
		}
		if candidate.IntroStart != nil && candidate.IntroEnd != nil && candidate.EffectiveIntroSource() != models.MarkerSourceScanner {
			continue
		}

		confidence := 0.85
		if source.segment.Algorithm == ChapterSilenceAlgorithm {
			confidence = 0.90
		}
		applied, patchErr := a.repo.PatchIntroMarker(ctx, IntroMarkerPatch{
			ExpectedFile: candidate.expectedFile(),
			FileID:       candidate.FileID,
			Start:        source.segment.Start,
			End:          source.segment.End,
			Source:       models.MarkerSourceScanner,
			Confidence:   confidence,
			Algorithm:    EpisodeVersionCopyAlgorithm,
			DetectedAt:   time.Now().UTC(),
		})
		if patchErr != nil {
			msg := fmt.Sprintf("file %d: %v", candidate.FileID, patchErr)
			summary.Errors = append(summary.Errors, msg)
			a.logger.WarnContext(ctx, "intro marker episode version copy failed", "file_id", candidate.FileID, "source_file_id", source.candidate.FileID, "error", patchErr)
			unresolved = append(unresolved, candidate)
			continue
		}
		if applied {
			summary.EpisodeVersionMarkersCopied++
		}
	}

	return unresolved, summary
}

// refineChapterSegment runs silence refinement on a chapter intro. It returns
// false, leaving the file untouched, when deadline passed while it waited for
// an ffmpeg slot.
func (a *Analyzer) refineChapterSegment(ctx context.Context, candidate Candidate, segment Segment, deadline time.Time, summary *RunSummary) (Segment, bool) {
	if a.refiner == nil || !a.config.normalized().SilenceRefinementEnabled {
		return segment, true
	}
	release, err := a.acquireFFmpeg(ctx)
	if err != nil {
		summary.SilenceRefinementsAttempted++
		summary.SilenceRefinementErrors++
		return segment, true
	}
	if !deadline.IsZero() && time.Now().After(deadline) {
		release()
		return segment, false
	}
	summary.SilenceRefinementsAttempted++
	refined, ok, err := a.refiner.RefineChapterEnd(ctx, candidate, segment)
	release()
	if err != nil {
		summary.SilenceRefinementErrors++
		a.logger.WarnContext(ctx, "intro marker silence refinement failed", "file_id", candidate.FileID, "path", candidate.FilePath, "error", err)
		if ctx.Err() == nil {
			a.recordSilenceAttempt(ctx, candidate, segment, err, summary)
		}
		return segment, true
	}
	if ok {
		summary.SilenceRefinementsApplied++
		return refined, true
	}
	a.recordSilenceAttempt(ctx, candidate, segment, nil, summary)
	return segment, true
}

const (
	silenceRetryBaseDelay = 12 * time.Hour
	silenceRetryMaxDelay  = 7 * 24 * time.Hour
)

// recordSilenceAttempt persists a refinement that kept the chapter boundary so
// the backfill stops spending its budget on the same unchanged file every run.
// A clean no-improvement result stands until the inputs change; a failure is
// retried with exponential backoff.
func (a *Analyzer) recordSilenceAttempt(ctx context.Context, candidate Candidate, segment Segment, refineErr error, summary *RunSummary) {
	attempt := SilenceRefinementAttempt{
		MediaFileID:     candidate.FileID,
		ConfigHash:      a.config.SilenceConfigHash(),
		FileHash:        candidate.FileHash,
		FileSize:        candidate.FileSize,
		DurationSeconds: candidate.DurationSeconds,
		ChaptersHash:    candidate.ChaptersHash,
		IntroStart:      segment.Start,
		IntroEnd:        segment.End,
		Status:          silenceAttemptNoImprovement,
		RecordedBy:      a.node,
		AttemptedAt:     time.Now().UTC(),
	}
	if refineErr != nil {
		previous, err := a.repo.LoadSilenceRefinementAttempt(ctx, candidate.FileID)
		if err != nil {
			summary.Errors = append(summary.Errors, fmt.Sprintf("file %d: %v", candidate.FileID, err))
			a.logger.WarnContext(ctx, "intro marker silence attempt load failed", "file_id", candidate.FileID, "error", err)
			return
		}
		attempt.Status = silenceAttemptFailed
		attempt.LastError = refineErr.Error()
		attempt.FailureCount = 1
		retryAfter := attempt.AttemptedAt.Add(silenceRetryDelay(1))
		// Backoff escalates only for this server's own consecutive failures; a
		// failure recorded elsewhere may come from that server's environment.
		if previous != nil && previous.Status == silenceAttemptFailed && previous.RecordedBy == attempt.RecordedBy &&
			previous.sameInputs(attempt) {
			if previous.RetryAfter != nil && attempt.AttemptedAt.Before(*previous.RetryAfter) {
				// A forced episode analysis failed inside the backoff window. That
				// is not a retry, so it must not escalate the backoff.
				attempt.FailureCount = previous.FailureCount
				retryAfter = *previous.RetryAfter
			} else {
				attempt.FailureCount = previous.FailureCount + 1
				retryAfter = attempt.AttemptedAt.Add(silenceRetryDelay(attempt.FailureCount))
			}
		}
		attempt.RetryAfter = &retryAfter
	}
	if err := a.repo.UpsertSilenceRefinementAttempt(ctx, attempt); err != nil {
		summary.Errors = append(summary.Errors, fmt.Sprintf("file %d: %v", candidate.FileID, err))
		a.logger.WarnContext(ctx, "intro marker silence attempt record failed", "file_id", candidate.FileID, "error", err)
	}
}

// silenceRetryDelay doubles from silenceRetryBaseDelay per consecutive failure,
// capped at silenceRetryMaxDelay. The base sits under the daily schedule so the
// first retry lands on the next scheduled run.
func silenceRetryDelay(failures int) time.Duration {
	delay := silenceRetryBaseDelay
	for i := 1; i < failures && delay < silenceRetryMaxDelay; i++ {
		delay *= 2
	}
	return min(delay, silenceRetryMaxDelay)
}

func setBestChapterSource(sources map[string]chapterSourceMarker, candidate Candidate, segment Segment) {
	existing, ok := sources[candidate.EpisodeID]
	if !ok || chapterSourceRank(segment) > chapterSourceRank(existing.segment) {
		sources[candidate.EpisodeID] = chapterSourceMarker{candidate: candidate, segment: segment}
	}
}

func chapterSourceRank(segment Segment) int {
	if segment.Algorithm == ChapterSilenceAlgorithm {
		return 2
	}
	if segment.Algorithm == ChapterAlgorithm {
		return 1
	}
	return 0
}

func compatibleEpisodeVersionDuration(source, target Candidate) bool {
	if source.DurationSeconds <= 0 || target.DurationSeconds <= 0 {
		return false
	}
	diff := source.DurationSeconds - target.DurationSeconds
	if diff < 0 {
		diff = -diff
	}
	return diff <= episodeVersionCopyDurationToleranceSeconds
}

func ownDetectionCandidates(candidates []Candidate) []Candidate {
	remaining := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.HasHigherPriorityIntro(models.MarkerSourceScanner) {
			continue
		}
		if candidate.IntroStart != nil && candidate.IntroEnd != nil && candidate.EffectiveIntroSource() != models.MarkerSourceScanner {
			continue
		}
		remaining = append(remaining, candidate)
	}
	return remaining
}

func (a *Analyzer) runSilenceBackfill(ctx context.Context) (RunSummary, error) {
	summary := RunSummary{}
	cfg := a.config.normalized()
	if !cfg.SilenceRefinementEnabled || cfg.SilenceBackfillLimit <= 0 {
		return summary, nil
	}
	candidates, err := a.repo.ListChapterSilenceBackfillCandidates(ctx, cfg.SilenceBackfillLimit, cfg, a.node)
	if err != nil {
		return summary, err
	}
	summary.SilenceBackfillConsidered = len(candidates)
	if len(candidates) == 0 {
		return summary, nil
	}
	// Backfill files are independent: no episode-version copies are made, so
	// the candidates split across workers.
	opts := chapterProcessingOptions{
		forceExistingScanner: true,
		deadline:             time.Now().Add(cfg.SilenceBackfillMaxDuration),
	}
	workers := min(len(candidates), a.workerCount())
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for w := range workers {
		share := make([]Candidate, 0, len(candidates)/workers+1)
		for i := w; i < len(candidates); i += workers {
			share = append(share, candidates[i])
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, shareSummary := a.processChapterCandidates(ctx, share, opts)
			mu.Lock()
			mergeRunSummary(&summary, shareSummary)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return summary, nil
}

// analyzeGroups runs season groups on workerCount workers. Most groups
// are skipped or served from cached fingerprints, so workers keep the ffmpeg
// slots busy while other groups compare or wait on the database. progress is
// called with the number of groups finished.
func (a *Analyzer) analyzeGroups(ctx context.Context, groups []candidateGroup, progress func(done int)) RunSummary {
	var (
		mu      sync.Mutex
		summary RunSummary
		done    int
		wg      sync.WaitGroup
	)
	work := make(chan candidateGroup)
	for range min(len(groups), a.workerCount()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for group := range work {
				groupSummary, err := a.analyzeGroup(ctx, group, analyzeGroupOptions{persistState: true})
				if err != nil {
					a.logger.WarnContext(ctx, "intro marker group analysis failed",
						"season_id", group.SeasonID,
						"media_folder_id", group.MediaFolderID,
						"group_key", group.AnalysisGroupKey,
						"error", err)
				}
				mu.Lock()
				mergeRunSummary(&summary, groupSummary)
				done++
				if progress != nil {
					progress(done)
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, group := range groups {
		select {
		case work <- group:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	return summary
}

type candidateGroup struct {
	SeasonID         string
	MediaFolderID    int
	AnalysisGroupKey string
	Candidates       []Candidate
}

type analyzeGroupOptions struct {
	force        bool
	patchFileIDs map[int]struct{}
	persistState bool
}

func groupCandidates(candidates []Candidate) []candidateGroup {
	byKey := map[string]*candidateGroup{}
	for _, candidate := range candidates {
		key := fmt.Sprintf("%d:%s:%s", candidate.MediaFolderID, candidate.SeasonID, candidate.AnalysisGroupKey())
		group := byKey[key]
		if group == nil {
			group = &candidateGroup{
				SeasonID:         candidate.SeasonID,
				MediaFolderID:    candidate.MediaFolderID,
				AnalysisGroupKey: candidate.AnalysisGroupKey(),
			}
			byKey[key] = group
		}
		group.Candidates = append(group.Candidates, candidate)
	}

	groups := make([]candidateGroup, 0, len(byKey))
	for _, group := range byKey {
		episodeIDs := map[string]struct{}{}
		for _, candidate := range group.Candidates {
			episodeIDs[candidate.EpisodeID] = struct{}{}
		}
		if len(episodeIDs) < 2 {
			continue
		}
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].MediaFolderID != groups[j].MediaFolderID {
			return groups[i].MediaFolderID < groups[j].MediaFolderID
		}
		if groups[i].SeasonID != groups[j].SeasonID {
			return groups[i].SeasonID < groups[j].SeasonID
		}
		return groups[i].AnalysisGroupKey < groups[j].AnalysisGroupKey
	})
	return groups
}

func (a *Analyzer) analyzeGroup(ctx context.Context, group candidateGroup, opts analyzeGroupOptions) (RunSummary, error) {
	summary := RunSummary{}
	state := SeasonState{
		SeasonID:         group.SeasonID,
		MediaFolderID:    group.MediaFolderID,
		AnalysisGroupKey: group.AnalysisGroupKey,
		InputSignature:   InputSignature(group.Candidates),
		EpisodeCount:     distinctEpisodeCount(group.Candidates),
		FileCount:        len(group.Candidates),
	}
	existing, err := a.repo.LoadSeasonState(ctx, state, a.config)
	if err != nil {
		return summary, err
	}
	if !opts.force && existing != nil && existing.InputSignature == state.InputSignature && existing.settled(time.Now()) {
		summary.GroupsSkipped++
		return summary, nil
	}

	inputs, hits, computed, failed, err := a.ensureFingerprints(ctx, group.Candidates)
	summary.FingerprintCacheHits += hits
	summary.FingerprintsComputed += computed
	summary.FingerprintExtractionErrors += failed
	// A failed extraction (unlike a file with no audio to fingerprint) may
	// succeed later, so the group stays partial and is retried even though its
	// inputs have not changed.
	settle := func(status string) {
		state.Status = status
		if failed > 0 {
			state.Status = seasonStatusPartial
			state.LastError = fmt.Sprintf("%d fingerprint extraction(s) failed", failed)
		}
	}
	if err != nil {
		state.Status = seasonStatusFailed
		state.LastError = err.Error()
		if opts.persistState {
			_ = a.repo.UpsertSeasonState(ctx, state, a.config)
		}
		summary.Errors = append(summary.Errors, err.Error())
		return summary, err
	}
	if distinctFingerprintEpisodeCount(inputs) < 2 {
		state.LastError = "too few fingerprints"
		settle(seasonStatusNotFound)
		if opts.persistState {
			if err := a.repo.UpsertSeasonState(ctx, state, a.config); err != nil {
				return summary, err
			}
		}
		summary.GroupsNotFound++
		return summary, nil
	}

	segments := CompareFingerprints(inputs, a.config)
	if len(segments) == 0 {
		settle(seasonStatusNotFound)
		if opts.persistState {
			if err := a.repo.UpsertSeasonState(ctx, state, a.config); err != nil {
				return summary, err
			}
		}
		summary.GroupsNotFound++
		return summary, nil
	}

	byFileID := make(map[int]Candidate, len(group.Candidates))
	for _, candidate := range group.Candidates {
		byFileID[candidate.FileID] = candidate
	}
	// When subtitle refinement fails, a file that already has a
	// subtitle-refined marker keeps it: the unrefined result would outrank it.
	// Other files still get the unrefined marker. Either way the group is
	// recorded as failed so the next run retries the refinement.
	refinementFailures := 0
	for fileID, segment := range segments {
		if !shouldPatchGroupFile(fileID, opts.patchFileIDs) {
			continue
		}
		candidate := byFileID[fileID]
		var refineErr error
		segment, refineErr = a.refineChromaprintSegment(ctx, candidate, segment, &summary)
		if refineErr != nil {
			if err := ctx.Err(); err != nil {
				return summary, err
			}
			refinementFailures++
			if candidate.hasSubtitleRefinedIntro() {
				continue
			}
		}
		applied, patchErr := a.repo.PatchIntroMarker(ctx, IntroMarkerPatch{
			ExpectedFile: candidate.expectedFile(),
			FileID:       fileID,
			Start:        segment.Start,
			End:          segment.End,
			Source:       models.MarkerSourceScanner,
			Confidence:   segment.Confidence,
			Algorithm:    segment.Algorithm,
			DetectedAt:   time.Now().UTC(),
		})
		if patchErr != nil {
			msg := fmt.Sprintf("file %d: %v", fileID, patchErr)
			summary.Errors = append(summary.Errors, msg)
			a.logger.WarnContext(ctx, "intro marker chromaprint patch failed", "file_id", fileID, "path", candidate.FilePath, "error", patchErr)
			continue
		}
		if applied {
			summary.ChromaprintMarkersWritten++
		}
	}

	if opts.persistState {
		settle(seasonStatusComplete)
		if refinementFailures > 0 {
			state.Status = seasonStatusFailed
			state.LastError = fmt.Sprintf("subtitle refinement failed for %d file(s)", refinementFailures)
		}
		state.MarkersWritten = summary.ChromaprintMarkersWritten
		if err := a.repo.UpsertSeasonState(ctx, state, a.config); err != nil {
			return summary, err
		}
	}
	return summary, nil
}

// refineChromaprintSegment returns the segment to write, unchanged when
// refinement is disabled or does not apply, and the refinement error if any.
func (a *Analyzer) refineChromaprintSegment(ctx context.Context, candidate Candidate, segment Segment, summary *RunSummary) (Segment, error) {
	if a.chromaprintRefiner == nil || !a.config.normalized().DialogueRefinementEnabled {
		return segment, nil
	}
	summary.DialogueRefinementsAttempted++
	refined, ok, err := a.chromaprintRefiner.RefineChromaprintStart(ctx, candidate, segment)
	if err != nil {
		summary.DialogueRefinementErrors++
		a.logger.WarnContext(ctx, "intro marker dialogue refinement failed", "file_id", candidate.FileID, "path", candidate.FilePath, "error", err)
		return segment, err
	}
	if ok {
		summary.DialogueRefinementsApplied++
		// A later start can leave a short intro, which rates as one.
		if refined.End-refined.Start < shortIntroSeconds {
			refined.Confidence = min(refined.Confidence, chromaprintShortConfidence)
		}
		return refined, nil
	}
	return segment, nil
}

// hasSubtitleRefinedIntro reports whether the file's current intro came from
// Chromaprint with subtitle refinement, in any version.
func (c Candidate) hasSubtitleRefinedIntro() bool {
	return c.IntroMarkersAlgorithm != nil &&
		strings.HasPrefix(*c.IntroMarkersAlgorithm, chromaprintDialogueAlgorithmPrefix)
}

func candidateFileIDs(candidates []Candidate) map[int]struct{} {
	fileIDs := make(map[int]struct{}, len(candidates))
	for _, candidate := range candidates {
		fileIDs[candidate.FileID] = struct{}{}
	}
	return fileIDs
}

func shouldPatchGroupFile(fileID int, allowed map[int]struct{}) bool {
	if allowed == nil {
		return true
	}
	_, ok := allowed[fileID]
	return ok
}

// ensureFingerprints loads cached fingerprints and extracts missing ones. It
// returns the inputs with their cache hits, extractions, and failed
// extractions; a database error or cancellation is returned as err.
func (a *Analyzer) ensureFingerprints(ctx context.Context, candidates []Candidate) ([]fingerprintInput, int, int, int, error) {
	var (
		mu       sync.Mutex
		inputs   []fingerprintInput
		hits     int
		computed int
		failed   int
		firstErr error
	)
	setErr := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	acquire := a.acquireFFmpeg
	if a.sharedSlots() == nil {
		// Analyzers built without NewAnalyzer still bound this call.
		local := &Analyzer{ffmpegSlots: newSlotLimiter(a.workerCount())}
		acquire = local.acquireFFmpeg
	}
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ctx.Err(); err != nil {
				setErr(err)
				return
			}
			cached, err := a.loadFingerprint(ctx, candidate)
			if err != nil {
				setErr(err)
				return
			}
			if cached != nil {
				mu.Lock()
				inputs = append(inputs, fingerprintInput{Candidate: candidate, Points: cached.Points})
				hits++
				mu.Unlock()
				return
			}

			release, err := acquire(ctx)
			if err != nil {
				setErr(err)
				return
			}
			fp, ok, err := a.extractor.Extract(ctx, candidate)
			release()
			if err != nil {
				if ctx.Err() != nil {
					setErr(ctx.Err())
					return
				}
				a.logger.WarnContext(ctx, "intro marker fingerprint extraction failed", "file_id", candidate.FileID, "path", candidate.FilePath, "error", err)
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			if !ok {
				return
			}
			if err := a.repo.UpsertFingerprint(ctx, fp); err != nil {
				setErr(err)
				return
			}
			mu.Lock()
			inputs = append(inputs, fingerprintInput{Candidate: candidate, Points: fp.Points})
			computed++
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(inputs, func(i, j int) bool {
		return inputs[i].Candidate.FileID < inputs[j].Candidate.FileID
	})
	return inputs, hits, computed, failed, firstErr
}

func distinctEpisodeCount(candidates []Candidate) int {
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		seen[candidate.EpisodeID] = struct{}{}
	}
	return len(seen)
}

func distinctFingerprintEpisodeCount(inputs []fingerprintInput) int {
	seen := map[string]struct{}{}
	for _, input := range inputs {
		seen[input.Candidate.EpisodeID] = struct{}{}
	}
	return len(seen)
}

func mergeRunSummary(dst *RunSummary, src RunSummary) {
	dst.LibrariesScanned += src.LibrariesScanned
	dst.FilesConsidered += src.FilesConsidered
	dst.SeasonGroupsConsidered += src.SeasonGroupsConsidered
	dst.FingerprintsComputed += src.FingerprintsComputed
	dst.FingerprintCacheHits += src.FingerprintCacheHits
	dst.ChapterMarkersWritten += src.ChapterMarkersWritten
	dst.ChromaprintMarkersWritten += src.ChromaprintMarkersWritten
	dst.GroupsNotFound += src.GroupsNotFound
	dst.GroupsSkipped += src.GroupsSkipped
	dst.FingerprintExtractionErrors += src.FingerprintExtractionErrors
	dst.Errors = append(dst.Errors, src.Errors...)
	if src.ChromaprintSupported {
		dst.ChromaprintSupported = true
	}
	if src.ChromaprintSupportMessage != "" {
		dst.ChromaprintSupportMessage = src.ChromaprintSupportMessage
	}
	dst.SilenceRefinementsAttempted += src.SilenceRefinementsAttempted
	dst.SilenceRefinementsApplied += src.SilenceRefinementsApplied
	dst.SilenceRefinementErrors += src.SilenceRefinementErrors
	dst.EpisodeVersionMarkersCopied += src.EpisodeVersionMarkersCopied
	dst.SilenceBackfillConsidered += src.SilenceBackfillConsidered
	dst.DialogueRefinementsAttempted += src.DialogueRefinementsAttempted
	dst.DialogueRefinementsApplied += src.DialogueRefinementsApplied
	dst.DialogueRefinementErrors += src.DialogueRefinementErrors
}
