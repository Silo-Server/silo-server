package intromarkers

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// Intro audio comparison reads each file's opening as a loudness envelope
// instead of a Chromaprint fingerprint.
//
// A Chromaprint point summarizes about 2.4 seconds of audio, so a match's
// edges are only as sharp as that window and have to be corrected by fixed
// leads, and two files of the same studio logo match as readily as two files
// of the same theme. The speech band's loudness (mediasample's Speech output,
// 200 to 3400 Hz, one level per 10 ms) keeps a recognizable shape for the same
// recording under lossy re-encoding, and its resolution is fine enough to
// place an edge within a few frames. Credits keep their Chromaprint tail pass,
// which shares one decode with the video keyframes.
const (
	// ArtifactKindIntroLevels is the loudness envelope of an episode's
	// opening window.
	ArtifactKindIntroLevels = "intro_levels"

	// LevelsFormat is the payload of an intro levels artifact: one byte per
	// mediasample.SpeechFrameSeconds, holding the level in whole dB above
	// -100 dBFS, as mediasample.SpeechLevels.Levels defines it.
	LevelsFormat = "levels:db8:10ms"

	// LevelsHopSeconds is the resolution of an envelope. It matches
	// mediasample.SpeechFrameSeconds.
	LevelsHopSeconds = mediasample.SpeechFrameSeconds

	// levelsFloorDB is the level below which a frame counts as silent. The
	// speech writer reports levels above -100 dBFS, so 0 dB is digital
	// silence.
	levelsFloorDB = 0.0
)

// levelsParams are the parameters that shape an envelope. Changing them
// discards every cached envelope.
const levelsParams = "band=200-3400;rate=8000;frame=10ms;window=head"

// introLevelsKey keys an episode's cached opening envelope.
func introLevelsKey() mediaartifact.Key {
	return mediaartifact.Key{
		Kind:             ArtifactKindIntroLevels,
		AlgorithmVersion: AlgorithmVersion,
		ConfigHash:       mediaartifact.ConfigHash(ArtifactKindIntroLevels, levelsParams),
	}
}

// levelsDetailNoAudio marks a window with no audio to measure.
const levelsDetailNoAudio = "no_audio"

// levelsInput is one file's envelope, ready to compare with its season.
type levelsInput struct {
	Candidate Candidate
	// Levels holds one dB level per LevelsHopSeconds from WindowStart.
	Levels []byte
	// WindowStart is where the envelope begins in the file, in seconds.
	WindowStart float64
}

// levelsWindow is the opening of a file intro detection measures. It is the
// same window the Chromaprint path uses, so the two never disagree about how
// much of the file is read.
func levelsWindow(candidate Candidate, cfg Config) fingerprintWindow {
	return headWindow(candidate, cfg)
}

// introLevelsRequest is the sampling request for a candidate's opening
// envelope.
func introLevelsRequest(ctx context.Context, candidate Candidate, window fingerprintWindow) mediasample.Request {
	return mediasample.Request{
		Input:  candidate.FilePath,
		Window: &mediasample.Window{StartSeconds: window.Start, DurationSeconds: window.duration()},
		// The speech output band-limits the audio itself, so the request
		// takes no other output and no other run shares it.
		Audio:      &mediasample.AudioOutput{Speech: &mediasample.SpeechParams{}},
		Threads:    1,
		Background: backgroundAnalysis(ctx),
	}
}

// PreflightLevels reports what ffmpeg lacks for an envelope run.
func (e *ChromaprintExtractor) PreflightLevels(ctx context.Context) error {
	caps, err := mediasample.LoadCapabilities(ctx, e.config.FFmpegPath)
	if err != nil {
		return err
	}
	return caps.Require(introLevelsRequest(ctx, Candidate{FilePath: "preflight"}, fingerprintWindow{End: 1}))
}

// ExtractLevels measures the candidate's opening loudness envelope for intro
// detection.
func (e *ChromaprintExtractor) ExtractLevels(ctx context.Context, candidate Candidate) (levelsInput, bool, error) {
	window := levelsWindow(candidate, e.config)
	if window.empty() {
		return levelsInput{}, false, nil
	}
	result, err := runAnalysis(ctx, e.config, analysisRunner(e.config), introLevelsRequest(ctx, candidate, window))
	if err != nil {
		return levelsInput{}, false, fmt.Errorf("measuring the loudness envelope of file %d: %w", candidate.FileID, err)
	}
	if result.Speech == nil || len(result.Speech.Levels) == 0 {
		return levelsInput{}, false, nil
	}
	return levelsInput{Candidate: candidate, Levels: result.Speech.Levels, WindowStart: window.Start}, true, nil
}

// levelsCounts tallies how a group's envelopes were obtained.
type levelsCounts struct {
	hits     int
	computed int
	failed   int
	deferred int
	unusable int
}

