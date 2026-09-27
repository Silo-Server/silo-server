package chapterthumbs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

type FrameExtractOptions struct {
	InputPath            string
	SeekSeconds          float64
	FFmpegPath           string
	HWAccel              string
	HWDevice             string
	ToneMap              bool
	AllowSoftwareToneMap bool
	// RunFunc runs ffmpeg in place of a process; tests set it. It returns the
	// JPEG frame, or an error whose text carries ffmpeg's log.
	RunFunc func(ctx context.Context, ffmpegPath string, args []string) ([]byte, error)

	loadCapabilities func(ctx context.Context, ffmpegPath string) (mediasample.Capabilities, error)
	resolveHWAccel   func(ctx context.Context, hwAccel, ffmpegPath, hwDevice string) string
}

const (
	hwAccelNone                = "none"
	hwAccelVideoToolbox        = "videotoolbox"
	reasonChapterExtractFailed = "chapter_extract_failed"
	reasonDecodeInvalidData    = "decode_invalid_data"
	reasonFFmpegProbeFailed    = "ffmpeg_probe_failed"
	reasonToneMapUnsupported   = "tonemap_unsupported"
	reasonHWKilled             = "hw_killed"
	reasonHWTimeout            = "hw_timeout"
	reasonCPUTimeout           = "cpu_timeout"
	// softwareToneMapProbeTimeout budgets the ffmpeg capability listings
	// that software tone mapping needs before its first extraction; see
	// mediasample.LoadCapabilities.
	softwareToneMapProbeTimeout = mediasample.CapabilitiesTimeout
)

// ExtractFrame extracts the frame at opts.SeekSeconds as a JPEG through
// mediasample. It returns the frame, or a failure reason (one of the reason
// constants above) and the error.
func ExtractFrame(ctx context.Context, opts FrameExtractOptions) ([]byte, string, error) {
	ffmpegPath := playback.ResolveFFmpegPath(opts.FFmpegPath)
	resolveHWAccel := opts.resolveHWAccel
	if resolveHWAccel == nil {
		resolveHWAccel = playback.ResolveHWAccelWithFFmpegContext
	}
	resolvedAccel := resolveHWAccel(ctx, opts.HWAccel, ffmpegPath, opts.HWDevice)

	attempts := extractAttempts(resolvedAccel, opts.ToneMap, opts.AllowSoftwareToneMap)
	plan := extractPlan{attempts: attempts, accel: resolvedAccel, toneMap: opts.ToneMap}
	req := mediasample.Request{
		Input:    opts.InputPath,
		At:       &mediasample.At{Seconds: opts.SeekSeconds},
		Images:   &mediasample.ImageOutput{},
		Attempts: attempts,
	}
	if opts.ToneMap {
		req.Images.ToneMap = &mediasample.ToneMap{AllowSoftware: opts.AllowSoftwareToneMap}
	}
	runner := mediasample.Runner{
		FFmpegPath:   ffmpegPath,
		HWAccel:      resolvedAccel,
		HWDevice:     opts.HWDevice,
		Workload:     processmetrics.Thumbnail,
		Capabilities: opts.loadCapabilities,
		Fallback:     plan.fallback,
	}
	if opts.RunFunc != nil {
		runner.Exec = execRunFunc(opts.RunFunc)
	}
	result, err := runner.Run(ctx, req)
	if err != nil {
		reason, err := plan.failure(err)
		return nil, reason, err
	}
	return result.Images[0].JPEG, "", nil
}

// extractAttempts plans the decode attempts. A backend that decodes in
// hardware tries hardware first and falls back to software, unless tone
// mapping is needed and software tone mapping is not allowed. Any other
// configured backend (such as NVENC) tries an SDR frame twice in software,
// first on the hardware attempt's shorter budget. Without a backend there is
// one software attempt. mediasample refuses an attempt that would tone map in
// software when that is not allowed.
func extractAttempts(accel string, toneMap bool, allowSoftwareToneMap bool) []mediasample.Attempt {
	software := extractAttempt(false, false, toneMap)
	switch {
	case mediasample.SupportsHardwareDecode(accel):
		attempts := []mediasample.Attempt{extractAttempt(true, true, toneMap)}
		if !toneMap || allowSoftwareToneMap {
			attempts = append(attempts, software)
		}
		return attempts
	case accel != hwAccelNone && !toneMap:
		return []mediasample.Attempt{extractAttempt(false, true, false), software}
	}
	return []mediasample.Attempt{software}
}

// extractAttempt is one attempt, on the hardware or software time budget.
func extractAttempt(hardware bool, hardwareBudget bool, hdr bool) mediasample.Attempt {
	return mediasample.Attempt{
		Hardware:       hardware,
		TimeoutSeconds: extractTimeoutForAttempt(hardwareBudget, hdr).Seconds(),
	}
}

// execRunFunc runs a RunFunc in place of ffmpeg. Its error text carries
// ffmpeg's log, so it becomes the log mediasample classifies.
func execRunFunc(run func(ctx context.Context, ffmpegPath string, args []string) ([]byte, error)) mediasample.ExecFunc {
	return func(ctx context.Context, name string, args []string, _ io.Reader, stdout, stderr io.Writer) error {
		data, err := run(ctx, name, args)
		if err != nil {
			_, _ = io.WriteString(stderr, err.Error())
			return err
		}
		_, err = stdout.Write(data)
		return err
	}
}

