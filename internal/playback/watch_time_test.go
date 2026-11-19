package playback

import (
	"math"
	"testing"
	"time"
)

func TestWatchTimeObserve(t *testing.T) {
	type sample struct {
		at       float64 // seconds after the first sample
		position float64
		paused   bool
		confirm  bool // a confirmed pause, which carries no position
	}
	cases := []struct {
		name          string
		samples       []sample
		wantStart     float64
		wantPlayed    float64
		wantPaused    float64
		wantUnmeasure bool
	}{
		{
			name:          "no samples is unmeasured",
			wantUnmeasure: true,
		},
		{
			name:       "resume an hour in credits only the viewing",
			samples:    []sample{{0, 3600, false, false}, {60, 3660, false, false}, {120, 3720, false, false}},
			wantStart:  3600,
			wantPlayed: 120,
		},
		{
			name: "a pause counts as paused, not watched",
			samples: []sample{
				{0, 0, false, false}, {10, 10, true, false}, {310, 10, false, false}, {320, 20, false, false},
			},
			wantPlayed: 20,
			wantPaused: 300,
		},
		{
			name:       "a seek forward is not watched",
			samples:    []sample{{0, 0, false, false}, {10, 10, false, false}, {20, 1800, false, false}, {30, 1810, false, false}},
			wantPlayed: 20,
		},
		{
			name:       "a rewind, buffering or a replayed sample credits nothing until play moves on",
			samples:    []sample{{0, 600, false, false}, {10, 610, false, false}, {20, 300, false, false}, {30, 310, false, false}},
			wantStart:  600,
			wantPlayed: 20,
		},
		{
			name:       "a confirmed pause keeps the viewing before it",
			samples:    []sample{{0, 0, false, false}, {8, 0, true, true}, {40, 8, true, false}},
			wantPlayed: 8,
			wantPaused: 32,
		},
		{
			name:       "a seek while paused after a confirmed pause is not watched",
			samples:    []sample{{0, 0, false, false}, {10, 0, true, true}, {100, 100, true, false}},
			wantPaused: 90,
		},
		{
			name:       "a resume before the next sample keeps the viewing after it",
			samples:    []sample{{0, 0, false, false}, {2, 0, true, true}, {10, 9, false, false}},
			wantPlayed: 9,
		},
	}

	base := time.Date(2026, 1, 1, 20, 0, 0, 0, time.UTC)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var w WatchTime
			for _, s := range tc.samples {
				at := base.Add(time.Duration(s.at * float64(time.Second)))
				if s.confirm {
					w.Pause(at)
				} else {
					w.Observe(s.position, s.paused, at)
				}
			}
			if w.Measured == tc.wantUnmeasure {
				t.Fatalf("Measured = %v, want %v", w.Measured, !tc.wantUnmeasure)
			}
			for _, c := range []struct {
				field     string
				got, want float64
			}{
				{"StartPosition", w.StartPosition, tc.wantStart},
				{"PlayedSeconds", w.PlayedSeconds, tc.wantPlayed},
				{"PausedSeconds", w.PausedSeconds, tc.wantPaused},
			} {
				if math.Abs(c.got-c.want) > 1e-9 {
					t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
				}
			}
		})
	}
}
