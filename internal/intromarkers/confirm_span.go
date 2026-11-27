package intromarkers

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// Chromaprint confirmation of an envelope's intro.
//
// The envelope comparison is what finds an opening: a loudness trace survives
// re-encoding, it places an edge within a frame, and it cannot be fooled by the
// same studio logo the way a coarse spectral hash was. What it cannot do is
// tell two recordings apart as sharply as a fingerprint can, so a season's
// agreement is its only defense against a shared sound-alike.
//
// A Chromaprint fingerprint of the placed span answers exactly that question,
// over thirty seconds of audio instead of the whole opening. Two files whose
// spans really are one recording match point for point; files whose envelope
// merely resembled each other do not. A confirmed span is also tightened onto
// the shared audio, and a span no partner confirms loses confidence, or is
// dropped when the rest of the season confirmed each other.
const (
	// ArtifactKindIntroSpan is a Chromaprint fingerprint of a placed intro
	// span, for confirming it against the season.
	ArtifactKindIntroSpan = "intro_span_fingerprint"
	// spanFingerprintParams shape a span fingerprint; the span itself is in
	// the artifact's identity.
	spanFingerprintParams = "margin=2;format=" + ChromaprintFormat
	// spanMarginSeconds is how much audio on each side of a placed span is
	// fingerprinted, so an edge the envelope placed a little wide or narrow
	// still overlaps its partners' audio.
	spanMarginSeconds = 2.0
	// spanMatchMinimumSeconds is the shortest shared run that confirms a
	// span. A shorter one is a cue, not an opening: on unrelated audio the
	// matcher's gap bridging chains a handful of chance point matches into a
	// run of a few seconds.
	spanMatchMinimumSeconds = 6.0
	// spanConfirmationShareSeconds is the share of the placed span a
	// confirming run has to cover.
	spanConfirmationShareSeconds = 0.3
	// spanConfirmationShare is the share of the season's placed spans that
	// must confirm each other before an unconfirmed span is dropped.
	spanConfirmationShare = 0.5
	// spanUnconfirmedPenalty is taken off the confidence of a span no partner
	// confirms while the rest of the season does confirm each other below it.
	spanUnconfirmedPenalty = 0.10
)

// introSpanKey keys a file's cached span fingerprint.
func introSpanKey() mediaartifact.Key {
	return mediaartifact.Key{
		Kind:             ArtifactKindIntroSpan,
		AlgorithmVersion: AlgorithmVersion,
		ConfigHash:       mediaartifact.ConfigHash(ArtifactKindIntroSpan, spanFingerprintParams),
	}
}

// spanWindow is the audio fingerprinted for a placed span.
func spanWindow(candidate Candidate, segment Segment) fingerprintWindow {
	start := math.Max(0, segment.Start-spanMarginSeconds)
	end := segment.End + spanMarginSeconds
	if candidate.DurationSeconds > 0 {
		end = math.Min(end, candidate.DurationSeconds)
	}
	return fingerprintWindow{Start: start, End: end}
}

// spanProfile is the pair comparison for confirming a span: short runs count,
// and the chapter snap is off, because the span is what is being checked.
func spanProfile(longest float64) matchProfile {
	return matchProfile{
		MinSeconds:             spanMatchMinimumSeconds,
		MaxSeconds:             longest + 2*spanMarginSeconds + 10,
		AdjustedMinSeconds:     spanMatchMinimumSeconds,
		AdjustedMaxSeconds:     longest + 2*spanMarginSeconds + 10,
		ShortSeconds:           shortIntroSeconds,
		SeasonToleranceSeconds: seasonDurationToleranceSeconds,
		Algorithm:              LevelAlgorithm,
		ConsistentConfidence:   chromaprintConsistentConfidence,
		InconsistentConfidence: chromaprintInconsistentConfidence,
		ShortConfidence:        chromaprintShortConfidence,
	}
}

// confirmLevelSegments checks each placed intro against its partners' shared
// audio, tightens the spans that confirm, and rates the ones that do not.
func (a *Analyzer) confirmLevelSegments(ctx context.Context, candidates []Candidate, segments map[int]Segment) map[int]Segment {
	if len(segments) < 2 {
		return segments
	}
	byID := make(map[int]Candidate, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.FileID] = candidate
	}
	spans := make(map[int][]uint32, len(segments))
	for fileID, segment := range segments {
		candidate, ok := byID[fileID]
		if !ok {
			continue
		}
		if points, ok := a.spanFingerprint(ctx, candidate, segment); ok {
			spans[fileID] = points
		}
	}
	if len(spans) < 2 {
		return segments
	}
	out := decideSpanConfirmations(spans, segments, byID, func(left, right int) matchProfile {
		return spanProfile(math.Max(segments[left].End-segments[left].Start, segments[right].End-segments[right].Start))
	})
	if out == nil {
		return segments
	}
	return out
}

