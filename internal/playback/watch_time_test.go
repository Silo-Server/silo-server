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

// A resume confirmation can carry a paused seek that another replica
// accepted. It is measured as paused time, not credited as viewing.
func TestSessionManagerResumeAdoptsPausedSeek(t *testing.T) {
	m := NewSessionManager(0, 0)
	s := startPlaybackTestSession(t, m, 1, "profile-1", 100, PlayDirect)
	if err := m.UpdateProgressSample(s.ID, ProgressSampleV3{Sequence: 1, Position: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetPaused(s.ID, true, 1, nil); err != nil {
		t.Fatal(err)
	}
	// Paused for half an hour.
	m.mu.Lock()
	w := &m.sessions[s.ID].Watch
	w.Until = w.Until.Add(-30 * time.Minute)
	w.pausedAt = w.pausedAt.Add(-30 * time.Minute)
	m.mu.Unlock()
	if _, err := m.SetPaused(s.ID, false, 2, &ProgressSampleV3{Sequence: 2, Position: 1230, IsPaused: true}); err != nil {
		t.Fatal(err)
	}
	got, err := m.GetSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Watch.PlayedSeconds != 0 || got.Watch.PausedSeconds < 30*60 {
		t.Fatalf("played %v, paused %v; want 0 played and the half hour paused", got.Watch.PlayedSeconds, got.Watch.PausedSeconds)
	}
}

// A resume confirmed while the session already plays can carry an older
// paused report. Measurement follows the confirmed state, so the playback
// after it is still credited as viewing.
func TestSessionManagerConfirmedStateOverridesAdoptedPause(t *testing.T) {
	m := NewSessionManager(0, 0)
	s := startPlaybackTestSession(t, m, 1, "profile-1", 100, PlayDirect)
	if err := m.UpdateProgressSample(s.ID, ProgressSampleV3{Sequence: 1, Position: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetPaused(s.ID, false, 2, &ProgressSampleV3{Sequence: 2, Position: 40, IsPaused: true}); err != nil {
		t.Fatal(err)
	}
	// Ten seconds of playback before the next report.
	m.mu.Lock()
	w := &m.sessions[s.ID].Watch
	w.Until = w.Until.Add(-10 * time.Second)
	m.mu.Unlock()
	if err := m.UpdateProgressSample(s.ID, ProgressSampleV3{Sequence: 3, Position: 50}); err != nil {
		t.Fatal(err)
	}
	got, err := m.GetSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Watch.PlayedSeconds != 10 || got.Watch.PausedSeconds != 0 {
		t.Fatalf("played %v, paused %v; want 10 played and none paused", got.Watch.PlayedSeconds, got.Watch.PausedSeconds)
	}
}
