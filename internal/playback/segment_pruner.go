package playback

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	segmentPruneHysteresis = 5
	segmentPruneBatchSize  = 512
	segmentPruneRetryDelay = 5 * time.Second
	copyManifestTailBytes  = 64 << 10
)

type segmentPruneCandidate struct {
	number int
	path   string
}

// copyManifestDurationIndex retains the duration entries already observed for
// one FFmpeg generation. Copy-mode manifests can grow to 50,000 entries, so a
// prune pass must not reread and walk the complete playlist every five segment
// downloads. The map keeps floor lookup proportional to the retention window,
// while incremental loads parse only a bounded tail in the common case.
type copyManifestDurationIndex struct {
	generation   uint64
	loaded       bool
	durations    map[int]float64
	firstSegment int
	lastSegment  int
}

// scheduleSegmentPruneLocked starts one asynchronous prune pass after the
// client has advanced far enough to make useful work. The caller must hold
// s.mu. Segment retention is measured in media time so custom HLS segment
// durations retain the same back-seek window.
func (s *TranscodeSession) scheduleSegmentPruneLocked() {
	retentionSeconds := s.opts.SegmentRetentionSeconds
	if retentionSeconds <= 0 || s.segmentPruneRunning || s.restarting != nil {
		return
	}

	if strings.EqualFold(s.opts.TargetCodecVideo, "copy") {
		// Copy-mode fragments follow source keyframes, so their real media-time
		// floor is resolved asynchronously from EXTINF durations below. Use the
		// download high-water mark only to avoid reparsing the manifest for every
		// segment response.
		if s.lastCompletedSegment-s.lastPruneHighWater < segmentPruneHysteresis {
			return
		}
	} else {
		segmentDuration := s.opts.SegmentDuration
		if segmentDuration <= 0 {
			segmentDuration = defaultSegmentDuration
		}
		retainedSegments := (retentionSeconds + segmentDuration - 1) / segmentDuration
		floor := s.lastCompletedSegment - retainedSegments
		if (!s.pruneBeforeStart && floor <= s.opts.StartSegmentNumber) || floor-s.lastPruneFloor < segmentPruneHysteresis {
			return
		}
	}

	s.segmentPruneRunning = true
	generation := s.segmentGeneration
	downloadedThrough := s.lastCompletedSegment
	s.lastPruneHighWater = downloadedThrough
	go s.pruneDownloadedSegments(generation, downloadedThrough, false)
}

