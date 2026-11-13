package keyframes

import "math"

// PlanSegments returns the start time of each HLS segment for a stream copied
// from a file with these keyframes, cut the way FFmpeg's HLS muxer cuts
// copied video: a segment ends at the first keyframe at or after its target,
// and each cut moves the target on by segmentSeconds. Targets count from the
// first keyframe, where FFmpeg's output starts when its first packet is
// video; PlanMuxSegments covers audio that comes first. Jellyfin's
// DynamicHlsPlaylistGenerator.ComputeSegments uses the same rule.
//
// When keyframes are further apart than segmentSeconds the targets fall
// behind, and the following keyframes each start a segment until they catch
// up, as FFmpeg's muxer does.
func PlanSegments(keyframes []float64, segmentSeconds float64) []float64 {
	if len(keyframes) == 0 || segmentSeconds <= 0 {
		return nil
	}
	starts, _ := planFrom(keyframes, segmentSeconds, keyframes[0])
	return starts
}

// planFrom is PlanSegments with the targets measured from origin, which
// FFmpeg takes from the first packet it muxes (PlanMuxSegments). It reports
// false for an origin before the first keyframe that leaves a keyframe
// within muxTie of a target: rounded to FFmpeg's time bases, it could fall
// on either side.
func planFrom(keyframes []float64, segmentSeconds, origin float64) ([]float64, bool) {
	starts := []float64{keyframes[0]}
	// A keyframe within targetSlack of a target counts as reaching it:
	// FFmpeg compares integer timestamps, where a keyframe exactly on a
	// target reaches it, but a nonzero origin leaves float error ("2.28 -
	// 0.28" isn't quite 2).
	exact := origin == keyframes[0]
	target := segmentSeconds
	for _, k := range keyframes[1:] {
		at := k - origin
		if !exact && math.Abs(at-target) < muxTie {
			return nil, false
		}
		if at >= target-targetSlack {
			starts = append(starts, k)
			target += segmentSeconds
		}
	}
	return starts, true
}

// targetSlack is the float error PlanSegments allows when comparing a
// keyframe with a target: a microsecond, FFmpeg's finest time base and well
// under any frame's duration.
const targetSlack = 1e-6

// SegmentDurations returns each planned segment's duration: up to the next
// segment's start, and for the last one up to the media's end. A last segment
// that would be empty or negative, from an end before the last start, is
// given zero.
func SegmentDurations(starts []float64, end float64) []float64 {
	durations := make([]float64, len(starts))
	for i, start := range starts {
		next := end
		if i+1 < len(starts) {
			next = starts[i+1]
		}
		if next > start {
			durations[i] = next - start
		}
	}
	return durations
}
