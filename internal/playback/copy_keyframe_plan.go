package playback

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/playback/keyframes"
)

// copySegmentPlan is a copy-video session's complete segment list, worked out
// from the source's keyframe index before FFmpeg starts (#1466). The session
// serves it as a VOD playlist instead of FFmpeg's growing one, so a player
// never sees a partial playlist: Safari's native HLS can fall back to one and
// stop mid-stream.
//
// The segments come from keyframe groups FFmpeg cuts at every keyframe,
// joined by a copyGroupAssembler, so a restart can start FFmpeg anywhere and
// still produce the planned segments.
type copySegmentPlan struct {
	durations []float64
	// keyframes is the source's complete keyframe list, in seconds on the
	// container timeline.
	keyframes []float64
	// firstKey is each segment's first keyframe, as an index into keyframes.
	firstKey []int
}

// loadKeyframeIndex reads a source's keyframe index; tests replace it.
var loadKeyframeIndex = keyframes.Load

// planCopySegments returns the segment plan for a session, or nil when the
// session keeps FFmpeg's real playlist: the setting is off, the video isn't
// copied into fMP4, the stream doesn't start at the beginning, or the source
// has no complete keyframe index.
func planCopySegments(opts TranscodeOpts) *copySegmentPlan {
	if !opts.KeyframePlaylist ||
		!strings.EqualFold(opts.TargetCodecVideo, "copy") || !videoUsesFMP4(opts) ||
		opts.SeekSeconds > 0 || opts.StartSegmentNumber > 0 ||
		!CanGenerateSyntheticManifest(opts.TotalDuration, opts.SegmentDuration) {
		return nil
	}
	idx, err := loadKeyframeIndex(opts.InputPath)
	if err != nil {
		if !errors.Is(err, keyframes.ErrNoIndex) {
			log.Printf("playback: read keyframe index for %s: %v; using FFmpeg's playlist", opts.SessionID, err)
		}
		return nil
	}
	segmentSeconds := float64(opts.SegmentDuration)
	if segmentSeconds <= 0 {
		segmentSeconds = defaultSegmentDuration
	}
	firstKey := keyframes.PlanSegmentIndices(idx.Keyframes, segmentSeconds)
	if len(firstKey) == 0 || len(firstKey) > maxSyntheticManifestSegments {
		return nil
	}
	starts := make([]float64, len(firstKey))
	for i, k := range firstKey {
		starts[i] = idx.Keyframes[k]
	}
	// The probed duration counts from the file's start, which is the first
	// keyframe or just before it; the keyframe times are the container's,
	// which can start later than zero.
	durations := keyframes.SegmentDurations(starts, idx.Keyframes[0]+opts.TotalDuration)
	if durations[len(durations)-1] <= 0 {
		// The index runs past the probed duration; the plan can't describe
		// the end of the stream.
		return nil
	}
	return &copySegmentPlan{durations: durations, keyframes: idx.Keyframes, firstKey: firstKey}
}

// copyPlanRecordFile keeps a session's planning decision in its output
// directory, which a session rebuilt from its recipe reuses.
const copyPlanRecordFile = "keyframe-plan.json"

type copyPlanRecord struct {
	Planned   bool      `json:"planned"`
	Durations []float64 `json:"durations,omitempty"`
	Keyframes []float64 `json:"keyframes,omitempty"`
	FirstKey  []int     `json:"first_key,omitempty"`
}

// resolveCopyPlan returns the plan a session serves. The first start decides
// and records the decision; a rebuilt session follows the record, so it
// serves the playlist its player already has. Deciding afresh could differ:
// a file's index that was still being checked may be verified by then.
func resolveCopyPlan(opts TranscodeOpts) *copySegmentPlan {
	if !opts.KeyframePlaylist || opts.OutputDir == "" {
		return planCopySegments(opts)
	}
	path := filepath.Join(opts.OutputDir, copyPlanRecordFile)
	if data, err := os.ReadFile(path); err == nil {
		var rec copyPlanRecord
		if err := json.Unmarshal(data, &rec); err == nil {
			if !rec.Planned {
				return nil
			}
			if plan := rec.plan(); plan != nil {
				return plan
			}
		}
		log.Printf("playback: session %s has an unreadable keyframe plan record; using FFmpeg's playlist", opts.SessionID)
		return nil
	}

	plan := planCopySegments(opts)
	rec := copyPlanRecord{Planned: plan != nil}
	if plan != nil {
		rec.Durations, rec.Keyframes, rec.FirstKey = plan.durations, plan.keyframes, plan.firstKey
	}
	data, err := json.Marshal(rec)
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o644); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		// Without a record a rebuilt session can't promise the same
		// playlist, so don't serve a planned one.
		log.Printf("playback: record keyframe plan for %s: %v; using FFmpeg's playlist", opts.SessionID, err)
		return nil
	}
	return plan
}