// ensureIntroLevels loads the cached envelopes of candidates and measures the
// missing ones. A file whose window has no audio is stored unusable so it is
// not decoded again until it changes. A database error or cancellation is
// returned as err.
func (a *Analyzer) ensureIntroLevels(ctx context.Context, candidates []Candidate) ([]levelsInput, levelsCounts, error) {
	var (
		mu       sync.Mutex
		inputs   []levelsInput
		counts   levelsCounts
		firstErr error
	)
	setErr := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	add := func(field *int, input *levelsInput) {
		mu.Lock()
		*field++
		if input != nil {
			inputs = append(inputs, *input)
		}
		mu.Unlock()
	}
	stored, err := a.loadCreditsArtifacts(ctx, candidates, introLevelsKey())
	if err != nil {
		return nil, counts, err
	}

	acquire := a.ffmpegAcquirer()
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ctx.Err(); err != nil {
				setErr(err)
				return
			}
			if artifact, ok := stored[candidate.FileID]; ok {
				switch artifact.State(levelsWindow(candidate, a.config).identity(candidate), a.nodeName(), time.Now()) {
				case mediaartifact.Ready:
					if artifact.PayloadFormat == LevelsFormat && len(artifact.Payload) > 0 {
						add(&counts.hits, &levelsInput{Candidate: candidate, Levels: artifact.Payload, WindowStart: artifact.WindowStartSeconds})
						return
					}
				case mediaartifact.Skipped:
					if artifact.Status == mediaartifact.StatusFailed {
						add(&counts.deferred, nil)
					} else {
						add(&counts.unusable, nil)
					}
					return
				}
			}

			release, err := acquire(ctx)
			if err != nil {
				setErr(err)
				return
			}
			input, ok, err := a.extractor.ExtractLevels(ctx, candidate)
			release()
			if err != nil {
				if ctx.Err() != nil {
					setErr(ctx.Err())
					return
				}
				a.logger.WarnContext(ctx, "marker loudness envelope extraction failed", "file_id", candidate.FileID, "path", candidate.FilePath, "error", err)
				add(&counts.failed, nil)
				if recordErr := a.recordLevelsFailure(ctx, candidate, err); recordErr != nil {
					a.logger.WarnContext(ctx, "marker loudness envelope failure record failed", "file_id", candidate.FileID, "error", recordErr)
				}
				return
			}
			if !ok {
				if err := a.storeLevelsNoAudio(ctx, candidate); err != nil {
					setErr(err)
					return
				}
				add(&counts.unusable, nil)
				return
			}
			if err := a.storeIntroLevels(ctx, input); err != nil {
				setErr(err)
				return
			}
			add(&counts.computed, &input)
		}()
	}
	wg.Wait()
	sort.Slice(inputs, func(i, j int) bool {
		return inputs[i].Candidate.FileID < inputs[j].Candidate.FileID
	})
	return inputs, counts, firstErr
}

// storeIntroLevels caches a measured envelope.
func (a *Analyzer) storeIntroLevels(ctx context.Context, input levelsInput) error {
	window := levelsWindow(input.Candidate, a.config)
	return a.repo.UpsertArtifact(ctx, mediaartifact.Artifact{
		MediaFileID:           input.Candidate.FileID,
		Key:                   introLevelsKey(),
		Identity:              window.identity(input.Candidate),
		Status:                mediaartifact.StatusComplete,
		PayloadFormat:         LevelsFormat,
		SampleDurationSeconds: float64(len(input.Levels)) * LevelsHopSeconds,
		ItemCount:             len(input.Levels),
		Payload:               input.Levels,
		MarkersInvalidatedAt:  input.Candidate.MarkersInvalidatedAt,
	})
}

// storeLevelsNoAudio records that the candidate's opening has no audio to
// measure.
func (a *Analyzer) storeLevelsNoAudio(ctx context.Context, candidate Candidate) error {
	return a.repo.UpsertArtifact(ctx, mediaartifact.Artifact{
		MediaFileID: candidate.FileID,
		Key:         introLevelsKey(),
		Identity:    levelsWindow(candidate, a.config).identity(candidate),
		Status:      mediaartifact.StatusUnusable,
		Detail:      levelsDetailNoAudio,
	})
}

// recordLevelsFailure records a failed envelope measurement so this server
// backs off before decoding the file again.
func (a *Analyzer) recordLevelsFailure(ctx context.Context, candidate Candidate, failure error) error {
	return a.repo.RecordArtifactFailure(ctx, mediaartifact.Failure{
		MediaFileID: candidate.FileID,
		Key:         introLevelsKey(),
		Identity:    levelsWindow(candidate, a.config).identity(candidate),
		RecordedBy:  a.nodeName(),
		Error:       failure.Error(),
		At:          time.Now().UTC(),
	})
}

// distinctLevelEpisodeCount is how many episodes a set of envelopes covers.
func distinctLevelEpisodeCount(inputs []levelsInput) int {
	seen := map[string]struct{}{}
	for _, input := range inputs {
		seen[input.Candidate.EpisodeID] = struct{}{}
	}
	return len(seen)
}

// envelopeOf turns a levels input into its comparable form: dB levels, gain
// normalized over the window.
func envelopeOf(input levelsInput) envelope {
	values := make([]float64, len(input.Levels))
	for i, level := range input.Levels {
		v := float64(level)
		if v < levelsFloorDB {
			v = levelsFloorDB
		}
		values[i] = v
	}
	return newEnvelope(values, LevelsHopSeconds).normalized()
}
