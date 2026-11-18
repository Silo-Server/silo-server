package mediasample

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// The stats chain the credits tail pass measures: software frames and
// downloaded VideoToolbox surfaces are cropped and then scaled, VAAPI surfaces
// are scaled and converted to NV12 on the GPU and then cropped.
const (
	softwareTailChain = "crop=trunc(iw*0.9/2)*2:trunc(ih*0.8/2)*2,scale=480:-2:flags=area,format=yuv420p," +
		"blackframe=amount=0:threshold=20,blackframe=amount=0:threshold=26,blackframe=amount=0:threshold=32,signalstats,metadata=print"
	gpuTailChain = "scale_vaapi=w=534:h=-2:format=nv12,hwdownload,format=nv12,crop=trunc(iw*0.9/2)*2:trunc(ih*0.8/2)*2,format=yuv420p," +
		"blackframe=amount=0:threshold=20,blackframe=amount=0:threshold=26,blackframe=amount=0:threshold=32,signalstats,metadata=print"
	vtTailChain      = "hwdownload,format=nv12," + softwareTailChain
	vt10BitTailChain = "hwdownload,format=p010le," + softwareTailChain
)

// creditsTailRequest is the episode credits tail pass: audio and keyframe
// statistics of one window.
func creditsTailRequest() Request {
	return Request{
		Input:    "/media/show/episode.mkv",
		Window:   &Window{StartSeconds: 2250, DurationSeconds: 450, KeyframesOnly: true},
		Audio:    &AudioOutput{Fingerprint: true, Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.5}},
		Stats:    validStats(),
		Threads:  1,
		Attempts: []Attempt{{Hardware: true}, {}},
	}
}

func TestBuildArgsRejectsHardwareItCannotRun(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		hw   hardwareDecode
		want string
	}{
		{name: "vaapi without a device", req: creditsTailRequest(), hw: hardwareDecode{Accel: "vaapi"}, want: "vaapi requires a render device"},
		{name: "nvenc", req: creditsTailRequest(), hw: hardwareDecode{Accel: "nvenc"}, want: `does not support "nvenc"`},
		{name: "audio only", req: validRequest(), hw: hardwareDecode{Accel: "videotoolbox"}, want: "needs a video output"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := buildArgs(tt.req, Attempt{Hardware: true}, tt.hw, 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want containing %q", err, tt.want)
			}
		})
	}
}

// fakeStatsExec answers each process with the next of its results: a log
// written to stderr, and an error when fail is set.
type fakeStatsExec struct {
	results []fakeStatsResult
	calls   [][]string
}

type fakeStatsResult struct {
	log  string
	fail bool
}

func (f *fakeStatsExec) exec(_ context.Context, _ string, args []string, _ io.Reader, _, stderr io.Writer) error {
	f.calls = append(f.calls, args)
	result := f.results[len(f.calls)-1]
	_, _ = io.WriteString(stderr, result.log)
	if result.fail {
		return errors.New("exit status 1")
	}
	return nil
}
