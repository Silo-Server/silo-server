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
	sources := map[string][]chapterSourceMarker{}
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
		sources[candidate.EpisodeID] = append(sources[candidate.EpisodeID], chapterSourceMarker{candidate: candidate, segment: segment})
		if owned && a.patchCredits(ctx, candidate, segment, &summary) {
			summary.CreditsChapterMarkersWritten++
		}
	}

	for _, candidate := range unresolved {
		if err := ctx.Err(); err != nil {
			summary.Errors = append(summary.Errors, err.Error())
			return summary
		}
		source, ok := closestCreditsSource(sources[candidate.EpisodeID], candidate)
		if !ok {
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

// closestCreditsSource returns the chapter source whose duration is closest
// to target's among the sources a copy may come from, so each version of an
// episode is placed from the version it matches.
func closestCreditsSource(sources []chapterSourceMarker, target Candidate) (chapterSourceMarker, bool) {
	var best chapterSourceMarker
	found := false
	for _, source := range sources {
		if !compatibleEpisodeVersionDuration(source.candidate, target) {
			continue
		}
		if !found || math.Abs(source.candidate.DurationSeconds-target.DurationSeconds) <
			math.Abs(best.candidate.DurationSeconds-target.DurationSeconds) {
			best, found = source, true
		}
	}
	return best, found
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
	if !opts.force {
		// A run without tail passes also keeps off a group a tail-capable
		// run settled: its audio-only result could replace the audio and
		// video credits written there, and that run would not come back.
		hashes := []string{analysisHash}
		if !opts.creditsTail {
			hashes = append(hashes, CreditsAnalysisConfigHash(true))
		}
		for _, hash := range hashes {
			existing, err := a.repo.LoadSeasonState(ctx, state, hash)
			if err != nil {
				return summary, err
			}
			if existing != nil && existing.InputSignature == state.InputSignature && existing.settled(time.Now()) {
				summary.CreditsGroupsSkipped++
				return summary, nil
			}
		}
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
	errorsBefore := len(summary.Errors)
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
	// A marker that failed to write leaves the group unsettled, so the next
	// run writes it instead of skipping the group until its inputs change.
	if failed := len(summary.Errors) - errorsBefore; failed > 0 {
		state.LastError = fmt.Sprintf("credits marker write failed for %d file(s)", failed)
		return summary, persist(seasonStatusFailed)
	}
	return summary, persist(seasonStatusComplete)
}

// patchCredits writes a credits segment onto the candidate's file unless its
// stored credits already match, and reports whether the write applied.
// Errors are recorded in summary.
func (a *Analyzer) patchCredits(ctx context.Context, candidate Candidate, segment Segment, summary *RunSummary) bool {
	applied, err := a.writeCredits(ctx, candidate, segment)
	if err != nil {
		a.creditsPatchFailed(ctx, candidate, segment, err, summary)
		return false
	}
	return applied
}

// creditsPatchFailed records a failed credits write in summary.
func (a *Analyzer) creditsPatchFailed(ctx context.Context, candidate Candidate, segment Segment, err error, summary *RunSummary) {
	summary.Errors = append(summary.Errors, fmt.Sprintf("file %d: %v", candidate.FileID, err))
	a.logger.WarnContext(ctx, "credits marker patch failed", "file_id", candidate.FileID, "algorithm", segment.Algorithm, "error", err)
}

// writeCredits writes a credits segment onto the candidate's file unless its
// stored credits already match, and reports whether the write applied.
func (a *Analyzer) writeCredits(ctx context.Context, candidate Candidate, segment Segment) (bool, error) {
	if candidate.marker(kindCredits).matches(segment) {
		return false, nil
	}
	return a.repo.PatchMarker(ctx, MarkerPatch{
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
