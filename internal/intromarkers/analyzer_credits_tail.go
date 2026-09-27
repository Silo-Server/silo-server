package intromarkers

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// creditsInputOptions selects the tail evidence a credits group needs.
type creditsInputOptions struct {
	// fingerprints asks for every file's tail fingerprint, which only a
	// group of two or more episodes can compare.
	fingerprints bool
	// tails asks for the tail pass of the files in tailFileIDs.
	tails       bool
	tailFileIDs map[int]struct{}
}

// tailCounts tallies how a group's tail passes were obtained.
type tailCounts struct {
	hits     int
	computed int
	// failed counts tail passes that failed in this analysis.
	failed int
	// deferred counts files skipped while an earlier failure backs off.
	deferred int
	// unusable counts files whose tail cannot be classified.
	unusable int
}

// creditsInputs is the tail evidence of a credits group.
type creditsInputs struct {
	fingerprints      []fingerprintInput
	tails             map[int]*creditsTail
	fingerprintCounts fingerprintCounts
	tailCounts        tailCounts
}

// ensureCreditsInputs loads the cached credits fingerprints and tail passes
// of candidates and computes missing ones. A file that needs both gets them
// from one ffmpeg run. A database error or cancellation is returned as err.
func (a *Analyzer) ensureCreditsInputs(ctx context.Context, candidates []Candidate, opts creditsInputOptions) (creditsInputs, error) {
	inputs := creditsInputs{tails: map[int]*creditsTail{}}
	tailSampler := a.tailSampler
	if tailSampler == nil {
		opts.tails = false
	}
	var fingerprints, tails map[int]Artifact
	if opts.fingerprints {
		var err error
		if fingerprints, err = a.loadCreditsArtifacts(ctx, candidates, creditsFingerprintKey()); err != nil {
			return inputs, err
		}
	}
	if opts.tails {
		var err error
		if tails, err = a.loadCreditsArtifacts(ctx, candidates, creditsTailKey()); err != nil {
			return inputs, err
		}
	}

	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	locked := func(fn func()) {
		mu.Lock()
		defer mu.Unlock()
		fn()
	}
	setErr := func(err error) {
		locked(func() {
			if firstErr == nil {
				firstErr = err
			}
		})
	}
	addFingerprint := func(candidate Candidate, fp *Fingerprint, computed bool) {
		locked(func() {
			inputs.fingerprints = append(inputs.fingerprints, fingerprintInput{Candidate: candidate, Points: fp.Points, WindowStart: fp.WindowStartSeconds})
			if computed {
				inputs.fingerprintCounts.computed++
			} else {
				inputs.fingerprintCounts.hits++
			}
		})
	}
	addTail := func(candidate Candidate, tail *creditsTail, computed bool) {
		locked(func() {
			inputs.tails[candidate.FileID] = tail
			if computed {
				inputs.tailCounts.computed++
			} else {
				inputs.tailCounts.hits++
			}
		})
	}
	count := func(field *int) { locked(func() { *field++ }) }
	acquire := a.ffmpegAcquirer()

	for _, candidate := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ctx.Err(); err != nil {
				setErr(err)
				return
			}
			needFingerprint := false
			if opts.fingerprints {
				artifact, stored := fingerprints[candidate.FileID]
				var fp *Fingerprint
				lookup := fingerprintMissing
				if stored {
					fp, lookup = a.creditsFingerprint(&artifact, candidate)
				}
				switch lookup {
				case fingerprintCached:
					addFingerprint(candidate, fp, false)
				case fingerprintDeferred:
					count(&inputs.fingerprintCounts.deferred)
				case fingerprintMissing:
					needFingerprint = true
				}
			}
			needTail := false
			_, wanted := opts.tailFileIDs[candidate.FileID]
			switch {
			case !opts.tails || !wanted:
			case tailUnusableBeforeSampling(candidate) != "":
				// Decided from the file's current probe metadata, which a
				// probe repair can change without changing the file, so it
				// is not stored.
				count(&inputs.tailCounts.unusable)
			default:
				artifact, stored := tails[candidate.FileID]
				var storedArtifact *Artifact
				if stored {
					storedArtifact = &artifact
				}
				tail, state := a.creditsTailArtifact(storedArtifact, candidate, episodeTailSpec(candidate))
				switch {
				case tail != nil:
					addTail(candidate, tail, false)
				case state == ArtifactSkipped && artifact.Status == ArtifactFailed:
					count(&inputs.tailCounts.deferred)
				case state == ArtifactSkipped:
					count(&inputs.tailCounts.unusable)
				default:
					needTail = true
				}
			}
			if !needFingerprint && !needTail {
				return
			}

			release, err := acquire(ctx)
			if err != nil {
				setErr(err)
				return
			}
			// A file without audio gets its tail pass alone; its fingerprint
			// goes the audio-only way, and a run that finds no audio stream
			// is stored as having none. A tail pass that finds no stream
			// has already ruled out missing audio alone (see
			// SampleCreditsTail), so its video is missing and the audio-only
			// run decides the fingerprint.
			tailFingerprint := needFingerprint && needTail && candidate.hasAudio()
			var sample creditsTailSample
			var sampleErr error
			if needTail {
				sample, sampleErr = tailSampler.SampleCreditsTail(ctx, candidate, tailFingerprint)
			}
			var fp Fingerprint
			var fpOK bool
			var fpErr error
			switch {
			case needFingerprint && !tailFingerprint,
				tailFingerprint && mediasample.Classify(sampleErr) == mediasample.ReasonNoStream:
				fp, fpOK, fpErr = a.extractor.ExtractCredits(ctx, candidate)
			case tailFingerprint && sampleErr != nil:
				fpErr = sampleErr
			case tailFingerprint && sample.Fingerprint != nil:
				fp = *sample.Fingerprint
				fpOK = len(fp.Points) > 0
			}
			release()
			if ctx.Err() != nil && (sampleErr != nil || fpErr != nil) {
				setErr(ctx.Err())
				return
			}

			if needTail {
				// A clean run that parsed no keyframes from a file with video
				// points at the log format, not the file: retry it later
				// rather than storing the tail as permanently sparse.
				tailErr := sampleErr
				if tailErr == nil && len(sample.Tail.Frames) == 0 {
					tailErr = errNoTailFrames
				}
				tail, err := a.settleCreditsTail(ctx, candidate, episodeTailSpec(candidate), sample.Tail, tailErr)
				if err != nil {
					setErr(err)
					return
				}
				switch {
				case tailErr != nil && !mediasample.Classify(tailErr).Permanent():
					count(&inputs.tailCounts.failed)
				case tail != nil:
					addTail(candidate, tail, true)
				default:
					count(&inputs.tailCounts.unusable)
				}
			}
			if needFingerprint {
				switch {
				case fpErr != nil && mediasample.Classify(fpErr) == mediasample.ReasonNoStream:
					if err := a.storeCreditsNoAudio(ctx, candidate); err != nil {
						setErr(err)
					}
				case fpErr != nil:
					a.logger.WarnContext(ctx, "marker fingerprint extraction failed", "kind", kindCredits.String(), "file_id", candidate.FileID, "path", candidate.FilePath, "error", fpErr)
					if recordErr := a.recordCreditsFingerprintFailure(ctx, candidate, fpErr); recordErr != nil {
						a.logger.WarnContext(ctx, "marker fingerprint failure record failed", "kind", kindCredits.String(), "file_id", candidate.FileID, "error", recordErr)
					}
					count(&inputs.fingerprintCounts.failed)
				case !fpOK:
					if err := a.storeCreditsNoAudio(ctx, candidate); err != nil {
						setErr(err)
					}
				default:
					if err := a.storeCreditsFingerprint(ctx, fp); err != nil {
						setErr(err)
						return
					}
					addFingerprint(candidate, &fp, true)
				}
			}
		}()
	}
	wg.Wait()
	sort.Slice(inputs.fingerprints, func(i, j int) bool {
		return inputs.fingerprints[i].Candidate.FileID < inputs.fingerprints[j].Candidate.FileID
	})
	return inputs, firstErr
}

