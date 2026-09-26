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
		{name: "too many attempts", modify: func(r *Request) { r.Attempts = make([]Attempt, 5) }},
		{name: "negative timeout", modify: func(r *Request) { r.Attempts = []Attempt{{TimeoutSeconds: -1}} }},
		{name: "timeout past a day", modify: func(r *Request) { r.Attempts = []Attempt{{TimeoutSeconds: 1e12}} }},
		{name: "hardware attempt", modify: func(r *Request) { r.Stats = validStats(); r.Attempts = []Attempt{{Hardware: true}} }},
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