// plan rebuilds a recorded plan, or nil if the record is inconsistent.
func (r copyPlanRecord) plan() *copySegmentPlan {
	if len(r.Durations) == 0 || len(r.FirstKey) != len(r.Durations) || len(r.Keyframes) == 0 {
		return nil
	}
	for i, k := range r.FirstKey {
		if k < 0 || k >= len(r.Keyframes) || (i > 0 && k <= r.FirstKey[i-1]) {
			return nil
		}
	}
	return &copySegmentPlan{durations: r.Durations, keyframes: r.Keyframes, firstKey: r.FirstKey}
}

// keyframeRange returns segment n's keyframes as [first, end) indices.
func (p *copySegmentPlan) keyframeRange(n int) (first, end int) {
	first = p.firstKey[n]
	end = len(p.keyframes)
	if n+1 < len(p.firstKey) {
		end = p.firstKey[n+1]
	}
	return first, end
}

// segmentOfKeyframe returns the segment holding keyframe k.
func (p *copySegmentPlan) segmentOfKeyframe(k int) int {
	return sort.SearchInts(p.firstKey, k+1) - 1
}

// restartSeekSeconds is the -ss that makes FFmpeg start at or before segment
// n's first keyframe: FFmpeg's Matroska seek lands on a keyframe at or before
// its target, so aim at the keyframe before. The seek counts from the file's
// start, which is at or before the first keyframe, so measuring from the
// first keyframe can only land earlier.
func (p *copySegmentPlan) restartSeekSeconds(n int) float64 {
	if n <= 0 || n >= len(p.firstKey) {
		return 0
	}
	k := max(p.firstKey[n]-1, 0)
	return max(p.keyframes[k]-p.keyframes[0], 0)
}

// plannedWaitSegments is how far past the assembled head a missing segment
// is waited for instead of restarting FFmpeg at it. Stream copy runs many
// times faster than playback, so FFmpeg reaches these within moments.
const plannedWaitSegments = 10

// plannedSegmentRecoveryDecision handles a missing segment in a planned
// session. One the running FFmpeg is about to reach is waited for, and
// recorded as the player's position so the throttler doesn't hold FFmpeg
// back from it. Anything else restarts FFmpeg at that segment: segments
// match the playlist wherever FFmpeg starts.
func (s *TranscodeSession) plannedSegmentRecoveryDecision(segNum int, decision SegmentRecoveryDecision) SegmentRecoveryDecision {
	progress := decision.Progress
	switch {
	case segNum < 0 || segNum >= len(s.copyPlan.durations):
		// Not a segment of the playlist.
		decision.Wait = false
		decision.RestartOnTimeout = false
		decision.Reason = "planned_segment_out_of_range"
	case progress.Restarting:
		decision.Wait = true
		decision.WaitTimeout = activeSegmentWait
		decision.RestartOnTimeout = false
		decision.Reason = segmentReasonRestarting
	case progress.Running && segNum >= progress.StartSegmentNumber &&
		segNum <= progress.ProducedHead+plannedWaitSegments:
		s.mu.Lock()
		if segNum > s.lastRequestedSegment {
			s.lastRequestedSegment = segNum
		}
		s.mu.Unlock()
		decision.Wait = true
		decision.WaitTimeout = activeSegmentWait
		decision.RestartOnTimeout = true
		decision.Reason = "planned_segment_near_head"
	default:
		decision.Wait = false
		decision.RestartOnTimeout = true
		decision.Reason = "planned_segment_restart"
	}
	return decision
}

// retentionFloor is the first segment the pruner keeps when the player has
// downloaded through downloadedThrough: enough planned media behind it to
// cover retentionSeconds.
func (p *copySegmentPlan) retentionFloor(downloadedThrough, retentionSeconds int) int {
	n := min(downloadedThrough, len(p.durations)-1)
	for covered := 0.0; n >= 0 && covered < float64(retentionSeconds); n-- {
		covered += p.durations[n]
	}
	return n
}