// pruneDownloadedSegments removes completed media files strictly behind floor.
// It visits each newly expired segment number at most once in bounded batches,
// avoiding repeated full-directory scans when FFmpeg has generated far ahead
// of the client. The current process's startup window remains present so real
// manifest reloads cannot get stuck behind startupFilesReady after cleanup.
func (s *TranscodeSession) pruneDownloadedSegments(generation uint64, downloadedThrough int, continuation bool) {
	started := time.Now()
	s.mu.Lock()
	if generation != s.segmentGeneration || s.restarting != nil {
		// A generation change means restart/shutdown bookkeeping already reset
		// segmentPruneRunning (and may have scheduled a successor pass). A
		// same-generation abort — a restart that is still validating and may
		// yet fail without replacing the process — must release the flag here
		// or pruning stays disabled for the rest of the generation.
		if generation == s.segmentGeneration {
			s.segmentPruneRunning = false
		}
		s.mu.Unlock()
		return
	}
	opts := s.opts
	outputDir := s.outputDir
	fromFloor := s.lastPruneFloor
	pruneBeforeStart := s.pruneBeforeStart
	s.mu.Unlock()

	floor, complete, err := s.segmentRetentionFloor(generation, opts, downloadedThrough)
	if err != nil {
		slog.Warn("resolve transcode segment retention floor", "component", "playback", "error", err, "session", opts.SessionID, "playback_session_id", opts.SessionID)
		s.finishSegmentPrune(generation, fromFloor, fromFloor, downloadedThrough, segmentPruneRetryDelay)
		return
	}
	if !complete || (!pruneBeforeStart && floor <= opts.StartSegmentNumber) || floor <= fromFloor || (!continuation && floor-fromFloor < segmentPruneHysteresis) {
		s.finishSegmentPruneAttempt(generation)
		return
	}

	segmentDuration := opts.SegmentDuration
	if segmentDuration <= 0 {
		segmentDuration = defaultSegmentDuration
	}
	freshGuard := 2*time.Duration(segmentDuration)*time.Second + 30*time.Second
	startupEnd := opts.StartSegmentNumber + startupSegmentRequirement(opts)
	targetFloor := floor
	fromSegment := max(fromFloor, startupEnd)
	if pruneBeforeStart {
		// A forward restart intentionally keeps the previous generation's back
		// buffer until the replacement has produced its own. Retire that older
		// range first, including its startup files, then resume normal pruning
		// without touching the replacement process's startup window. The old
		// range can be very sparse after a long seek, so enumerate actual files
		// below the new start rather than walking every intervening number.
		targetFloor = min(floor, opts.StartSegmentNumber)
	}
	if !pruneBeforeStart && fromSegment >= targetFloor {
		s.finishSegmentPrune(generation, targetFloor, targetFloor, downloadedThrough, 0)
		return
	}

	if _, err := os.Stat(outputDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.finishSegmentPrune(generation, targetFloor, targetFloor, downloadedThrough, 0)
			return
		}
		slog.Warn("stat transcode directory for pruning", "component", "playback", "error", err, "session", opts.SessionID, "playback_session_id", opts.SessionID)
		retryFloor := fromSegment
		if pruneBeforeStart {
			retryFloor = fromFloor
		}
		s.finishSegmentPrune(generation, retryFloor, targetFloor, downloadedThrough, segmentPruneRetryDelay)
		return
	}

	var candidates []segmentPruneCandidate
	moreCandidates := false
	if pruneBeforeStart {
		entries, readErr := os.ReadDir(outputDir)
		if readErr != nil {
			slog.Warn("read transcode directory for preserved segment pruning", "component", "playback", "error", readErr, "session", opts.SessionID, "playback_session_id", opts.SessionID)
			s.finishSegmentPrune(generation, fromFloor, targetFloor, downloadedThrough, segmentPruneRetryDelay)
			return
		}
		for _, entry := range entries {
			segment, parseErr := ParseSegmentNumber(entry.Name())
			if entry.IsDir() || parseErr != nil || segment >= targetFloor || entry.Name() != segmentFilename(segment, opts) {
				continue
			}
			if len(candidates) == segmentPruneBatchSize {
				moreCandidates = true
				break
			}
			candidates = append(candidates, segmentPruneCandidate{
				number: segment,
				path:   filepath.Join(outputDir, entry.Name()),
			})
		}
	} else {
		toSegment := min(targetFloor, fromSegment+segmentPruneBatchSize)
		for segment := fromSegment; segment < toSegment; segment++ {
			candidates = append(candidates, segmentPruneCandidate{
				number: segment,
				path:   filepath.Join(outputDir, segmentFilename(segment, opts)),
			})
		}
		moreCandidates = toSegment < targetFloor
	}
	if len(candidates) == 0 && !moreCandidates {
		s.finishSegmentPrune(generation, targetFloor, targetFloor, downloadedThrough, 0)
		return
	}

	processedFloor := targetFloor
	if !pruneBeforeStart {
		processedFloor = candidates[len(candidates)-1].number + 1
	}
	var retryAfter time.Duration
	removed := 0
	var freedBytes int64
	for _, candidate := range candidates {
		info, err := os.Stat(candidate.path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if pruneBeforeStart {
				retryAfter = max(retryAfter, segmentPruneRetryDelay)
			} else {
				processedFloor, retryAfter = earlierPruneRetry(processedFloor, retryAfter, candidate.number, segmentPruneRetryDelay)
			}
			slog.Warn("stat downloaded transcode segment", "component", "playback", "error", err, "segment", candidate.number, "session", opts.SessionID, "playback_session_id", opts.SessionID)
			continue
		}
		freshUntil := info.ModTime().Add(freshGuard)
		if delay := time.Until(freshUntil); delay > 0 {
			if pruneBeforeStart {
				retryAfter = max(retryAfter, delay)
			} else {
				processedFloor, retryAfter = earlierPruneRetry(processedFloor, retryAfter, candidate.number, delay)
			}
			continue
		}

		// Serialize the unlink with restart and shutdown generation changes.
		// Once either advances the generation, this pass cannot delete files
		// generated for a replacement timeline or session object.
		s.mu.Lock()
		if generation != s.segmentGeneration || s.restarting != nil {
			if generation == s.segmentGeneration {
				s.segmentPruneRunning = false
			}
			s.mu.Unlock()
			return
		}
		removeErr := os.Remove(candidate.path)
		s.mu.Unlock()
		if removeErr != nil {
			if !errors.Is(removeErr, os.ErrNotExist) {
				if pruneBeforeStart {
					retryAfter = max(retryAfter, segmentPruneRetryDelay)
				} else {
					processedFloor, retryAfter = earlierPruneRetry(processedFloor, retryAfter, candidate.number, segmentPruneRetryDelay)
				}
				slog.Warn("remove downloaded transcode segment", "component", "playback", "error", removeErr, "segment", candidate.number, "session", opts.SessionID, "playback_session_id", opts.SessionID)
			}
			continue
		}
		removed++
		freedBytes += info.Size()
	}
	if pruneBeforeStart && (moreCandidates || retryAfter > 0) {
		processedFloor = fromFloor
	}

	s.finishSegmentPrune(generation, processedFloor, targetFloor, downloadedThrough, retryAfter)
	if removed > 0 {
		slog.Info("pruned downloaded transcode segments",
			"component", "playback",
			"count", removed,
			"freed_bytes", freedBytes,
			"floor_segment", processedFloor,
			"duration_ms", time.Since(started).Milliseconds(),
			"session", opts.SessionID,
			"playback_session_id", opts.SessionID,
		)
	}
}

