package mediasample

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestClassify(t *testing.T) {
	exit := func(err error, tail string) error {
		return &Error{Reason: ReasonExit, Attempts: []AttemptError{{Reason: ReasonExit, Err: err, StderrTail: tail}}}
	}
	exitStatus := errors.New("exit status 1")
	tests := []struct {
		name      string
		err       error
		want      Reason
		permanent bool
	}{
		{name: "nil", err: nil, want: ""},
		{name: "invalid request", err: errors.New("invalid sampling request: no input"), want: ReasonFailed},
		{name: "bare cancellation", err: fmt.Errorf("sampling: %w", context.Canceled), want: ReasonCanceled},
		{name: "bare deadline", err: context.DeadlineExceeded, want: ReasonTimeout},
		{name: "canceled attempt", err: &Error{Attempts: []AttemptError{{Reason: ReasonCanceled, Err: context.Canceled}}}, want: ReasonCanceled},
		{name: "timed out attempt", err: &Error{Attempts: []AttemptError{{Reason: ReasonTimeout, Err: context.DeadlineExceeded}}}, want: ReasonTimeout},
		{name: "ffmpeg missing", err: &Error{Attempts: []AttemptError{{Reason: ReasonStart, Err: errors.New("executable file not found")}}}, want: ReasonFailed},
		{
			name:      "corrupt container",
			err:       exit(exitStatus, "[matroska,webm @ 0x1] 0x00 at pos 1234 (0x4d2) invalid as first byte of an EBML number\n[in#0 @ 0x2] Error opening input: Invalid data found when processing input"),
			want:      ReasonInvalidData,
			permanent: true,
		},
		{
			name:      "no video stream",
			err:       exit(exitStatus, "[out#1/null @ 0x1] Stream map '0:V:0' matches no streams."),
			want:      ReasonNoStream,
			permanent: true,
		},
		{name: "missing filter", err: exit(exitStatus, "[AVFilterGraph @ 0x1] No such filter: 'signalstats'"), want: ReasonUnsupported},
		{name: "missing muxer", err: exit(exitStatus, "[out#0 @ 0x1] Requested output format 'chromaprint' is not known."), want: ReasonUnsupported},
		{
			name: "killed after decode warnings",
			err:  exit(errors.New("signal: killed"), "[h264 @ 0x1] Invalid NAL unit size (1234 > 99)."),
			want: ReasonKilled,
		},
		{name: "other exit", err: exit(exitStatus, "Conversion failed!"), want: ReasonFailed},
		{name: "refused attempt", err: &Error{Attempts: []AttemptError{{Reason: ReasonUnsupported, Err: errors.New("software HDR tone mapping is disabled")}}}, want: ReasonUnsupported},
		{name: "capability listing failed", err: &Error{Attempts: []AttemptError{{Reason: ReasonCapabilities, Err: errors.New("ffmpeg filter listing failed")}}}, want: ReasonCapabilities},
		{name: "no image", err: &Error{Attempts: []AttemptError{{Reason: ReasonEmpty, Err: errors.New("ffmpeg wrote no image")}}}, want: ReasonFailed},
		{
			name: "last attempt decides",
			err: &Error{Attempts: []AttemptError{
				{Reason: ReasonExit, Err: exitStatus, StderrTail: "Invalid data found when processing input"},
				{Reason: ReasonTimeout, Err: context.DeadlineExceeded},
			}},
			want: ReasonTimeout,
		},
		{name: "wrapped run error", err: fmt.Errorf("file 7: %w", exit(exitStatus, "moov atom not found")), want: ReasonInvalidData, permanent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.err)
			if got != tt.want {
				t.Fatalf("Classify = %q, want %q", got, tt.want)
			}
			if got.Permanent() != tt.permanent {
				t.Fatalf("%q.Permanent() = %t, want %t", got, got.Permanent(), tt.permanent)
			}
		})
	}
}
