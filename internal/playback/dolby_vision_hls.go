package playback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const dv7SourceDirectory = "dv7-source"

// prepareDV7Manifest publishes only a playlist whose startup media has passed
// conversion. Original Profile 7 files stay outside the public segment directory.
func (s *TranscodeSession) prepareDV7Manifest(currentGeneration bool) error {
	s.mu.Lock()
	enabled := s.opts.DV7ToDV81
	s.mu.Unlock()
	if !enabled {
		return nil
	}
	s.dvMu.Lock()
	defer s.dvMu.Unlock()
	s.mu.Lock()
	generation, ctx, dir, inherited, restarting := s.segmentGeneration, s.dvContext, s.outputDir, s.dvInheritedManifest, s.restarting != nil
	running := s.running
	required := startupSegmentRequirement(s.opts)
	s.mu.Unlock()
	if restarting || ctx == nil || ctx.Err() != nil {
		return ErrManifestNotReady
	}
	rawDir := filepath.Join(dir, dv7SourceDirectory)
	manifest, info, err := readManifestFile(filepath.Join(rawDir, hlsVideoManifestFilename))
	if err != nil {
		return ErrManifestNotReady
	}
	if currentGeneration && inherited != nil && sameManifestFile(info, inherited) {
		return ErrManifestNotReady
	}
	timeline, err := parseManifestTimeline(manifest)
	if err != nil || len(timeline.entries) == 0 {
		return ErrManifestNotReady
	}
	if running && len(timeline.entries) < required && !bytes.Contains(manifest, []byte("#EXT-X-ENDLIST")) {
		return ErrManifestNotReady
	}
	for _, entry := range timeline.entries[:min(required, len(timeline.entries))] {
		if err := s.convertDV7SegmentLocked(ctx, generation, fmt.Sprintf("seg_%05d.m4s", entry.number)); err != nil {
			return err
		}
	}
	return s.publishDV7File(generation, hlsVideoManifestFilename, manifest)
}

func (s *TranscodeSession) ensureDV7Segment(name string) error {
	s.mu.Lock()
	enabled, ctx, generation := s.opts.DV7ToDV81, s.dvContext, s.segmentGeneration
	restarting := s.restarting != nil
	s.mu.Unlock()
	if !enabled {
		return nil
	}
	if restarting || ctx == nil || ctx.Err() != nil {
		return ErrSegmentNotFound
	}
	if name == hlsInitFilename {
		return s.prepareDV7Manifest(false)
	}
	number, err := ParseSegmentNumber(name)
	if err != nil || number < 0 || name != fmt.Sprintf("seg_%05d.m4s", number) {
		return nil
	}
	s.dvMu.Lock()
	defer s.dvMu.Unlock()
	return s.convertDV7SegmentLocked(ctx, generation, name)
}

func (s *TranscodeSession) convertDV7SegmentLocked(ctx context.Context, generation uint64, name string) error {
	s.mu.Lock()
	if generation != s.segmentGeneration || s.restarting != nil {
		s.mu.Unlock()
		return ErrSegmentNotFound
	}
	dir, tool, policy := s.outputDir, s.dvToolPath, s.opts.TranscodePolicy
	s.mu.Unlock()
	if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Size() > 0 {
		return nil
	}
	if err := policy.check(); err != nil {
		return err
	}
	conversionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case dvConversionSlots <- struct{}{}:
		defer func() { <-dvConversionSlots }()
	case <-conversionCtx.Done():
		return conversionCtx.Err()
	}
	if err := conversionCtx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	stale := generation != s.segmentGeneration || s.restarting != nil
	s.mu.Unlock()
	if stale {
		return ErrSegmentNotFound
	}
	if err := policy.check(); err != nil {
		return err
	}
	rawDir := filepath.Join(dir, dv7SourceDirectory)
	init, err := readBoundedHLSFile(filepath.Join(rawDir, hlsInitFilename), 2<<20)
	if err != nil {
		return ErrSegmentNotFound
	}
	path := filepath.Join(rawDir, name)
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 {
		return ErrSegmentNotFound
	}
	if info.Size() > maxDVFragmentBytes {
		return errors.New("dolby vision fragment exceeds conversion limit")
	}
	data, err := readBoundedHLSFile(path, maxDVFragmentBytes)
	if err != nil {
		return ErrSegmentNotFound
	}
	convertedInit, convertedData, err := convertDV7FragmentWithSlot(conversionCtx, tool, init, data, policy)
	if err != nil {
		return err
	}
	if err := s.publishDV7File(generation, hlsInitFilename, convertedInit); err != nil {
		return err
	}
	return s.publishDV7File(generation, name, convertedData)
}

func (s *TranscodeSession) publishDV7File(generation uint64, name string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.segmentGeneration || s.restarting != nil || s.dvContext == nil || s.dvContext.Err() != nil {
		return ErrSegmentNotFound
	}
	path := filepath.Join(s.outputDir, name)
	if name == hlsInitFilename {
		if existing, err := readBoundedHLSFile(path, maxDVFragmentBytes); err == nil {
			if bytes.Equal(existing, data) {
				return nil
			}
			return errors.New("dolby vision initialization changed within a transport")
		}
	}
	file, err := os.CreateTemp(s.outputDir, ".dv-convert-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func prepareDV7HLS(ctx context.Context, opts TranscodeOpts) (string, error) {
	if !opts.DV7ToDV81 {
		return "", nil
	}
	if err := opts.TranscodePolicy.check(); err != nil {
		return "", err
	}
	if !opts.SeparateAudioHLS || opts.CopyVideoMPEGTS || !strings.EqualFold(opts.TargetCodecVideo, "copy") || !strings.EqualFold(opts.SourceVideoCodec, "hevc") || opts.VideoSampleEntry != VideoSampleEntryDVH1 {
		return "", errors.New("unsupported Dolby Vision conversion recipe")
	}
	path, valid := probeDV7ToDV81ToolchainV3(ctx)
	if !valid {
		return "", errors.New("dolby vision conversion tool is unavailable")
	}
	return path, nil
}

func (s *TranscodeSession) cleanDV7SourceForRestart(startSegment int) {
	dir := filepath.Join(s.outputDir, dv7SourceDirectory)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		number, parseErr := ParseSegmentNumber(name)
		if name == hlsVideoManifestFilename || parseErr == nil && number >= startSegment {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
