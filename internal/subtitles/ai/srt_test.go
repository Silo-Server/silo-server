package ai

import (
	"testing"
	"time"
)

func TestParseCuesSRT(t *testing.T) {
	in := "1\n00:00:01,000 --> 00:00:02,500\nHello world\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\nLine one\nLine two\n"

	cues, err := ParseCues([]byte(in))
	if err != nil {
		t.Fatalf("ParseCues: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2", len(cues))
	}
	if cues[0].Start != time.Second || cues[0].End != 2500*time.Millisecond {
		t.Errorf("cue 0 timing = %v..%v", cues[0].Start, cues[0].End)
	}
	if len(cues[0].Lines) != 1 || cues[0].Lines[0] != "Hello world" {
		t.Errorf("cue 0 lines = %#v", cues[0].Lines)
	}
	if len(cues[1].Lines) != 2 {
		t.Errorf("cue 1 lines = %#v", cues[1].Lines)
	}
}

func TestParseCuesVTT(t *testing.T) {
	in := "WEBVTT\n\n" +
		"00:00:01.000 --> 00:00:02.500 line:80%\nHello\n\n" +
		"NOTE a comment block\n\n" +
		"00:01:00.000 --> 00:01:01.000\nWorld\n"

	cues, err := ParseCues([]byte(in))
	if err != nil {
		t.Fatalf("ParseCues: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("got %d cues, want 2 (NOTE block must be skipped)", len(cues))
	}
	if cues[0].Start != time.Second || cues[0].End != 2500*time.Millisecond {
		t.Errorf("cue 0 timing = %v..%v", cues[0].Start, cues[0].End)
	}
	if cues[1].Start != time.Minute {
		t.Errorf("cue 1 start = %v, want 1m", cues[1].Start)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	orig := []SubtitleCue{
		{Start: time.Second, End: 2500 * time.Millisecond, Lines: []string{"Hello world"}},
		{Start: 3 * time.Second, End: 4 * time.Second, Lines: []string{"Line one", "Line two"}},
	}

	reparsed, err := ParseCues(SerializeSRT(orig))
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if len(reparsed) != len(orig) {
		t.Fatalf("round-trip changed cue count: got %d, want %d", len(reparsed), len(orig))
	}
	for i := range orig {
		if reparsed[i].Start != orig[i].Start || reparsed[i].End != orig[i].End {
			t.Errorf("cue %d timing drifted: %v..%v vs %v..%v",
				i, reparsed[i].Start, reparsed[i].End, orig[i].Start, orig[i].End)
		}
		if len(reparsed[i].Lines) != len(orig[i].Lines) {
			t.Errorf("cue %d line count changed: %#v", i, reparsed[i].Lines)
		}
	}
}

func TestParseCuesEmpty(t *testing.T) {
	if _, err := ParseCues([]byte("not a subtitle file")); err == nil {
		t.Error("expected error for input with no cues")
	}
}
