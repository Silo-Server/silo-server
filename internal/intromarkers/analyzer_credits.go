package intromarkers

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// processCreditsChapters writes credits from authored chapters and copies
// them to other versions of the same episode. Chapter credits are
// authoritative. It needs no ffmpeg, so every run checks every file; a file
// whose stored marker already matches is not written again.
func (a *Analyzer) processCreditsChapters(ctx context.Context, candidates []Candidate) RunSummary {
	summary := RunSummary{}
	sources := map[string]chapterSourceMarker{}
	var unresolved []Candidate

	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			summary.Errors = append(summary.Errors, err.Error())
			return summary
		}
		owned := candidate.ownsMarker(kindCredits)
		segment, ok := DetectChapterCredits(candidate.Chapters, candidate.DurationSeconds, false)
		if !ok {
			if owned {
				unresolved = append(unresolved, candidate)
			}
			continue
		}
		// A file's chapters can place credits on its other versions even
		// when its own marker came from a higher-priority source.
		if _, ok := sources[candidate.EpisodeID]; !ok {
			sources[candidate.EpisodeID] = chapterSourceMarker{candidate: candidate, segment: segment}
		}
		if owned && a.patchCredits(ctx, candidate, segment, &summary) {
			summary.CreditsChapterMarkersWritten++
		}
	}

	for _, candidate := range unresolved {
		if err := ctx.Err(); err != nil {
			summary.Errors = append(summary.Errors, err.Error())
			return summary
		}
		source, ok := sources[candidate.EpisodeID]
		if !ok || !compatibleEpisodeVersionDuration(source.candidate, candidate) {
			continue
		}
		segment, ok := copyCreditsToVersion(source, candidate)
		if !ok {
			summary.CreditsRejected++
			continue
		}
		if a.patchCredits(ctx, candidate, segment, &summary) {
			summary.CreditsVersionMarkersCopied++
		}
	}
	return summary
}

// copyCreditsToVersion places a chapter credits result on another version of
// the same episode whose duration is within
// episodeVersionCopyDurationToleranceSeconds. Versions usually differ by what
// precedes the credits, such as studio logos, so the copy keeps its distance
// from the end of the file, and credits that ran to the end of the source run
// to the end of the target.
func copyCreditsToVersion(source chapterSourceMarker, target Candidate) (Segment, bool) {
	if !compatibleEpisodeVersionDuration(source.candidate, target) {
		return Segment{}, false
	}
	sourceDuration, duration := source.candidate.DurationSeconds, target.DurationSeconds
	segment := Segment{
		Start:      duration - (sourceDuration - source.segment.Start),
		End:        duration - (sourceDuration - source.segment.End),
		Confidence: creditsVersionCopyConfidence,
		Algorithm:  CreditsVersionCopyAlgorithm,
	}
	if reachesEOF(source.segment.End, sourceDuration) {
		segment.End = duration
	}
	return applyCreditsGuards(segment, target, creditsLimitsFor(false))
}

