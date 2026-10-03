package keyframes

// PlanSegments returns the start time of each HLS segment for a stream copied
// from a file with these keyframes, cut the way FFmpeg's HLS muxer cuts
// copied video: a segment ends at the first keyframe at or after its target,
// and each cut moves the target on by segmentSeconds. Targets count from the
// first keyframe, where FFmpeg's output starts. Jellyfin's
// DynamicHlsPlaylistGenerator.ComputeSegments uses the same rule.
//
// When keyframes are further apart than segmentSeconds the targets fall
// behind, and the following keyframes each start a segment until they catch
// up, as FFmpeg's muxer does.
func PlanSegments(keyframes []float64, segmentSeconds float64) []float64 {
	indices := PlanSegmentIndices(keyframes, segmentSeconds)
	starts := make([]float64, len(indices))
	for i, k := range indices {
		starts[i] = keyframes[k]
	}
	if len(starts) == 0 {
		return nil
	}
	return starts
}

// PlanSegmentIndices is PlanSegments returning each segment's first keyframe
// as an index into keyframes.
func PlanSegmentIndices(keyframes []float64, segmentSeconds float64) []int {
	if len(keyframes) == 0 || segmentSeconds <= 0 {
		return nil
	}
	starts := []int{0}
	// Targets are measured from the first keyframe, and a keyframe within
	// targetSlack of one counts as reaching it: FFmpeg compares integer
	// timestamps, where a keyframe exactly on a target reaches it, but a
	// nonzero origin leaves float error ("2.28 - 0.28" isn't quite 2).
	target := segmentSeconds
	for i, k := range keyframes[1:] {
		if k-keyframes[0] >= target-targetSlack {
			starts = append(starts, i+1)
			target += segmentSeconds
		}
	}
	return starts
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
