package playback

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// vaapiCappedModes are the VAAPI rate-control modes that keep a capped encode
// under -maxrate, in order of preference. FFmpeg's automatic mode is avoided
// because it tries AVBR first (FFmpeg 7.x even with a buffer size), and AVBR
// does not honor -maxrate.
const (
	vaapiRateControlVBR = "VBR"
	vaapiRateControlCBR = "CBR"
)

var vaapiCappedModes = []string{vaapiRateControlVBR, vaapiRateControlCBR}

// resolveVAAPIRateControl records the rate-control mode a capped encode on
// the concrete VAAPI device should force: VBR where the driver offers it for
// the target encoder, else CBR. When neither passes, the encode moves to
// software, as it does on a device without HEVC encoding, because no VAAPI
// mode left to FFmpeg is known to honor the cap; it first checks that the
// software encoder (libx264 or libx265) works, like the HEVC encoder choice. Each answer is cached per
// device and derived again on every start, like the HEVC encoder choice, so
// recipe cards never freeze it. A canceled context is returned so the caller
// stops before launching FFmpeg.
func resolveVAAPIRateControl(ctx context.Context, opts TranscodeOpts) (TranscodeOpts, error) {
	opts.vaapiRateControl = ""
	if opts.HWAccel != transcodeHWVAAPI || opts.TargetBitrateKbps <= 0 || opts.softwareEncode {
		return opts, nil
	}
	encoder, key := encoderH264VAAPI, transcodeHWVAAPI
	switch strings.ToLower(strings.TrimSpace(opts.TargetCodecVideo)) {
	case codecCopyV3:
		return opts, nil
	case transcodeCodecHEVC:
		encoder, key = "hevc_vaapi", "vaapi:hevc"
	}
	for _, mode := range vaapiCappedModes {
		probe := hwBackendProbe{commandCount: 1, run: func(ctx context.Context, path, device string, timeout time.Duration) hwProbeResult {
			output, err := runFFmpegProbe(ctx, timeout, path, vaapiRateControlSmokeArgs(device, encoder, mode)...)
			if err != nil {
				return hwProbeResult{reason: FormatFFmpegProbeFailure(err, output)}
			}
			return hwProbeResult{available: true}
		}}
		available, _ := cachedHardwareProbeContext(ctx, key+":"+strings.ToLower(mode), opts.FFmpegPath, opts.HWDevice, probe)
		if err := ctx.Err(); err != nil {
			return opts, err
		}
		if available {
			opts.vaapiRateControl = mode
			return opts, nil
		}
	}
	if err := requireSoftwareEncoder(ctx, opts.FFmpegPath, opts.TargetCodecVideo); err != nil {
		return opts, err
	}
	if opts.ToneMapMode == tonemap.ModeHardware {
		opts.softwareEncode = true
	} else {
		opts.HWAccel = transcodeHWNone
	}
	slog.WarnContext(ctx, "VAAPI offers no capped rate control; using software encoding", "device", opts.HWDevice, "encoder", encoder)
	return opts, nil
}

// vaapiRateControlSmokeArgs is the VAAPI hardware smoke encode with the
// encoder swapped in and the capped mode the transcode would request.
func vaapiRateControlSmokeArgs(device, encoder, mode string) []string {
	base := hardwareSmokeEncodeArgs(transcodeHWVAAPI, device)
	sink := base[len(base)-3:] // -f null -
	args := append([]string{}, base[:len(base)-3]...)
	for i := range args {
		if args[i] == encoderH264VAAPI {
			args[i] = encoder
		}
	}
	args = append(args, "-rc_mode", mode, "-b:v", "1800k", "-maxrate", "2000k")
	return append(args, sink...)
}
