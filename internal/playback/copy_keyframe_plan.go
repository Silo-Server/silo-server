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
	// FFmpeg's last segment ends with the copied video, even when an audio
	// or subtitle track runs on: the container's duration can be longer.
	if idx.VideoEnd <= idx.Keyframes[len(idx.Keyframes)-1] {
		// The video's end is unknown, so the plan can't describe the last
		// segment.
		return nil
	}
	durations := keyframes.SegmentDurations(starts, idx.VideoEnd)
	return &copySegmentPlan{durations: durations}
}

// copyPlanRecordFile keeps a session's planning decision in its output
// directory, which a session rebuilt from its recipe reuses.
const copyPlanRecordFile = "keyframe-plan.json"

type copyPlanRecord struct {
	Planned   bool      `json:"planned"`
	Durations []float64 `json:"durations,omitempty"`
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

	// Record the decision whatever the start's fate: StartTranscode launches
	// FFmpeg even if its caller has gone, and that session serves this
	// playlist.
	plan := planCopySegments(opts)
	rec := copyPlanRecord{Planned: plan != nil}
	if plan != nil {
		rec.Durations = plan.durations
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
	if len(r.Durations) == 0 {
		return nil
	}
	for _, d := range r.Durations {
		if !(d > 0) || math.IsInf(d, 0) {
			return nil
		}
	}
	return &copySegmentPlan{durations: r.Durations}
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
		decision.Reason = segmentReasonRestarting
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