// analyzeCreditsGroup places the credits of a season group's files from
// their tail evidence: the season's tail audio compared across episodes, and
// each file's tail keyframes and silences when opts.creditsTail is set. A
// group of one episode has no audio to compare and uses video alone. Files
// with chapter credits were settled by processCreditsChapters, which wins.
func (a *Analyzer) analyzeCreditsGroup(ctx context.Context, group candidateGroup, opts analyzeGroupOptions) (RunSummary, error) {
	summary := RunSummary{}
	state := SeasonState{
		SeasonID:         group.SeasonID,
		MediaFolderID:    group.MediaFolderID,
		AnalysisGroupKey: group.AnalysisGroupKey,
		InputSignature:   creditsInputSignature(group.Candidates),
		EpisodeCount:     distinctEpisodeCount(group.Candidates),
		FileCount:        len(group.Candidates),
	}
	analysisHash := CreditsAnalysisConfigHash(opts.creditsTail)
	existing, err := a.repo.LoadSeasonState(ctx, state, analysisHash)
	if err != nil {
		return summary, err
	}
	if !opts.force && existing != nil && existing.InputSignature == state.InputSignature && existing.settled(time.Now()) {
		summary.CreditsGroupsSkipped++
		return summary, nil
	}

	var targets []Candidate
	for _, candidate := range group.Candidates {
		if !shouldPatchGroupFile(candidate.FileID, opts.patchFileIDs) {
			continue
		}
		if _, ok := DetectChapterCredits(candidate.Chapters, candidate.DurationSeconds, false); ok {
			continue
		}
		targets = append(targets, candidate)
	}
	inputs, err := a.ensureCreditsInputs(ctx, group.Candidates, creditsInputOptions{
		fingerprints: state.EpisodeCount >= 2,
		tails:        opts.creditsTail,
		tailFileIDs:  candidateFileIDs(targets),
	})
	summary.CreditsFingerprintCacheHits += inputs.fingerprintCounts.hits
	summary.CreditsFingerprintsComputed += inputs.fingerprintCounts.computed
	summary.CreditsFingerprintErrors += inputs.fingerprintCounts.failed
	summary.CreditsTailCacheHits += inputs.tailCounts.hits
	summary.CreditsTailScansComputed += inputs.tailCounts.computed
	summary.CreditsTailScanErrors += inputs.tailCounts.failed
	summary.CreditsTailUnusable += inputs.tailCounts.unusable
	counts := inputs.fingerprintCounts
	counts.failed += inputs.tailCounts.failed
	counts.deferred += inputs.tailCounts.deferred
	persist := func(status string) error {
		settleSeasonState(&state, status, counts)
		if !opts.persistState {
			return nil
		}
		return a.repo.UpsertSeasonState(ctx, state, analysisHash)
	}
	if err != nil {
		state.Status = seasonStatusFailed
		state.LastError = err.Error()
		if opts.persistState {
			_ = a.repo.UpsertSeasonState(ctx, state, analysisHash)
		}
		summary.Errors = append(summary.Errors, err.Error())
		return summary, err
	}

	var matches map[int]seasonMatch
	if distinctFingerprintEpisodeCount(inputs.fingerprints) >= 2 {
		matches = matchSeason(inputs.fingerprints, creditsProfile())
	}
	profile := creditsProfile()
	limits := creditsLimitsFor(false)
	found := 0
	written := 0
	for _, candidate := range targets {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		evidence := creditsEvidence{}
		if match, ok := matches[candidate.FileID]; ok {
			audio := creditsAudioFor(match, profile)
			evidence.Audio = &audio
		}
		if tail := inputs.tails[candidate.FileID]; tail != nil {
			evidence.Keyframes = classifyKeyframes(tail.Frames)
			evidence.Silences = tail.Silences
		}
		if evidence.Audio == nil && len(evidence.Keyframes) == 0 {
			continue
		}
		segment, ok := combineCredits(candidate, evidence, limits)
		if !ok {
			if evidence.Audio != nil {
				summary.CreditsRejected++
			}
			continue
		}
		found++
		if !a.patchCredits(ctx, candidate, segment, &summary) {
			continue
		}
		written++
		switch segment.Algorithm {
		case CreditsAudioVideoAlgorithm:
			summary.CreditsAudioVideoMarkersWritten++
		case CreditsVideoAlgorithm:
			summary.CreditsVideoMarkersWritten++
		default:
			summary.CreditsAudioMarkersWritten++
		}
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	if found == 0 {
		summary.CreditsGroupsNotFound++
		return summary, persist(seasonStatusNotFound)
	}
	state.MarkersWritten = written
	return summary, persist(seasonStatusComplete)
}

// patchCredits writes a credits segment onto the candidate's file unless its
// stored credits already match, and reports whether the write applied.
// Errors are recorded in summary.
func (a *Analyzer) patchCredits(ctx context.Context, candidate Candidate, segment Segment, summary *RunSummary) bool {
	if candidate.marker(kindCredits).matches(segment) {
		return false
	}
	applied, err := a.repo.PatchMarker(ctx, MarkerPatch{
		Kind:         kindCredits,
		ExpectedFile: candidate.expectedFile(),
		FileID:       candidate.FileID,
		Start:        segment.Start,
		End:          segment.End,
		Source:       models.MarkerSourceScanner,
		Confidence:   segment.Confidence,
		Algorithm:    segment.Algorithm,
		DetectedAt:   time.Now().UTC(),
	})
	if err != nil {
		summary.Errors = append(summary.Errors, fmt.Sprintf("file %d: %v", candidate.FileID, err))
		a.logger.WarnContext(ctx, "credits marker patch failed", "file_id", candidate.FileID, "algorithm", segment.Algorithm, "error", err)
		return false
	}
	return applied
}

// storedMarkerTolerance is the boundary difference below which a stored
// marker already holds a result. It matches the marker writer's tolerance.
const storedMarkerTolerance = 0.5

// matches reports whether the stored marker already holds segment, from the
// same algorithm with the same confidence, so writing it again would change
// nothing.
func (m candidateMarker) matches(segment Segment) bool {
	return m.present() && m.Algorithm != nil && *m.Algorithm == segment.Algorithm &&
		m.Confidence != nil && *m.Confidence == segment.Confidence &&
		math.Abs(*m.Start-segment.Start) <= storedMarkerTolerance &&
		math.Abs(*m.End-segment.End) <= storedMarkerTolerance
}
