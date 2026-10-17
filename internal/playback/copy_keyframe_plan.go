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
	"slices"
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
// a playback may have rejected the file's index since (keyframes.Reject).
//
// The record lives in the output directory, which a clean shutdown removes.
// A rebuilt session without one plans again from the index, which gives the
// same plan as the first start unless the file has been rejected. If that
// start had fallen back to FFmpeg's playlist, the rebuilt session may now
// plan: a planned session copies from the beginning, where FFmpeg cuts the
// same segments, so the player's earlier segments still match.
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

// Tolerances for comparing a segment FFmpeg wrote with its planned duration.
// FFmpeg measures the last segment to a late packet, within a frame of the
// video's end; a frame at 20 fps is 50ms.
const (
	planSegmentTolerance     = 0.002
	planLastSegmentTolerance = 0.05
)

// checkCopyPlan compares the segments FFmpeg has written, from its own
// playlist, with the plan. complete says FFmpeg finished the stream cleanly
// and ended its playlist; the playlist must then list every planned segment.
// Otherwise the last segment listed isn't compared: a run that failed, for
// example on a read error, ends its last segment early, which says nothing
// about the file's Cues. A segment past the plan's end always counts. The plan comes from the file's Cues, which only a
// few sampled clusters vouch for (#2453). Cues that leave out keyframes
// elsewhere make FFmpeg cut where the plan doesn't, so the first mismatch
// rejects the file (keyframes.Reject): later playbacks keep FFmpeg's
// playlist. This session goes on serving the plan, which its player already
// has.
func (s *TranscodeSession) checkCopyPlan(entries []manifestSegmentEntry, complete bool) {
	durations := s.copyPlan.durations
	s.mu.Lock()
	if s.copyPlanRejected {
		s.mu.Unlock()
		return
	}
	var mismatch string
	seen := make([]bool, len(durations))
	for i, entry := range entries {
		if entry.number < 0 {
			continue
		}
		if entry.number >= len(durations) {
			mismatch = fmt.Sprintf("FFmpeg wrote segment %d, past the keyframe playlist's %d", entry.number, len(durations))
			break
		}
		seen[entry.number] = true
		if !complete && i == len(entries)-1 {
			break
		}
		tolerance := planSegmentTolerance
		if entry.number == len(durations)-1 {
			tolerance = planLastSegmentTolerance
		}
		if math.Abs(entry.duration-durations[entry.number]) > tolerance {
			mismatch = fmt.Sprintf("FFmpeg wrote segment %d as %.3fs, the keyframe playlist has %.3fs",
				entry.number, entry.duration, durations[entry.number])
			break
		}
	}
	if mismatch == "" && complete {
		if missing := slices.Index(seen, false); missing >= 0 {
			mismatch = fmt.Sprintf("FFmpeg finished without segment %d of the keyframe playlist's %d", missing, len(durations))
		}
	}
	if mismatch == "" {
		s.mu.Unlock()
		return
	}
	s.copyPlanRejected = true
	opts := s.opts
	s.mu.Unlock()

	log.Printf("playback: session %s: %s; later playbacks of %s use FFmpeg's playlist", opts.SessionID, mismatch, opts.InputPath)
	if err := keyframes.Reject(opts.InputPath); err != nil {
		log.Printf("playback: reject keyframe index for %s: %v", opts.SessionID, err)
	}
}

// checkCopyPlanOutput runs checkCopyPlan on FFmpeg's playlist as an FFmpeg
// process exits, having finished, failed (waitErr) or been stopped. Nothing
// else need read the session's progress (SegmentProgress) for its plan to be
// checked.
func (s *TranscodeSession) checkCopyPlanOutput(waitErr error) {
	if s.copyPlan == nil {
		return
	}
	manifest, err := os.ReadFile(filepath.Join(s.outputDir, "stream.m3u8"))
	if err != nil {
		return
	}
	timeline, err := parseManifestTimeline(manifest)
	if err != nil {
		return
	}
	s.checkCopyPlan(timeline.entries, waitErr == nil && ffmpegPlaylistComplete(manifest))
}

// ffmpegPlaylistComplete reports whether FFmpeg finished its playlist. Its
// HLS muxer ends it only when the stream ends, not when it is killed.
func ffmpegPlaylistComplete(manifest []byte) bool {
	return bytes.Contains(manifest, []byte("#EXT-X-ENDLIST"))
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
