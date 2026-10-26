package mediasample

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// fakeExec answers a run without starting a process.
func fakeExec(fn func(ctx context.Context, args []string, stdout, stderr io.Writer) error) ExecFunc {
	return func(ctx context.Context, _ string, args []string, _ io.Reader, stdout, stderr io.Writer) error {
		return fn(ctx, args, stdout, stderr)
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunParsesFingerprintAndSilence(t *testing.T) {
	req := Request{
		Input:  "/media/a.mkv",
		Window: &Window{StartSeconds: 100, DurationSeconds: 30},
		Audio:  &AudioOutput{Fingerprint: true, Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.5}},
	}
	wantArgs, _, err := buildArgs(req, Attempt{}, hardwareDecode{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var gotArgs []string
	runner := Runner{FFmpegPath: "ffmpeg", Workload: processmetrics.Analysis, Exec: fakeExec(func(_ context.Context, args []string, stdout, stderr io.Writer) error {
		gotArgs = args
		_, _ = stdout.Write(EncodeRawFingerprint([]uint32{7, 8, 9}))
		_, _ = io.WriteString(stderr, "size=N/A time=00:00:01.00\r[silencedetect @ 0x1] silence_start: -0.01\n"+
			"[silencedetect @ 0x1] silence_end: 1.5 | silence_duration: 1.51\n"+
			"[silencedetect @ 0x1] silence_start: 20\n")
		return nil
	})}

	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args %q, want %q", gotArgs, wantArgs)
	}
	if !reflect.DeepEqual(result.Fingerprint, []uint32{7, 8, 9}) {
		t.Fatalf("fingerprint %v", result.Fingerprint)
	}
	wantSilences := []Interval{{Start: 100, End: 101.5}, {Start: 120}}
	if !reflect.DeepEqual(result.Silences, wantSilences) {
		t.Fatalf("silences %+v, want %+v", result.Silences, wantSilences)
	}
	if result.Decoder != "software" {
		t.Fatalf("decoder %q", result.Decoder)
	}
}

func TestRunReportsExitWithStderrTail(t *testing.T) {
	ffmpeg := writeScript(t, `
echo "Input #0, matroska" >&2
echo "/media/a.mkv: Invalid data found when processing input" >&2
exit 1`)
	runner := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Analysis}

	_, err := runner.Run(context.Background(), validRequest())
	var sampleErr *Error
	if !errors.As(err, &sampleErr) {
		t.Fatalf("error %v (%T), want *Error", err, err)
	}
	if sampleErr.Reason != ReasonExit || len(sampleErr.Attempts) != 1 {
		t.Fatalf("reason %q attempts %d, want exit after one attempt", sampleErr.Reason, len(sampleErr.Attempts))
	}
	if !strings.Contains(sampleErr.Attempts[0].StderrTail, "Input #0") {
		t.Fatalf("stderr tail %q lost earlier lines", sampleErr.Attempts[0].StderrTail)
	}
	if !strings.HasSuffix(err.Error(), ": /media/a.mkv: Invalid data found when processing input") {
		t.Fatalf("message %q does not end with ffmpeg's last line", err.Error())
	}
}

func TestRunReportsStartFailure(t *testing.T) {
	runner := Runner{FFmpegPath: filepath.Join(t.TempDir(), "missing-ffmpeg"), Workload: processmetrics.Analysis}
	_, err := runner.Run(context.Background(), validRequest())
	var sampleErr *Error
	if !errors.As(err, &sampleErr) || sampleErr.Reason != ReasonStart {
		t.Fatalf("error %v, want a start failure", err)
	}
}

// blockUntilDone models an ffmpeg killed when its context ends.
func blockUntilDone(ctx context.Context, _ []string, _, _ io.Writer) error {
	<-ctx.Done()
	return errors.New("signal: killed")
}

func TestRunMovesToTheNextAttemptAfterATimeout(t *testing.T) {
	var calls atomic.Int32
	runner := Runner{Workload: processmetrics.Analysis, Exec: fakeExec(func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		if calls.Add(1) == 1 {
			return blockUntilDone(ctx, args, stdout, stderr)
		}
		_, _ = stdout.Write(EncodeRawFingerprint([]uint32{1}))
		return nil
	})}
	req := validRequest()
	req.Attempts = []Attempt{{TimeoutSeconds: 0.02}, {}}

	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls.Load() != 2 || len(result.Fingerprint) != 1 {
		t.Fatalf("calls %d fingerprint %v, want the second attempt's result", calls.Load(), result.Fingerprint)
	}
}

