package intromarkers

import (
	"math"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// levelTrace builds an envelope from a list of (seconds, dB) steps.
type levelTrace struct{ levels []float64 }

func (l *levelTrace) hold(seconds, db float64) *levelTrace {
	for range framesOf(seconds) {
		l.levels = append(l.levels, db)
	}
	return l
}

// ramp lowers the level linearly over seconds, as a fade does.
func (l *levelTrace) ramp(seconds, from, to float64) *levelTrace {
	frames := framesOf(seconds)
	for i := range frames {
		l.levels = append(l.levels, from+(to-from)*float64(i)/float64(frames))
	}
	return l
}

func (l *levelTrace) envelope() envelope {
	return newEnvelope(l.levels, LevelsHopSeconds).normalized()
}

func TestRefineLevelStartMovesToTheMusicOnset(t *testing.T) {
	// Quiet dialogue, a beat of silence, then the theme. A matcher's edge
	// lands a window early; the trace puts it on the onset.
	trace := (&levelTrace{}).hold(20, 30).hold(1.6, 6).hold(20, 55).hold(20, 30)
	start, end := refineLevelEdges(trace.envelope(), 0, 20.8, 41.4)
	if math.Abs(start-21.6) > 0.35 {
		t.Fatalf("start refined to %.2f, want the onset at 21.60", start)
	}
	if end < 41 || end > 42.5 {
		t.Fatalf("end refined to %.2f, want about 41.40", end)
	}
}

func TestRefineLevelStartKeepsAnEdgeWithNoQuietBefore(t *testing.T) {
	// A cold open's dialogue runs into the theme without a break, so there is
	// no onset to find and the matched start stands.
	trace := (&levelTrace{}).hold(20, 52).hold(20, 55).hold(20, 52)
	start, _ := refineLevelEdges(trace.envelope(), 0, 20, 40)
	if math.Abs(start-20) > 0.35 {
		t.Fatalf("start moved to %.2f with no quiet before it", start)
	}
}

func TestRefineLevelEndFollowsTheFade(t *testing.T) {
	// The music fades over a second and a half; the end moves to the bottom
	// of the fade, where the music stops.
	trace := (&levelTrace{}).hold(20, 30).hold(20, 55).ramp(1.5, 55, 20).hold(20, 25)
	end := refineLevelEnd(trace.envelope(), 0, 20, 41.5)
	if math.Abs(end-41.4) > 0.6 {
		t.Fatalf("end refined to %.2f, want the bottom of the fade about 41.40", end)
	}
}

func TestZeroStartLimitOnlySnapsTheFilesFirstFrames(t *testing.T) {
	// An opening that begins at 1.2 seconds keeps 1.2: the Chromaprint path's
	// two-second snap turned openings after a short logo into openings at
	// zero.
	cfg := DefaultConfig("ffmpeg")
	ep := syntheticEpisode{
		fileID: 1, episode: "a", duration: 1400,
		levels: (&traceBuilder{}).dialogue(1, 1.2).music(11, 30).dialogue(2, 300).build(),
	}
	segment := refineIntroSegment(ep.input(), Segment{Start: 1.2, End: 31.2, Algorithm: LevelAlgorithm, Confidence: 0.9}, cfg)
	if segment.Start < 1 {
		t.Fatalf("start snapped to %.2f, want 1.20", segment.Start)
	}

	ep.levels = (&traceBuilder{}).music(11, 30).dialogue(2, 300).build()
	segment = refineIntroSegment(ep.input(), Segment{Start: 0, End: 30, Algorithm: LevelAlgorithm, Confidence: 0.9}, cfg)
	if segment.Start != 0 {
		t.Fatalf("start at the beginning of the file became %.2f", segment.Start)
	}
}

func TestSnapIntroToChapters(t *testing.T) {
	candidate := Candidate{
		DurationSeconds: 1400,
		Chapters: []models.MediaChapter{
			{Title: "Cold Open", StartSeconds: 0, EndSeconds: 92.5},
			{Title: "Opening", StartSeconds: 92.5, EndSeconds: 122},
		},
	}
	start, end := snapIntroToChapters(candidate, Segment{Start: 91.4, End: 120.6})
	if start != 92.5 || end != 122 {
		t.Fatalf("chapter snap gave %.2f-%.2f, want 92.50-122.00", start, end)
	}
	// A boundary far from any chapter stays where it is.
	start, end = snapIntroToChapters(candidate, Segment{Start: 300, End: 340})
	if start != 300 || end != 340 {
		t.Fatalf("an unrelated segment moved to %.2f-%.2f", start, end)
	}
}
