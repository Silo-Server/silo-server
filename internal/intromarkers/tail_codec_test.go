package intromarkers

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

func TestCreditsTailRoundTrip(t *testing.T) {
	const windowStart = 2554
	tail := creditsTail{
		Frames: []mediasample.FrameStats{
			{Seconds: 2554.004, PBlack: []uint8{2, 50, 68}, YMin: 17, YLow: 21, YAvg: 31.63, YHigh: 50, YMax: 161, SatLow: 1, SatAvg: 3.1, SatHigh: 8, SatMax: 24},
			{Seconds: 2923.003, PBlack: []uint8{99, 99, 99}, YMin: 15, YLow: 15, YAvg: 16.45, YHigh: 16, YMax: 160, SatAvg: 95.999},
		},
		Silences: []mediasample.Interval{{Start: 2600.25, End: 2601.5}, {Start: 3001.251}},
	}
	payload, err := encodeCreditsTail(tail, windowStart, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := tailHeaderBytes + 2*18 + 2*tailSilenceBytes; len(payload) != want {
		t.Fatalf("payload is %d bytes, want %d (18 per frame)", len(payload), want)
	}
	got, err := decodeCreditsTail(payload, windowStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Frames) != 2 || !reflect.DeepEqual(got.Silences, tail.Silences) {
		t.Fatalf("decoded %+v", got)
	}
	for i, frame := range got.Frames {
		want := tail.Frames[i]
		if math.Abs(frame.Seconds-want.Seconds) > 0.0005 || !reflect.DeepEqual(frame.PBlack, want.PBlack) ||
			frame.YMin != want.YMin || frame.YLow != want.YLow || frame.YHigh != want.YHigh || frame.YMax != want.YMax ||
			frame.SatLow != want.SatLow || frame.SatHigh != want.SatHigh || frame.SatMax != want.SatMax ||
			math.Abs(float64(frame.YAvg-want.YAvg)) >= 1.0/averageScale || math.Abs(float64(frame.SatAvg-want.SatAvg)) >= 1.0/averageScale {
			t.Fatalf("frame %d decoded as %+v, want %+v", i, frame, want)
		}
		// Averages round down, so they stay on the same side of any
		// threshold on a 1/256 step.
		if frame.SatAvg > want.SatAvg || frame.YAvg > want.YAvg {
			t.Fatalf("frame %d averages rounded up: %+v", i, frame)
		}
	}
	if got.Frames[1].SatAvg >= maxCardSaturation {
		t.Fatalf("saturation %v crossed the card threshold", got.Frames[1].SatAvg)
	}
}

func TestDecodeCreditsTailRejectsDamagedPayloads(t *testing.T) {
	payload, err := encodeCreditsTail(creditsTail{
		Frames:   []mediasample.FrameStats{{Seconds: 10, PBlack: []uint8{1, 2, 3}}},
		Silences: []mediasample.Interval{{Start: 11, End: 12}},
	}, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	version := append([]byte(nil), payload...)
	version[0] = 2
	for name, damaged := range map[string][]byte{
		"empty":         nil,
		"short header":  payload[:5],
		"truncated":     payload[:len(payload)-1],
		"trailing byte": append(append([]byte(nil), payload...), 0),
		"newer version": version,
	} {
		if _, err := decodeCreditsTail(damaged, 0); err == nil {
			t.Errorf("%s: decoded without an error", name)
		}
	}
}

func TestEncodeCreditsTailRejectsMismatchedFrames(t *testing.T) {
	_, err := encodeCreditsTail(creditsTail{Frames: []mediasample.FrameStats{{Seconds: 1, PBlack: []uint8{1}}}}, 0, 3)
	if err == nil || !strings.Contains(err.Error(), "black measurements") {
		t.Fatalf("err = %v, want a black measurement mismatch", err)
	}
	if _, err := encodeCreditsTail(creditsTail{Frames: []mediasample.FrameStats{{Seconds: 1e10, PBlack: []uint8{1, 2, 3}}}}, 0, 3); err == nil {
		t.Fatal("encoded a time far outside the window")
	}
}