// segmentRetentionFloor returns the first segment that must remain to preserve
// the configured media-time window behind downloadedThrough. Encoded HLS uses
// fixed-duration fragments. Copy HLS follows source keyframes, so its floor is
// derived from the current manifest's actual EXTINF durations. complete is
// false when the manifest does not yet cover the full requested back buffer.
func (s *TranscodeSession) segmentRetentionFloor(generation uint64, opts TranscodeOpts, downloadedThrough int) (floor int, complete bool, err error) {
	retentionSeconds := opts.SegmentRetentionSeconds
	if retentionSeconds <= 0 {
		return 0, false, nil
	}

	if !strings.EqualFold(opts.TargetCodecVideo, "copy") {
		segmentDuration := opts.SegmentDuration
		if segmentDuration <= 0 {
			segmentDuration = defaultSegmentDuration
		}
		retainedSegments := (retentionSeconds + segmentDuration - 1) / segmentDuration
		return downloadedThrough - retainedSegments, true, nil
	}

	s.copyDurationMu.Lock()
	defer s.copyDurationMu.Unlock()

	index := &s.copyDurationIndex
	if index.generation != generation {
		index.reset(generation)
	}
	manifestPath := filepath.Join(s.outputDir, "stream.m3u8")
	if err := index.loadThrough(manifestPath, downloadedThrough); err != nil {
		return 0, false, err
	}
	return index.retentionFloor(downloadedThrough, retentionSeconds)
}

func (i *copyManifestDurationIndex) reset(generation uint64) {
	*i = copyManifestDurationIndex{generation: generation}
}

func (i *copyManifestDurationIndex) loadThrough(manifestPath string, downloadedThrough int) error {
	if !i.loaded {
		return i.loadFull(manifestPath)
	}
	if _, ok := i.durations[downloadedThrough]; ok || downloadedThrough <= i.lastSegment {
		return nil
	}

	entries, foundLast, err := copyManifestEntriesAfter(manifestPath, i.lastSegment)
	if err != nil {
		return fmt.Errorf("read copy manifest tail: %w", err)
	}
	if !foundLast {
		return i.replace(entries)
	}
	return i.append(entries)
}

func (i *copyManifestDurationIndex) loadFull(manifestPath string) error {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read copy manifest: %w", err)
	}
	timeline, err := parseManifestTimeline(manifest)
	if err != nil {
		return fmt.Errorf("parse copy manifest: %w", err)
	}
	return i.replace(timeline.entries)
}

func (i *copyManifestDurationIndex) replace(entries []manifestSegmentEntry) error {
	replacement := copyManifestDurationIndex{
		generation: i.generation,
		loaded:     true,
		durations:  make(map[int]float64, len(entries)),
	}
	if len(entries) == 0 {
		replacement.lastSegment = -1
		*i = replacement
		return nil
	}
	replacement.firstSegment = entries[0].number
	replacement.lastSegment = entries[0].number - 1
	if err := replacement.append(entries); err != nil {
		return err
	}
	*i = replacement
	return nil
}

