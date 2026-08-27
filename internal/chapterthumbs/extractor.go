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
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

type FrameExtractOptions struct {
	InputPath            string
	SeekSeconds          float64
	FFmpegPath           string
	HWAccel              string
	HWDevice             string
	ToneMap              bool
	AllowSoftwareToneMap bool
	RunFunc              func(ctx context.Context, ffmpegPath string, args []string) ([]byte, error)

	softwareToneMapResolver *softwareToneMapFilterResolver
	resolveHWAccel          func(ctx context.Context, hwAccel, ffmpegPath string) string
}

const (
	hwAccelNone                 = "none"
	hwAccelQSV                  = "qsv"
	hwAccelVAAPI                = "vaapi"
	hwAccelVideoToolbox         = "videotoolbox"
	reasonChapterExtractFailed  = "chapter_extract_failed"
	reasonDecodeInvalidData     = "decode_invalid_data"
	reasonFFmpegProbeFailed     = "ffmpeg_probe_failed"
	reasonToneMapUnsupported    = "tonemap_unsupported"
	softwareToneMapProbeTimeout = 3 * time.Second
	softwareToneMapFilterBT2390 = "tonemapx=tonemap=bt2390,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p"
	softwareToneMapFilterHable  = "zscale=t=linear:npl=100,format=gbrpf32le,tonemap=hable,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p"
)

type softwareToneMapProbeResult struct {
	filter        string
	failureReason string
	detail        string
	cacheable     bool
}

type softwareToneMapProbeCall struct {
	done    chan struct{}
	result  softwareToneMapProbeResult
	waiters int
}

type softwareToneMapFilterResolver struct {
	mu       sync.Mutex
	byPath   map[string]softwareToneMapProbeResult
	inFlight map[string]*softwareToneMapProbeCall
	probeFn  func(ffmpegPath string) ([]byte, error)
}

var defaultSoftwareToneMapFilterResolver = newSoftwareToneMapFilterResolver(runFFmpegFilterProbe)

func newSoftwareToneMapFilterResolver(probeFn func(ffmpegPath string) ([]byte, error)) *softwareToneMapFilterResolver {
	return &softwareToneMapFilterResolver{
		byPath:   make(map[string]softwareToneMapProbeResult),
		inFlight: make(map[string]*softwareToneMapProbeCall),
		probeFn:  probeFn,
	}
}

func (r *softwareToneMapFilterResolver) resolve(ffmpegPath string) (string, string, error) {
	cacheKey := softwareToneMapCacheKey(ffmpegPath)
	r.mu.Lock()
	if result, ok := r.byPath[cacheKey]; ok {
		r.mu.Unlock()
		return softwareToneMapProbeResultValue(result)
	}
	if call, ok := r.inFlight[cacheKey]; ok {
		call.waiters++
		r.mu.Unlock()
		<-call.done
		return softwareToneMapProbeResultValue(call.result)
	}
	call := &softwareToneMapProbeCall{done: make(chan struct{})}
	r.inFlight[cacheKey] = call
	r.mu.Unlock()

	result := probeSoftwareToneMapFilter(ffmpegPath, r.probeFn)
	r.mu.Lock()
	if result.cacheable {
		r.byPath[cacheKey] = result
	}
	call.result = result
	delete(r.inFlight, cacheKey)
	close(call.done)
	r.mu.Unlock()
	return softwareToneMapProbeResultValue(result)
}

func softwareToneMapCacheKey(ffmpegPath string) string {
	if strings.ContainsRune(ffmpegPath, os.PathSeparator) {
		if absolutePath, err := filepath.Abs(ffmpegPath); err == nil {
			return filepath.Clean(absolutePath)
		}
		return filepath.Clean(ffmpegPath)
	}
	return ffmpegPath
}

func softwareToneMapProbeResultValue(result softwareToneMapProbeResult) (string, string, error) {
	if result.filter != "" {
		return result.filter, "", nil
	}
	if result.failureReason == "" {
		result.failureReason = reasonChapterExtractFailed
	}
	if result.detail == "" {
		result.detail = "configured FFmpeg software HDR tone-map capability could not be determined"
	}
	return "", result.failureReason, errors.New(result.detail)
}

