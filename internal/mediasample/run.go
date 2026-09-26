package mediasample

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// Runner runs sampling requests with one ffmpeg binary.
type Runner struct {
	FFmpegPath string
	// HWAccel and HWDevice configure hardware decode for hardware attempts.
	// No output uses them yet.
	HWAccel  string
	HWDevice string
	// Workload labels the runs' process metrics. Sampling never transcodes, so
	// the zero value (processmetrics.Transcode) records as
	// processmetrics.Analysis.
	Workload processmetrics.Workload

	// exec runs ffmpeg; tests replace it. Nil means execFFmpeg.
	exec execFunc
}

// execFunc runs name with args, wiring the given stdin, stdout, and stderr,
// and returns the exited process state (nil when it never started).
type execFunc func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) (*os.ProcessState, error)

// waitDelay bounds how long a killed ffmpeg's pipes may stay open.
const waitDelay = 5 * time.Second

func execFFmpeg(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) (*os.ProcessState, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = waitDelay
	err := cmd.Run()
	return cmd.ProcessState, err
}

// Result is what a run produced. Times are absolute media seconds.
type Result struct {
	// Fingerprint holds raw Chromaprint points when the request asked for a
	// fingerprint. It is empty when the sampled span had no decodable audio.
	Fingerprint []uint32 `json:"fingerprint,omitempty"`
	// Silences holds the silences found, ordered by start, when the request
	// asked for silence detection.
	Silences []Interval `json:"silences,omitempty"`
	// Decoder names the attempt that produced the result: "software", or
	// "hardware:<accel>".
	Decoder string `json:"decoder"`
}

// Reason classifies why an attempt failed.
type Reason string

const (
	// ReasonCanceled means the caller's context ended.
	ReasonCanceled Reason = "canceled"
	// ReasonTimeout means the attempt's own timeout passed.
	ReasonTimeout Reason = "timeout"
	// ReasonStart means ffmpeg could not be started.
	ReasonStart Reason = "start"
	// ReasonExit means ffmpeg exited unsuccessfully.
	ReasonExit Reason = "exit"
	// ReasonArgs means the attempt could not be turned into arguments.
	ReasonArgs Reason = "args"
)

// AttemptError is one failed attempt.
type AttemptError struct {
	Decoder string
	Reason  Reason
	Err     error
	// StderrTail is the end of ffmpeg's log, bounded in size.
	StderrTail string
}

// Error reports a run in which every attempt failed.
type Error struct {
	// Reason is the last attempt's reason.
	Reason   Reason
	Attempts []AttemptError
}

func (e *Error) Error() string {
	if len(e.Attempts) == 0 {
		return fmt.Sprintf("ffmpeg sampling failed (%s)", e.Reason)
	}
	last := e.Attempts[len(e.Attempts)-1]
	var b strings.Builder
	fmt.Fprintf(&b, "ffmpeg sampling failed (%s)", e.Reason)
	if len(e.Attempts) > 1 {
		fmt.Fprintf(&b, " after %d attempts", len(e.Attempts))
	}
	if last.Err != nil {
		fmt.Fprintf(&b, ": %v", last.Err)
	}
	if line := lastLine(last.StderrTail); line != "" {
		fmt.Fprintf(&b, ": %s", line)
	}
	return b.String()
}

// Unwrap exposes every attempt's error, so errors.Is sees a context error.
func (e *Error) Unwrap() []error {
	errs := make([]error, 0, len(e.Attempts))
	for _, attempt := range e.Attempts {
		if attempt.Err != nil {
			errs = append(errs, attempt.Err)
		}
	}
	return errs
}

// Run validates req and makes its attempts in order until one succeeds. A
// failed attempt moves on to the next one unless ctx has ended. When every
// attempt fails the error is an *Error.
func (r Runner) Run(ctx context.Context, req Request) (Result, error) {
	if err := req.Validate(); err != nil {
		return Result{}, fmt.Errorf("invalid sampling request: %w", err)
	}
	failure := &Error{}
	for _, attempt := range req.attempts() {
		result, attemptErr := r.runAttempt(ctx, req, attempt)
		if attemptErr == nil {
			return result, nil
		}
		failure.Attempts = append(failure.Attempts, *attemptErr)
		failure.Reason = attemptErr.Reason
		if ctx.Err() != nil {
			break
		}
	}
	return Result{}, failure
}

func (r Runner) runAttempt(ctx context.Context, req Request, attempt Attempt) (Result, *AttemptError) {
	decoder := "software"
	if attempt.Hardware {
		decoder = "hardware:" + r.HWAccel
	}
	args, stdinBytes, err := buildArgs(req, attempt, hardwareDecode{Accel: r.HWAccel, Device: r.HWDevice})
	if err != nil {
		return Result{}, &AttemptError{Decoder: decoder, Reason: ReasonArgs, Err: err}
	}

	attemptCtx := ctx
	if attempt.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, time.Duration(attempt.TimeoutSeconds*float64(time.Second)))
		defer cancel()
	}

	var stdin io.Reader
	if stdinBytes != nil {
		stdin = bytes.NewReader(stdinBytes)
	}
	var stdout *bytes.Buffer
	var stdoutWriter io.Writer
	if req.Audio != nil && req.Audio.Fingerprint {
		stdout = &bytes.Buffer{}
		stdoutWriter = stdout
	}
	var handlers []func(string)
	var silences *silenceParser
	if req.Audio != nil && req.Audio.Silence != nil {
		silences = newSilenceParser(req.Window.StartSeconds)
		handlers = append(handlers, silences.line)
	}
	router := newStderrRouter(handlers...)
	stderr, waitStderr := router.start()

	run := r.exec
	if run == nil {
		run = execFFmpeg
	}
	state, err := run(attemptCtx, r.FFmpegPath, args, stdin, stdoutWriter, stderr)
	_ = stderr.Close()
	waitStderr()
	workload := r.Workload
	if workload == processmetrics.Transcode {
		workload = processmetrics.Analysis
	}
	processmetrics.Record(workload, state, err, attemptCtx.Err())

	if err == nil && state != nil && !state.Success() {
		err = fmt.Errorf("ffmpeg %s", state)
	}
	if err != nil {
		failure := &AttemptError{Decoder: decoder, Reason: ReasonExit, Err: err, StderrTail: router.Tail()}
		switch {
		case ctx.Err() != nil:
			failure.Reason = ReasonCanceled
			failure.Err = ctx.Err()
		case attemptCtx.Err() != nil:
			failure.Reason = ReasonTimeout
			failure.Err = attemptCtx.Err()
		case state == nil:
			failure.Reason = ReasonStart
		}
		return Result{}, failure
	}

	result := Result{Decoder: decoder}
	if stdout != nil {
		result.Fingerprint = DecodeRawFingerprint(stdout.Bytes())
	}
	if silences != nil {
		result.Silences = silences.result()
	}
	return result, nil
}
