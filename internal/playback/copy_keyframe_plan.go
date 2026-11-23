package playback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
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
// the planned segments. A seek restart begins at the same planned boundary
// (see ResolveSegmentRecoveryTarget), so it keeps cutting the same segments
// under the same numbers and the VOD playlist the player holds stays valid.
type copySegmentPlan struct {
	durations []float64
	// origin is the source-timeline start of segment 0. A container whose
	// first video keyframe is later than zero begins there; the served VOD
	// playlist's player time zero is this source instant.
	origin float64
	// seekBias is added to a segment's start when FFmpeg is asked to seek
	// there. FFmpeg's Matroska demuxer resolves a request less than roughly
	// 130 ms into a cluster to the previous cluster, so seeking exactly at a
	// keyframe can leave the replacement generation cutting segments from an
	// earlier keyframe. Requesting a position a little past the boundary keeps
	// the request inside the boundary keyframe's interval; see copySeekBias.
	seekBias []float64
}

// copySeekBiasSeconds is the largest offset added to a planned boundary when
// asking FFmpeg to seek to it. The offset is clamped to half the gap to the
// next keyframe so the request can never cross into the next interval.
const copySeekBiasSeconds = 0.5

// copySeekBias returns the offset to add to the seek request for a segment
// starting at start. A keyframe immediately after the boundary leaves little
// room, so the request moves to the midpoint between the two; the demuxer
// resolves it to the boundary whenever the request lands inside its interval.
// copySeekBiases only calls this with a keyframe after start (or +Inf for the
// final boundary), so the gap is always positive.
func copySeekBias(nextKeyframe, start float64) float64 {
	gap := nextKeyframe - start
	return min(copySeekBiasSeconds, gap/2)
}

// CopyVODPlan is the seekable extent of a planned copy stream. The plan lists
// the whole media as a VOD playlist, so a client may seek anywhere inside
// [Origin, Origin+Duration] and request that segment directly.
type CopyVODPlan struct {
	Origin   float64
	Duration float64
	Segments int
}

// totalDuration is the planned playlist length in source seconds.
func (p *copySegmentPlan) totalDuration() float64 {
	if p == nil {
		return 0
	}
	var total float64
	for _, duration := range p.durations {
		total += duration
	}
	return total
}

// segmentStart returns the source-timeline start of segment segNum.
func (p *copySegmentPlan) segmentStart(segNum int) (float64, bool) {
	if p == nil || segNum < 0 || segNum >= len(p.durations) {
		return 0, false
	}
	start := p.origin
	for i := 0; i < segNum; i++ {
		start += p.durations[i]
	}
	return start, true
}

// seekTarget maps a requested source position to the planned segment that
// contains it. A position before the first segment starts segment 0; one at or
// after the end keeps the last segment so a player seeking to the very end
// still gets bytes.
func (p *copySegmentPlan) seekTarget(position float64) (float64, int, bool) {
	if p == nil || len(p.durations) == 0 {
		return 0, 0, false
	}
	if position <= p.origin {
		return p.origin, 0, true
	}
	start := p.origin
	for i, duration := range p.durations {
		next := start + duration
		if position < next || i == len(p.durations)-1 {
			return start, i, true
		}
		start = next
	}
	return start, len(p.durations) - 1, true
}

// segmentSeekPosition returns the position to pass FFmpeg's -ss to begin
// producing segment segNum. It is the segment's planned start plus its seek
// bias, so the demuxer lands on the boundary keyframe rather than an earlier
// cluster (see copySeekBias). Segment 0 needs no seek: FFmpeg's first keyframe
// is the plan's origin.
func (p *copySegmentPlan) segmentSeekPosition(segNum int) (float64, bool) {
	start, ok := p.segmentStart(segNum)
	if !ok {
		return 0, false
	}
	if segNum == 0 || start <= 0 {
		return 0, true
	}
	if segNum < len(p.seekBias) {
		start += p.seekBias[segNum]
	}
	return start, true
}

// loadKeyframeIndex reads a source's keyframe index, and
// loadVerifiedKeyframeIndex waits for its full check, stopping when ctx is
// done; tests replace both. loadHTTPKeyframeIndex reads a remote relay's
// Matroska index over HTTP Range without downloading the media; its quick
// check leaves VideoEnd unknown, so the planner falls back to the container
// duration for the final segment.
var (
	loadKeyframeIndex         = keyframes.Load
	loadVerifiedKeyframeIndex = keyframes.LoadVerified
	loadHTTPKeyframeIndex     = keyframes.ReadHTTP
)

// httpKeyframeTimeout bounds one remote index read. A relay that cannot
// answer the small metadata ranges inside this budget falls back to FFmpeg's
// growing playlist rather than delaying playback indefinitely.
const httpKeyframeTimeout = 20 * time.Second

var httpKeyframeClient = &http.Client{Timeout: httpKeyframeTimeout}