// decideSpanConfirmations is the season decision: which placed spans their
// partners' fingerprints confirm, where the confirming runs put their edges,
// and what happens to a span nothing confirms.
func decideSpanConfirmations(spans map[int][]uint32, segments map[int]Segment, candidates map[int]Candidate, profileFor func(left, right int) matchProfile) map[int]Segment {
	fileIDs := make([]int, 0, len(spans))
	for fileID := range spans {
		fileIDs = append(fileIDs, fileID)
	}
	sort.Ints(fileIDs)

	type confirmation struct {
		starts, ends []float64
	}
	confirmed := map[int]*confirmation{}
	for i := 0; i < len(fileIDs); i++ {
		for j := i + 1; j < len(fileIDs); j++ {
			left, right := fileIDs[i], fileIDs[j]
			// Versions of one episode share their opening audio, so two of
			// them confirming each other says nothing about the season.
			if a, b := candidates[left].EpisodeID, candidates[right].EpisodeID; a != "" && a == b {
				continue
			}
			profile := profileFor(left, right)
			leftSeg, rightSeg, ok := comparePair(spans[left], spans[right], profile)
			if !ok {
				continue
			}
			for _, side := range []struct {
				fileID  int
				segment Segment
			}{{left, leftSeg}, {right, rightSeg}} {
				candidate := candidates[side.fileID]
				adjusted := adjustSegment(side.segment, fingerprintInput{
					Candidate:   candidate,
					WindowStart: spanWindow(candidate, segments[side.fileID]).Start,
				}, profile)
				placed := segments[side.fileID]
				shortest := math.Max(spanMatchMinimumSeconds, spanConfirmationShareSeconds*(placed.End-placed.Start))
				if adjusted.End-adjusted.Start < shortest {
					continue
				}
				entry := confirmed[side.fileID]
				if entry == nil {
					entry = &confirmation{}
					confirmed[side.fileID] = entry
				}
				entry.starts = append(entry.starts, adjusted.Start)
				entry.ends = append(entry.ends, adjusted.End)
			}
		}
	}
	if len(confirmed) == 0 {
		return nil
	}
	// The share is counted over episodes, not files: two versions of one
	// episode must not satisfy it between them.
	confirmedEpisodes := map[string]struct{}{}
	for fileID := range confirmed {
		if episode := candidates[fileID].EpisodeID; episode != "" {
			confirmedEpisodes[episode] = struct{}{}
			continue
		}
		confirmedEpisodes[fmt.Sprintf("file:%d", fileID)] = struct{}{}
	}
	allEpisodes := map[string]struct{}{}
	for fileID := range segments {
		if episode := candidates[fileID].EpisodeID; episode != "" {
			allEpisodes[episode] = struct{}{}
			continue
		}
		allEpisodes[fmt.Sprintf("file:%d", fileID)] = struct{}{}
	}
	dropUnconfirmed := float64(len(confirmedEpisodes)) >= spanConfirmationShare*float64(len(allEpisodes))
	out := make(map[int]Segment, len(segments))
	for fileID, segment := range segments {
		entry, ok := confirmed[fileID]
		if !ok || len(entry.starts) == 0 {
			// A span with no fingerprint behind it was never measured, so it
			// is not a non-match: only a span whose fingerprint was compared
			// and did not confirm can be dropped.
			if _, measured := spans[fileID]; !measured {
				out[fileID] = segment
				continue
			}
			if dropUnconfirmed {
				continue
			}
			segment.Confidence = math.Max(chromaprintShortConfidence, segment.Confidence-spanUnconfirmedPenalty)
			out[fileID] = segment
			continue
		}
		// The confirming runs tightened onto the shared audio: the envelope
		// placed the span, the fingerprints say exactly where it is.
		start, end := medianSeconds(entry.starts), medianSeconds(entry.ends)
		if start < segment.Start {
			start = segment.Start
		}
		if end > segment.End {
			end = segment.End
		}
		if end <= start {
			out[fileID] = segment
			continue
		}
		segment.Start, segment.End = start, end
		segment.Algorithm = SpanConfirmedAlgorithm
		out[fileID] = segment
	}
	return out
}

// spanFingerprint returns the cached fingerprint of a placed span, extracting
// it when missing. A file that cannot be fingerprinted confirms nothing and is
// left to the envelope's rating.
func (a *Analyzer) spanFingerprint(ctx context.Context, candidate Candidate, segment Segment) ([]uint32, bool) {
	window := spanWindow(candidate, segment)
	if window.empty() {
		return nil, false
	}
	identity := window.identity(candidate)
	key := introSpanKey()
	var stored mediaartifact.Artifact
	if artifacts, err := a.loadCreditsArtifacts(ctx, []Candidate{candidate}, key); err == nil {
		stored = artifacts[candidate.FileID]
	}
	switch stored.State(identity, a.nodeName(), time.Now()) {
	case mediaartifact.Ready:
		if stored.PayloadFormat == ChromaprintFormat {
			if points := mediasample.DecodeRawFingerprint(stored.Payload); len(points) > 0 {
				return points, true
			}
		}
	case mediaartifact.Skipped:
		return nil, false
	}

	acquire := a.ffmpegAcquirer()
	release, err := acquire(ctx)
	if err != nil {
		return nil, false
	}
	result, err := runAnalysis(ctx, a.config, analysisRunner(a.config), mediasample.Request{
		Input:      candidate.FilePath,
		Window:     &mediasample.Window{StartSeconds: window.Start, DurationSeconds: window.duration()},
		Audio:      &mediasample.AudioOutput{Fingerprint: true},
		Threads:    1,
		Background: backgroundAnalysis(ctx),
	})
	release()
	if err != nil {
		a.logger.WarnContext(ctx, "intro span fingerprint failed", "file_id", candidate.FileID, "error", err)
		return nil, false
	}
	if len(result.Fingerprint) == 0 {
		return nil, false
	}
	_ = a.repo.UpsertArtifact(ctx, mediaartifact.Artifact{
		MediaFileID:           candidate.FileID,
		Key:                   key,
		Identity:              identity,
		Status:                mediaartifact.StatusComplete,
		PayloadFormat:         ChromaprintFormat,
		SampleDurationSeconds: window.duration(),
		ItemCount:             len(result.Fingerprint),
		Payload:               mediasample.EncodeRawFingerprint(result.Fingerprint),
		MarkersInvalidatedAt:  candidate.MarkersInvalidatedAt,
	})
	return result.Fingerprint, true
}
