package chapterthumbs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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

	resolvedAccel := playback.ResolveHWAccelWithFFmpeg(opts.HWAccel, ffmpegPath)
	resolvedDevice := opts.HWDevice
	if resolvedDevice == "" && (resolvedAccel == "qsv" || resolvedAccel == "vaapi") {
		resolvedDevice = playback.PickRenderDevice("")
	}

	if resolvedAccel != "none" {
		args, buildErr := buildFrameExtractArgs(opts.InputPath, opts.SeekSeconds, resolvedAccel, resolvedDevice, opts.ToneMap)
		if buildErr != nil {
			if opts.ToneMap {
				return nil, "tonemap_unsupported", wrapReason("tonemap_unsupported", buildErr)
			}
		} else {
			attemptCtx, cancel := context.WithTimeout(ctx, extractTimeoutForAttempt(true, opts.ToneMap))
			data, err := runExtract(attemptCtx, ffmpegPath, args)
			cancel()
			if err == nil {
				return data, "", nil
			}

			hwReason := classifyExtractError("hw", err)
			if opts.ToneMap {
				return nil, hwReason, wrapReason(hwReason, err)
			}

			cpuData, cpuReason, cpuErr := extractFrameCPU(
				ctx,
				opts.InputPath,
				opts.SeekSeconds,
				false,
				runExtract,
				ffmpegPath,
			)
			if cpuErr == nil {
				return cpuData, "", nil
			}
			return nil, cpuReason, fmt.Errorf("hardware extraction failed: %v; cpu fallback failed: %w", wrapReason(hwReason, err), cpuErr)
		}
	}

	if opts.ToneMap {
		err := errors.New("hardware HDR tone mapping unavailable")
		return nil, "tonemap_unsupported", wrapReason("tonemap_unsupported", err)
	}

	return extractFrameCPU(ctx, opts.InputPath, opts.SeekSeconds, opts.ToneMap, runExtract, ffmpegPath)
}

func extractFrameCPU(
	ctx context.Context,
	inputPath string,
	seekSeconds float64,
	toneMap bool,
	runExtract func(ctx context.Context, ffmpegPath string, args []string) ([]byte, error),
	ffmpegPath string,
) ([]byte, string, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, extractTimeoutForAttempt(false, toneMap))
	defer cancel()
	data, err := runExtract(attemptCtx, ffmpegPath, buildCPUFrameExtractArgs(inputPath, seekSeconds, toneMap))
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
		return "tonemap_unsupported"
	case strings.Contains(lower, "invalid nal unit size"),
		strings.Contains(lower, "error splitting the input into nal units"),
		strings.Contains(lower, "invalid data found when processing input"),
		strings.Contains(lower, "invalid as first byte of an ebml number"):
		return "decode_invalid_data"
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

func buildCPUFrameExtractArgs(inputPath string, seekSeconds float64, toneMap bool) []string {
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-ss", fmt.Sprintf("%.3f", seekSeconds),
		"-i", inputPath,
	}
	if toneMap {
		args = append(args, "-vf", "zscale=t=linear:npl=100,format=gbrpf32le,tonemap=bt2390,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p")
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
	case "qsv":
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
	case "vaapi":
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
		return buildCPUFrameExtractArgs(inputPath, seekSeconds, toneMap), nil
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
