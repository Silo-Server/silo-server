package playback

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// EffectiveEncoderHWAccel reports video encoding independently of a GPU used
// for decoding or tone mapping. The execution backend remains in HWAccel.
func (opts TranscodeOpts) EffectiveEncoderHWAccel() string {
	if opts.softwareEncode {
		return transcodeHWNone
	}
	if opts.EncoderHWAccel != "" {
		return opts.EncoderHWAccel
	}
	return opts.HWAccel
}

// resolveHEVCTranscodeEncoder validates the target on the concrete device
// selected by the allocator. Backend detection establishes H.264 readiness;
// older GPUs may still lack HEVC encoding even when FFmpeg lists that encoder.
func resolveHEVCTranscodeEncoder(ctx context.Context, opts TranscodeOpts) (TranscodeOpts, error) {
	opts.softwareEncode = false
	opts.EncoderHWAccel = ""
	if !strings.EqualFold(opts.TargetCodecVideo, transcodeCodecHEVC) {
		return opts, nil
	}
	backend := opts.HWAccel
	var hardwareOK bool
	switch backend {
	case transcodeHWVideoToolbox:
		hardwareOK, _ = videoToolboxSupportsTargetCodecContext(ctx, opts.FFmpegPath, transcodeCodecHEVC)
	case transcodeHWQSV, transcodeHWVAAPI, transcodeHWNVENC:
		probe := hwBackendProbe{commandCount: 1, run: func(ctx context.Context, path, device string, timeout time.Duration) hwProbeResult {
			args := hardwareSmokeEncodeArgs(backend, device)
			for i := range args {
				if args[i] == hardwareEncoder(backend) {
					args[i] = "hevc_" + backend
				}
			}
			output, err := runFFmpegProbe(ctx, timeout, path, args...)
			if err != nil {
				return hwProbeResult{reason: FormatFFmpegProbeFailure(err, output)}
			}
			return hwProbeResult{available: true}
		}}
		hardwareOK, _ = cachedHardwareProbeContext(ctx, backend+":hevc", opts.FFmpegPath, opts.HWDevice, probe)
	}
	if err := ctx.Err(); err != nil {
		return opts, err
	}
	if hardwareOK {
		return opts, nil
	}
	if err := requireSoftwareEncoder(ctx, opts.FFmpegPath, transcodeCodecHEVC); err != nil {
		return opts, err
	}
	if opts.ToneMapMode == tonemap.ModeHardware {
		// Preserve the frozen GPU conversion; only its final SDR frames move
		// to the CPU encoder. Reconstruction validates the target again.
		opts.softwareEncode = true
	} else {
		opts.HWAccel = transcodeHWNone
	}
	if backend != "" && backend != transcodeHWNone {
		slog.InfoContext(ctx, "HEVC hardware encoder unavailable; using libx265", "backend", backend, "tone_map_mode", opts.ToneMapMode)
	}
	return opts, nil
}

// requireSoftwareEncoder verifies, once per FFmpeg binary, that the software
// encoder for codec (libx265 for HEVC, libx264 otherwise) can encode, so a
// software fallback never starts on a build without it.
func requireSoftwareEncoder(ctx context.Context, ffmpegPath, codec string) error {
	args, key, name := h264SoftwareSmokeArgs(), "software:h264", "H.264"
	if strings.EqualFold(codec, transcodeCodecHEVC) {
		args, key, name = hevcSoftwareSmokeArgsV3(), "software:hevc", "HEVC"
	}
	probe := hwBackendProbe{commandCount: 1, run: func(ctx context.Context, path, _ string, timeout time.Duration) hwProbeResult {
		output, err := runFFmpegProbe(ctx, timeout, path, args...)
		if err != nil {
			return hwProbeResult{reason: FormatFFmpegProbeFailure(err, output)}
		}
		return hwProbeResult{available: true}
	}}
	available, reason := cachedHardwareProbeContext(ctx, key, ffmpegPath, "", probe)
	if err := ctx.Err(); err != nil {
		return err
	}
	if !available {
		return fmt.Errorf("%s encoder unavailable: %s", name, reason)
	}
	return nil
}

// h264SoftwareSmokeArgs is hevcSoftwareSmokeArgsV3 with libx264 in place of
// libx265 and without the libx265-only option.
func h264SoftwareSmokeArgs() []string {
	hevc := hevcSoftwareSmokeArgsV3()
	args := make([]string, 0, len(hevc))
	for i := 0; i < len(hevc); i++ {
		switch hevc[i] {
		case hevcSoftwareEncoderV3:
			args = append(args, "libx264")
		case "-x265-params":
			i++ // skip its value too
		default:
			args = append(args, hevc[i])
		}
	}
	return args
}
