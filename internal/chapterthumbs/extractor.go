package chapterthumbs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type FrameExtractOptions struct {
	InputPath   string
	SeekSeconds float64
	FFmpegPath  string
	HWAccel     string
	HWDevice    string
	ToneMap     bool
	RunFunc     func(ctx context.Context, ffmpegPath string, args []string) ([]byte, error)

	softwareToneMapResolver *softwareToneMapFilterResolver
}

const (
	defaultFFmpegExecutable     = "ffmpeg"
	hwAccelQSV                  = "qsv"
	hwAccelVAAPI                = "vaapi"
	reasonDecodeInvalidData     = "decode_invalid_data"
	reasonToneMapUnsupported    = "tonemap_unsupported"
	softwareToneMapProbeTimeout = 3 * time.Second
	softwareToneMapFilterBT2390 = "zscale=t=linear:npl=100,format=gbrpf32le,tonemapx=tonemap=bt2390,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p"
	softwareToneMapFilterHable  = "zscale=t=linear:npl=100,format=gbrpf32le,tonemap=hable,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p"
)

type softwareToneMapProbeResult struct {
	filter string
	reason string
}

type softwareToneMapFilterResolver struct {
	mu      sync.Mutex
	byPath  map[string]softwareToneMapProbeResult
	probeFn func(ffmpegPath string) ([]byte, error)
}

var defaultSoftwareToneMapFilterResolver = newSoftwareToneMapFilterResolver(runFFmpegFilterProbe)

func newSoftwareToneMapFilterResolver(probeFn func(ffmpegPath string) ([]byte, error)) *softwareToneMapFilterResolver {
	return &softwareToneMapFilterResolver{
		byPath:  make(map[string]softwareToneMapProbeResult),
		probeFn: probeFn,
	}
}

func (r *softwareToneMapFilterResolver) resolve(ffmpegPath string) (string, error) {
	ffmpegPath = normalizeSoftwareToneMapFFmpegPath(ffmpegPath)

	r.mu.Lock()
	defer r.mu.Unlock()
	if result, ok := r.byPath[ffmpegPath]; ok {
		return softwareToneMapProbeResultValue(result)
	}

	result := probeSoftwareToneMapFilter(ffmpegPath, r.probeFn)
	r.byPath[ffmpegPath] = result
	return softwareToneMapProbeResultValue(result)
}

func normalizeSoftwareToneMapFFmpegPath(ffmpegPath string) string {
	ffmpegPath = strings.TrimSpace(ffmpegPath)
	if ffmpegPath == "" {
		return defaultFFmpegExecutable
	}
	if strings.ContainsRune(ffmpegPath, os.PathSeparator) {
		return filepath.Clean(ffmpegPath)
	}
	return ffmpegPath
}

func softwareToneMapProbeResultValue(result softwareToneMapProbeResult) (string, error) {
	if result.filter != "" {
		return result.filter, nil
	}
	if result.reason == "" {
		result.reason = "configured FFmpeg has no supported software HDR tone-map filter chain"
	}
	return "", errors.New(result.reason)
}

func probeSoftwareToneMapFilter(
	ffmpegPath string,
	probeFn func(ffmpegPath string) ([]byte, error),
) softwareToneMapProbeResult {
	if probeFn == nil {
		return softwareToneMapProbeResult{reason: "FFmpeg filter probe is unavailable"}
	}
	output, err := probeFn(ffmpegPath)
	if err != nil {
		return softwareToneMapProbeResult{reason: "FFmpeg filter probe failed: " + filterProbeFailure(err, output)}
	}
	if !ffmpegFilterOutputHasToken(output, "zscale") {
		return softwareToneMapProbeResult{reason: "configured FFmpeg lacks the required zscale filter"}
	}
	if ffmpegFilterOutputHasToken(output, "tonemapx") {
		return softwareToneMapProbeResult{filter: softwareToneMapFilterBT2390}
	}
	if ffmpegFilterOutputHasToken(output, "tonemap") {
		return softwareToneMapProbeResult{filter: softwareToneMapFilterHable}
	}
	return softwareToneMapProbeResult{reason: "configured FFmpeg lacks the required tonemapx or tonemap filter"}
}

