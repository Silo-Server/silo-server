package intromarkers

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// stats builds a keyframe: PBlack at luma 20/26/32, then luma min, low,
// average, high, max, and saturation low, average, high, max.
func stats(pb20, pb26, pb32 uint8, y [5]float32, sat [4]float32) mediasample.FrameStats {
	return mediasample.FrameStats{
		PBlack: []uint8{pb20, pb26, pb32},
		YMin:   y[0], YLow: y[1], YAvg: y[2], YHigh: y[3], YMax: y[4],
		SatLow: sat[0], SatAvg: sat[1], SatHigh: sat[2], SatMax: sat[3],
	}
}

// The real keyframes below come from frame-checked validation files, with
// their tail's black level. The harmful ones were classed as credits text by
// earlier rules and started credits inside the story.
func TestClassifyKeyframe(t *testing.T) {
	tests := []struct {
		name       string
		frame      mediasample.FrameStats
		blackLevel float64
		want       keyframeClass
	}{
		// Real credits.
		{"credits text on black", stats(99, 99, 99, [5]float32{15, 15, 16.45, 16, 160}, [4]float32{0, 0.33, 1, 1}), 15, keyframeLettered},
		{"black between credits", stats(100, 100, 100, [5]float32{15, 15, 15.74, 16, 16}, [4]float32{0, 0.33, 1, 1}), 15, keyframeBlack},
		{"clock card on black", stats(98, 98, 98, [5]float32{16, 16, 17.61, 16, 136}, [4]float32{0, 0.58, 0, 42}), 16, keyframeLettered},
		{"dense credit columns on black", stats(78, 80, 82, [5]float32{3, 16, 37, 116, 237}, [4]float32{0, 0, 0, 0}), 16, keyframeMostlyBlack},
		{"dark text on a light card", stats(0, 0, 1, [5]float32{28, 217, 208.21, 217, 218}, [4]float32{0, 0, 0, 0}), 30, keyframeCard},
		{"text on a colored card", stats(0, 0, 0, [5]float32{18, 216, 216.38, 217, 217}, [4]float32{73, 72.92, 73, 73}), 16, keyframeCard},

		// Harmful under earlier rules.
		{"dark fading shot with bright fire", stats(44, 82, 91, [5]float32{17, 18, 24.41, 30, 160}, [4]float32{1, 1.81, 2, 26}), 15, keyframeContent},
		{"fade nearly black with fire", stats(58, 89, 96, [5]float32{17, 18, 21, 26, 159}, [4]float32{1, 1.27, 2, 23}), 15, keyframeContent},
		{"dark scene with one highlight", stats(0, 81, 94, [5]float32{19, 21, 24.41, 28, 220}, [4]float32{1, 2, 3, 33}), 16, keyframeContent},
		{"spotlit dark stage", stats(51, 97, 98, [5]float32{17, 18, 21.11, 21, 187}, [4]float32{3, 4.55, 6, 15}), 16, keyframeContent},
		// A dark letterboxed night scene still passes as text on black; only
		// the video-only end-of-file bound keeps it out (see the combination
		// cases).
		{"dark letterboxed night scene", stats(10, 82, 87, [5]float32{16, 19, 25.2, 37, 168}, [4]float32{0, 1.76, 5, 43}), 17, keyframeLettered},

		// Each rule on its own.
		{"strict threshold follows a low black level", stats(74, 99, 99, [5]float32{16, 16, 20, 16, 200}, [4]float32{}), 16, keyframeContent},
		{"strict threshold 26 above black level 16", stats(10, 75, 99, [5]float32{17, 17, 20, 17, 200}, [4]float32{}), 17, keyframeLettered},
		{"strict threshold 26 up to black level 22", stats(10, 74, 99, [5]float32{22, 22, 25, 22, 200}, [4]float32{}), 22, keyframeContent},
		{"strict threshold 32 above black level 22", stats(0, 0, 99, [5]float32{23, 23, 25, 23, 200}, [4]float32{}), 23, keyframeLettered},
		{"too little black for a black background", stats(99, 99, 84, [5]float32{16, 16, 20, 16, 200}, [4]float32{}), 16, keyframeMostlyBlack},
		{"too little black", stats(99, 99, 69, [5]float32{16, 16, 20, 16, 200}, [4]float32{}), 16, keyframeContent},
		{"mostly black but not strict black", stats(74, 99, 84, [5]float32{16, 16, 20, 16, 200}, [4]float32{}), 16, keyframeContent},
		{"background above the black level", stats(99, 99, 99, [5]float32{16, 19, 20, 19, 200}, [4]float32{}), 16, keyframeContent},
		{"saturated dark background", stats(99, 99, 99, [5]float32{16, 16, 20, 16, 200}, [4]float32{10, 12, 14, 20}), 16, keyframeContent},
		{"text contrast just enough", stats(99, 99, 99, [5]float32{16, 16, 20, 16, 76}, [4]float32{}), 16, keyframeLettered},
		{"black without text", stats(99, 99, 99, [5]float32{16, 16, 20, 16, 75}, [4]float32{}), 16, keyframeBlack},
		{"card background not flat", stats(0, 0, 0, [5]float32{20, 200, 205, 209, 255}, [4]float32{}), 16, keyframeContent},
		{"card text too faint", stats(0, 0, 0, [5]float32{150, 200, 202, 204, 250}, [4]float32{}), 16, keyframeContent},
		{"card too saturated", stats(0, 0, 0, [5]float32{20, 200, 202, 204, 204}, [4]float32{96, 96, 96, 96}), 16, keyframeContent},
		{"flat dark night sky with a bright light", stats(0, 0, 0, [5]float32{30, 39, 40, 41, 200}, [4]float32{}), 16, keyframeContent},
		{"card just lighter than black", stats(0, 0, 0, [5]float32{0, 40, 41, 42, 200}, [4]float32{}), 16, keyframeCard},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyKeyframe(tt.frame, tt.blackLevel); got != tt.want {
				t.Fatalf("class = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTailBlackLevel(t *testing.T) {
	frames := func(lows ...float32) []mediasample.FrameStats {
		out := make([]mediasample.FrameStats, len(lows))
		for i, low := range lows {
			out[i].YLow = low
		}
		return out
	}
	if got := tailBlackLevel(frames(40, 17, 16, 90)); got != 16 {
		t.Fatalf("black level of a short tail = %v, want its darkest keyframe", got)
	}
	many := make([]float32, 300)
	for i := range many {
		many[i] = 50
	}
	many[0], many[1], many[2], many[3] = 1, 2, 16, 17
	// The 1st percentile of 300 keyframes is the third darkest, skipping
	// two broken ones.
	if got := tailBlackLevel(frames(many...)); got != 16 {
		t.Fatalf("black level = %v, want 16", got)
	}
	if got := tailBlackLevel(frames(45, 50, 60)); got != maxBlackLevel {
		t.Fatalf("black level of a bright tail = %v, want the cap %v", got, maxBlackLevel)
	}
}

func TestClassifyKeyframesDropsIncompleteFrames(t *testing.T) {
	frames := []mediasample.FrameStats{
		{Seconds: 10, PBlack: []uint8{99, 99, 99}, YMin: 16, YLow: 16, YHigh: 16, YMax: 200},
		{Seconds: 11, PBlack: []uint8{99}, YMin: 16, YLow: 16, YHigh: 16, YMax: 200},
	}
	got := classifyKeyframes(frames)
	if want := []creditsKeyframe{{Seconds: 10, Class: keyframeLettered}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keyframes %+v, want %+v", got, want)
	}
}

// keyframes builds classified keyframes from "seconds class" pairs: c
// content, b black, L lettered, C card.
func keyframes(t *testing.T, spec string) []creditsKeyframe {
	t.Helper()
	parsed, err := parseKeyframes(spec)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// every returns tokens for keyframes of class from start to end, step apart.
func every(start, end, step float64, class string) string {
	out := ""
	for s := start; s <= end+1e-9; s += step {
		out += " " + formatFixtureSeconds(s) + class
	}
	return out
}

func TestBuildRuns(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want []creditsRun
	}{
		{
			name: "text on black with black between",
			spec: every(0, 5, 1, "c") + every(6, 20, 2, "L") + " 21b 22b" + every(23, 30, 1, "L") + every(31, 40, 1, "c"),
			want: []creditsRun{{Start: 6, End: 30, First: 6, Last: 23, Text: 16, Lettered: 16}},
		},
		{
			name: "a gap over 20 s ends a run",
			spec: every(0, 20, 1, "L") + " 41L" + every(42, 60, 1, "L"),
			want: []creditsRun{
				{Start: 0, End: 20, First: 0, Last: 20, Text: 21, Lettered: 21},
				{Start: 41, End: 60, First: 21, Last: 40, Text: 20, Lettered: 20},
			},
		},
		{
			name: "story keyframes within 20 s of credits stay inside",
			spec: every(0, 10, 1, "L") + every(11, 14, 1, "c") + every(15, 20, 1, "C"),
			want: []creditsRun{{Start: 0, End: 20, First: 0, Last: 20, Text: 17, Lettered: 11}},
		},
		{
			name: "a run ends on its last text keyframe",
			spec: every(0, 20, 1, "L") + every(21, 30, 1, "b"),
			want: []creditsRun{{Start: 0, End: 20, First: 0, Last: 20, Text: 21, Lettered: 21}},
		},
		{name: "shorter than 15 s", spec: every(0, 14, 1, "L")},
		{name: "fewer than three text keyframes", spec: " 0L 10b 20L"},
		{name: "text is half the run or less", spec: " 0L 1c 2c 5L 6c 7c 10L 11c 12c 15L 16c 17c 20L 21c 22c 25L"},
		{name: "a black keyframe does not start a run", spec: every(0, 30, 1, "b")},
		{
			name: "mostly black keyframes join text",
			spec: every(0, 20, 1, "L") + every(22, 44, 3, "m") + every(46, 60, 1, "L"),
			want: []creditsRun{{Start: 0, End: 60, First: 0, Last: 43, Text: 36, Lettered: 36}},
		},
		{
			name: "mostly black keyframes do not count against the text share",
			spec: " 0L" + every(1, 16, 1, "m") + " 17L 18c 19L",
			want: []creditsRun{{Start: 0, End: 19, First: 0, Last: 19, Text: 3, Lettered: 3}},
		},
		{
			name: "a run does not end on mostly black keyframes",
			spec: every(0, 20, 1, "L") + every(21, 30, 1, "m"),
			want: []creditsRun{{Start: 0, End: 20, First: 0, Last: 20, Text: 21, Lettered: 21}},
		},
		{name: "a mostly black keyframe does not start a run", spec: every(0, 30, 1, "m")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildRuns(keyframes(t, tt.spec))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("runs\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestClusterRuns(t *testing.T) {
	runs := []creditsRun{
		{Start: 100, End: 130, First: 0, Last: 30, Text: 30, Lettered: 30},
		{Start: 150, End: 170, First: 40, Last: 60, Text: 20, Lettered: 0},
		{Start: 200, End: 230, First: 80, Last: 110, Text: 30, Lettered: 30},
	}
	want := []creditsRun{
		{Start: 100, End: 170, First: 0, Last: 60, Text: 50, Lettered: 30},
		{Start: 200, End: 230, First: 80, Last: 110, Text: 30, Lettered: 30},
	}
	if got := clusterRuns(runs); !reflect.DeepEqual(got, want) {
		t.Fatalf("clusters\n got %+v\nwant %+v", got, want)
	}
}
