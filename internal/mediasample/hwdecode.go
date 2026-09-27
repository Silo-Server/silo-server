package mediasample

import (
	"fmt"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// Hardware decode backends a hardware attempt can use, named as
// playback.ResolveHWAccelWithFFmpegContext resolves the configured
// accelerator. NVENC is not among them: nothing here decodes on CUDA.
const (
	hwAccelQSV          = "qsv"
	hwAccelVAAPI        = "vaapi"
	hwAccelVideoToolbox = "videotoolbox"
)

// hwaccelOption selects ffmpeg's hardware decoder.
const hwaccelOption = "-hwaccel"

// hwDownloadFilter copies a decoded VAAPI surface to system memory as 8-bit
// 4:2:0.
const hwDownloadFilter = "hwdownload,format=nv12"

// videoToolboxDownloadFilter copies a decoded VideoToolbox surface of a
// 4:2:0 source with bitDepth bits (zero when unknown, taken as 8) to system
// memory. hwdownload can only name the surface's own format, which follows
// the source's depth: NV12 up to 8 bits and P010 above. A source whose
// surfaces have another format, such as 4:2:2, fails the download.
func videoToolboxDownloadFilter(bitDepth int) string {
	if bitDepth > 8 {
		return "hwdownload,format=p010le"
	}
	return hwDownloadFilter
}

// SupportsHardwareDecode reports whether hardware attempts can decode on the
// resolved accelerator accel.
func SupportsHardwareDecode(accel string) bool {
	return accel == hwAccelQSV || accel == hwAccelVAAPI || accel == hwAccelVideoToolbox
}

// hardwareDecodeArgs returns the input options that decode on hw, which go
// before the input's -ss and -i. QSV decodes through its VAAPI parent device,
// so both leave VAAPI surfaces that filters must download (hwDownloadFilter)
// or process on the GPU first. VideoToolbox decodes into system-memory frames
// that software filters apply to directly, unless vtSurfaces asks for its
// surfaces, which filters must download (videoToolboxDownloadFilter).
//
// Only surfaces prove that VideoToolbox decoded: given a stream it cannot
// decode, such as VP8, ffmpeg quietly decodes in software, and the frames
// then fail the download instead of passing as hardware-decoded.
func hardwareDecodeArgs(hw hardwareDecode, vtSurfaces bool) ([]string, error) {
	switch hw.Accel {
	case hwAccelQSV:
		if hw.Device == "" {
			return nil, fmt.Errorf("qsv requires a render device")
		}
		args := tonemap.QSVInitDeviceArgs(hw.Device)
		return append(args,
			"-filter_hw_device", "va",
			hwaccelOption, hwAccelVAAPI,
			"-hwaccel_output_format", "vaapi",
		), nil
	case hwAccelVAAPI:
		if hw.Device == "" {
			return nil, fmt.Errorf("vaapi requires a render device")
		}
		args := tonemap.VAAPIInitDeviceArgs("hw", hw.Device)
		return append(args,
			"-filter_hw_device", "hw",
			hwaccelOption, hwAccelVAAPI,
			"-hwaccel_output_format", "vaapi",
		), nil
	case hwAccelVideoToolbox:
		args := []string{hwaccelOption, hwAccelVideoToolbox}
		if vtSurfaces {
			args = append(args, "-hwaccel_output_format", "videotoolbox_vld")
		}
		return args, nil
	default:
		return nil, fmt.Errorf("hardware decode does not support %q", hw.Accel)
	}
}

// framesInSystemMemory reports whether accel decodes images into
// system-memory frames that software filters take as they are.
func framesInSystemMemory(accel string) bool {
	return accel == hwAccelVideoToolbox
}
