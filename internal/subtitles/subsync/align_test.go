package subsync

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
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
		"offset":       {OffsetMS: 3200},
		"pal speedup":  {Scale: 25 / 23.976, OffsetMS: -1500},
		"in sync":      {},
		"drifting cut": {Scale: 1.00077, OffsetMS: -23700},
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

// laughTrack loads a sitcom episode's speech levels, where a laugh track
// and music fill the pauses, with the cue times of a subtitle that matches
// it as it is.
func laughTrack(t *testing.T) ([]mediasample.SpeechLevels, []subtitles.SubtitleCue) {
	t.Helper()
	packed, err := os.ReadFile("testdata/laugh-track.speech.gz")
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	windows, err := decodeSpeech(raw)
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile("testdata/laugh-track.cues")
	if err != nil {
		t.Fatal(err)
	}
	var cues []subtitles.SubtitleCue
	for _, line := range strings.Split(strings.TrimSpace(string(text)), "\n") {
		var start, end int
		if _, err := fmt.Sscanf(line, "%d %d", &start, &end); err != nil {
			continue
		}
		cues = append(cues, subtitles.SubtitleCue{
			Start: time.Duration(start) * time.Millisecond, End: time.Duration(end) * time.Millisecond, Lines: []string{"line"},
		})
	}
	return windows, cues
}

// Under a laugh track no single window's best lag stands out, so too few
// agree; the joint search finds the timing all of them support.
func TestAlignFindsDialogUnderALaughTrack(t *testing.T) {
	windows, cues := laughTrack(t)
	const runtime = 1269.27

	if got := alignWindowsOnly(windows, cues); got.Matched() {
		t.Fatalf("the window search alone matched; the fixture no longer exercises the joint search: %+v", got)
	}
	got, err := Align(windows, cues)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Matched() || got.JointProminence == 0 {
		t.Fatalf("no joint match: %+v", got)
	}
	if status := Decide(got, subtitles.Timing{}, seconds(runtime)); status != StatusAlreadySynced {
		t.Fatalf("status %s, timing %+v", status, got.Timing)
	}

	// The same subtitle cut for 25 fps and running 41.5 s late is put back.
	truth := subtitles.Timing{Scale: 25 / 23.976, OffsetMS: -41500}
	moved := make([]subtitles.SubtitleCue, 0, len(cues))
	for _, c := range cues {
		moved = append(moved, subtitles.SubtitleCue{
			Start: seconds((c.Start.Seconds() + 41.5) / truth.Scale), End: seconds((c.End.Seconds() + 41.5) / truth.Scale), Lines: c.Lines,
		})
	}
	got, err = Align(windows, moved)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Matched() {
		t.Fatalf("no match for the shifted subtitle: %+v", got)
	}
	for _, at := range []float64{0, runtime / 2, runtime} {
		if diff := math.Abs((got.Timing.Apply(seconds(at)) - truth.Apply(seconds(at))).Seconds()); diff > 0.15 {
			t.Fatalf("timing %+v is %.3fs from %+v at %.0fs", got.Timing, diff, truth, at)
		}
	}
}

// alignWindowsOnly is Align without the joint search.
func alignWindowsOnly(windows []mediasample.SpeechLevels, cues []subtitles.SubtitleCue) Alignment {
	signals, cues, search, err := prepare(windows, cues)
	if err != nil {
		return Alignment{}
	}
	return alignWindows(signals, cues, search)
}

// The cue map marks exactly the union of the cues' spans, whatever their
// order, overlap, or reach past the start of the clock.
func TestCueMapMarksTheUnionOfCues(t *testing.T) {
	cue := func(from, to float64) subtitles.SubtitleCue {
		return subtitles.SubtitleCue{Start: seconds(from), End: seconds(to), Lines: []string{"x"}}
	}
	m := newCueMap([]subtitles.SubtitleCue{
		cue(0.08, 0.12), cue(0.02, 0.05), cue(0.03, 0.04), // nested, unsorted
		cue(0.10, 0.15),  // overlapping the first
		cue(-0.05, 0.01), // before the clock starts
		cue(0.18, 0.18),  // empty
	}, 1)
	want := []byte{1, 0, 1, 1, 1, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0}
	if !slices.Equal(m.fine, want) {
		t.Fatalf("fine = %v, want %v", m.fine, want)
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
