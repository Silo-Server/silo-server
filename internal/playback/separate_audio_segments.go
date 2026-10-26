package playback

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ParseAudioSegmentNumber keeps audio ordinals separate from video recovery:
// an audio segment with the same number can begin at a different source time.
func ParseAudioSegmentNumber(name string) (int, error) {
	if len(name) > 64 || !strings.HasPrefix(name, "audio_") || !strings.HasSuffix(name, ".ts") {
		return 0, errors.New("invalid audio segment")
	}
	number, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "audio_"), ".ts"))
	if err != nil || number < 0 || name != fmt.Sprintf("audio_%05d.ts", number) {
		return 0, errors.New("invalid audio segment")
	}
	return number, nil
}

// ReportMediaSegmentDownloadedForGeneration accounts for a completed response
// on its own rendition and only in the generation that served the file.
func (s *TranscodeSession) ReportMediaSegmentDownloadedForGeneration(name string, generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.segmentGeneration {
		return
	}
	s.reportMediaSegmentDownloadedLocked(name)
}

// ReportMediaSegmentDownloadedForGenerationToken is the authenticated relay's
// counterpart. A delayed acknowledgement cannot advance a replacement job.
func (s *TranscodeSession) ReportMediaSegmentDownloadedForGenerationToken(name, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" || token != s.segmentGenerationTokenLocked() {
		return
	}
	s.reportMediaSegmentDownloadedLocked(name)
}

func (s *TranscodeSession) reportMediaSegmentDownloadedLocked(name string) {
	if number, err := ParseSegmentNumber(name); err == nil {
		s.reportSegmentDownloadedLocked(number)
	}
	if number, err := ParseAudioSegmentNumber(name); err == nil && s.opts.SeparateAudioHLS {
		s.audioCompletedSegment = max(s.audioCompletedSegment, number)
		s.audioCompleted = true
		s.scheduleAudioPruneLocked()
	}
}

type audioSegmentDemand struct {
	generation uint64
	name       string
	audioTrack int
	channels   int
}

// OpenMediaSegment waits on its own rendition without borrowing video ordinals.
func (s *TranscodeSession) OpenMediaSegment(name string) (*SegmentLease, error) {
	if segment, err := s.OpenSegment(name); err == nil || !errors.Is(err, ErrSegmentNotFound) {
		return segment, err
	}
	number, err := ParseAudioSegmentNumber(name)
	s.mu.Lock()
	wait := err == nil && s.opts.SeparateAudioHLS && number >= s.opts.StartSegmentNumber && number-s.opts.StartSegmentNumber < maxSyntheticManifestSegments
	demand := &audioSegmentDemand{s.segmentGeneration, name, s.opts.AudioTrackIndex, s.opts.SourceAudioChannels}
	if wait {
		if s.audioSegmentDemands == nil {
			s.audioSegmentDemands = make(map[*audioSegmentDemand]struct{})
		}
		s.audioSegmentDemands[demand] = struct{}{}
	}
	s.mu.Unlock()
	if !wait {
		return s.OpenSegment(name)
	}
	defer func() {
		s.mu.Lock()
		delete(s.audioSegmentDemands, demand)
		s.mu.Unlock()
	}()
	if s.audioSegmentNeedsRecovery(name) {
		return nil, ErrSegmentNotFound
	}
	return s.WaitForOpenSegment(name, 60*time.Second)
}

func (s *TranscodeSession) audioTimelineSnapshot(dir string) (manifestTimeline, os.FileInfo, error) {
	path := filepath.Join(dir, hlsAudioManifestFilename)
	info, err := os.Stat(path)
	if err != nil {
		return manifestTimeline{}, nil, err
	}
	s.mu.Lock()
	if s.audioManifestInfo != nil && sameManifestFile(info, s.audioManifestInfo) {
		timeline := s.audioTimeline
		s.mu.Unlock()
		return timeline, info, nil
	}
	s.mu.Unlock()
	manifest, info, err := readManifestFile(path)
	if err != nil {
		return manifestTimeline{}, nil, err
	}
	timeline, err := parseManifestTimeline(manifest)
	if err == nil {
		s.mu.Lock()
		s.audioManifestInfo, s.audioTimeline = info, timeline
		s.mu.Unlock()
	}
	return timeline, info, err
}

