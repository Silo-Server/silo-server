package playback

import (
	"context"
	"strings"
	"time"
)

// resolveVAAPIRateControl records whether the concrete VAAPI device offers
// VBR for the target encoder. A capped encode forces VBR where it exists,
// because FFmpeg's automatic VAAPI mode tries AVBR first and AVBR does not
// honor -maxrate. On a driver without VBR the mode is left to FFmpeg, which
// falls back to CBR, so an explicitly configured VAAPI backend still starts.
// The answer is cached per device and derived again on every start, like the
// HEVC encoder choice, so recipe cards never freeze it.
func resolveVAAPIRateControl(ctx context.Context, opts TranscodeOpts) TranscodeOpts {
	opts.vaapiVBR = false
	if opts.HWAccel != transcodeHWVAAPI || opts.TargetBitrateKbps <= 0 || opts.softwareHEVCEncode {
		return opts
	}
	encoder, key := encoderH264VAAPI, "vaapi:vbr"
	switch strings.ToLower(strings.TrimSpace(opts.TargetCodecVideo)) {
	case codecCopyV3:
		return opts
	case transcodeCodecHEVC:
		encoder, key = "hevc_vaapi", "vaapi:hevc:vbr"
	}
	probe := hwBackendProbe{commandCount: 1, run: func(ctx context.Context, path, device string, timeout time.Duration) hwProbeResult {
		output, err := runFFmpegProbe(ctx, timeout, path, vaapiVBRSmokeArgs(device, encoder)...)
		if err != nil {
			return hwProbeResult{reason: FormatFFmpegProbeFailure(err, output)}
		}
		return hwProbeResult{available: true}
	}}
	opts.vaapiVBR, _ = cachedHardwareProbeContext(ctx, key, opts.FFmpegPath, opts.HWDevice, probe)
	return opts
}

// vaapiVBRSmokeArgs is the VAAPI hardware smoke encode with the encoder
// swapped in and the capped VBR mode the transcode would request.
func vaapiVBRSmokeArgs(device, encoder string) []string {
	base := hardwareSmokeEncodeArgs(transcodeHWVAAPI, device)
	sink := base[len(base)-3:] // -f null -
	args := append([]string{}, base[:len(base)-3]...)
	for i := range args {
		if args[i] == encoderH264VAAPI {
			args[i] = encoder
		}
	}
	args = append(args, "-rc_mode", "VBR", "-b:v", "1800k", "-maxrate", "2000k")
	return append(args, sink...)
}
