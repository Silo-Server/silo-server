package mediasample

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
)

func speechRequest(center bool) Request {
	return Request{
		Input:  "/media/a.mkv",
		Window: &Window{StartSeconds: 600, DurationSeconds: 120},
		Audio:  &AudioOutput{Speech: &SpeechParams{AudioStream: 1, CenterChannel: center}},
	}
}

func TestSpeechRequestValidation(t *testing.T) {
	cases := map[string]func(*Request){
		"fingerprint too": func(r *Request) { r.Audio.Fingerprint = true },
		"silence too":     func(r *Request) { r.Audio.Silence = &SilenceParams{NoiseDB: -50, MinSeconds: 1} },
		"stats too": func(r *Request) {
			r.Stats = &StatsOutput{CropWidth: 1, CropHeight: 1, Width: 64}
		},
		"keyframes only":  func(r *Request) { r.Window.KeyframesOnly = true },
		"stream range":    func(r *Request) { r.Audio.Speech.AudioStream = maxAudioStream + 1 },
		"negative":        func(r *Request) { r.Audio.Speech.AudioStream = -1 },
		"window too long": func(r *Request) { r.Window.DurationSeconds = maxSpeechWindowSeconds + 1 },
	}
	if err := speechRequest(true).Validate(); err != nil {
		t.Fatalf("valid speech request: %v", err)
	}
	for name, mutate := range cases {
		req := speechRequest(false)
		mutate(&req)
		if err := req.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, req)
		}
	}
}

func TestSpeechArgs(t *testing.T) {
	args, stdin, err := buildArgs(speechRequest(true), Attempt{}, hardwareDecode{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if stdin != nil {
		t.Fatalf("speech run feeds stdin")
	}
	want := []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning",
		"-ss", "600", "-i", "/media/a.mkv",
		"-t", "120", "-map", "0:a:1", "-vn", "-sn", "-dn",
		"-af", "pan=mono|c0=FC,highpass=f=200,lowpass=f=3400,aresample=8000:async=1:first_pts=0",
		"-ac", "1", "-f", "s16le", "-acodec", "pcm_s16le", "-",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args\n%q\nwant\n%q", args, want)
	}
	stereo, _, _ := buildArgs(speechRequest(false), Attempt{}, hardwareDecode{}, 0)
	if strings.Contains(strings.Join(stereo, " "), "pan=") {
		t.Fatalf("stereo speech selects a channel: %q", stereo)
	}
}

func TestSpeechCapabilities(t *testing.T) {
	caps := Capabilities{filters: map[string]struct{}{"highpass": {}, "lowpass": {}, "aresample": {}}}
	if err := caps.Require(speechRequest(false)); err != nil {
		t.Fatalf("stereo speech: %v", err)
	}
	if err := caps.Require(speechRequest(true)); err == nil {
		t.Fatal("center-channel speech without pan was accepted")
	}
}

// pcm encodes samples as s16le.
func pcm(samples []int16) []byte {
	out := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(s))
	}
	return out
}

func tone(n int, amplitude float64) []int16 {
	samples := make([]int16, n)
	for i := range samples {
		samples[i] = int16(amplitude * math.Sin(2*math.Pi*440*float64(i)/speechSampleRate))
	}
	return samples
}

func TestSpeechWriterLevels(t *testing.T) {
	// One frame of silence, one of a full-scale sine (-3 dBFS RMS), one of a
	// -40 dBFS sine, and half a frame that still reports a level.
	var samples []int16
	samples = append(samples, make([]int16, speechFrameSamples)...)
	samples = append(samples, tone(speechFrameSamples, 32767)...)
	samples = append(samples, tone(speechFrameSamples, 32767*0.01)...)
	samples = append(samples, tone(speechFrameSamples/2, 32767)...)

	w := newSpeechWriter(0.04)
	data := pcm(samples)
	// Odd-sized writes split samples across calls.
	for len(data) > 0 {
		n := min(len(data), 37)
		if _, err := w.Write(data[:n]); err != nil {
			t.Fatal(err)
		}
		data = data[n:]
	}
	got := w.result(12.5)
	if got.StartSeconds != 12.5 || got.FrameSeconds != SpeechFrameSeconds {
		t.Fatalf("timeline %+v", got)
	}
	want := []byte{0, 97, 57, 97}
	if !reflect.DeepEqual(got.Levels, want) {
		t.Fatalf("levels %v, want %v", got.Levels, want)
	}
}

func TestRunSpeech(t *testing.T) {
	req := speechRequest(false)
	runner := Runner{FFmpegPath: "ffmpeg", Exec: fakeExec(func(_ context.Context, _ []string, stdout, _ io.Writer) error {
		_, err := stdout.Write(pcm(tone(3*speechFrameSamples, 32767)))
		return err
	})}
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Speech == nil || len(result.Speech.Levels) != 3 || result.Speech.StartSeconds != 600 {
		t.Fatalf("speech %+v", result.Speech)
	}
	// The result crosses to and from a node as JSON.
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Result
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Speech, result.Speech) {
		t.Fatalf("round trip %+v, want %+v", decoded.Speech, result.Speech)
	}
}
