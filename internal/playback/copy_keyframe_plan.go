package playback

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback/keyframes"
)

// copySegmentPlan is a copy-video session's complete segment list, worked out
// from the source's keyframe index before FFmpeg starts (#1466). The session
// serves it as a VOD playlist instead of FFmpeg's growing one, so a player
// never sees a partial playlist: Safari's native HLS can fall back to one and
// stop mid-stream.
//
// FFmpeg's HLS muxer, run from the start of the file, cuts copied video at
// the planned keyframes (keyframes.PlanSegments), so its seg_NNNNN files are
// the planned segments. A seek restart would cut at different keyframes, so
// a planned session restarts from the beginning instead (see restart).
type copySegmentPlan struct {
	durations []float64
}

// loadKeyframeIndex reads a source's keyframe index; tests replace it.
var loadKeyframeIndex = keyframes.Load

// planCopySegments returns the segment plan for a session, or nil when the
// session keeps FFmpeg's real playlist: the setting is off, the video isn't
// copied, the stream doesn't start at the beginning, or the source has no
// keyframe index.
func planCopySegments(opts TranscodeOpts) *copySegmentPlan {
	if !opts.KeyframePlaylist ||
		!strings.EqualFold(opts.TargetCodecVideo, "copy") ||
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
	starts := keyframes.PlanSegments(idx.Keyframes, segmentSeconds)
	if len(starts) == 0 || len(starts) > maxSyntheticManifestSegments {
		return nil
	}
	// The source duration is measured from the container's zero, as the
	// keyframe times are.
	durations := keyframes.SegmentDurations(starts, opts.TotalDuration)
	if durations[len(durations)-1] <= 0 {
		// The index runs past the probed duration; the plan can't describe
		// the end of the stream.
		return nil
	}
	return &copySegmentPlan{durations: durations}
}

// plannedSegmentWait is how long a request for a segment FFmpeg hasn't
// reached yet waits. A planned stream doesn't seek-restart forward, so a far
// seek waits for FFmpeg to copy up to it; stream copy runs many times faster
// than playback, and a missing segment can end Safari's native playback.
const plannedSegmentWait = 30 * time.Second

// plannedSegmentRecoveryDecision handles a missing segment in a planned
// session. One FFmpeg hasn't reached is waited for, and recorded as the
// player's position so the throttler doesn't hold FFmpeg back from it. One
// that is gone (pruned behind the player) or will never come (FFmpeg stopped)
// regenerates the stream from the beginning.
func (s *TranscodeSession) plannedSegmentRecoveryDecision(segNum int, decision SegmentRecoveryDecision) SegmentRecoveryDecision {
	progress := decision.Progress
	switch {
	case progress.Restarting:
		decision.Wait = true
		decision.WaitTimeout = activeSegmentWait
		decision.RestartOnTimeout = false
		decision.Reason = "transcode_restarting"
	case progress.Running && segNum > progress.ProducedHead:
		s.mu.Lock()
		if segNum > s.lastRequestedSegment {
			s.lastRequestedSegment = segNum
		}
		s.mu.Unlock()
		decision.Wait = true
		decision.WaitTimeout = plannedSegmentWait
		decision.RestartOnTimeout = false
		decision.Reason = "planned_segment_ahead"
	default:
		decision.Wait = false
		decision.RestartOnTimeout = true
		decision.Reason = "planned_segment_regenerate"
	}
	return decision
}

// manifest returns the plan as a complete VOD playlist for the session's
// segment files, in the same shape as GenerateFullManifest.
func (p *copySegmentPlan) manifest(opts TranscodeOpts, segPrefix, rawQuery string) []byte {
	queryDefinition, suffix, queryVersion := syntheticManifestQuery(len(p.durations), rawQuery)
	segExt := hlsSegmentExtension(opts)
	hlsVersion := 3
	if segExt == ".m4s" {
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
	if segExt == ".m4s" {
		fmt.Fprintf(&buf, "#EXT-X-MAP:URI=\"%sinit.mp4%s\"\n", segPrefix, suffix)
	}
	for i, d := range p.durations {
		fmt.Fprintf(&buf, "#EXTINF:%.6f,\n", d)
		fmt.Fprintf(&buf, "%sseg_%05d%s%s\n", segPrefix, i, segExt, suffix)
	}
	buf.WriteString("#EXT-X-ENDLIST\n")
	return buf.Bytes()
}