// probeSoftwareToneMapFilter selects a usable FFmpeg software tone-map filter.
func probeSoftwareToneMapFilter(
	ffmpegPath string,
	probeFn func(ffmpegPath string) ([]byte, error),
) softwareToneMapProbeResult {
	if probeFn == nil {
		return softwareToneMapProbeResult{
			failureReason: reasonFFmpegProbeFailed,
			detail:        "FFmpeg filter probe is unavailable",
		}
	}
	output, err := probeFn(ffmpegPath)
	if err != nil {
		return softwareToneMapProbeResult{
			failureReason: reasonFFmpegProbeFailed,
			detail:        "FFmpeg filter probe failed: " + playback.FormatFFmpegProbeFailure(err, output),
		}
	}
	filter, hasZScale := tonemap.SelectSoftwareFilter(output)
	if !hasZScale {
		return softwareToneMapProbeResult{
			failureReason: reasonToneMapUnsupported,
			detail:        "configured FFmpeg lacks the required zscale filter",
			cacheable:     true,
		}
	}
	if filter == tonemap.SoftwareFilterBT2390 {
		return softwareToneMapProbeResult{filter: softwareToneMapFilterBT2390, cacheable: true}
	}
	if filter == tonemap.SoftwareFilterHable {
		return softwareToneMapProbeResult{filter: softwareToneMapFilterHable, cacheable: true}
	}
	return softwareToneMapProbeResult{
		failureReason: reasonToneMapUnsupported,
		detail:        "configured FFmpeg lacks the required tonemapx or tonemap filter",
		cacheable:     true,
	}
}

func ExtractFrame(ctx context.Context, opts FrameExtractOptions) ([]byte, string, error) {
	ffmpegPath := playback.ResolveFFmpegPath(opts.FFmpegPath)
	runExtract := opts.RunFunc
	if runExtract == nil {
		runExtract = runFFmpegFrameExtract
	}
	softwareToneMapResolver := opts.softwareToneMapResolver
	if softwareToneMapResolver == nil {
		softwareToneMapResolver = defaultSoftwareToneMapFilterResolver
	}
	resolveHWAccel := opts.resolveHWAccel
	if resolveHWAccel == nil {
		resolveHWAccel = playback.ResolveHWAccelWithFFmpegContext
	}
	cpuOpts := cpuFrameExtractOptions{
		ctx:                     ctx,
		inputPath:               opts.InputPath,
		seekSeconds:             opts.SeekSeconds,
		toneMap:                 opts.ToneMap,
		runExtract:              runExtract,
		ffmpegPath:              ffmpegPath,
		softwareToneMapResolver: softwareToneMapResolver,
	}

	resolvedAccel := resolveHWAccel(ctx, opts.HWAccel, ffmpegPath)
	if supportsHardwareFrameExtract(resolvedAccel) {
		softwareToneMapFilter := ""
		if resolvedAccel == hwAccelVideoToolbox && opts.ToneMap {
			if !opts.AllowSoftwareToneMap {
				err := errors.New("software HDR tone mapping is disabled")
				return nil, reasonToneMapUnsupported, wrapReason(reasonToneMapUnsupported, err)
			}
			filter, reason, err := softwareToneMapResolver.resolve(ffmpegPath)
			if err != nil {
				return nil, reason, wrapReason(reason, err)
			}
			softwareToneMapFilter = filter
		}

		// Resolve a multi-device hw_device list to one concrete GPU for this
		// extraction; the reservation spans only the hardware attempt below.
		resolvedDevice, releaseHWDevice := playback.AcquireHWDevice(opts.HWDevice, resolvedAccel)
		defer releaseHWDevice()
		if resolvedDevice == "" {
			resolvedDevice = playback.PickRenderDevice("")
		}

		args, buildErr := buildFrameExtractArgs(
			opts.InputPath,
			opts.SeekSeconds,
			resolvedAccel,
			resolvedDevice,
			opts.ToneMap,
			softwareToneMapFilter,
		)
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
			if opts.ToneMap && !opts.AllowSoftwareToneMap {
				return nil, hwReason, wrapReason(hwReason, err)
			}
			return extractFrameCPUFallback(
				cpuOpts,
				hwReason,
				err,
			)
		}

		releaseHWDevice()
		if opts.ToneMap && !opts.AllowSoftwareToneMap {
			return nil, reasonToneMapUnsupported, wrapReason(reasonToneMapUnsupported, buildErr)
		}
		return extractFrameCPUFallback(
			cpuOpts,
			reasonChapterExtractFailed,
			buildErr,
		)
	}

	if opts.ToneMap && !opts.AllowSoftwareToneMap {
		err := errors.New("software HDR tone mapping is disabled")
		return nil, reasonToneMapUnsupported, wrapReason(reasonToneMapUnsupported, err)
	}

	if resolvedAccel != hwAccelNone && !opts.ToneMap {
		return extractFrameUnsupportedSDRWithRetry(cpuOpts)
	}

	return extractFrameCPU(cpuOpts)
}