func (i *copyManifestDurationIndex) append(entries []manifestSegmentEntry) error {
	lastSegment := i.lastSegment
	for _, entry := range entries {
		if entry.duration <= 0 {
			return fmt.Errorf("copy segment %d has non-positive duration %.6f", entry.number, entry.duration)
		}
		if entry.number <= lastSegment {
			return fmt.Errorf("copy manifest segment %d does not advance past %d", entry.number, lastSegment)
		}
		lastSegment = entry.number
	}
	for _, entry := range entries {
		if len(i.durations) == 0 {
			i.firstSegment = entry.number
		}
		i.durations[entry.number] = entry.duration
		i.lastSegment = entry.number
	}
	for len(i.durations) > maxSyntheticManifestSegments {
		delete(i.durations, i.firstSegment)
		i.firstSegment++
	}
	return nil
}

func (i *copyManifestDurationIndex) retentionFloor(downloadedThrough, retentionSeconds int) (floor int, complete bool, err error) {
	if _, ok := i.durations[downloadedThrough]; !ok {
		return 0, false, nil
	}

	retainedSeconds := 0.0
	for segment := downloadedThrough - 1; segment >= i.firstSegment; segment-- {
		duration, ok := i.durations[segment]
		if !ok {
			return 0, false, nil
		}
		floor = segment
		retainedSeconds += duration
		if retainedSeconds >= float64(retentionSeconds) {
			return floor, true, nil
		}
	}
	return 0, false, nil
}

// copyManifestEntriesAfter reads progressively larger tails until it finds the
// last indexed segment. FFmpeg publishes manifests via temp-file rename, so an
// open descriptor is an immutable snapshot while the tail is inspected.
func copyManifestEntriesAfter(manifestPath string, lastSegment int) (entries []manifestSegmentEntry, foundLast bool, err error) {
	manifest, err := os.Open(manifestPath)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		err = errors.Join(err, manifest.Close())
	}()

	info, err := manifest.Stat()
	if err != nil {
		return nil, false, err
	}
	for window := min(info.Size(), int64(copyManifestTailBytes)); ; window = min(info.Size(), window*2) {
		start := info.Size() - window
		chunk := make([]byte, int(window))
		n, readErr := manifest.ReadAt(chunk, start)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, false, readErr
		}
		chunk = chunk[:n]
		parseable := make([]byte, 0, len(chunk)+len("#EXTM3U\n"))
		parseable = append(parseable, "#EXTM3U\n"...)
		parseable = append(parseable, chunk...)
		timeline, parseErr := parseManifestTimeline(parseable)
		if parseErr != nil {
			return nil, false, fmt.Errorf("parse copy manifest tail: %w", parseErr)
		}
		lastIndex := slices.IndexFunc(timeline.entries, func(entry manifestSegmentEntry) bool {
			return entry.number == lastSegment
		})
		if lastIndex >= 0 {
			return timeline.entries[lastIndex+1:], true, nil
		}
		if start == 0 {
			return timeline.entries, false, nil
		}
	}
}

func earlierPruneRetry(currentFloor int, currentDelay time.Duration, segment int, delay time.Duration) (int, time.Duration) {
	if segment < currentFloor {
		return segment, max(delay, time.Millisecond)
	}
	return currentFloor, currentDelay
}

func (s *TranscodeSession) finishSegmentPruneAttempt(generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.segmentGeneration {
		return
	}
	s.segmentPruneRunning = false
	s.scheduleSegmentPruneLocked()
}

func (s *TranscodeSession) finishSegmentPrune(
	generation uint64,
	processedFloor int,
	targetFloor int,
	downloadedThrough int,
	retryAfter time.Duration,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.segmentGeneration {
		return
	}
	if processedFloor > s.lastPruneFloor {
		s.lastPruneFloor = processedFloor
	}
	if s.pruneBeforeStart && s.lastPruneFloor >= s.opts.StartSegmentNumber {
		s.pruneBeforeStart = false
	}
	if retryAfter > 0 {
		time.AfterFunc(retryAfter, func() {
			s.mu.Lock()
			if generation != s.segmentGeneration || s.restarting != nil {
				if generation == s.segmentGeneration {
					s.segmentPruneRunning = false
				}
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
			go s.pruneDownloadedSegments(generation, downloadedThrough, true)
		})
		return
	}
	if processedFloor < targetFloor {
		go s.pruneDownloadedSegments(generation, downloadedThrough, true)
		return
	}
	s.segmentPruneRunning = false
	s.scheduleSegmentPruneLocked()
}
