package trickplay

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// Attempt timeouts grow with the samples a request decodes: a base for
// opening the input, plus a per-sample allowance generous enough for a 4K
// HDR keyframe decoded and tone mapped in software at idle priority on a
// busy host. A run that outlives it is stuck, not slow.
const (
	attemptBaseSeconds       = 120
	hardwareSecondsPerSample = 0.5
	softwareSecondsPerSample = 2.0
	maxAttemptTimeoutSeconds = 6 * 60 * 60
)

// AttemptPlan is the attempts of a request for n samples when hardware may
// decode: hardware first and software after a hardware failure, each with a
// timeout for n samples. Without hardware it is one software attempt.
func AttemptPlan(hardware bool, n int) []mediasample.Attempt {
	software := mediasample.Attempt{TimeoutSeconds: attemptTimeout(softwareSecondsPerSample, n)}
	if !hardware {
		return []mediasample.Attempt{software}
	}
	return []mediasample.Attempt{{Hardware: true, TimeoutSeconds: attemptTimeout(hardwareSecondsPerSample, n)}, software}
}

func attemptTimeout(perSample float64, n int) float64 {
	return min(attemptBaseSeconds+perSample*float64(n), maxAttemptTimeoutSeconds)
}

// LocalExtractor makes sheets with this server's ffmpeg, on the hardware
// playback.hw_accel and playback.hw_device name.
type LocalExtractor struct {
	settings SettingsReader
	hardware *mediasample.HardwareResolver
	logger   *slog.Logger
	// exec replaces ffmpeg in tests.
	exec mediasample.ExecFunc
}

// NewLocalExtractor returns an extractor configured by settings.
func NewLocalExtractor(settings SettingsReader) *LocalExtractor {
	return &LocalExtractor{
		settings: settings,
		hardware: mediasample.NewHardwareResolver("", ""),
		logger:   slog.Default().With("component", "trickplay"),
	}
}

// Extract runs req on this server.
func (e *LocalExtractor) Extract(ctx context.Context, _ *Job, req mediasample.Request) (mediasample.Result, error) {
	ffmpeg := playback.ResolveFFmpegPath(readSetting(ctx, e.settings, ffmpegPathSetting))
	accel := readSetting(ctx, e.settings, hwAccelSetting)
	if accel == "" {
		accel = "auto"
	}
	e.hardware.Set(accel, readSetting(ctx, e.settings, hwDeviceSetting))
	resolved, device := e.hardware.Backend(ctx, ffmpeg)
	hardware := mediasample.SupportsHardwareDecode(resolved)
	req.Attempts = AttemptPlan(hardware, len(req.Samples.Seconds))
	runner := mediasample.Runner{
		FFmpegPath: ffmpeg,
		Workload:   processmetrics.Trickplay,
		Exec:       e.exec,
		Fallback:   e.fallback(ctx, resolved),
	}
	if hardware {
		runner.HWAccel, runner.HWDevice = resolved, device
	}
	result, err := runner.Run(ctx, req)
	// Opening media stays in the subprocess, where the attempt timeout and
	// caller cancellation apply to input opening too. Keep absent
	// mounts and permissions as retryable input failures.
	if failure, ok := errors.AsType[*mediasample.Error](err); ok && ctx.Err() == nil {
		for _, attempt := range failure.Attempts {
			message := strings.ToLower(attempt.StderrTail)
			if strings.Contains(message, "no such file or directory") || strings.Contains(message, "permission denied") {
				return result, &inputError{err: err}
			}
		}
	}
	return result, err
}

// fallback moves a failed hardware attempt on to software, logging the
// first failure per accelerator loudly. An input that has no video stream
// fails the same way in software, so that ends the run.
func (e *LocalExtractor) fallback(ctx context.Context, accel string) func(mediasample.Attempt, mediasample.AttemptError) bool {
	return func(attempt mediasample.Attempt, failure mediasample.AttemptError) bool {
		if !attempt.Hardware {
			return true
		}
		if failure.Cause() == mediasample.ReasonNoStream {
			return false
		}
		level := slog.LevelDebug
		if e.hardware.FirstFailure(accel) {
			level = slog.LevelWarn
		}
		e.logger.Log(ctx, level, "trickplay hardware decode failed; using software",
			"decoder", failure.Decoder, "reason", failure.Reason, "error", failure.Err, "stderr_tail", failure.StderrTail)
		return true
	}
}

func readSetting(ctx context.Context, settings SettingsReader, key string) string {
	if settings == nil {
		return ""
	}
	value, err := settings.Get(ctx, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}
