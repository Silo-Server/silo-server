package keyframes

import (
	"slices"
	"testing"
)

func TestPlanSegments(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keyframes []float64
		want      []float64
	}{
		{name: "no keyframes", keyframes: nil, want: nil},
		{name: "one keyframe", keyframes: []float64{0}, want: []float64{0}},
		// Regular 1s keyframes: one segment every 2s.
		{name: "regular", keyframes: []float64{0, 1, 2, 3, 4, 5, 6}, want: []float64{0, 2, 4, 6}},
		// A keyframe after the target ends the segment there.
		{name: "late keyframe", keyframes: []float64{0, 1.3, 2.6, 3.9, 5.2}, want: []float64{0, 2.6, 5.2}},
		// After a long gap the targets lag behind, and the next keyframes each
		// start a segment until they catch up, as FFmpeg's muxer does.
		{name: "catching up", keyframes: []float64{0, 7.0, 7.4, 7.8, 9.9}, want: []float64{0, 7.0, 7.4, 7.8, 9.9}},
		// Targets count from the first keyframe, where FFmpeg's output starts.
		{name: "late start", keyframes: []float64{0.5, 1.5, 2.5, 3.5}, want: []float64{0.5, 2.5}},
		// A keyframe exactly on a target cuts there, though a nonzero origin
		// leaves float error in the subtraction.
		{name: "exact targets after a nonzero origin", keyframes: []float64{0.28, 2.28, 4.28, 6.28, 8.28}, want: []float64{0.28, 2.28, 4.28, 6.28, 8.28}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanSegments(tc.keyframes, 2); !slices.Equal(got, tc.want) {
				t.Fatalf("PlanSegments = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSegmentDurations(t *testing.T) {
	got := SegmentDurations([]float64{0, 2.6, 5.2}, 6)
	want := []float64{2.6, 2.6, 0.8}
	for i := range want {
		if diff := got[i] - want[i]; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("SegmentDurations = %v, want %v", got, want)
		}
	}
	if got := SegmentDurations([]float64{0, 5}, 4); got[1] != 0 {
		t.Fatalf("last duration past the end = %v, want 0", got[1])
	}
}

// FFmpeg's HLS muxer measures its targets from the first packet it muxes,
// which is audio when the audio starts before the video (#4049); the
// FFmpeg-in-the-loop tests check the same against FFmpeg itself.
func TestPlanMuxSegments(t *testing.T) {
	keyframes := []float64{0, 1.98, 4, 6, 8}
	video := Track{Type: 1, HasStart: true}
	audio := func(start float64) Track {
		return Track{Type: 2, Start: start, HasStart: true, SampleRate: 48000}
	}
	fromKeyframe, fromAudio := []float64{0, 4, 6, 8}, []float64{0, 1.98, 4, 6, 8}
	for _, tc := range []struct {
		name    string
		tracks  []Track
		reorder int
		frame   float64
		audio   MuxAudio
		want    []float64 // nil: no plan
	}{
		{name: "no audio track", tracks: []Track{video}, want: fromKeyframe},
		{name: "mapped track missing", tracks: []Track{video, audio(-0.021)}, audio: MuxAudio{Track: 1}, want: fromKeyframe},
		{name: "audio starts after the video", tracks: []Track{video, audio(0.01)}, want: fromKeyframe},
		// 1.98s is 2.001s after the audio's first packet, 1.985s after
		// this one's.
		{name: "AAC copied, 21ms ahead", tracks: []Track{video, audio(-0.021)}, want: fromAudio},
		{name: "AC3 copied, 5ms ahead", tracks: []Track{video, audio(-0.005)}, want: fromKeyframe},
		{name: "second audio track, earliest of all", tracks: []Track{video, audio(-0.005), audio(-0.021)}, audio: MuxAudio{Track: 1}, want: fromAudio},
		// The AAC encoder's 1024 samples at 48kHz put the AC3 track 26ms
		// ahead.
		{name: "encoded to AAC", tracks: []Track{video, audio(-0.005)}, audio: MuxAudio{Encoder: AudioAAC}, want: fromAudio},
		// Another encoder's padding isn't known: 1.98s may or may not reach
		// the target.
		{name: "encoded to Opus", tracks: []Track{video, audio(-0.005)}, audio: MuxAudio{Encoder: AudioOtherEncoder}, want: nil},
		// A 2-frame B-frame delay of 80ms muxes the video first.
		{name: "B-frames delay the video past the audio", tracks: []Track{video, audio(-0.021)}, reorder: 2, frame: 0.04, want: fromKeyframe},
		// A delay of at least 20ms may or may not reach 21ms.
		{name: "B-frames may delay the video past the audio", tracks: []Track{video, audio(-0.021)}, reorder: 1, frame: 0.02, want: nil},
		// Too few video frames in the first cluster to tell: B-frames may
		// still mux the video first.
		{name: "reordering unknown", tracks: []Track{video, audio(-0.021)}, reorder: ReorderUnknown, frame: 0.04, want: nil},
		// Encoded to AAC at an unknown rate, FFmpeg's priming may lead the
		// video by more than another encoder's.
		{name: "encoded to AAC at an unknown rate", tracks: []Track{video, {Type: 2, Start: 0.06, HasStart: true}}, audio: MuxAudio{Encoder: AudioAAC}, want: nil},
		// The audio's first packet isn't 0 after FFmpeg's shift, and the
		// muxer would subtract it in another time base.
		{name: "another track starts first", tracks: []Track{video, audio(-0.021), {Type: 17, Start: -1, HasStart: true}}, want: nil},
		// 1.98s is 0.2ms past the target from the audio, too close to call.
		{name: "keyframe on a target from the audio", tracks: []Track{video, audio(-0.0202)}, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx := Index{Keyframes: keyframes, Tracks: tc.tracks, VideoReorder: tc.reorder, VideoFrame: tc.frame}
			got, ok := PlanMuxSegments(idx, 2, tc.audio)
			if !ok {
				got = nil
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("PlanMuxSegments = %v (ok %v), want %v", got, ok, tc.want)
			}
		})
	}
}