func ExtractFrame(ctx context.Context, opts FrameExtractOptions) ([]byte, string, error) {
	ffmpegPath := opts.FFmpegPath
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	runExtract := opts.RunFunc
	if runExtract == nil {
		runExtract = runFFmpegFrameExtract
	}
	softwareToneMapResolver := opts.softwareToneMapResolver
	if softwareToneMapResolver == nil {
		softwareToneMapResolver = defaultSoftwareToneMapFilterResolver
	}

	resolvedAccel := playback.ResolveHWAccelWithFFmpeg(opts.HWAccel, ffmpegPath)
	if supportsHardwareFrameExtract(resolvedAccel) {
		// Resolve a multi-device hw_device list to one concrete GPU for this
		// extraction; the reservation spans only the hardware attempt below.
		resolvedDevice, releaseHWDevice := playback.AcquireHWDevice(opts.HWDevice, resolvedAccel)
		defer releaseHWDevice()
		if resolvedDevice == "" {
			resolvedDevice = playback.PickRenderDevice("")
		}

		args, buildErr := buildFrameExtractArgs(opts.InputPath, opts.SeekSeconds, resolvedAccel, resolvedDevice, opts.ToneMap)
		if buildErr == nil {
			attemptCtx, cancel := context.WithTimeout(ctx, extractTimeoutForAttempt(true, opts.ToneMap))
			data, err := runExtract(attemptCtx, ffmpegPath, args)
			cancel()
			releaseHWDevice()
			if err == nil {
				return data, "", nil
			}

			hwReason := classifyExtractError("hw", err)
			if hwReason == reasonDecodeInvalidData {
				return nil, hwReason, wrapReason(hwReason, err)
			}
			return extractFrameCPUFallback(
				ctx,
				opts.InputPath,
				opts.SeekSeconds,
				opts.ToneMap,
				runExtract,
				ffmpegPath,
				softwareToneMapResolver,
				hwReason,
				err,
			)
		}

		releaseHWDevice()
		return extractFrameCPUFallback(
			ctx,
			opts.InputPath,
			opts.SeekSeconds,
			opts.ToneMap,
			runExtract,
			ffmpegPath,
			softwareToneMapResolver,
			"chapter_extract_failed",
			buildErr,
		)
	}

	return extractFrameCPU(
		ctx,
		opts.InputPath,
		opts.SeekSeconds,
		opts.ToneMap,
		runExtract,
		ffmpegPath,
		softwareToneMapResolver,
	)
}

func supportsHardwareFrameExtract(hwAccel string) bool {
	return hwAccel == hwAccelQSV || hwAccel == hwAccelVAAPI
}

func extractFrameCPUFallback(
	ctx context.Context,
	inputPath string,
	seekSeconds float64,
	toneMap bool,
	runExtract func(ctx context.Context, ffmpegPath string, args []string) ([]byte, error),
	ffmpegPath string,
	softwareToneMapResolver *softwareToneMapFilterResolver,
	hwReason string,
	hwErr error,
) ([]byte, string, error) {
	cpuData, cpuReason, cpuErr := extractFrameCPU(
		ctx,
		inputPath,
		seekSeconds,
		toneMap,
		runExtract,
		ffmpegPath,
		softwareToneMapResolver,
	)
	if cpuErr == nil {
		return cpuData, "", nil
	}
	return nil, cpuReason, fmt.Errorf(
		"hardware extraction failed: %w; cpu fallback failed: %w",
		wrapReason(hwReason, hwErr),
		cpuErr,
	)
}

func extractFrameCPU(
	ctx context.Context,
	inputPath string,
	seekSeconds float64,
	toneMap bool,
	runExtract func(ctx context.Context, ffmpegPath string, args []string) ([]byte, error),
	ffmpegPath string,
	softwareToneMapResolver *softwareToneMapFilterResolver,
) ([]byte, string, error) {
	softwareToneMapFilter := ""
	if toneMap {
		filter, err := softwareToneMapResolver.resolve(ffmpegPath)
		if err != nil {
			return nil, reasonToneMapUnsupported, wrapReason(reasonToneMapUnsupported, err)
		}
		softwareToneMapFilter = filter
	}

	attemptCtx, cancel := context.WithTimeout(ctx, extractTimeoutForAttempt(false, toneMap))
	defer cancel()
	data, err := runExtract(attemptCtx, ffmpegPath, buildCPUFrameExtractArgs(inputPath, seekSeconds, softwareToneMapFilter))
	if err != nil {
		reason := classifyExtractError("cpu", err)
		return nil, reason, wrapReason(reason, err)
	}
	return data, "", nil
}

