package keyframes

import (
	"fmt"
	"io"
	"math"
	"slices"

	"github.com/Silo-Server/silo-server/internal/matroska"
)

// Track is one of a file's tracks as the planning read (readPlanIndex) finds
// it.
type Track struct {
	// Type is the Matroska TrackType.
	Type uint64
	// Start is when the track's first block in the file's first cluster is
	// presented, as FFmpeg's demuxer stamps it (less the track's CodecDelay),
	// in seconds on the Keyframes timeline. HasStart is false when that
	// cluster holds none of the track's blocks.
	Start    float64
	HasStart bool
	// SampleRate is the rate an audio track decodes to, or 0 when unknown.
	SampleRate float64
	// CodecDelay is the track's CodecDelay in seconds.
	CodecDelay float64
}

// MuxAudio is the audio a session muxes with the copied video: the file's
// audio track Track (FFmpeg's 0:a:Track), copied or encoded as Encoder says.
// A file without that track muxes no audio.
type MuxAudio struct {
	Track   int
	Encoder AudioEncoder
}

// AudioEncoder is how a session's audio reaches the muxer.
type AudioEncoder int

const (
	// AudioCopied copies the track's packets.
	AudioCopied AudioEncoder = iota
	// AudioAAC encodes it with FFmpeg's AAC encoder.
	AudioAAC
	// AudioOtherEncoder encodes it with another encoder.
	AudioOtherEncoder
)

const (
	// muxTie is how close two times are that FFmpeg could order either way
	// once they are rounded to its time bases. A keyframe this close to a
	// target measured from an audio packet can't be planned either.
	muxTie = 0.0005
	// aacPriming is the padding, in samples, FFmpeg's AAC encoder puts
	// before the first sample: its first packet is that much before the
	// audio it encodes.
	aacPriming = 1024
	// maxEncoderPriming bounds the padding of the other encoders a session
	// uses (AC3 and E-AC3 256 samples, Opus 312 at 48kHz).
	maxEncoderPriming = 0.05
	// maxReorderFrames bounds the video frames of the first cluster that are
	// checked for B-frame reordering.
	maxReorderFrames = 256
)

// PlanMuxSegments returns the start time of each HLS segment FFmpeg's HLS
// muxer cuts when a session copies the video of the file idx describes, with
// audio, or false when it can't tell (#4049).
//
// The muxer measures its targets from the first packet it receives
// (hlsenc.c: start_pts), not from the first keyframe as PlanSegments does.
// FFmpeg interleaves packets by decode time, so that is an audio packet when
// the audio starts before the video's first decode time: an AAC track
// FFmpeg encoded starts 1024 samples early, and the session's own AAC
// encoder adds as much. A keyframe just under a target then reaches it.
// PlanMuxSegments works out the possible starts and plans from each; when
// they disagree, or a keyframe sits within muxTie of a target, it doesn't
// plan.
func PlanMuxSegments(idx Index, segmentSeconds float64, audio MuxAudio) ([]float64, bool) {
	if len(idx.Keyframes) == 0 || segmentSeconds <= 0 {
		return nil, false
	}
	origins, ok := muxOrigins(idx, audio)
	if !ok {
		return nil, false
	}
	var plan []float64
	for i, origin := range origins {
		starts, ok := planFrom(idx.Keyframes, segmentSeconds, origin)
		if !ok || (i > 0 && !slices.Equal(starts, plan)) {
			return nil, false
		}
		plan = starts
	}
	return plan, true
}

// muxOrigins returns the times, on the Keyframes timeline, the HLS muxer
// could measure its targets from. FFmpeg's muxer takes the first packet's
// timestamp, and replaces an audio packet's with a video packet's that is
// lower. The video's first packet is the first keyframe, decoded its B-frame
// delay early.
//
// An audio origin must also be 0 once FFmpeg has shifted the timestamps
// (-start_at_zero, -avoid_negative_ts): the muxer subtracts it from video
// timestamps in another time base, so only 0 gives a predictable target.
// It is 0 when the audio starts first of every track in the file.
func muxOrigins(idx Index, audio MuxAudio) ([]float64, bool) {
	k0 := idx.Keyframes[0]
	track, ok := mappedAudioTrack(idx.Tracks, audio)
	if !ok || !track.HasStart {
		// No audio, or none in the first cluster: it starts after the video.
		return []float64{k0}, true
	}
	lo, hi := audioMuxStart(track, audio.Encoder)

	// The video's first decode time is the first keyframe less its B-frame
	// delay: at least the reordering the first cluster shows, and without
	// reordering, none.
	videoLatest := k0 - float64(idx.VideoReorder)*idx.VideoFrame
	videoEarliest := k0
	if idx.VideoReorder > 0 {
		videoEarliest = math.Inf(-1)
	}
	if videoLatest < lo-muxTie {
		return []float64{k0}, true
	}

	firstStart := math.Inf(1)
	for _, t := range idx.Tracks {
		if t.HasStart {
			firstStart = min(firstStart, t.Start)
		}
	}
	if hi > firstStart+muxTie {
		// Another track starts before the audio's first packet, which then
		// isn't 0 after FFmpeg's shift.
		return nil, false
	}
	origins := []float64{min(lo, k0), min(hi, k0)}
	if hi >= videoEarliest-muxTie {
		// The video could come first.
		origins = append(origins, k0)
	}
	return origins, true
}