func isHTTPSource(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// loadCopyKeyframeIndex reads the index the plan is built from. A remote
// source is read through its range-capable relay; local sources keep the
// on-disk reader, and a rebuild waits for the full check so it reproduces the
// playlist the player already holds.
func loadCopyKeyframeIndex(ctx context.Context, opts TranscodeOpts) (keyframes.Index, error) {
	if isHTTPSource(opts.InputPath) {
		readCtx, cancel := context.WithTimeout(ctx, httpKeyframeTimeout)
		defer cancel()
		return loadHTTPKeyframeIndex(readCtx, httpKeyframeClient, opts.InputPath)
	}
	if opts.Reconstructed {
		return loadVerifiedKeyframeIndex(ctx, opts.InputPath)
	}
	return loadKeyframeIndex(opts.InputPath)
}

// planCopySegments returns the segment plan for a session, or nil when the
// session keeps FFmpeg's real playlist: the setting is off, the video isn't
// copied, or the source has no usable keyframe index. A non-nil error means
// the index read failed transiently and the decision must not be recorded, so
// a later playback can retry.
func planCopySegments(ctx context.Context, opts TranscodeOpts) (*copySegmentPlan, error) {
	if !opts.KeyframePlaylist ||
		!strings.EqualFold(opts.TargetCodecVideo, "copy") ||
		!CanGenerateSyntheticManifest(opts.TotalDuration, opts.SegmentDuration) {
		return nil, nil
	}
	idx, err := loadCopyKeyframeIndex(ctx, opts)
	if err != nil {
		if errors.Is(err, keyframes.ErrTransient) {
			return nil, err
		}
		if !errors.Is(err, keyframes.ErrNoIndex) {
			log.Printf("playback: read keyframe index for %s: %v; using FFmpeg's playlist", opts.SessionID, err)
		}
		return nil, nil
	}
	segmentSeconds := float64(opts.SegmentDuration)
	if segmentSeconds <= 0 {
		segmentSeconds = defaultSegmentDuration
	}
	starts := keyframes.PlanSegments(idx.Keyframes, segmentSeconds)
	if len(starts) == 0 || len(starts) > maxSyntheticManifestSegments {
		return nil, nil
	}
	// FFmpeg's last segment ends with the copied video, even when an audio
	// or subtitle track runs on: the container's duration can be longer. A
	// remote quick read cannot see the video's end, so it uses the container
	// duration; the final playlist entry may then cover a trailing audio
	// tail that FFmpeg's last fragment does not.
	end := idx.VideoEnd
	if end <= idx.Keyframes[len(idx.Keyframes)-1] {
		end = idx.Duration
	}
	if end <= starts[len(starts)-1] {
		return nil, nil
	}
	return &copySegmentPlan{
		durations: keyframes.SegmentDurations(starts, end),
		origin:    starts[0],
		seekBias:  copySeekBiases(idx.Keyframes, starts),
	}, nil
}

// copySeekBiases returns each planned segment's seek bias: half the gap from
// the segment start to the next source keyframe, capped at copySeekBiasSeconds.
// A boundary with no later keyframe gets the full bias.
func copySeekBiases(sourceKeyframes, starts []float64) []float64 {
	biases := make([]float64, len(starts))
	next := 0
	for i, start := range starts {
		for next < len(sourceKeyframes) && sourceKeyframes[next] <= start+1e-6 {
			next++
		}
		nextKeyframe := math.Inf(1)
		if next < len(sourceKeyframes) {
			nextKeyframe = sourceKeyframes[next]
		}
		biases[i] = copySeekBias(nextKeyframe, start)
	}
	return biases
}

// copyPlanRecordFile keeps a session's planning decision in its output
// directory, which a session rebuilt from its recipe reuses.
const copyPlanRecordFile = "keyframe-plan.json"

type copyPlanRecord struct {
	Planned   bool      `json:"planned"`
	Durations []float64 `json:"durations,omitempty"`
	Origin    float64   `json:"origin,omitempty"`
	SeekBias  []float64 `json:"seek_bias,omitempty"`
}

// resolveCopyPlan returns the plan a session serves. The first start decides
// and records the decision; a rebuilt session follows the record, so it
// serves the playlist its player already has. Deciding afresh could differ:
// a file's index that was still being checked may be verified by then.
//
// The record lives in the output directory, which a clean shutdown removes.
// A rebuilt session without one plans again from the verified index (see
// planCopySegments), which gives the same plan as the first start. If that
// start had fallen back to FFmpeg's playlist, the rebuilt session now plans:
// the complete playlist is the same whether FFmpeg started at its first
// segment or at the seek target, because every segment is cut on the same
// planned keyframe boundary.
func resolveCopyPlan(ctx context.Context, opts TranscodeOpts) *copySegmentPlan {
	if !opts.KeyframePlaylist || opts.OutputDir == "" {
		plan, _ := planCopySegments(ctx, opts)
		return plan
	}
	path := filepath.Join(opts.OutputDir, copyPlanRecordFile)
	//nolint:nestif // upstream resolveCopyPlan in copy_keyframe_plan.go exceeds Vio's nestif complexity-4 limit; refactor tracked separately
	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // Path is the transcode output directory's own plan record.
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
	plan, planErr := planCopySegments(ctx, opts)
	if planErr != nil {
		// A transient range read is not a verdict about the source. Leave no
		// record so the next playback retries instead of permanently falling
		// back to FFmpeg's growing playlist.
		log.Printf("playback: read keyframe index for %s: %v; retrying on the next playback", opts.SessionID, planErr)
		return nil
	}
	if plan == nil && opts.Reconstructed && ctx.Err() != nil {
		// The rebuild's request went away during the keyframe check. Its
		// outcome says nothing about the file, so don't record it:
		// StartTranscode abandons this rebuild, and the next one plans.
		return nil
	}
	rec := copyPlanRecord{Planned: plan != nil}
	if plan != nil {
		rec.Durations = plan.durations
		rec.Origin = plan.origin
		rec.SeekBias = plan.seekBias
	}
	data, err := json.Marshal(rec)
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
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
	// A record written before seek biases existed has none; the boundary
	// itself still works, with the demuxer's own cluster rounding.
	biases := r.SeekBias
	if len(biases) != len(r.Durations) {
		biases = nil
	}
	return &copySegmentPlan{durations: r.Durations, origin: r.Origin, seekBias: biases}
}

// plannedSegmentWait is how long a request for the next segment FFmpeg has
// not produced yet waits. Sequential playback asks for these constantly; a
// copy stream produces them far faster than real time, so a short wait avoids
// killing a healthy copy for a fragment that is about to appear.
const plannedSegmentWait = 12 * time.Second

// plannedSegmentPrefetchSegments is how far past the produced head a request
// is still read-ahead rather than a seek. Players buffer several segments while
// playing in order; a request beyond that window names a position FFmpeg would
// have to copy the whole prefix to reach, so the session restarts at it instead
// of waiting.
const plannedSegmentPrefetchSegments = 4

// plannedSegmentRecoveryDecision handles a missing segment in a planned
// session. A segment just past the produced head is waited for and recorded
// as the player's position so the throttler runs FFmpeg on to it. Anything
// further away, or behind the produced window, is a real seek: FFmpeg is
// restarted in place at the plan's segment boundary, and the complete VOD
// playlist the client already holds stays valid. The transport is not rebuilt.
func (s *TranscodeSession) plannedSegmentRecoveryDecision(segNum int, decision SegmentRecoveryDecision) SegmentRecoveryDecision {
	progress := decision.Progress
	switch {
	case progress.Restarting:
		// A request past the prefetch window that arrived after the restart in
		// flight is a newer seek the restart will not produce. It joins that
		// restart's coalescing instead of waiting out the head budget and
		// leaving the generation at the older target. A plan-level restart has
		// no originating request, so its own target is the reference.
		newerThanRestart := decision.RestartSuperseded ||
			(decision.RestartPlanLevel && decision.BeyondWaitReach &&
				segNum > decision.RestartTargetSegment+plannedSegmentPrefetchSegments)
		if newerThanRestart && segNum > progress.ProducedHead+plannedSegmentPrefetchSegments {
			decision.BeyondWaitReach = true
			decision.Wait = false
			decision.RestartOnTimeout = true
			decision.Reason = "planned_segment_seek"
			break
		}
		decision.Wait = true
		decision.WaitTimeout = activeSegmentWait
		decision.RestartOnTimeout = false
		decision.Reason = segmentReasonRestarting
	case progress.Running && segNum > progress.ProducedHead &&
		segNum <= progress.ProducedHead+plannedSegmentPrefetchSegments:
		s.notePlannedSegmentRequest(segNum)
		decision.Wait = true
		decision.WaitTimeout = plannedSegmentWait
		decision.RestartOnTimeout = false
		decision.Reason = "planned_segment_ahead"
	default:
		// A far forward target, a pruned segment behind the player, or a
		// stopped producer: restart at the plan's exact segment start. Only a
		// forward target is a viewer seek that supersedes an older wait.
		decision.BeyondWaitReach = segNum > progress.ProducedHead
		decision.Wait = false
		decision.RestartOnTimeout = true
		decision.Reason = "planned_segment_seek"
	}
	return decision
}

// notePlannedSegmentRequest records a request for planned segment segNum,
// which FFmpeg hasn't reached, as the player's position, so the throttler
// lets FFmpeg run on to it rather than hold the request until it times out.
func (s *TranscodeSession) notePlannedSegmentRequest(segNum int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if segNum > s.lastRequestedSegment {
		s.lastRequestedSegment = segNum
	}
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
