package mediasample

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func validRequest() Request {
	return Request{
		Input:  "/media/a.mkv",
		Window: &Window{StartSeconds: 10, DurationSeconds: 20},
		Audio:  &AudioOutput{Fingerprint: true},
	}
}

func validStats() *StatsOutput {
	return &StatsOutput{CropWidth: 0.9, CropHeight: 0.8, Width: 480, BlackThresholds: []int{20, 26, 32}}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Request)
		ok     bool
	}{
		{name: "valid", modify: func(*Request) {}, ok: true},
		{name: "silence only", modify: func(r *Request) {
			r.Audio = &AudioOutput{Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.33}}
		}, ok: true},
		{name: "stats only", modify: func(r *Request) { r.Audio, r.Stats = nil, validStats() }, ok: true},
		{name: "keyframes with stats", modify: func(r *Request) { r.Stats = validStats(); r.Window.KeyframesOnly = true }, ok: true},
		{name: "stats without black thresholds", modify: func(r *Request) { r.Stats = validStats(); r.Stats.BlackThresholds = nil }, ok: true},
		{name: "stats crop of zero", modify: func(r *Request) { r.Stats = validStats(); r.Stats.CropWidth = 0 }},
		{name: "stats crop past the picture", modify: func(r *Request) { r.Stats = validStats(); r.Stats.CropHeight = 1.1 }},
		{name: "stats crop NaN", modify: func(r *Request) { r.Stats = validStats(); r.Stats.CropHeight = math.NaN() }},
		{name: "stats odd width", modify: func(r *Request) { r.Stats = validStats(); r.Stats.Width = 481 }},
		{name: "stats zero width", modify: func(r *Request) { r.Stats = validStats(); r.Stats.Width = 0 }},
		{name: "stats width past bound", modify: func(r *Request) { r.Stats = validStats(); r.Stats.Width = 7680 }},
		{name: "stats black threshold past 8 bits", modify: func(r *Request) { r.Stats = validStats(); r.Stats.BlackThresholds = []int{256} }},
		{name: "stats too many black thresholds", modify: func(r *Request) { r.Stats = validStats(); r.Stats.BlackThresholds = make([]int, 9) }},
		{name: "attempts with timeouts", modify: func(r *Request) { r.Attempts = []Attempt{{TimeoutSeconds: 30}, {}} }, ok: true},
		{name: "no input", modify: func(r *Request) { r.Input = "  " }},
		{name: "no sampling mode", modify: func(r *Request) { r.Window = nil }},
		{name: "no output", modify: func(r *Request) { r.Audio = nil }},
		{name: "empty audio output", modify: func(r *Request) { r.Audio = &AudioOutput{} }},
		{name: "negative start", modify: func(r *Request) { r.Window.StartSeconds = -1 }},
		{name: "NaN start", modify: func(r *Request) { r.Window.StartSeconds = math.NaN() }},
		{name: "zero duration", modify: func(r *Request) { r.Window.DurationSeconds = 0 }},
		{name: "infinite duration", modify: func(r *Request) { r.Window.DurationSeconds = math.Inf(1) }},
		{name: "keyframes without video output", modify: func(r *Request) { r.Window.KeyframesOnly = true }},
		{name: "positive noise", modify: func(r *Request) { r.Audio.Silence = &SilenceParams{NoiseDB: 1, MinSeconds: 1} }},
		{name: "noise below bound", modify: func(r *Request) { r.Audio.Silence = &SilenceParams{NoiseDB: -201, MinSeconds: 1} }},
		{name: "zero silence minimum", modify: func(r *Request) { r.Audio.Silence = &SilenceParams{NoiseDB: -50} }},
		{name: "negative threads", modify: func(r *Request) { r.Threads = -1 }},
		{name: "too many threads", modify: func(r *Request) { r.Threads = 65 }},
		{name: "10-bit source", modify: func(r *Request) { r.VideoBitDepth = 10 }, ok: true},
		{name: "negative bit depth", modify: func(r *Request) { r.VideoBitDepth = -1 }},
		{name: "bit depth too high", modify: func(r *Request) { r.VideoBitDepth = 17 }},
		{name: "too many attempts", modify: func(r *Request) { r.Attempts = make([]Attempt, 5) }},
		{name: "negative timeout", modify: func(r *Request) { r.Attempts = []Attempt{{TimeoutSeconds: -1}} }},
		{name: "timeout past a day", modify: func(r *Request) { r.Attempts = []Attempt{{TimeoutSeconds: 1e12}} }},
		{name: "hardware attempt without video", modify: func(r *Request) { r.Attempts = []Attempt{{Hardware: true}, {}} }},
		{name: "stats on hardware", modify: func(r *Request) { r.Stats = validStats(); r.Attempts = []Attempt{{Hardware: true}, {}} }, ok: true},
		{name: "samples on hardware", modify: func(r *Request) { samplesMode(3, 6)(r); r.Attempts = []Attempt{{Hardware: true}, {}} }, ok: true},
		{name: "frame image", modify: atMode(42.5, &ImageOutput{}), ok: true},
		{name: "frame image at zero", modify: atMode(0, &ImageOutput{Width: 320, ToneMap: &ToneMap{}}), ok: true},
		{name: "frame image on hardware", modify: func(r *Request) {
			atMode(1, &ImageOutput{})(r)
			r.Attempts = []Attempt{{Hardware: true, TimeoutSeconds: 8}, {TimeoutSeconds: 10}}
		}, ok: true},
		{name: "frame without output", modify: atMode(1, nil)},
		{name: "negative frame time", modify: atMode(-1, &ImageOutput{})},
		{name: "NaN frame time", modify: atMode(math.NaN(), &ImageOutput{})},
		{name: "frame with stats", modify: func(r *Request) { atMode(1, &ImageOutput{})(r); r.Stats = validStats() }},
		{name: "frame with audio", modify: func(r *Request) { atMode(1, &ImageOutput{})(r); r.Audio = &AudioOutput{Fingerprint: true} }},
		{name: "frame and window", modify: func(r *Request) { atMode(1, &ImageOutput{})(r); r.Window = &Window{DurationSeconds: 1} }},
		{name: "images of a window", modify: func(r *Request) { r.Audio, r.Images = nil, &ImageOutput{} }},
		{name: "odd image width", modify: atMode(1, &ImageOutput{Width: 321})},
		{name: "negative image width", modify: atMode(1, &ImageOutput{Width: -2})},
		{name: "samples with stats", modify: samplesMode(0, 3, 6.5), ok: true},
		{name: "window and samples", modify: func(r *Request) { samplesMode(3)(r); r.Window = &Window{DurationSeconds: 1} }},
		{name: "samples with audio", modify: func(r *Request) { samplesMode(3)(r); r.Audio = &AudioOutput{Fingerprint: true} }},
		{name: "samples without output", modify: func(r *Request) { samplesMode(3)(r); r.Stats = nil }},
		{name: "no samples", modify: samplesMode()},
		{name: "negative sample", modify: samplesMode(-1, 3)},
		{name: "NaN sample", modify: samplesMode(math.NaN())},
		{name: "infinite sample", modify: samplesMode(3, math.Inf(1))},
		{name: "unsorted samples", modify: samplesMode(6, 3)},
		{name: "repeated sample", modify: samplesMode(3, 3)},
		{name: "too many samples", modify: func(r *Request) {
			seconds := make([]float64, maxSamples+1)
			for i := range seconds {
				seconds[i] = float64(i)
			}
			samplesMode(seconds...)(r)
		}},
		{name: "most samples", modify: func(r *Request) {
			seconds := make([]float64, maxSamples)
			for i := range seconds {
				seconds[i] = float64(i)
			}
			samplesMode(seconds...)(r)
		}, ok: true},
		{name: "samples of a path with a line break", modify: func(r *Request) { samplesMode(3)(r); r.Input = "/media/a\n.mkv" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			tt.modify(&req)
			err := req.Validate()
			if tt.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
		})
	}
}

