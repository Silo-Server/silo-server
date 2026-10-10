package intromarkers

import (
	"context"
	"fmt"
	"math"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// Movie credits are best effort. Movies have no season to compare audio
// with and rarely share music with anything, so they get credits from
// chapters and video alone, and never intros. In validation video found
// credits in 30 of 42 movies, starting late more often than early; every
// early start checked against frames was benign.
const (
	// movieCreditsMinimumDurationSeconds is the shortest file analyzed as a
	// movie. Shorter ones are mostly shorts and specials, whose tail window
	// would reach into the story.
	movieCreditsMinimumDurationSeconds = 1200
	// movieTailSampleSeconds is how far apart the movie tail pass samples
	// keyframes. Thinning episode keyframes to one per three or four
	// seconds left their accuracy unchanged, and a movie's tail is twice an
	// episode's, often in 4K, so a dense pass would cost several times more.
	movieTailSampleSeconds = 3.0
	// movieSilenceLeadSeconds and movieSilenceTrailSeconds bound the audio
	// read around the start video placed, for a silence between the story
	// and the credits: the sample spacing and the black lead leave that gap
	// a few seconds long.
	movieSilenceLeadSeconds  = 10.0
	movieSilenceTrailSeconds = 2.0
	// movieSilenceEdgeSeconds is how close to the start of the audio read a
	// silence may start and still be known to start there rather than
	// before it.
	movieSilenceEdgeSeconds = 0.05
)

// movieTailWindow is the end of a movie the movie tail pass samples.
func movieTailWindow(candidate Candidate) fingerprintWindow {
	duration := candidate.DurationSeconds
	if duration <= 0 {
		return fingerprintWindow{}
	}
	return fingerprintWindow{Start: creditsLimitsFor(true).windowStart(duration), End: duration}
}

// movieCreditsTailParams are the parameters that shape a movie tail payload.
// They differ from the episode tail's, so a movie tail never shares a key
// with an episode tail. Changing them discards every cached movie tail.
var movieCreditsTailParams = fmt.Sprintf("movie;tail=%.0f:%.2f;every=%.1f;crop=%.2fx%.2f;width=%d;black=%s;format=%s",
	movieCreditsTailSeconds, movieCreditsTailFraction, movieTailSampleSeconds, tailCropWidth, tailCropHeight, tailWidth,
	joinInts(creditsBlackThresholds), creditsTailFormat)

// movieCreditsTailKey keys a movie's cached tail pass.
func movieCreditsTailKey() mediaartifact.Key {
	return mediaartifact.Key{
		Kind:             ArtifactKindCreditsTail,
		AlgorithmVersion: AlgorithmVersion,
		ConfigHash:       mediaartifact.ConfigHash(ArtifactKindCreditsTail, movieCreditsTailParams),
	}
}

// movieTailSpec locates a movie's tail pass.
func movieTailSpec(candidate Candidate) creditsTailSpec {
	return creditsTailSpec{key: movieCreditsTailKey(), window: movieTailWindow(candidate)}
}

// movieTailSamples are the times the movie tail pass samples: every
// movieTailSampleSeconds from the start of the window.
func movieTailSamples(window fingerprintWindow) []float64 {
	var seconds []float64
	for i := 0; ; i++ {
		at := window.Start + float64(i)*movieTailSampleSeconds
		if at >= window.End {
			return seconds
		}
		seconds = append(seconds, at)
	}
}

// movieTailRequest samples the keyframe at or before every sample time of a
// movie's tail window, with the same statistics as the episode tail pass.
// It reads no audio: movies place no credits from audio.
func movieTailRequest(ctx context.Context, candidate Candidate, window fingerprintWindow) mediasample.Request {
	return mediasample.Request{
		Input:   candidate.FilePath,
		Samples: &mediasample.Samples{Seconds: movieTailSamples(window)},
		Stats: &mediasample.StatsOutput{
			CropWidth:       tailCropWidth,
			CropHeight:      tailCropHeight,
			Width:           tailWidth,
			BlackThresholds: creditsBlackThresholds,
		},
		Threads:       1,
		VideoBitDepth: mediasample.VideoBitDepthHint(candidate.VideoBitDepth),
		Background:    backgroundAnalysis(ctx),
	}
}

// movieSilenceRequest reads the silences in window of a movie's audio.
func movieSilenceRequest(ctx context.Context, candidate Candidate, window fingerprintWindow) mediasample.Request {
	return mediasample.Request{
		Input:      candidate.FilePath,
		Window:     &mediasample.Window{StartSeconds: window.Start, DurationSeconds: window.duration()},
		Audio:      &mediasample.AudioOutput{Silence: &mediasample.SilenceParams{NoiseDB: tailSilenceNoiseDB, MinSeconds: tailSilenceSeconds}},
		Threads:    1,
		Background: backgroundAnalysis(ctx),
	}
}

// movieTailSampler runs movie tail passes. ChromaprintExtractor implements
// it.
type movieTailSampler interface {
	// PreflightMovieTail reports what the ffmpeg binary lacks for a movie
	// tail pass.
	PreflightMovieTail(ctx context.Context) error
	// SampleMovieTail runs the movie tail pass over a candidate with video.
	SampleMovieTail(ctx context.Context, candidate Candidate) (creditsTail, error)
	// SampleMovieSilences returns the silences in window of the
	// candidate's audio.
	SampleMovieSilences(ctx context.Context, candidate Candidate, window fingerprintWindow) ([]mediasample.Interval, error)
}

// PreflightMovieTail reports what ffmpeg lacks for a movie tail pass.
func (e *ChromaprintExtractor) PreflightMovieTail(ctx context.Context) error {
	caps, err := mediasample.LoadCapabilities(ctx, e.config.FFmpegPath)
	if err != nil {
		return err
	}
	return caps.Require(movieTailRequest(ctx, tailPreflightCandidate, fingerprintWindow{End: 1}))
}

// SampleMovieTail runs the movie tail pass over the candidate's tail window.
func (e *ChromaprintExtractor) SampleMovieTail(ctx context.Context, candidate Candidate) (creditsTail, error) {
	window := movieTailWindow(candidate)
	if window.empty() {
		return creditsTail{}, fmt.Errorf("file %d has no tail window", candidate.FileID)
	}
	req := movieTailRequest(ctx, candidate, window)
	result, err := e.tailRunner(ctx, &req).Run(ctx, req)
	if err != nil {
		return creditsTail{}, fmt.Errorf("sampling the movie tail of file %d: %w", candidate.FileID, err)
	}
	e.logTailDecoder(ctx, candidate, req, result)
	return creditsTail{Frames: result.Frames}, nil
}

// SampleMovieSilences returns the silences in window of the candidate's
// audio.
func (e *ChromaprintExtractor) SampleMovieSilences(ctx context.Context, candidate Candidate, window fingerprintWindow) ([]mediasample.Interval, error) {
	result, err := analysisRunner(e.config).Run(ctx, movieSilenceRequest(ctx, candidate, window))
	if err != nil {
		return nil, fmt.Errorf("detecting movie credits silence for file %d: %w", candidate.FileID, err)
	}
	return result.Silences, nil
}

// placeMovieCredits places a movie's credits from its tail keyframes: the
// video-only rules with the movie limits. The movie tail pass reads no
// audio, so the silence that may move the start back to the end of the
// story is looked for only around the start video placed, in a window
// silences returns the silences of. A nil silences, or one that fails,
// leaves the start where video placed it; the failure is returned with the
// result.
func placeMovieCredits(candidate Candidate, keyframes []creditsKeyframe, silences func(fingerprintWindow) ([]mediasample.Interval, error)) (Segment, bool, error) {
	limits := creditsLimitsFor(true)
	evidence := creditsEvidence{Keyframes: keyframes}
	windowStart := limits.windowStart(candidate.DurationSeconds)
	draft, ok := placeVideoCredits(buildRuns(keyframes), keyframes, nil, windowStart, candidate.DurationSeconds, limits)
	if !ok || silences == nil {
		segment, ok := combineCredits(candidate, evidence, limits)
		return segment, ok, nil
	}
	window := movieSilenceWindow(draft.Start, windowStart, candidate.DurationSeconds)
	found, err := silences(window)
	if err == nil {
		evidence.Silences = silencesKnownToStartInWindow(found, window, windowStart)
	}
	segment, ok := combineCredits(candidate, evidence, limits)
	return segment, ok, err
}

// movieSilenceWindow is the audio read for a silence before a movie's
// credits placed at start, inside the tail window.
func movieSilenceWindow(start, windowStart, duration float64) fingerprintWindow {
	return fingerprintWindow{
		Start: math.Max(windowStart, start-movieSilenceLeadSeconds),
		End:   math.Min(duration, start+movieSilenceTrailSeconds),
	}
}

// silencesKnownToStartInWindow drops a silence that starts at the start of
// the audio read, which may have begun before it and so reach back into the
// story. At the start of the tail window it is kept, as the episode tail
// pass keeps it.
func silencesKnownToStartInWindow(silences []mediasample.Interval, window fingerprintWindow, windowStart float64) []mediasample.Interval {
	if window.Start <= windowStart {
		return silences
	}
	kept := make([]mediasample.Interval, 0, len(silences))
	for _, silence := range silences {
		if silence.Start >= window.Start+movieSilenceEdgeSeconds {
			kept = append(kept, silence)
		}
	}
	return kept
}
