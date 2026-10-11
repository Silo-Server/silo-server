package mediasample

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// Runner runs sampling requests with one ffmpeg binary.
type Runner struct {
	FFmpegPath string
	// HWAccel and HWDevice configure hardware attempts. HWAccel is a resolved
	// backend (see SupportsHardwareDecode), never "auto". HWDevice is the
	// configured device value, which may list several render devices; each
	// hardware attempt reserves one of them while it runs.
	HWAccel  string
	HWDevice string
	// Workload labels the runs' process metrics. Sampling never transcodes, so
	// the zero value (processmetrics.Transcode) records as
	// processmetrics.Analysis.
	Workload processmetrics.Workload

	// Exec runs ffmpeg in place of starting a process; tests set it. Nil
	// starts ffmpeg.
	Exec ExecFunc
	// Capabilities returns the binary's inventory when a run needs it, which
	// only software tone mapping of images does so far. Nil means
	// LoadCapabilities; tests replace it.
	Capabilities func(ctx context.Context, ffmpegPath string) (Capabilities, error)
	// Fallback reports whether a run moves on to its next attempt after
	// attempt failed with failure. Nil always moves on. A run never moves on
	// once the caller's context has ended.
	Fallback func(attempt Attempt, failure AttemptError) bool
}

// ExecFunc stands in for one ffmpeg process: it runs name with args, reading
// stdin and writing ffmpeg's output and log to stdout and stderr. A non-nil
// error counts as ffmpeg exiting unsuccessfully, or as the attempt timing out
// when it wraps context.DeadlineExceeded. No process metrics are recorded for
// it.
type ExecFunc func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error

// waitDelay bounds how long a killed ffmpeg's pipes may stay open.
const waitDelay = 5 * time.Second

// execFFmpeg runs a real process and returns its exited state (nil when it
// never started). Background processes start at lowered priority where the
// platform supports it (see startBackground).
func execFFmpeg(ctx context.Context, background bool, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) (*os.ProcessState, error) {
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

// startCommand starts cmd, at background priority when background is set.
func startCommand(cmd *exec.Cmd, background bool) error {
	if background {
		return startBackground(cmd)
	}
	return cmd.Start()
}

// softwareDecoder names a software attempt in Result.Decoder.
const softwareDecoder = "software"

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
	// Images holds the decoded images when the request asked for Images.
	Images []Image `json:"images,omitempty"`
	// Sheets holds the sprite sheets, in order, when the request asked for
	// Sheets, and SheetFrames how their cells were filled.
	Sheets      []Sheet     `json:"sheets,omitempty"`
	SheetFrames SheetFrames `json:"sheet_frames,omitzero"`
	// SheetTileHeight is the actual cell height, which UseInputAspect may
	// change from the request after probing the input's display matrix.
	SheetTileHeight int `json:"sheet_tile_height,omitzero"`
	// Speech holds the speech levels when the request asked for them.
	Speech *SpeechLevels `json:"speech,omitempty"`
	// Decoder names the attempt that produced the result: "software", or
	// "hardware:<accel>".
	Decoder string `json:"decoder"`
}

// Run validates req and makes its attempts in order until one succeeds. A
// failed attempt moves on to the next one unless ctx has ended or the
// runner's Fallback declines. When every attempt made fails the error is an
// *Error.
func (r Runner) Run(ctx context.Context, req Request) (Result, error) {
	if err := req.Validate(); err != nil {
		return Result{}, fmt.Errorf("invalid sampling request: %w", err)
	}
	failure := &Error{}
	toneMap := &toneMapResolver{}
	for _, attempt := range req.attempts() {
		result, attemptErr := r.runAttempt(ctx, req, attempt, toneMap)
		if attemptErr == nil {
			return result, nil
		}
		failure.Attempts = append(failure.Attempts, *attemptErr)
		failure.Reason = attemptErr.Reason
		if ctx.Err() != nil || (r.Fallback != nil && !r.Fallback(attempt, *attemptErr)) {
			break
		}
	}
	return Result{}, failure
}