func classifyExtractError(stage string, err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(message, "No such filter") || strings.Contains(message, "tonemap") && strings.Contains(message, "Error"):
		return reasonToneMapUnsupported
	case strings.Contains(lower, "invalid nal unit size"),
		strings.Contains(lower, "error splitting the input into nal units"),
		strings.Contains(lower, "invalid data found when processing input"),
		strings.Contains(lower, "invalid as first byte of an ebml number"):
		return reasonDecodeInvalidData
	case stage == "hw" && strings.Contains(message, "signal: killed"):
		return "hw_killed"
	case stage == "hw" && isDeadlineError(err):
		return "hw_timeout"
	case stage == "cpu" && isDeadlineError(err):
		return "cpu_timeout"
	default:
		return "chapter_extract_failed"
	}
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

func buildCPUFrameExtractArgs(inputPath string, seekSeconds float64, softwareToneMapFilter string) []string {
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-ss", fmt.Sprintf("%.3f", seekSeconds),
		"-i", inputPath,
	}
	if softwareToneMapFilter != "" {
		args = append(args, "-vf", softwareToneMapFilter)
	}
	args = append(args,
		"-frames:v", "1",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-",
	)
	return args
}

func buildFrameExtractArgs(inputPath string, seekSeconds float64, hwAccel string, hwDevice string, toneMap bool) ([]string, error) {
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
	}
	switch hwAccel {
	case hwAccelQSV:
		if hwDevice == "" {
			return nil, fmt.Errorf("qsv requires a render device")
		}
		args = append(args,
			"-init_hw_device", fmt.Sprintf("vaapi=va:%s,driver=iHD,kernel_driver=i915,vendor_id=0x8086", hwDevice),
			"-init_hw_device", "qsv=qs@va",
			"-filter_hw_device", "va",
			"-hwaccel", "vaapi",
			"-hwaccel_output_format", "vaapi",
		)
	case hwAccelVAAPI:
		if hwDevice == "" {
			return nil, fmt.Errorf("vaapi requires a render device")
		}
		args = append(args,
			"-init_hw_device", fmt.Sprintf("vaapi=hw:%s", hwDevice),
			"-filter_hw_device", "hw",
			"-hwaccel", "vaapi",
			"-hwaccel_output_format", "vaapi",
		)
	default:
		return nil, fmt.Errorf("hardware chapter thumbnail extraction does not support %q", hwAccel)
	}

	filter := "hwdownload,format=nv12"
	if toneMap {
		filter = "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,procamp_vaapi=b=16:c=1,tonemap_vaapi=format=nv12:p=bt709:t=bt709:m=bt709,hwdownload,format=nv12"
	}
	args = append(args,
		"-ss", fmt.Sprintf("%.3f", seekSeconds),
		"-i", inputPath,
		"-vf", filter,
		"-frames:v", "1",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-",
	)
	return args, nil
}

func runFFmpegFilterProbe(ffmpegPath string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), softwareToneMapProbeTimeout)
	defer cancel()
	return exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-filters").CombinedOutput()
}

func ffmpegFilterOutputHasToken(output []byte, token string) bool {
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.Contains(fields[2], "->") {
			continue
		}
		if strings.EqualFold(fields[1], token) {
			return true
		}
	}
	return false
}

func filterProbeFailure(err error, output []byte) string {
	message := strings.TrimSpace(err.Error())
	if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
		if len(trimmed) > 240 {
			trimmed = trimmed[:240] + "..."
		}
		message += ": " + trimmed
	}
	return message
}

func runFFmpegFrameExtract(ctx context.Context, ffmpegPath string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg extract frame: %w (%s)", err, stderr.String())
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg extract frame: empty output")
	}
	return stdout.Bytes(), nil
}