// samplesMode replaces a request's window and audio with samples at the
// given times and a stats output.
// TestVideoBitDepthHintKeepsProbedRequestsValid covers a probe reporting an
// implausible depth, such as 32 for a raw float source: the hint drops it to
// unknown, so the request still validates and software can decode it.
func TestVideoBitDepthHintKeepsProbedRequestsValid(t *testing.T) {
	for _, test := range []struct{ probed, want int }{
		{-1, 0}, {0, 0}, {8, 8}, {10, 10}, {16, 16}, {17, 0}, {32, 0},
	} {
		got := VideoBitDepthHint(test.probed)
		if got != test.want {
			t.Errorf("VideoBitDepthHint(%d) = %d, want %d", test.probed, got, test.want)
		}
		req := validRequest()
		req.Stats = validStats()
		req.VideoBitDepth = got
		if err := req.Validate(); err != nil {
			t.Errorf("request with probed depth %d: %v", test.probed, err)
		}
	}
}

func samplesMode(seconds ...float64) func(*Request) {
	return func(r *Request) {
		r.Window, r.Audio, r.Stats = nil, nil, validStats()
		r.Samples = &Samples{Seconds: seconds}
	}
}

func TestSamplesRequestRoundTripsThroughJSON(t *testing.T) {
	req := Request{Input: "/media/a.mkv", Samples: &Samples{Seconds: []float64{0, 3, 6.5}}, Stats: validStats(), Background: true}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, req) {
		t.Fatalf("decoded %+v, want %+v (json %s)", decoded, req, data)
	}
}

func TestRequestRoundTripsThroughJSON(t *testing.T) {
	req := Request{
		Input:      "/media/a.mkv",
		Window:     &Window{StartSeconds: 1.5, DurationSeconds: 30, KeyframesOnly: true},
		Audio:      &AudioOutput{Fingerprint: true, Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.33}},
		Stats:      validStats(),
		Attempts:   []Attempt{{TimeoutSeconds: 60}},
		Threads:    1,
		Background: true,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, req) {
		t.Fatalf("decoded %+v, want %+v (json %s)", decoded, req, data)
	}
}

// atMode replaces the request's sampling mode and outputs with a single
// frame at seconds and images.
func atMode(seconds float64, images *ImageOutput) func(*Request) {
	return func(r *Request) {
		r.Window, r.Audio, r.Stats = nil, nil, nil
		r.At = &At{Seconds: seconds}
		r.Images = images
	}
}