// mappedAudioTrack returns the audio track a session maps, FFmpeg's 0:a:N.
func mappedAudioTrack(tracks []Track, audio MuxAudio) (Track, bool) {
	n := max(audio.Track, 0)
	for _, t := range tracks {
		if t.Type != matroska.TrackTypeAudio {
			continue
		}
		if n == 0 {
			return t, true
		}
		n--
	}
	return Track{}, false
}

// audioMuxStart returns the earliest and latest time the audio's first
// muxed packet can have, copied or encoded by encoder.
func audioMuxStart(track Track, encoder AudioEncoder) (lo, hi float64) {
	if encoder == AudioCopied {
		return track.Start, track.Start
	}
	// A decoder drops a CodecDelay's samples, which moves the first
	// decoded frame up to that much later.
	lo, hi = track.Start, track.Start+track.CodecDelay
	if encoder == AudioAAC && validAACRate(track.SampleRate) {
		priming := aacPriming / track.SampleRate
		return lo - priming, hi - priming
	}
	return lo - maxEncoderPriming, hi
}

// validAACRate reports whether FFmpeg's AAC encoder takes audio at rate as
// it is; any other rate is resampled to one it takes, which changes the
// priming's duration.
func validAACRate(rate float64) bool {
	switch rate {
	case 96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350:
		return true
	}
	return false
}

// trackEntries returns the entries of a Tracks element that FFmpeg's
// Matroska demuxer makes streams of, in order, which is its stream order.
func trackEntries(tracks []byte) []matroska.TrackEntry {
	var entries []matroska.TrackEntry
	matroska.Children(tracks, func(id uint64, entry []byte) {
		if id != idTrackEntry {
			return
		}
		t := matroska.ParseTrackEntry(entry)
		switch t.Type {
		case matroska.TrackTypeVideo, matroska.TrackTypeAudio, matroska.TrackTypeSubtitle:
		default:
			return
		}
		if t.Number == 0 || t.CodecID == "" {
			return
		}
		entries = append(entries, t)
	})
	return entries
}

// readTrackStarts reads the block headers of the cluster at off, the
// file's first, and sets m's Tracks, VideoReorder and VideoFrame from them.
func readTrackStarts(r io.ReaderAt, off int64, m *matroskaIndex) error {
	starts, video, err := clusterBlocks(r, off, m.segEnd, m.video)
	if err != nil {
		return fmt.Errorf("keyframes: read the first cluster at %d: %w", off, err)
	}
	scale := float64(m.timescale) / 1e9
	m.Tracks = make([]Track, len(m.entries))
	for i, e := range m.entries {
		t := Track{Type: e.Type, CodecDelay: float64(e.CodecDelay) / 1e9, SampleRate: e.SamplingFrequency}
		if e.OutputSamplingFrequency > 0 {
			t.SampleRate = e.OutputSamplingFrequency
		}
		if ticks, ok := starts[e.Number]; ok {
			t.Start, t.HasStart = float64(ticks)*scale-t.CodecDelay, true
		}
		m.Tracks[i] = t
	}
	m.VideoReorder = reorderDepth(video)
	m.VideoFrame = m.frameSeconds
	if m.VideoFrame <= 0 {
		sorted := slices.Clone(video)
		slices.Sort(sorted)
		var gap int64
		for i := 1; i < len(sorted); i++ {
			if d := sorted[i] - sorted[i-1]; d > 0 && (gap == 0 || d < gap) {
				gap = d
			}
		}
		m.VideoFrame = float64(gap) * scale
	}
	return nil
}

// clusterBlocks returns, for the cluster at off, each track's earliest block
// timestamp in ticks, and the timestamps of the video track's first blocks
// in the order they are stored, which is decode order.
func clusterBlocks(r io.ReaderAt, off, segEnd int64, video uint64) (map[uint64]int64, []int64, error) {
	rels := map[uint64]int64{}
	var videoRels []int64
	clusterTicks, _, err := walkCluster(r, off, segEnd, func(track uint64, rel int64, _ bool, _ int64) {
		if earliest, ok := rels[track]; !ok || rel < earliest {
			rels[track] = rel
		}
		if track == video && len(videoRels) < maxReorderFrames {
			videoRels = append(videoRels, rel)
		}
	})
	if err != nil {
		return nil, nil, err
	}
	starts := make(map[uint64]int64, len(rels))
	for track, rel := range rels {
		starts[track] = clusterTicks + rel
	}
	for i := range videoRels {
		videoRels[i] += clusterTicks
	}
	return starts, videoRels, nil
}

// reorderDepth returns how many frames, at most, a frame is decoded later
// than its place in presentation order: the B-frame delay a decoder needs to
// output decodeOrder's frames in order. FFmpeg's delay for the stream is at
// least this.
func reorderDepth(decodeOrder []int64) int {
	sorted := slices.Clone(decodeOrder)
	slices.Sort(sorted)
	depth := 0
	for i, t := range decodeOrder {
		// A frame presented at position p but decoded at i is i-p frames
		// late.
		p, _ := slices.BinarySearch(sorted, t)
		depth = max(depth, i-p)
	}
	return depth
}
