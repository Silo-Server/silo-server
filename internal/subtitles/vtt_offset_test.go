package subtitles

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestWebVTTOffsetStreamsWholeCuesOnReceiverClock(t *testing.T) {
	input := "WEBVTT\r\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0\r\n\r\nNOTE untouched --> text\r\n\r\nold\r\n00:00:01.000 --> 00:00:03.000\r\nBefore resume\r\n\r\nactive\r\n00:09:59.000 --> 00:10:02.500 align:start\r\n<b>hello</b><00:10:01.000>world\r\n\r\nnext\r\n10:04.000 --> 10:05.000\r\nAfter resume"
	want := "WEBVTT\n\nNOTE untouched --> text\n\nactive\n00:00:00.000 --> 00:00:02.500 align:start\n<b>hello</b><00:00:01.000>world\n\nnext\n00:00:04.000 --> 00:00:05.000\nAfter resume\n\n"
	for _, chunk := range []int{1, 7, len(input)} {
		var output bytes.Buffer
		w := NewWebVTTOffsetWriter(&output, -600*time.Second)
		for p := input; len(p) > 0; {
			n := min(chunk, len(p))
			if _, err := io.WriteString(w, p[:n]); err != nil {
				t.Fatal(err)
			}
			p = p[n:]
		}
		if !strings.Contains(output.String(), "world\n\n") {
			t.Fatal("completed cue withheld until EOF")
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if output.String() != want {
			t.Fatalf("chunk %d:\n%q\nwant %q", chunk, output.String(), want)
		}
	}
}

func TestWebVTTOffsetReportsMalformedAndOversizedCues(t *testing.T) {
	for _, input := range []string{"WEBVTT\n\nbad --> bad\ntext\n\n", strings.Repeat("x", maxWebVTTBlockBytes+1)} {
		w := NewWebVTTOffsetWriter(io.Discard, time.Second)
		_, err := io.WriteString(w, input)
		if err == nil {
			t.Fatal("invalid subtitle unexpectedly accepted")
		}
		if w.Close() == nil {
			t.Fatal("lost streaming error at close")
		}
	}
}

type failingWebVTTWriter struct{}

func (failingWebVTTWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestWebVTTOffsetPropagatesClientDisconnect(t *testing.T) {
	w := NewWebVTTOffsetWriter(failingWebVTTWriter{}, time.Second)
	if _, err := io.WriteString(w, "WEBVTT\n\n"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error=%v", err)
	}
}
