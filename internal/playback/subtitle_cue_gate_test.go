package playback

import (
	"bytes"
	"strings"
	"testing"
)

// A windowed text response must not commit its muxer header before a cue: a
// failed extract after the header is answered retryably, so the held bytes are
// dropped and the response never reaches 200.
func TestSubtitleCueGateHoldsHeaderUntilACue(t *testing.T) {
	var out bytes.Buffer
	gate := newSubtitleCueGate(&out, SubtitleFormatVTTV3)

	if _, err := gate.Write([]byte("WEBVTT\n")); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("header-only write reached the response: %q", out.String())
	}
	if gate.heldBytes() == 0 {
		t.Fatal("the gate did not hold the header")
	}
	gate.abort()
	if out.Len() != 0 {
		t.Fatalf("aborted gate wrote %q", out.String())
	}
	if gate.heldBytes() != 0 {
		t.Fatalf("aborted gate retained %d bytes", gate.heldBytes())
	}
}

func TestSubtitleCueGateReleasesOnCue(t *testing.T) {
	var out bytes.Buffer
	gate := newSubtitleCueGate(&out, SubtitleFormatVTTV3)
	doc := "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nhello\n"
	// Split across writes so the cue itself is what releases the buffer.
	if _, err := gate.Write([]byte("WEBVTT\n\n00:00:00.000 --> 00:00:01.000\n")); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("gate released before the cue text: %q", out.String())
	}
	if _, err := gate.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if out.String() != doc {
		t.Fatalf("released body = %q, want %q", out.String(), doc)
	}
}

// A clean window with no dialog is complete, not failed: commit releases the
// document FFmpeg wrote so the client still gets a valid (empty) window.
func TestSubtitleCueGateCommitsCueLessWindow(t *testing.T) {
	var out bytes.Buffer
	gate := newSubtitleCueGate(&out, SubtitleFormatVTTV3)
	if _, err := gate.Write([]byte("WEBVTT\n\n")); err != nil {
		t.Fatal(err)
	}
	if err := gate.commit(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "WEBVTT\n\n" {
		t.Fatalf("committed cue-less window = %q", out.String())
	}
}

// The hold is bounded: output past the cap is not a header-only body, so it is
// released and streamed instead of growing without limit.
func TestSubtitleCueGateReleasesPastItsCap(t *testing.T) {
	var out bytes.Buffer
	gate := newSubtitleCueGate(&out, SubtitleFormatVTTV3)
	chunk := bytes.Repeat([]byte("x"), subtitleCueGateMaxBytes)
	if _, err := gate.Write(chunk); err != nil {
		t.Fatal(err)
	}
	if out.Len() != len(chunk) {
		t.Fatalf("oversized cue-less output = %d bytes, want %d released", out.Len(), len(chunk))
	}
	if !strings.HasPrefix(out.String(), "xxxx") {
		t.Fatal("released output is not the buffered document")
	}
}
