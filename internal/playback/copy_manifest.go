package playback

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"strings"
	"time"
)

const (
	copyManifestProbeTimeout = 2 * time.Minute
	copyManifestInitialWait  = 10 * time.Second
	copyManifestFMP4Ext      = ".m4s"
)

type copyVideoKeyframeProbe func(context.Context, string) ([]float64, float64, error)

// buildCompleteCopyVideoManifest mirrors Jellyfin's remux playlist contract:
// choose the first source keyframe at or after each desired cut time, then
// publish those real durations as a complete VOD timeline. FFmpeg's HLS muxer
// uses the same cut rule for stream copy, so segment numbers remain truthful.
func (s *TranscodeSession) completeCopyVideoManifest() []byte {
	s.copyManifestMu.Lock()
	defer s.copyManifestMu.Unlock()
	return append([]byte(nil), s.copyManifest...)
}

func (s *TranscodeSession) startCompleteCopyVideoManifest(ctx context.Context, opts TranscodeOpts) <-chan struct{} {
	s.copyManifestMu.Lock()
	if s.copyManifestStarted {
		done := s.copyManifestDone
		s.copyManifestMu.Unlock()
		return done
	}
	s.copyManifestStarted = true
	s.copyManifestDone = make(chan struct{})
	done := s.copyManifestDone
	probe := s.copyManifestProbe
	if probe == nil {
		probe = probeCopyVideoKeyframes
	}
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), copyManifestProbeTimeout)
	s.copyManifestCancel = cancel
	s.copyManifestMu.Unlock()

	go func() {
		defer cancel()
		defer close(done)
		manifest, timeline, err := buildCompleteCopyVideoManifest(probeCtx, opts, probe)
		s.copyManifestMu.Lock()
		s.copyManifestCancel = nil
		if err == nil {
			s.copyManifest = append([]byte(nil), manifest...)
			s.copyManifestTimeline = cloneManifestTimeline(timeline)
		}
		s.copyManifestMu.Unlock()
		if err != nil {
			slog.WarnContext(probeCtx, "copy-video keyframe manifest unavailable; continuing with growing FFmpeg manifest", "error", err)
			return
		}
		slog.InfoContext(probeCtx, "copy-video keyframe manifest ready", "segments", len(timeline.entries))
	}()
	return done
}

func (s *TranscodeSession) waitForCompleteCopyVideoManifest(ctx context.Context, done <-chan struct{}) []byte {
	if done == nil {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, copyManifestInitialWait)
	defer cancel()
	select {
	case <-done:
		return s.completeCopyVideoManifest()
	case <-waitCtx.Done():
		return nil
	}
}

func buildCompleteCopyVideoManifest(ctx context.Context, opts TranscodeOpts, probe copyVideoKeyframeProbe) ([]byte, manifestTimeline, error) {
	keyframes, probedDuration, err := probe(ctx, opts.InputPath)
	if err != nil {
		return nil, manifestTimeline{}, err
	}
	totalDuration := probedDuration
	if totalDuration <= 0 {
		totalDuration = opts.TotalDuration
	}
	durations, err := copyVideoSegmentDurations(keyframes, totalDuration, opts.SegmentDuration)
	if err != nil {
		return nil, manifestTimeline{}, err
	}
	manifest := buildDurationManifest(durations, opts)
	timeline, err := parseManifestTimeline(manifest)
	if err != nil {
		return nil, manifestTimeline{}, fmt.Errorf("parse complete copy manifest: %w", err)
	}
	return manifest, timeline, nil
}

func probeCopyVideoKeyframes(ctx context.Context, inputPath string) ([]float64, float64, error) {
	if strings.TrimSpace(inputPath) == "" {
		return nil, 0, fmt.Errorf("probe copy-video keyframes: empty input path")
	}
	switch strings.ToLower(filepath.Ext(inputPath)) {
	case ".mkv", ".webm":
		return probeMatroskaCueTimeline(ctx, inputPath)
	default:
		return nil, 0, fmt.Errorf("copy-video keyframe metadata is unavailable for %s", filepath.Ext(inputPath))
	}
}

func copyVideoSegmentDurations(keyframes []float64, totalDuration float64, segmentDuration int) ([]float64, error) {
	if segmentDuration <= 0 {
		segmentDuration = defaultSegmentDuration
	}
	if totalDuration <= 0 || math.IsNaN(totalDuration) || math.IsInf(totalDuration, 0) {
		return nil, fmt.Errorf("invalid copy-video duration %v", totalDuration)
	}
	estimatedSegments := math.Ceil(totalDuration / float64(segmentDuration))
	if estimatedSegments > maxSyntheticManifestSegments {
		return nil, fmt.Errorf("copy-video manifest exceeds %d segments", maxSyntheticManifestSegments)
	}

	lastKeyframe := 0.0
	desiredCutTime := float64(segmentDuration)
	durations := make([]float64, 0, int(estimatedSegments))
	for _, keyframe := range keyframes {
		if keyframe < desiredCutTime || keyframe <= lastKeyframe || keyframe >= totalDuration {
			continue
		}
		durations = append(durations, keyframe-lastKeyframe)
		if len(durations) >= maxSyntheticManifestSegments {
			return nil, fmt.Errorf("copy-video manifest exceeds %d segments", maxSyntheticManifestSegments)
		}
		lastKeyframe = keyframe
		desiredCutTime += float64(segmentDuration)
	}
	durations = append(durations, totalDuration-lastKeyframe)
	if durations[len(durations)-1] <= 0 || len(durations) > maxSyntheticManifestSegments {
		return nil, fmt.Errorf("invalid copy-video segment timeline")
	}
	return durations, nil
}

func buildDurationManifest(durations []float64, opts TranscodeOpts) []byte {
	hlsVersion := 3
	if hlsSegmentExtension(opts) == copyManifestFMP4Ext {
		hlsVersion = 7
	}
	targetDuration := 1
	for _, duration := range durations {
		targetDuration = max(targetDuration, int(math.Ceil(duration)))
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "#EXTM3U\n#EXT-X-VERSION:%d\n#EXT-X-TARGETDURATION:%d\n", hlsVersion, targetDuration)
	buf.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	extension := hlsSegmentExtension(opts)
	if extension == copyManifestFMP4Ext {
		buf.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
	}
	for segment, duration := range durations {
		fmt.Fprintf(&buf, "#EXTINF:%.6f,\nseg_%05d%s\n", duration, segment, extension)
	}
	buf.WriteString("#EXT-X-ENDLIST\n")
	return buf.Bytes()
}
