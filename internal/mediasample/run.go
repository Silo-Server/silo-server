package mediasample

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// execFFmpeg returns the execFunc that runs a real process. Background
// processes start at lowered priority where the platform supports it (see
// startBackground).
func execFFmpeg(background bool) execFunc {
	return func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) (*os.ProcessState, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdin = stdin
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		cmd.WaitDelay = waitDelay
		if err := startCommand(cmd, background); err != nil {
			return nil, err
		}
		err := cmd.Wait()
		return cmd.ProcessState, err
	}
}

// startCommand starts cmd, at background priority when background is set.
func startCommand(cmd *exec.Cmd, background bool) error {
	if background {
		return startBackground(cmd)
	}
	return cmd.Start()
}

// Result is what a run produced. Times are absolute media seconds.
type Result struct {
	// Fingerprint holds raw Chromaprint points when the request asked for a
	// fingerprint. It is empty when the sampled span had no decodable audio.
	Fingerprint []uint32 `json:"fingerprint,omitempty"`
	// Silences holds the silences found, ordered by start, when the request
	// asked for silence detection.
	Silences []Interval `json:"silences,omitempty"`
	// Frames holds the statistics of each decoded video frame, in decode
	// order, when the request asked for Stats. It is empty when the sampled
	// span had no frame to decode.
	Frames []FrameStats `json:"frames,omitempty"`
	// Decoder names the attempt that produced the result: "software", or
	// "hardware:<accel>".
	Decoder string `json:"decoder"`
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
	attemptCtx := ctx
	if attempt.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, time.Duration(attempt.TimeoutSeconds*float64(time.Second)))
		defer cancel()
	}
	run := attemptRun{runner: r, ctx: ctx, attemptCtx: attemptCtx, attempt: attempt, decoder: decoder}
	if req.Samples != nil {
		return run.samples(req)
	}
	return run.decode(req, 0)
}

// attemptRun is one attempt in progress. ctx is the caller's context and
// attemptCtx the attempt's, which the attempt's timeout may end first; every
// ffmpeg process of the attempt shares it.
type attemptRun struct {
	runner     Runner
	ctx        context.Context
	attemptCtx context.Context
	attempt    Attempt
	decoder    string
}

// samples runs a Samples request. It probes the input first (see probe.go),
// then reads it through a concat list when its container seeks to keyframes,
// and otherwise as one keyframes-only window whose keyframes it picks the
// samples from.
func (a attemptRun) samples(req Request) (Result, *AttemptError) {
	header := &inputHeaderParser{}
	if _, failure := a.exec(req, probeArgs(req.Input), nil, false, header.line); failure != nil {
		return Result{}, failure
	}
	if header.info.seeksToKeyframes() {
		return a.decode(req, header.info.StartSeconds)
	}
	window := sampledWindow(req.Samples.Seconds)
	windowReq := req
	windowReq.Samples = nil
	windowReq.Window = &window
	result, failure := a.decode(windowReq, 0)
	if failure != nil {
		return Result{}, failure
	}
	result.Frames = pickSampleFrames(result.Frames, req.Samples.Seconds)
	return result, nil
}

// decode runs req's decode and parses its outputs. inputStart offsets the
// inpoints of a Samples list.
func (a attemptRun) decode(req Request, inputStart float64) (Result, *AttemptError) {
	args, stdinBytes, err := buildArgs(req, a.attempt, hardwareDecode{Accel: a.runner.HWAccel, Device: a.runner.HWDevice}, inputStart)
	if err != nil {
		return Result{}, &AttemptError{Decoder: a.decoder, Reason: ReasonArgs, Err: err}
	}
	var handlers []func(string)
	var silences *silenceParser
	if req.Audio != nil && req.Audio.Silence != nil {
		silences = newSilenceParser(req.Window.StartSeconds)
		handlers = append(handlers, silences.line)
	}
	var stats *statsParser
	if req.Stats != nil {
		if req.Samples != nil {
			stats = newSampledStatsParser(buildStatsGraph(*req.Stats))
		} else {
			stats = newStatsParser(buildStatsGraph(*req.Stats), req.Window.StartSeconds)
		}
		handlers = append(handlers, stats.line)
	}
	stdout, failure := a.exec(req, args, stdinBytes, req.Audio != nil && req.Audio.Fingerprint, handlers...)
	if failure != nil {
		return Result{}, failure
	}

	result := Result{Decoder: a.decoder}
	if stdout != nil {
		result.Fingerprint = DecodeRawFingerprint(stdout.Bytes())
	}
	if silences != nil {
		result.Silences = silences.result()
	}
	if stats != nil {
		result.Frames = stats.result()
	}
	return result, nil
}

// exec runs one ffmpeg process of the attempt with args, feeding it stdin
// when that is not nil and routing its log to handlers. It returns the
// process's stdout when captureStdout is set.
func (a attemptRun) exec(req Request, args []string, stdinBytes []byte, captureStdout bool, handlers ...func(string)) (*bytes.Buffer, *AttemptError) {
	var stdin io.Reader
	if stdinBytes != nil {
		stdin = bytes.NewReader(stdinBytes)
	}
	var stdout *bytes.Buffer
	var stdoutWriter io.Writer
	if captureStdout {
		stdout = &bytes.Buffer{}
		stdoutWriter = stdout
	}
	router := newStderrRouter(handlers...)
	stderr, waitStderr := router.start()

	run := a.runner.exec
	if run == nil {
		run = execFFmpeg(req.Background)
	}
	state, err := run(a.attemptCtx, a.runner.FFmpegPath, args, stdin, stdoutWriter, stderr)
	_ = stderr.Close()
	waitStderr()
	workload := a.runner.Workload
	if workload == processmetrics.Transcode {
		workload = processmetrics.Analysis
	}
	processmetrics.Record(workload, state, err, a.attemptCtx.Err())

	if err == nil && state != nil && !state.Success() {
		err = fmt.Errorf("ffmpeg %s", state)
	}
	if err != nil {
		failure := &AttemptError{Decoder: a.decoder, Reason: ReasonExit, Err: err, StderrTail: router.Tail()}
		switch {
		case a.ctx.Err() != nil:
			failure.Reason = ReasonCanceled
			failure.Err = a.ctx.Err()
		case a.attemptCtx.Err() != nil:
			failure.Reason = ReasonTimeout
			failure.Err = a.attemptCtx.Err()
		case state == nil:
			failure.Reason = ReasonStart
		}
		return nil, failure
	}
	return stdout, nil
}
