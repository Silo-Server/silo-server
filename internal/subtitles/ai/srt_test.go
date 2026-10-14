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
	wantSRT := "1\n00:00:01,000 --> 00:00:02,500\nHello world\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\nLine one\nLine two\n\n"
	if got := string(SerializeSRT(cues)); got != wantSRT {
		t.Fatalf("serialized SRT = %q, want %q", got, wantSRT)
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

func TestParseCuesEmpty(t *testing.T) {
	if _, err := ParseCues([]byte("not a subtitle file")); err == nil {
		t.Error("expected error for input with no cues")
	}
}
