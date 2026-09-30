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
	if len(keyframes) == 0 || segmentSeconds <= 0 {
		return nil
	}
	starts := []float64{keyframes[0]}
	target := keyframes[0] + segmentSeconds
	for _, k := range keyframes[1:] {
		if k >= target {
			starts = append(starts, k)
			target += segmentSeconds
		}
	}
	return starts
}

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
