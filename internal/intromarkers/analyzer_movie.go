package intromarkers

import (
	"context"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// movieCreditsRunBudget bounds how long one scheduled run starts new movies.
// Movies are best effort and a large library's backlog takes many runs, so
// they must not hold the run, and the cluster lock, for days. Movies started
// before the budget runs out finish.
const movieCreditsRunBudget = 60 * time.Minute

// movieRunOptions shapes one analysis of movie files.
type movieRunOptions struct {
	// deadline stops starting new movies; zero means none.
	deadline time.Time
	// progress is called with the number of movies finished.
	progress func(done int)
}

// movieCandidatePageSize is how many movies the scheduled run lists at a
// time, so a large library's backlog is neither read nor held at once.
const movieCandidatePageSize = 200

// runMovies analyzes the movies the scheduled run covers, a page at a time,
// within the movie budget, after the episodes. The budget includes listing
// the movies. progress is called with the number of movies finished and the
// share of the budget used, from 0 to 1.
func (a *Analyzer) runMovies(ctx context.Context, progress func(done int, budgetUsed float64)) (RunSummary, error) {
	budget := a.movieBudget
	if budget <= 0 {
		budget = movieCreditsRunBudget
	}
	started := a.clock()
	deadline := started.Add(budget)
	pageSize := a.moviePageSize
	if pageSize <= 0 {
		pageSize = movieCandidatePageSize
	}
	summary := RunSummary{}
	var after *movieCandidateCursor
	for {
		candidates, next, err := a.repo.ListMovieCandidates(ctx, a.nodeName(), after, pageSize)
		if err != nil {
			return summary, err
		}
		finished := summary.MoviesConsidered
		summary.MoviesConsidered += len(candidates)
		if len(candidates) == 0 {
			break
		}
		mergeRunSummary(&summary, a.analyzeMovies(ctx, candidates, movieRunOptions{
			deadline: deadline,
			progress: func(done int) {
				if progress != nil {
					progress(finished+done, min(1, float64(a.clock().Sub(started))/float64(budget)))
				}
			},
		}))
		if summary.MovieBudgetExhausted || ctx.Err() != nil || len(candidates) < pageSize {
			break
		}
		if a.pastDeadline(deadline) {
			summary.MovieBudgetExhausted = true
			break
		}
		after = next
	}
	return summary, ctx.Err()
}

// AnalyzeMovie looks for the credits of a movie item's files again, from
// their cached tail passes where they have them. Admin refresh uses it.
func (a *Analyzer) AnalyzeMovie(ctx context.Context, contentID string) (RunSummary, error) {
	candidates, err := a.repo.ListMovieCandidatesForItem(ctx, contentID)
	if err != nil {
		return RunSummary{}, err
	}
	return a.analyzeMovieCandidates(ctx, candidates)
}

// AnalyzeMovieFile looks for the credits of one movie file. Playback uses
// it, under WithPlaybackPriority, for a played movie without credits.
func (a *Analyzer) AnalyzeMovieFile(ctx context.Context, fileID int) (RunSummary, error) {
	candidates, err := a.repo.ListMovieCandidatesForFile(ctx, fileID)
	if err != nil {
		return RunSummary{}, err
	}
	return a.analyzeMovieCandidates(ctx, candidates)
}

func (a *Analyzer) analyzeMovieCandidates(ctx context.Context, candidates []Candidate) (RunSummary, error) {
	summary := RunSummary{FilesConsidered: len(candidates), MoviesConsidered: len(candidates)}
	if len(candidates) == 0 {
		return summary, nil
	}
	mergeRunSummary(&summary, a.analyzeMovies(ctx, candidates, movieRunOptions{}))
	return summary, ctx.Err()
}

// clock returns the time movie budgets are measured by.
func (a *Analyzer) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// pastDeadline reports whether deadline is set and has passed.
func (a *Analyzer) pastDeadline(deadline time.Time) bool {
	return !deadline.IsZero() && a.clock().After(deadline)
}

// movieTailReady reports whether movies can get credits from video: the
// analyzer has a movie sampler and ffmpeg offers what its pass needs.
// Without it, movies get credits from chapters alone.
func (a *Analyzer) movieTailReady(ctx context.Context) bool {
	if a.movieSampler == nil {
		return false
	}
	if err := a.movieSampler.PreflightMovieTail(ctx); err != nil {
		if ctx.Err() == nil {
			a.movieWarnOnce.Do(func() {
				a.logger.WarnContext(ctx, "movie credits visuals unavailable; movie credits use chapters only", "error", err)
			})
		}
		return false
	}
	return true
}

// analyzeMovies looks for the credits of movie candidates on workerCount
// workers. A worker starts no movie once opts.deadline has passed; the run
// then reports the movie budget exhausted.
func (a *Analyzer) analyzeMovies(ctx context.Context, candidates []Candidate, opts movieRunOptions) RunSummary {
	tailReady := a.movieTailReady(ctx)
	var (
		mu      sync.Mutex
		summary RunSummary
		next    int
		done    int
		wg      sync.WaitGroup
	)
	take := func() (Candidate, bool) {
		mu.Lock()
		defer mu.Unlock()
		if next >= len(candidates) || ctx.Err() != nil {
			return Candidate{}, false
		}
		if a.pastDeadline(opts.deadline) {
			summary.MovieBudgetExhausted = true
			return Candidate{}, false
		}
		next++
		return candidates[next-1], true
	}
	for range min(len(candidates), a.workerCount()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				candidate, ok := take()
				if !ok {
					return
				}
				movieSummary := a.analyzeMovie(ctx, candidate, tailReady, opts.deadline)
				mu.Lock()
				mergeRunSummary(&summary, movieSummary)
				done++
				if opts.progress != nil {
					opts.progress(done)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		summary.Errors = append(summary.Errors, err.Error())
	}
	return summary
}

// analyzeMovie places one movie's credits: from a credits chapter when it
// has one, otherwise from its tail keyframes when tailReady. A movie whose
// credits came from a higher-priority source is left alone.
func (a *Analyzer) analyzeMovie(ctx context.Context, candidate Candidate, tailReady bool, deadline time.Time) RunSummary {
	summary := RunSummary{}
	if !candidate.ownsMarker(kindCredits) {
		return summary
	}
	if segment, ok := DetectChapterCredits(candidate.Chapters, candidate.DurationSeconds, true); ok {
		if a.patchCredits(ctx, candidate, segment, &summary) {
			summary.MovieCreditsMarkersWritten++
		}
		return summary
	}
	if !tailReady {
		return summary
	}
	tail, sampled := a.movieTail(ctx, candidate, deadline, &summary)
	if tail == nil {
		return summary
	}
	acquire := a.ffmpegAcquirer()
	var silences func(fingerprintWindow) ([]mediasample.Interval, error)
	if candidate.hasAudio() {
		silences = func(window fingerprintWindow) ([]mediasample.Interval, error) {
			release, err := acquire(ctx)
			if err != nil {
				return nil, err
			}
			defer release()
			return a.movieSampler.SampleMovieSilences(ctx, candidate, window)
		}
	}
	segment, ok, silenceErr := placeMovieCredits(candidate, classifyKeyframes(tail.Frames), silences)
	if silenceErr != nil {
		a.logger.WarnContext(ctx, "movie credits silence detection failed; keeping the video start", "file_id", candidate.FileID, "path", candidate.FilePath, "error", silenceErr)
	}
	// A freshly sampled tail is stored only once its placement is settled:
	// the scheduled run skips a movie with a complete tail, so a movie
	// canceled or failed before then must stay eligible.
	if ctx.Err() != nil {
		return summary
	}
	if ok {
		applied, err := a.writeCredits(ctx, candidate, segment)
		if err != nil {
			a.creditsPatchFailed(ctx, candidate, segment, err, &summary)
			return summary
		}
		if applied {
			summary.MovieCreditsMarkersWritten++
		}
	}
	if sampled {
		if err := a.storeCreditsTail(ctx, candidate, movieTailSpec(candidate), *tail); err != nil {
			summary.Errors = append(summary.Errors, err.Error())
		}
	}
	return summary
}

// movieTail returns the candidate's movie tail pass, from the cache or by
// sampling it, and records how it was obtained in summary. sampled reports
// a tail sampled now, which the caller stores once it has placed the
// credits. It returns no tail when the tail is unusable, backing off after
// a failure, fails now, or deadline passed while it waited for ffmpeg.
func (a *Analyzer) movieTail(ctx context.Context, candidate Candidate, deadline time.Time, summary *RunSummary) (tail *creditsTail, sampled bool) {
	// Decided from the file's current probe metadata, which a probe repair
	// can change without changing the file, so it is not stored.
	if tailUnusableBeforeSampling(candidate) != "" {
		summary.CreditsTailUnusable++
		return nil, false
	}
	spec := movieTailSpec(candidate)
	artifacts, err := a.loadCreditsArtifacts(ctx, []Candidate{candidate}, spec.key)
	if err != nil {
		summary.Errors = append(summary.Errors, err.Error())
		return nil, false
	}
	var stored *Artifact
	if artifact, ok := artifacts[candidate.FileID]; ok {
		stored = &artifact
	}
	tail, state := a.creditsTailArtifact(stored, candidate, spec)
	switch {
	case tail != nil:
		summary.CreditsTailCacheHits++
		return tail, false
	case state == ArtifactSkipped && stored.Status != ArtifactFailed:
		summary.CreditsTailUnusable++
		return nil, false
	case state == ArtifactSkipped:
		return nil, false
	}
	release, err := a.ffmpegAcquirer()(ctx)
	if err != nil {
		return nil, false
	}
	if a.pastDeadline(deadline) {
		release()
		summary.MovieBudgetExhausted = true
		return nil, false
	}
	sampledTail, sampleErr := a.movieSampler.SampleMovieTail(ctx, candidate)
	release()
	if ctx.Err() != nil {
		return nil, false
	}
	// As with episodes, a clean run without keyframes points at the log
	// format rather than the file, so it is retried later.
	if sampleErr == nil && len(sampledTail.Frames) == 0 {
		sampleErr = errNoTailFrames
	}
	usable, err := a.settleUnusableCreditsTail(ctx, candidate, spec, sampledTail, sampleErr)
	switch {
	case err != nil:
		summary.Errors = append(summary.Errors, err.Error())
		return nil, false
	case sampleErr != nil && !mediasample.Classify(sampleErr).Permanent():
		summary.CreditsTailScanErrors++
		return nil, false
	case !usable:
		summary.CreditsTailUnusable++
		return nil, false
	}
	summary.CreditsTailScansComputed++
	return &sampledTail, true
}