// extractPlan is one extraction's attempts and the settings that shaped
// them. It maps failed attempts to the persisted reasons with the rules
// chapter thumbnails have always used, which mediasample.Classify does not
// share: the stored reasons decide per-file backoff (see
// shouldApplyFileFailure).
type extractPlan struct {
	attempts []mediasample.Attempt
	accel    string
	toneMap  bool
}

// fallback reports whether a failed attempt moves on to the next one. Only a
// hardware attempt ends the run early: when its decode found invalid data,
// which software decoding would find too, or when VideoToolbox's software
// tone mapping was refused before ffmpeg started, which the software attempt
// would repeat.
func (p extractPlan) fallback(attempt mediasample.Attempt, failure mediasample.AttemptError) bool {
	if !attempt.Hardware {
		return true
	}
	if p.refusedSoftwareToneMap(attempt, failure) {
		return false
	}
	return p.reason(attempt, failure, true) != reasonDecodeInvalidData
}

// failure turns a failed run into the persisted reason and error. The reason
// is the last attempt's. When a second attempt ran, the error reports both,
// as "hardware extraction failed: ...; cpu fallback failed: ...".
func (p extractPlan) failure(err error) (string, error) {
	var runErr *mediasample.Error
	if !errors.As(err, &runErr) || len(runErr.Attempts) == 0 {
		return reasonChapterExtractFailed, wrapReason(reasonChapterExtractFailed, err)
	}
	failed := runErr.Attempts
	reasons := make([]string, len(failed))
	errs := make([]error, len(failed))
	for i, attempt := range failed {
		// Every attempt but the last planned one runs on the hardware budget
		// and reports as the hardware stage, even a software one.
		lastPlanned := i == len(p.attempts)-1
		reasons[i] = p.reason(p.attempts[i], attempt, lastPlanned)
		errs[i] = wrapReason(reasons[i], &mediasample.Error{Reason: attempt.Reason, Attempts: []mediasample.AttemptError{attempt}})
	}
	last := len(failed) - 1
	if last == 0 {
		return reasons[0], errs[0]
	}
	return reasons[last], fmt.Errorf("hardware extraction failed: %w; cpu fallback failed: %w", errs[0], errs[last])
}

// reason maps a failed attempt to the persisted reason. lastPlanned reports
// whether no attempt follows it in the plan; the others report as the
// hardware stage.
func (p extractPlan) reason(attempt mediasample.Attempt, failure mediasample.AttemptError, lastPlanned bool) string {
	hardwareStage := attempt.Hardware || !lastPlanned
	switch failure.Reason {
	case mediasample.ReasonCapabilities:
		return reasonFFmpegProbeFailed
	case mediasample.ReasonUnsupported:
		// Refused before ffmpeg started. A hardware attempt that the host
		// cannot build (no render device) and that software follows is an
		// ordinary failure; every other refusal leaves no way to tone map.
		if attempt.Hardware && !lastPlanned && !p.refusedSoftwareToneMap(attempt, failure) {
			return reasonChapterExtractFailed
		}
		return reasonToneMapUnsupported
	case mediasample.ReasonExit, mediasample.ReasonTimeout:
	default:
		return reasonChapterExtractFailed
	}
	message := failure.StderrTail
	if failure.Err != nil {
		message = failure.Err.Error() + " (" + failure.StderrTail + ")"
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(message, "No such filter") || strings.Contains(message, "tonemap") && strings.Contains(message, "Error"):
		return reasonToneMapUnsupported
	case strings.Contains(lower, "invalid nal unit size"),
		strings.Contains(lower, "error splitting the input into nal units"),
		strings.Contains(lower, "invalid data found when processing input"),
		strings.Contains(lower, "invalid as first byte of an ebml number"):
		return reasonDecodeInvalidData
	case hardwareStage && strings.Contains(message, "signal: killed"):
		return reasonHWKilled
	case failure.Reason == mediasample.ReasonTimeout || isDeadlineError(failure.Err):
		if hardwareStage {
			return reasonHWTimeout
		}
		return reasonCPUTimeout
	}
	return reasonChapterExtractFailed
}

// refusedSoftwareToneMap reports a VideoToolbox HDR attempt refused before
// ffmpeg started. VideoToolbox frames tone map in software, and its
// arguments need no device, so the refusal is software tone mapping's:
// disabled, missing filters, or a failed capability listing.
func (p extractPlan) refusedSoftwareToneMap(attempt mediasample.Attempt, failure mediasample.AttemptError) bool {
	refused := failure.Reason == mediasample.ReasonUnsupported || failure.Reason == mediasample.ReasonCapabilities
	return refused && attempt.Hardware && p.toneMap && p.accel == hwAccelVideoToolbox
}

func isDeadlineError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), context.DeadlineExceeded.Error())
}

func extractTimeoutForAttempt(hardware bool, hdr bool) time.Duration {
	if hardware {
		if hdr {
			return hwExtractTimeoutHDR
		}
		return hwExtractTimeoutSDR
	}
	if hdr {
		return cpuExtractTimeoutHDR
	}
	return cpuExtractTimeoutSDR
}
