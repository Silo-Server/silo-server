package mediasample

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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