func (r Runner) runAttempt(ctx context.Context, req Request, attempt Attempt, toneMap *toneMapResolver) (Result, *AttemptError) {
	decoder := softwareDecoder
	if attempt.Hardware {
		decoder = "hardware:" + r.HWAccel
	}
	// A hardware attempt reserves its device, and an image or sheet attempt
	// settles its filters, before its timeout starts, so neither eats into
	// the decode's time.
	hw, release, failure := r.reserveHardware(attempt)
	if failure != nil {
		failure.Decoder = decoder
		return Result{}, failure
	}
	defer release()
	var imageArgs []string
	if req.At != nil {
		args, failure := r.prepareImage(ctx, req, attempt, hw, toneMap)
		if failure != nil {
			failure.Decoder = decoder
			return Result{}, failure
		}
		imageArgs = args
	}
	var sheetsGraph string
	if req.Sheets != nil {
		graph, failure := r.prepareSheets(ctx, req, attempt, toneMap)
		if failure != nil {
			failure.Decoder = decoder
			return Result{}, failure
		}
		sheetsGraph = graph
	}
	attemptCtx := ctx
	if attempt.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, time.Duration(attempt.TimeoutSeconds*float64(time.Second)))
		defer cancel()
	}
	run := attemptRun{runner: r, ctx: ctx, attemptCtx: attemptCtx, attempt: attempt, hw: hw, decoder: decoder, sheetsGraph: sheetsGraph, toneMap: toneMap}
	switch {
	case req.At != nil:
		return run.image(req, imageArgs)
	case req.Samples != nil:
		return run.samples(req)
	}
	return run.decode(req, 0)
}

// reserveHardware returns the hardware attempt decodes on. A hardware
// attempt reserves one render device through playback.AcquireHWDevice,
// falling back to playback.PickRenderDevice, until release is called. A
// multi-device HWDevice therefore resolves to one device per attempt, and the
// reservation spans only this attempt. A hardware attempt the host cannot
// run, such as VAAPI without a render device, fails as unsupported without
// starting ffmpeg: that is no fault of the request or the file.
func (r Runner) reserveHardware(attempt Attempt) (hardwareDecode, func(), *AttemptError) {
	hw := hardwareDecode{Accel: r.HWAccel}
	if !attempt.Hardware {
		return hw, func() {}, nil
	}
	device, release := playback.AcquireHWDevice(r.HWDevice, r.HWAccel)
	if device == "" {
		device = playback.PickRenderDevice("")
	}
	hw.Device = device
	if _, err := hardwareDecodeArgs(hw, false); err != nil {
		release()
		return hardwareDecode{}, nil, &AttemptError{Reason: ReasonUnsupported, Err: err}
	}
	return hw, release, nil
}

// attemptRun is one attempt in progress. ctx is the caller's context and
// attemptCtx the attempt's, which the attempt's timeout may end first; every
// ffmpeg process of the attempt shares it. hw is the hardware the attempt
// reserved, if it decodes on hardware.
type attemptRun struct {
	runner     Runner
	ctx        context.Context
	attemptCtx context.Context
	attempt    Attempt
	hw         hardwareDecode
	decoder    string
	// sheetsGraph is the video chain of a Sheets request.
	sheetsGraph string
	toneMap     *toneMapResolver
}