func supportsHardwareFrameExtract(hwAccel string) bool {
	return hwAccel == hwAccelQSV || hwAccel == hwAccelVAAPI || hwAccel == hwAccelVideoToolbox
}

type cpuFrameExtractOptions struct {
	ctx                     context.Context
	inputPath               string
	seekSeconds             float64
	toneMap                 bool
	runExtract              func(ctx context.Context, ffmpegPath string, args []string) ([]byte, error)
	ffmpegPath              string
	softwareToneMapResolver *softwareToneMapFilterResolver
}

func extractFrameUnsupportedSDRWithRetry(opts cpuFrameExtractOptions) ([]byte, string, error) {
	attemptCtx, cancel := context.WithTimeout(opts.ctx, extractTimeoutForAttempt(true, false))
	data, err := opts.runExtract(
		attemptCtx,
		opts.ffmpegPath,
		buildCPUFrameExtractArgs(opts.inputPath, opts.seekSeconds, ""),
	)
	cancel()
	if err == nil {
		return data, "", nil
	}

	hwReason := classifyExtractError("hw", err)
	return extractFrameCPUFallback(opts, hwReason, err)
}

func extractFrameCPUFallback(
	opts cpuFrameExtractOptions,
	hwReason string,
	hwErr error,
) ([]byte, string, error) {
	cpuData, cpuReason, cpuErr := extractFrameCPU(opts)
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
	opts cpuFrameExtractOptions,
) ([]byte, string, error) {
	softwareToneMapFilter := ""
	if opts.toneMap {
		filter, reason, err := opts.softwareToneMapResolver.resolve(opts.ffmpegPath)
		if err != nil {
			return nil, reason, wrapReason(reason, err)
		}
		softwareToneMapFilter = filter
	}

	attemptCtx, cancel := context.WithTimeout(opts.ctx, extractTimeoutForAttempt(false, opts.toneMap))
	defer cancel()
	data, err := opts.runExtract(
		attemptCtx,
		opts.ffmpegPath,
		buildCPUFrameExtractArgs(opts.inputPath, opts.seekSeconds, softwareToneMapFilter),
	)
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
		return reasonChapterExtractFailed
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

func buildFrameExtractArgs(
	inputPath string,
	seekSeconds float64,
	hwAccel string,
	hwDevice string,
	toneMap bool,
	softwareToneMapFilter string,
) ([]string, error) {
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
	case hwAccelVideoToolbox:
		// VideoToolbox decodes into system-memory frames unless an explicit
		// output format is requested. Keep them there so MJPEG output and the
		// optional software HDR tone-map filter need no download/upload roundtrip.
		args = append(args, "-hwaccel", hwAccelVideoToolbox)
	default:
		return nil, fmt.Errorf("hardware chapter thumbnail extraction does not support %q", hwAccel)
	}

	var filter string
	if hwAccel == hwAccelVideoToolbox {
		if toneMap && softwareToneMapFilter == "" {
			return nil, errors.New("videotoolbox HDR extraction requires a software tone-map filter")
		}
		filter = softwareToneMapFilter
	} else if toneMap {
		filter = "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,procamp_vaapi=b=16:c=1,tonemap_vaapi=format=nv12:p=bt709:t=bt709:m=bt709,hwdownload,format=nv12"
	} else {
		filter = "hwdownload,format=nv12"
	}
	args = append(args,
		"-ss", fmt.Sprintf("%.3f", seekSeconds),
		"-i", inputPath,
	)
	if filter != "" {
		args = append(args, "-vf", filter)
	}
	args = append(args,
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
