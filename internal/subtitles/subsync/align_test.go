package subsync

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// dialogCues returns cues of a two-hour title: lines of 1-5 s with gaps of
// 0.5-8 s, the rhythm of film dialog.
func dialogCues(seed uint64, runtime float64) []subtitles.SubtitleCue {
	r := rand.New(rand.NewPCG(seed, 1))
	var cues []subtitles.SubtitleCue
	for t := 30.0; t < runtime-10; {
		length := 1 + 4*r.Float64()
		cues = append(cues, subtitles.SubtitleCue{
			Start: seconds(t), End: seconds(t + length), Lines: []string{"line"},
		})
		t += length + 0.5 + 7.5*r.Float64()
	}
	return cues
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// speechFor renders windows of audio levels in which speech happens where
// truth places each cue on the media clock. Speech fills most of each line,
// background noise varies, and loud music or effects hit at random.
func speechFor(cues []subtitles.SubtitleCue, truth subtitles.Timing, starts []float64, length float64, seed uint64) []mediasample.SpeechLevels {
	r := rand.New(rand.NewPCG(seed, 2))
	var windows []mediasample.SpeechLevels
	for _, start := range starts {
		n := int(length / mediasample.SpeechFrameSeconds)
		levels := make([]byte, n)
		for i := range levels {
			levels[i] = byte(28 + r.IntN(8))
		}
		// Effects: bursts of loud non-speech.
		for range int(length / 20) {
			at := r.IntN(n)
			for i := at; i < min(n, at+r.IntN(300)); i++ {
				levels[i] = byte(50 + r.IntN(20))
			}
		}
		for _, c := range cues {
			from := (truth.Apply(c.Start).Seconds() - start) / mediasample.SpeechFrameSeconds
			to := (truth.Apply(c.End).Seconds() - start) / mediasample.SpeechFrameSeconds
			for i := max(int(from), 0); i < min(int(to), n); i++ {
				if r.Float64() < 0.8 { // pauses between words
					levels[i] = byte(55 + r.IntN(15))
				}
			}
		}
		windows = append(windows, mediasample.SpeechLevels{
			StartSeconds: start, FrameSeconds: mediasample.SpeechFrameSeconds, Levels: levels,
		})
	}
	return windows
}

func spread(runtime, length float64, count int) []float64 {
	starts := make([]float64, count)
	for i := range starts {
		starts[i] = (runtime - length) * (float64(i) + 0.5) / float64(count)
	}
	return starts
}

func TestAlignRecoversTiming(t *testing.T) {
	const runtime = 7200.0
	cases := map[string]subtitles.Timing{
		"offset":          {OffsetMS: 3200},
		"negative offset": {OffsetMS: -45300},
		"pal speedup":     {Scale: 25 / 23.976, OffsetMS: -1500},
		"pal slowdown":    {Scale: 23.976 / 25, OffsetMS: 800},
		"in sync":         {},
		"drifting cut":    {Scale: 1.00077, OffsetMS: -23700},
	}
	cues := dialogCues(1, runtime)
	for name, truth := range cases {
		t.Run(name, func(t *testing.T) {
			windows := speechFor(cues, truth, spread(runtime, 120, 12), 120, 7)
			input := cues
			if name == "offset" {
				// Corrupt cues must neither allocate their span nor disturb alignment.
				input = append(append([]subtitles.SubtitleCue(nil), cues...),
					subtitles.SubtitleCue{Start: 9999 * time.Hour, End: 9999*time.Hour + time.Second, Lines: []string{"x"}},
					subtitles.SubtitleCue{Start: seconds(100), End: 99999 * time.Hour, Lines: []string{"y"}})
			}
			got, err := Align(windows, input)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Matched() {
				t.Fatalf("no match: %+v", got)
			}
			assertTiming(t, got.Timing, truth, runtime)
			if status := Decide(got, subtitles.Timing{}, seconds(runtime)); truth.IsIdentity() != (status == StatusAlreadySynced) {
				t.Fatalf("status %s for %+v", status, truth)
			}
		})
	}
}

func TestAlignToleratesBadWindows(t *testing.T) {
	const runtime = 6000.0
	cues := dialogCues(3, runtime)
	truth := subtitles.Timing{OffsetMS: 12000}
	windows := speechFor(cues, truth, spread(runtime, 120, 12), 120, 9)
	// A third of the windows are music or action with no dialog.
	noise := speechFor(dialogCues(99, runtime), truth, []float64{0}, 120, 10)[0].Levels
	for i := 0; i < len(windows); i += 3 {
		windows[i].Levels = noise
	}
	got, err := Align(windows, cues)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Matched() {
		t.Fatalf("no match: %+v", got)
	}
	assertTiming(t, got.Timing, truth, runtime)
}

func TestAlignRejectsSubtitleOfAnotherTitle(t *testing.T) {
	const runtime = 7200.0
	speech := speechFor(dialogCues(4, runtime), subtitles.Timing{}, spread(runtime, 120, 12), 120, 11)
	got, err := Align(speech, dialogCues(5, runtime))
	if err != nil {
		t.Fatal(err)
	}
	if got.Matched() || Decide(got, subtitles.Timing{}, seconds(runtime)) != StatusNoMatch {
		t.Fatalf("another title's subtitle matched: %+v", got)
	}
}

func TestAlignInputs(t *testing.T) {
	if _, err := Align(nil, nil); !errors.Is(err, ErrNoCues) {
		t.Fatalf("no cues: %v", err)
	}
	flat := mediasample.SpeechLevels{FrameSeconds: mediasample.SpeechFrameSeconds, Levels: make([]byte, 6000)}
	if _, err := Align([]mediasample.SpeechLevels{flat}, dialogCues(1, 600)); !errors.Is(err, ErrNoSpeech) {
		t.Fatalf("silent audio: %v", err)
	}
}

func assertTiming(t *testing.T, got, want subtitles.Timing, runtime float64) {
	t.Helper()
	for _, at := range []float64{0, runtime / 2, runtime} {
		diff := math.Abs((got.Apply(seconds(at)) - want.Apply(seconds(at))).Seconds())
		if diff > 0.08 {
			t.Fatalf("timing %+v is %.3fs from %+v at %.0fs", got, diff, want, at)
		}
	}
}

func BenchmarkAlign(b *testing.B) {
	const runtime = 7200.0
	cues := dialogCues(1, runtime)
	windows := speechFor(cues, subtitles.Timing{Scale: 25 / 23.976, OffsetMS: 2000}, spread(runtime, 120, 12), 120, 7)
	for b.Loop() {
		if _, err := Align(windows, cues); err != nil {
			b.Fatal(err)
		}
	}
}
