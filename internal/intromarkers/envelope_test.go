package intromarkers

import (
	"math"
	"testing"
)

// envelopeFrom builds a comparable envelope from raw frames.
func envelopeFrom(frames ...float64) envelope {
	return newEnvelope(frames, LevelsHopSeconds).normalized()
}

// envelopeOfTrace builds a comparable envelope from a synthetic trace.
func envelopeOfTrace(levels []byte) envelope {
	return envelopeOf(levelsInput{Levels: levels})
}

func TestSharedRunsFindsOneCueAtItsOffset(t *testing.T) {
	theme := cueLevels(11, 20, false)
	x := &traceBuilder{}
	x.dialogue(1, 12).music(11, 20).dialogue(2, 30)
	y := &traceBuilder{}
	y.dialogue(3, 35).musicJittered(11, 20, 1.0).dialogue(4, 12)

	pairs := sharedRuns(envelopeOfTrace(x.build()), envelopeOfTrace(y.build()), defaultLevelMatchConfig())
	if len(pairs) == 0 {
		t.Fatal("the shared cue was not found")
	}
	best := pairs[0]
	for _, pair := range pairs[1:] {
		if pair.End-pair.Start > best.End-best.Start {
			best = pair
		}
	}
	if math.Abs(best.Start-12) > 1.0 || math.Abs(best.End-32) > 1.5 {
		t.Fatalf("cue found at %s, want 12.00-32.00", segmentRange(best.Start, best.End))
	}
	if math.Abs(best.PartnerStart-35) > 1.0 {
		t.Fatalf("partner start %.2f, want 35.00", best.PartnerStart)
	}
	if best.Correlation < 0.8 {
		t.Fatalf("correlation %.2f, want at least 0.8", best.Correlation)
	}
	_ = theme
}

func TestSharedRunsDoesNotBridgeALogoIntoTheTheme(t *testing.T) {
	// A studio logo heard in both episodes, a second of silence, then the
	// theme. Bridging the two would make the intro start at zero, which is
	// the fault this comparison exists to fix.
	x := &traceBuilder{}
	x.music(5, 4).silence(1.2).music(11, 25).dialogue(9, 20)
	y := &traceBuilder{}
	y.musicJittered(5, 4, 0.5).silence(1.5).musicJittered(11, 25, 0.8).dialogue(10, 20)

	pairs := sharedRuns(envelopeOfTrace(x.build()), envelopeOfTrace(y.build()), defaultLevelMatchConfig())
	for _, pair := range pairs {
		if pair.Start < 0.5 && pair.End > 10 {
			t.Fatalf("logo and theme were reported as one stretch: %s", segmentRange(pair.Start, pair.End))
		}
	}
	var theme levelPair
	found := false
	for _, pair := range pairs {
		if pair.Start > 4 && pair.End-pair.Start > 20 {
			theme, found = pair, true
		}
	}
	if !found {
		t.Fatalf("the theme was not found; pairs were %+v", pairs)
	}
	if math.Abs(theme.Start-5.2) > 1.5 {
		t.Fatalf("theme starts at %.2f, want about 5.2", theme.Start)
	}
}

func TestSharedRunsIgnoresUnrelatedAudio(t *testing.T) {
	x := &traceBuilder{}
	x.dialogue(21, 40).music(22, 20)
	y := &traceBuilder{}
	y.dialogue(31, 30).music(32, 25)

	if pairs := sharedRuns(envelopeOfTrace(x.build()), envelopeOfTrace(y.build()), defaultLevelMatchConfig()); len(pairs) != 0 {
		t.Fatalf("unrelated traces matched: %+v", pairs)
	}
}

func TestSharedRunsIgnoresDigitalSilence(t *testing.T) {
	x := &traceBuilder{}
	x.silence(30).music(11, 20)
	y := &traceBuilder{}
	y.silence(45).music(11, 20)

	// The music both files carry is evidence; a stretch of silence is not.
	// The run may start a window early, so the test asks only that no pair
	// lies inside the silence.
	for _, pair := range sharedRuns(envelopeOfTrace(x.build()), envelopeOfTrace(y.build()), defaultLevelMatchConfig()) {
		if pair.End <= 30.5 {
			t.Fatalf("silence matched as shared audio: %s", segmentRange(pair.Start, pair.End))
		}
	}
}

func TestEnvelopeNormalizedFlatTraceIsZero(t *testing.T) {
	env := envelopeFrom(40, 40, 40, 40, 40, 40, 40, 40, 40, 40)
	for _, v := range env.v {
		if v != 0 {
			t.Fatalf("flat trace normalized to %v, want zeros", env.v)
		}
	}
}

func TestOverlapCorrelationIsNegativeForInvertedTraces(t *testing.T) {
	x := []float64{1, -1, 1, -1, 1, -1, 1, -1, 1, -1}
	y := []float64{-1, 1, -1, 1, -1, 1, -1, 1, -1, 1}
	if got := overlapCorrelation(x, y, 0); got > -0.9 {
		t.Fatalf("inverted traces correlate at %.2f, want near -1", got)
	}
}