func TestRunReportsTimeoutWhenEveryAttemptTimesOut(t *testing.T) {
	runner := Runner{Workload: processmetrics.Analysis, Exec: fakeExec(blockUntilDone)}
	req := validRequest()
	req.Attempts = []Attempt{{TimeoutSeconds: 0.01}, {TimeoutSeconds: 0.01}}

	_, err := runner.Run(context.Background(), req)
	var sampleErr *Error
	if !errors.As(err, &sampleErr) || sampleErr.Reason != ReasonTimeout || len(sampleErr.Attempts) != 2 {
		t.Fatalf("error %v, want a timeout after two attempts", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v does not wrap context.DeadlineExceeded", err)
	}
}

func TestRunStopsWhenTheCallerCancels(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	runner := Runner{Workload: processmetrics.Analysis, Exec: fakeExec(func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		calls.Add(1)
		cancel()
		return blockUntilDone(ctx, args, stdout, stderr)
	})}
	req := validRequest()
	req.Attempts = []Attempt{{}, {}}

	_, err := runner.Run(ctx, req)
	var sampleErr *Error
	if !errors.As(err, &sampleErr) || sampleErr.Reason != ReasonCanceled {
		t.Fatalf("error %v, want canceled", err)
	}
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("error %v after %d calls, want context.Canceled after one", err, calls.Load())
	}
}

func TestRunRejectsInvalidRequestsWithoutStartingFFmpeg(t *testing.T) {
	runner := Runner{Exec: fakeExec(func(context.Context, []string, io.Writer, io.Writer) error {
		t.Fatal("ffmpeg started for an invalid request")
		return nil
	})}
	if _, err := runner.Run(context.Background(), Request{Input: "/media/a.mkv"}); err == nil {
		t.Fatal("Run accepted a request without a sampling mode")
	}
}

func TestStderrRouterSkipsAnOverlongLine(t *testing.T) {
	var lines []string
	router := newStderrRouter(func(line string) { lines = append(lines, line) })
	writer, wait := router.start()
	_, _ = io.WriteString(writer, "first\n")
	// If the router stopped reading here, this write would block forever.
	if _, err := io.WriteString(writer, strings.Repeat("x", maxStderrLine+10)+"\nlast\n"); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	wait()
	if !reflect.DeepEqual(lines, []string{"first", "last"}) {
		t.Fatalf("routed %d lines, want the lines around the dropped overlong one", len(lines))
	}
}

func TestStderrTailIsBounded(t *testing.T) {
	router := newStderrRouter()
	writer, wait := router.start()
	for range 1000 {
		_, _ = io.WriteString(writer, strings.Repeat("y", 100)+"\n")
	}
	_, _ = io.WriteString(writer, "the end\n")
	_ = writer.Close()
	wait()
	tail := router.Tail()
	if len(tail) > stderrTailBytes+200 || !strings.HasSuffix(tail, "the end") {
		t.Fatalf("tail is %d bytes ending %q", len(tail), tail[max(0, len(tail)-20):])
	}
}

func TestLastLineIsSafeForATextColumn(t *testing.T) {
	line := lastLine("ok\n/media/caf\xe9\x00.mkv: No such file\n\n")
	if line != "/media/caf.mkv: No such file" {
		t.Fatalf("lastLine = %q", line)
	}
	long := lastLine(strings.Repeat("é", errorLineBytes))
	if !strings.HasSuffix(long, "…") || len(long) > errorLineBytes+len("…") {
		t.Fatalf("long line %d bytes", len(long))
	}
}