// samples runs a Samples request. Unless it reads through, it probes the
// input first (see probe.go), then reads it through a concat list when its
// container seeks to keyframes. Otherwise it reads one keyframes-only window
// and picks the samples from its keyframes.
//
// Sheets read through a list that comes back empty because the decoder
// dropped keyframes as duplicates are read again as that window. One decoder serves every
// entry of a list, and nothing resets it between them: an HEVC CRA keyframe
// after a jump takes a picture order count derived from the previous
// sample's, and when that count matches a picture still in the decoder's
// buffer, the decoder drops the keyframe ("Duplicate POC in a sequence").
// Open-GOP encodes then lose most of their samples on every run. A window
// decodes its keyframes in order, so their counts stay consistent. A list
// that is empty for another reason, such as a truncated or damaged file,
// still fails.
func (a attemptRun) samples(req Request) (Result, *AttemptError) {
	var listFailure *AttemptError
	if !req.Samples.ReadThrough || (req.Sheets != nil && req.Sheets.UseInputAspect) {
		header := &inputHeaderParser{}
		if failure := a.exec(req, probeArgs(req.Input), nil, nil, header.line); failure != nil {
			return Result{}, failure
		}
		if req.Sheets != nil && req.Sheets.UseInputAspect {
			out := req.Sheets.forDisplayAspect(header.info.displayAspect())
			if err := out.validate(); err != nil {
				return Result{}, &AttemptError{Decoder: a.decoder, Reason: ReasonArgs, Err: err}
			}
			req.Sheets = &out
			graph, failure := a.runner.prepareSheets(a.attemptCtx, req, a.attempt, a.toneMap)
			if failure != nil {
				failure.Decoder = a.decoder
				return Result{}, failure
			}
			a.sheetsGraph = graph
		}
		if !req.Samples.ReadThrough && header.info.seeksToKeyframes() {
			if req.Sheets == nil {
				return a.decode(req, header.info.StartSeconds)
			}
			duplicates := false
			result, failure := a.sheets(req, req.Samples.Seconds, header.info.StartSeconds, watchDuplicatePOC(&duplicates))
			if failure == nil || failure.Reason != ReasonEmpty || !duplicates {
				return result, failure
			}
			listFailure = failure
		}
	}
	window := sampledWindow(req.Samples.Seconds)
	windowReq := req
	windowReq.Samples = nil
	windowReq.Window = &window
	if req.Sheets != nil {
		duplicates := false
		result, failure := a.sheets(windowReq, req.Samples.Seconds, 0, watchDuplicatePOC(&duplicates))
		if listFailure == nil {
			return result, failure
		}
		// Keyframes a whole picture order count cycle apart collide in a
		// window too, since skipped pictures do not advance the count. The
		// window would then give later samples the last keyframe it kept and
		// count them as decoded, so it fails instead.
		if failure == nil && duplicates {
			result, failure = Result{}, &AttemptError{Decoder: a.decoder, Reason: ReasonEmpty, Err: errors.New("ffmpeg dropped keyframes as duplicates in the window too")}
		}
		// A window that fails too reports its own failure, whose reason and
		// log describe the latest read, and names the list's in its error.
		if failure != nil {
			failure.Err = fmt.Errorf("%w (after the list run: %w)", failure.Err, listFailure.Err)
		}
		return result, failure
	}
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
	args, stdinBytes, err := buildArgs(req, a.attempt, a.hw, inputStart)
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
			stats = newSampledStatsParser(req.statsGraph(a.attempt, a.hw.Accel))
		} else {
			stats = newStatsParser(req.statsGraph(a.attempt, a.hw.Accel), req.Window.StartSeconds)
		}
		handlers = append(handlers, stats.line)
	}
	var stdout io.Writer
	var fingerprint *bytes.Buffer
	var speech *speechWriter
	switch {
	case req.speech() != nil:
		speech = newSpeechWriter(req.Window.DurationSeconds)
		stdout = speech
	case req.Audio != nil && req.Audio.Fingerprint:
		fingerprint = &bytes.Buffer{}
		stdout = fingerprint
	}
	if failure := a.exec(req, args, stdinBytes, stdout, handlers...); failure != nil {
		return Result{}, failure
	}

	result := Result{Decoder: a.decoder}
	if fingerprint != nil {
		result.Fingerprint = DecodeRawFingerprint(fingerprint.Bytes())
	}
	if speech != nil {
		result.Speech = speech.result(req.Window.StartSeconds)
	}
	if silences != nil {
		result.Silences = silences.result()
	}
	if stats != nil {
		result.Frames = stats.result()
	}
	return result, nil
}