// creditsTailSpec is where a file's tail pass is kept: its artifact key and
// the window it covers. Episodes and movies sample different windows in
// different ways, so their tails never share a key.
type creditsTailSpec struct {
	key    ArtifactKey
	window fingerprintWindow
}

// episodeTailSpec locates an episode's tail pass.
func episodeTailSpec(candidate Candidate) creditsTailSpec {
	return creditsTailSpec{key: creditsTailKey(), window: tailWindow(candidate)}
}

// creditsTailArtifact interprets a candidate's stored tail artifact: the
// decoded tail when it is ready, and the artifact's state.
func (a *Analyzer) creditsTailArtifact(artifact *Artifact, candidate Candidate, spec creditsTailSpec) (*creditsTail, ArtifactState) {
	window := spec.window
	state := artifact.State(window.identity(candidate), a.nodeName(), time.Now())
	if state == ArtifactSkipped && artifact.Status == ArtifactUnusable && metadataTailDetail(artifact.Detail) {
		// Stored by an earlier build from probe metadata the candidate
		// no longer has.
		return nil, ArtifactMissing
	}
	if state != ArtifactReady {
		return nil, state
	}
	if artifact.PayloadFormat != creditsTailFormat {
		return nil, ArtifactMissing
	}
	tail, err := decodeCreditsTail(artifact.Payload, window.Start)
	if err != nil {
		a.logger.Warn("stored credits tail is unreadable", "file_id", candidate.FileID, "error", err)
		return nil, ArtifactMissing
	}
	return &tail, ArtifactReady
}

