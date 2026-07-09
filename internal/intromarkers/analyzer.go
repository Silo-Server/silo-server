package intromarkers

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
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
}

type introRepository interface {
	CountEnabledLibraries(ctx context.Context) (int, error)
	ListEligibleCandidates(ctx context.Context) ([]Candidate, error)
	ListCandidatesForEpisode(ctx context.Context, episodeID string) ([]Candidate, error)
	ListCandidatesForGroup(ctx context.Context, mediaFolderID int, seasonID, analysisGroupKey string) ([]Candidate, error)
	ListChapterSilenceBackfillCandidates(ctx context.Context, limit int) ([]Candidate, error)
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
	return &Analyzer{
		repo:               repo,
		extractor:          NewChromaprintExtractor(config),
		refiner:            NewSilenceBoundaryRefiner(config),
		chromaprintRefiner: NewDialogueBoundaryRefiner(config),
		config:             config,
		logger:             logger,
	}
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

	for idx, group := range groups {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		percent := 40 + float64(idx)/float64(len(groups))*55
		report(percent, fmt.Sprintf("Analyzing intro group %d/%d", idx+1, len(groups)))
		groupSummary, err := a.analyzeGroup(ctx, group, analyzeGroupOptions{
			persistState: true,
		})
		summary.FingerprintsComputed += groupSummary.FingerprintsComputed
		summary.FingerprintCacheHits += groupSummary.FingerprintCacheHits
		summary.ChromaprintMarkersWritten += groupSummary.ChromaprintMarkersWritten
		summary.DialogueRefinementsAttempted += groupSummary.DialogueRefinementsAttempted
		summary.DialogueRefinementsApplied += groupSummary.DialogueRefinementsApplied
		summary.DialogueRefinementErrors += groupSummary.DialogueRefinementErrors
		summary.GroupsNotFound += groupSummary.GroupsNotFound
		summary.GroupsSkipped += groupSummary.GroupsSkipped
		summary.Errors = append(summary.Errors, groupSummary.Errors...)
		if err != nil {
			a.logger.WarnContext(ctx, "intro marker group analysis failed",
				"season_id", group.SeasonID,
				"media_folder_id", group.MediaFolderID,
				"group_key", group.AnalysisGroupKey,
				"error", err)
		}
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

		segment = a.refineChapterSegment(ctx, candidate, segment, &summary)
		applied, patchErr := a.repo.PatchIntroMarker(ctx, IntroMarkerPatch{
			FileID:     candidate.FileID,
			Start:      segment.Start,
			End:        segment.End,
			Source:     models.MarkerSourceScanner,
			Confidence: segment.Confidence,
			Algorithm:  segment.Algorithm,
			DetectedAt: time.Now().UTC(),
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
			FileID:     candidate.FileID,
			Start:      source.segment.Start,
			End:        source.segment.End,
			Source:     models.MarkerSourceScanner,
			Confidence: confidence,
			Algorithm:  EpisodeVersionCopyAlgorithm,
			DetectedAt: time.Now().UTC(),
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

func (a *Analyzer) refineChapterSegment(ctx context.Context, candidate Candidate, segment Segment, summary *RunSummary) Segment {
	if a.refiner == nil || !a.config.normalized().SilenceRefinementEnabled {
		return segment
	}
	summary.SilenceRefinementsAttempted++
	refined, ok, err := a.refiner.RefineChapterEnd(ctx, candidate, segment)
	if err != nil {
		summary.SilenceRefinementErrors++
		a.logger.WarnContext(ctx, "intro marker silence refinement failed", "file_id", candidate.FileID, "path", candidate.FilePath, "error", err)
		return segment
	}
	if ok {
		summary.SilenceRefinementsApplied++
		return refined
	}
	return segment
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
	candidates, err := a.repo.ListChapterSilenceBackfillCandidates(ctx, cfg.SilenceBackfillLimit)
	if err != nil {
		return summary, err
	}
	summary.SilenceBackfillConsidered = len(candidates)
	if len(candidates) == 0 {
		return summary, nil
	}
	_, backfillSummary := a.processChapterCandidates(ctx, candidates, chapterProcessingOptions{
		forceExistingScanner: true,
		deadline:             time.Now().Add(cfg.SilenceBackfillMaxDuration),
	})
	mergeRunSummary(&summary, backfillSummary)
	return summary, nil
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
	if !opts.force && existing != nil && existing.InputSignature == state.InputSignature &&
		(existing.Status == "complete" || existing.Status == "not_found") {
		summary.GroupsSkipped++
		return summary, nil
	}

	inputs, hits, computed, err := a.ensureFingerprints(ctx, group.Candidates)
	summary.FingerprintCacheHits += hits
	summary.FingerprintsComputed += computed
	if err != nil {
		state.Status = "failed"
		state.LastError = err.Error()
		if opts.persistState {
			_ = a.repo.UpsertSeasonState(ctx, state, a.config)
		}
		summary.Errors = append(summary.Errors, err.Error())
		return summary, err
	}
	if distinctFingerprintEpisodeCount(inputs) < 2 {
		state.Status = "not_found"
		state.LastError = "too few fingerprints"
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
		state.Status = "not_found"
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
	for fileID, segment := range segments {
		if !shouldPatchGroupFile(fileID, opts.patchFileIDs) {
			continue
		}
		candidate := byFileID[fileID]
		segment = a.refineChromaprintSegment(ctx, candidate, segment, &summary)
		applied, patchErr := a.repo.PatchIntroMarker(ctx, IntroMarkerPatch{
			FileID:     fileID,
			Start:      segment.Start,
			End:        segment.End,
			Source:     models.MarkerSourceScanner,
			Confidence: segment.Confidence,
			Algorithm:  segment.Algorithm,
			DetectedAt: time.Now().UTC(),
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
		state.Status = "complete"
		state.MarkersWritten = summary.ChromaprintMarkersWritten
		if err := a.repo.UpsertSeasonState(ctx, state, a.config); err != nil {
			return summary, err
		}
	}
	return summary, nil
}

func (a *Analyzer) refineChromaprintSegment(ctx context.Context, candidate Candidate, segment Segment, summary *RunSummary) Segment {
	if a.chromaprintRefiner == nil || !a.config.normalized().DialogueRefinementEnabled {
		return segment
	}
	summary.DialogueRefinementsAttempted++
	refined, ok, err := a.chromaprintRefiner.RefineChromaprintStart(ctx, candidate, segment)
	if err != nil {
		summary.DialogueRefinementErrors++
		a.logger.WarnContext(ctx, "intro marker dialogue refinement failed", "file_id", candidate.FileID, "path", candidate.FilePath, "error", err)
		return segment
	}
	if ok {
		summary.DialogueRefinementsApplied++
		return refined
	}
	return segment
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

func (a *Analyzer) ensureFingerprints(ctx context.Context, candidates []Candidate) ([]fingerprintInput, int, int, error) {
	var (
		mu       sync.Mutex
		inputs   []fingerprintInput
		hits     int
		computed int
		firstErr error
	)
	sem := make(chan struct{}, max(1, a.config.MaxParallelFFmpeg))
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ctx.Err(); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			cached, err := a.repo.LoadFingerprint(ctx, candidate, a.config)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			if cached != nil {
				mu.Lock()
				inputs = append(inputs, fingerprintInput{Candidate: candidate, Points: cached.Points})
				hits++
				mu.Unlock()
				return
			}

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				mu.Unlock()
				return
			}
			fp, ok, err := a.extractor.Extract(ctx, candidate)
			<-sem
			if err != nil {
				a.logger.WarnContext(ctx, "intro marker fingerprint extraction failed", "file_id", candidate.FileID, "path", candidate.FilePath, "error", err)
				return
			}
			if !ok {
				return
			}
			if err := a.repo.UpsertFingerprint(ctx, fp); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
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
	return inputs, hits, computed, firstErr
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