// duplicatePOCMessage is what ffmpeg's HEVC decoder logs when it drops a
// picture whose picture order count matches one still in its buffer. Other
// undecodable pictures, such as a damaged stretch of the file, log only
// "Skipping invalid undecodable NALU" and do not send a list to the window.
const duplicatePOCMessage = "Duplicate POC in a sequence"

// watchDuplicatePOC returns a log handler that sets *seen once ffmpeg logs
// duplicatePOCMessage.
func watchDuplicatePOC(seen *bool) func(string) {
	return func(line string) {
		*seen = *seen || strings.Contains(line, duplicatePOCMessage)
	}
}

// sheets runs a Sheets request for the sample times, reading req's list
// (with inpoints offset by inputStart) or window, and tiles the frames.
// logHandlers also read ffmpeg's log.
func (a attemptRun) sheets(req Request, times []float64, inputStart float64, logHandlers ...func(string)) (Result, *AttemptError) {
	var packetTimingPath string
	if req.Window != nil {
		dir, err := os.MkdirTemp("", "silo-sheets-*")
		if err != nil {
			return Result{}, &AttemptError{Decoder: a.decoder, Reason: ReasonOutput, Err: fmt.Errorf("create packet timing directory: %w", err)}
		}
		defer func() { _ = os.RemoveAll(dir) }()
		packetTimingPath = filepath.Join(dir, "packets.framecrc")
	}
	args, stdinBytes, err := buildSheetsArgs(req, a.attempt, a.hw, inputStart, a.sheetsGraph, packetTimingPath)
	if err != nil {
		return Result{}, &AttemptError{Decoder: a.decoder, Reason: ReasonArgs, Err: err}
	}
	offset := 0.0
	if req.Window != nil {
		offset = req.Window.StartSeconds
	}
	assembler := newSheetAssembler(*req.Sheets, times, req.Samples != nil, offset)
	if failure := a.exec(req, args, stdinBytes, assembler, append(logHandlers, assembler.line)...); failure != nil {
		return Result{}, failure
	}
	if packetTimingPath != "" {
		if err := assembler.readPacketTiming(packetTimingPath); err != nil {
			return Result{}, &AttemptError{Decoder: a.decoder, Reason: ReasonOutput, Err: fmt.Errorf("read packet timing: %w", err)}
		}
	}
	sheets, frames, failure := assembler.finish()
	if failure != nil {
		failure.Decoder = a.decoder
		return Result{}, failure
	}
	return Result{Decoder: a.decoder, Sheets: sheets, SheetFrames: frames, SheetTileHeight: req.Sheets.TileHeight}, nil
}

// exec runs one ffmpeg process of the attempt with args, feeding it stdin
// when that is not nil, writing its stdout to stdout when that is not nil,
// and routing its log to handlers.
func (a attemptRun) exec(req Request, args []string, stdinBytes []byte, stdoutWriter io.Writer, handlers ...func(string)) *AttemptError {
	var stdin io.Reader
	if stdinBytes != nil {
		stdin = bytes.NewReader(stdinBytes)
	}
	router := newStderrRouter(handlers...)
	stderr, waitStderr := router.start()

	var state *os.ProcessState
	var err error
	replaced := a.runner.Exec != nil
	if replaced {
		err = a.runner.Exec(a.attemptCtx, a.runner.FFmpegPath, args, stdin, stdoutWriter, stderr)
	} else {
		state, err = execFFmpeg(a.attemptCtx, req.Background, a.runner.FFmpegPath, args, stdin, stdoutWriter, stderr)
	}
	_ = stderr.Close()
	waitStderr()
	if !replaced {
		workload := a.runner.Workload
		if workload == processmetrics.Transcode {
			workload = processmetrics.Analysis
		}
		processmetrics.Record(workload, state, err, a.attemptCtx.Err())
		if err == nil && state != nil && !state.Success() {
			err = fmt.Errorf("ffmpeg %s", state)
		}
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
		case replaced && errors.Is(err, context.DeadlineExceeded):
			failure.Reason = ReasonTimeout
		case !replaced && state == nil:
			failure.Reason = ReasonStart
		}
		return failure
	}
	return nil
}