// errNoTailFrames fails a tail pass that exited cleanly without any parsed
// keyframe statistics.
var errNoTailFrames = errors.New("credits tail pass parsed no keyframes")

// settleCreditsTail stores the outcome of a tail pass and returns the tail
// when it can be classified.
func (a *Analyzer) settleCreditsTail(ctx context.Context, candidate Candidate, spec creditsTailSpec, tail creditsTail, sampleErr error) (*creditsTail, error) {
	usable, err := a.settleUnusableCreditsTail(ctx, candidate, spec, tail, sampleErr)
	if err != nil || !usable {
		return nil, err
	}
	if err := a.storeCreditsTail(ctx, candidate, spec, tail); err != nil {
		return nil, err
	}
	return &tail, nil
}

// settleUnusableCreditsTail stores the outcome of a tail pass that cannot be
// classified and reports whether the tail can be. A permanent failure, or a
// tail with too many or too few keyframes, is stored as unusable; any other
// failure is recorded so this server backs off. A usable tail is left for
// the caller to store.
func (a *Analyzer) settleUnusableCreditsTail(ctx context.Context, candidate Candidate, spec creditsTailSpec, tail creditsTail, sampleErr error) (bool, error) {
	if sampleErr != nil {
		reason := mediasample.Classify(sampleErr)
		a.logger.WarnContext(ctx, "credits tail pass failed", "file_id", candidate.FileID, "path", candidate.FilePath, "reason", reason, "error", sampleErr)
		if reason.Permanent() {
			return false, a.storeCreditsTailUnusable(ctx, candidate, spec, string(reason))
		}
		if err := a.repo.RecordArtifactFailure(ctx, ArtifactFailure{
			MediaFileID:      candidate.FileID,
			ArtifactKey:      spec.key,
			ArtifactIdentity: spec.window.identity(candidate),
			RecordedBy:       a.nodeName(),
			Error:            sampleErr.Error(),
			At:               time.Now().UTC(),
		}); err != nil {
			a.logger.WarnContext(ctx, "credits tail failure record failed", "file_id", candidate.FileID, "error", err)
		}
		return false, nil
	}
	if detail := tailUnusableAfterSampling(len(tail.Frames), spec.window); detail != "" {
		return false, a.storeCreditsTailUnusable(ctx, candidate, spec, detail)
	}
	return true, nil
}

// storeCreditsTail stores a usable tail pass as the candidate's complete
// tail artifact.
func (a *Analyzer) storeCreditsTail(ctx context.Context, candidate Candidate, spec creditsTailSpec, tail creditsTail) error {
	window := spec.window
	payload, err := encodeCreditsTail(tail, window.Start, len(creditsBlackThresholds))
	if err != nil {
		return err
	}
	return a.repo.UpsertArtifact(ctx, Artifact{
		MediaFileID:           candidate.FileID,
		ArtifactKey:           spec.key,
		ArtifactIdentity:      window.identity(candidate),
		Status:                ArtifactComplete,
		PayloadFormat:         creditsTailFormat,
		SampleDurationSeconds: window.duration(),
		ItemCount:             len(tail.Frames),
		Payload:               payload,
	})
}

// storeCreditsTailUnusable records why the candidate's decoded tail cannot
// be classified, so it is not decoded again until the file changes.
func (a *Analyzer) storeCreditsTailUnusable(ctx context.Context, candidate Candidate, spec creditsTailSpec, detail string) error {
	return a.repo.UpsertArtifact(ctx, Artifact{
		MediaFileID:      candidate.FileID,
		ArtifactKey:      spec.key,
		ArtifactIdentity: spec.window.identity(candidate),
		Status:           ArtifactUnusable,
		Detail:           detail,
	})
}
