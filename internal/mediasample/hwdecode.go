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

// SupportsHardwareDecode reports whether hardware attempts can decode on the
// resolved accelerator accel.
func SupportsHardwareDecode(accel string) bool {
	return accel == hwAccelQSV || accel == hwAccelVAAPI || accel == hwAccelVideoToolbox
}

// hardwareDecodeArgs returns the input options that decode on hw, which go
// before the input's -ss and -i. QSV decodes through its VAAPI parent device,
// so both leave VAAPI surfaces that filters must download (hwDownloadFilter)
// or process on the GPU first. VideoToolbox decodes into system-memory frames
// unless an explicit output format is requested, so software filters apply to
// its frames directly.
func hardwareDecodeArgs(hw hardwareDecode) ([]string, error) {
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
		return []string{hwaccelOption, hwAccelVideoToolbox}, nil
	default:
		return nil, fmt.Errorf("hardware decode does not support %q", hw.Accel)
	}
}

// framesInSystemMemory reports whether accel decodes into system-memory
// frames that software filters take as they are.
func framesInSystemMemory(accel string) bool {
	return accel == hwAccelVideoToolbox
}
