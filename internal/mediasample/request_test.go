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
		{name: "hardware attempt without video output", modify: func(r *Request) { r.Attempts = []Attempt{{Hardware: true}} }},
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
		Window:     &Window{StartSeconds: 1.5, DurationSeconds: 30},
		Audio:      &AudioOutput{Fingerprint: true, Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.33}},
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