func (timeline manifestTimeline) segmentTimes(number int) (start, end float64, found bool) {
	for _, entry := range timeline.entries {
		end = start + entry.duration
		if entry.number == number {
			return start, end, true
		}
		start = end
	}
	return 0, 0, false
}

func (s *TranscodeSession) separateAudioThrottleGap(progress SegmentProgress) (gap float64, resolved, replay bool) {
	s.mu.Lock()
	opts, completed, hasCompleted, dir, generation := s.opts, s.audioCompletedSegment, s.audioCompleted, s.outputDir, s.segmentGeneration
	var demands []string
	for demand := range s.audioSegmentDemands {
		if demand.generation == generation {
			demands = append(demands, demand.name)
		}
	}
	s.mu.Unlock()
	if !opts.SeparateAudioHLS {
		return 0, false, false
	}
	if generation != progress.Generation {
		return 0, false, true
	}
	video := progress.timeline
	if len(video.entries) == 0 || video.mediaSequence != opts.StartSegmentNumber {
		return 0, false, len(demands) > 0
	}
	_, produced, found := video.segmentTimes(progress.ProducedHead)
	if !found {
		return 0, false, len(demands) > 0
	}
	requested, _, found := video.segmentTimes(progress.LastRequestedSegment)
	if !found {
		return 0, false, len(demands) > 0
	}
	// Both renditions replay one frozen origin; elapsed EXTINF time shares that origin.
	audio, audioInfo, audioErr := s.audioTimelineSnapshot(dir)
	if audioErr == nil && audio.mediaSequence == opts.StartSegmentNumber && len(audio.entries) > 0 && (progress.GenerationStartedAt.IsZero() || !audioInfo.ModTime().Before(progress.GenerationStartedAt)) {
		head := audio.entries[len(audio.entries)-1].number
		if info, err := os.Stat(filepath.Join(dir, fmt.Sprintf("audio_%05d.ts", head))); err == nil && info.Size() > 0 {
			_, end, _ := audio.segmentTimes(head)
			produced = max(produced, end)
		}
	}
	if hasCompleted && audioErr == nil && audio.mediaSequence == opts.StartSegmentNumber {
		if _, end, found := audio.segmentTimes(completed); found {
			requested = max(requested, end)
		}
	}
	for _, name := range demands {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Size() > 0 {
			continue
		}
		number, _ := ParseAudioSegmentNumber(name)
		if audioErr == nil && audio.mediaSequence == opts.StartSegmentNumber {
			if _, end, found := audio.segmentTimes(number); found {
				requested = max(requested, end)
				continue
			}
		}
		// Cold recovery has no audio boundary evidence until origin replay reaches
		// the requested file. A guessed cutoff would reject valid distant seeks.
		replay = true
	}
	return produced - requested, true, replay
}

// RecoverAudioSegment replays a missing audio file while the caller holds its owner's lifecycle lock.
func (s *TranscodeSession) RecoverAudioSegment(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.audioSegmentNeedsRecovery(name) {
		return nil
	}
	s.mu.Lock()
	origin, requested := s.opts.SeekSeconds, s.lastRequestedSegment
	s.mu.Unlock()
	return s.Restart(ctx, origin, requested)
}

func (s *TranscodeSession) audioSegmentNeedsRecovery(name string) bool {
	number, err := ParseAudioSegmentNumber(name)
	if err != nil {
		return false
	}
	s.mu.Lock()
	opts, dir, generation, started := s.opts, s.outputDir, s.segmentGeneration, s.generationStartedAt
	eligible := opts.SeparateAudioHLS && s.restarting == nil && number >= opts.StartSegmentNumber && number-opts.StartSegmentNumber < maxSyntheticManifestSegments
	s.mu.Unlock()
	if !eligible {
		return false
	}
	if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Size() > 0 {
		return false
	}
	timeline, info, err := s.audioTimelineSnapshot(dir)
	if err != nil || timeline.mediaSequence != opts.StartSegmentNumber || (!started.IsZero() && info.ModTime().Before(started)) {
		return false
	}
	if _, _, found := timeline.segmentTimes(number); !found {
		return false
	}
	head := timeline.entries[len(timeline.entries)-1].number
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.segmentGeneration != generation || s.restarting != nil {
		return false
	}
	// A published timeline proves this cut completed even after its file or
	// the head file was pruned. Unpublished cuts must keep waiting.
	return number <= head
}