// plannedSegmentProgress reports a planned session's progress from its
// assembled segments: the current run's start segment and the highest
// segment assembled contiguously from it.
func (s *TranscodeSession) plannedSegmentProgress() SegmentProgress {
	s.mu.Lock()
	progress := SegmentProgress{
		Running:              s.running,
		Restarting:           s.restarting != nil,
		SegmentDuration:      s.opts.SegmentDuration,
		LastRequestedSegment: s.lastRequestedSegment,
		GenerationStartedAt:  s.generationStartedAt,
		HasManifest:          true,
	}
	s.mu.Unlock()
	if progress.SegmentDuration <= 0 {
		progress.SegmentDuration = defaultSegmentDuration
	}
	progress.StartSegmentNumber, progress.ProducedHead, progress.ProducedCount, progress.LastProducedAt = s.copyGroups.progress()
	// The throttler reads output older than the current FFmpeg as left over
	// from an earlier one. The current run's last group is its output.
	progress.ManifestModTime = progress.LastProducedAt
	return progress
}

// plannedManifestWhenReady returns the planned playlist once the current
// run has assembled its first segments, the readiness FFmpeg's own playlist
// gives other sessions, with the same errors while it isn't.
func (s *TranscodeSession) plannedManifestWhenReady() ([]byte, error) {
	s.mu.Lock()
	opts, running, restarting, waitErr := s.opts, s.running, s.restarting != nil, s.waitErr
	stderr := ""
	if s.stderr != nil {
		stderr = truncateStderr(s.stderr.String())
	}
	s.mu.Unlock()

	start, head, _, _ := s.copyGroups.progress()
	if head >= min(start+startupSegmentRequirement(opts)-1, len(s.copyPlan.durations)-1) {
		return s.copyPlan.manifest(opts, "", ""), nil
	}
	if !running && !restarting && waitErr != nil {
		if stderr != "" {
			return nil, fmt.Errorf("%w: %w (stderr: %s)", ErrTranscodeFailed, waitErr, stderr)
		}
		return nil, fmt.Errorf("%w: %w", ErrTranscodeFailed, waitErr)
	}
	return nil, ErrManifestNotReady
}

// audioRecipeKey identifies the audio bytes a run emits. Segments assembled
// from another audio recipe can't be served with this one.
func audioRecipeKey(opts TranscodeOpts) string {
	return fmt.Sprintf("%d/%d/%s/%d/%d", opts.AudioTrackIndex, opts.SourceAudioChannels,
		strings.ToLower(opts.TargetCodecAudio), opts.TargetAudioChannels, opts.TargetAudioBitrateKbps)
}

// manifest returns the plan as a complete VOD playlist for the session's
// segment files, in the same shape as GenerateFullManifest.
func (p *copySegmentPlan) manifest(opts TranscodeOpts, segPrefix, rawQuery string) []byte {
	queryDefinition, suffix, queryVersion := syntheticManifestQuery(len(p.durations), rawQuery)
	segExt := hlsSegmentExtension(opts)
	fmp4 := videoUsesFMP4(opts)
	hlsVersion := 3
	if fmp4 {
		hlsVersion = 7
	}
	hlsVersion = max(hlsVersion, queryVersion)

	// EXT-X-TARGETDURATION must cover every segment's rounded duration. A
	// keyframe gap longer than the segment length makes a longer segment.
	target := 1
	for _, d := range p.durations {
		target = max(target, int(math.Round(d)))
	}

	var buf bytes.Buffer
	buf.WriteString("#EXTM3U\n")
	fmt.Fprintf(&buf, "#EXT-X-VERSION:%d\n", hlsVersion)
	buf.WriteString(queryDefinition)
	fmt.Fprintf(&buf, "#EXT-X-TARGETDURATION:%d\n", target)
	buf.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	buf.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	if fmp4 {
		fmt.Fprintf(&buf, "#EXT-X-MAP:URI=\"%sinit.mp4%s\"\n", segPrefix, suffix)
	}
	for i, d := range p.durations {
		fmt.Fprintf(&buf, "#EXTINF:%.6f,\n", d)
		fmt.Fprintf(&buf, "%sseg_%05d%s%s\n", segPrefix, i, segExt, suffix)
	}
	buf.WriteString("#EXT-X-ENDLIST\n")
	return buf.Bytes()
}
