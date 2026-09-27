package mediasample

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// ImageOutput asks for the sampled frame as a JPEG image, encoded by ffmpeg's
// MJPEG encoder at its default quality.
type ImageOutput struct {
	// Width scales the image to this many pixels wide, keeping the aspect
	// ratio. Zero keeps the source size; otherwise it must be even.
	Width int `json:"width,omitempty"`
	// ToneMap converts an HDR source to SDR. Nil keeps the decoded colors.
	ToneMap *ToneMap `json:"tone_map,omitempty"`
}

// ToneMap configures HDR-to-SDR conversion of images. VAAPI and QSV
// attempts tone map on the GPU. Software attempts, and VideoToolbox attempts,
// whose frames arrive in system memory, tone map in software, which is slow
// on 4K frames and so needs the caller's consent.
type ToneMap struct {
	// AllowSoftware lets an attempt tone map in software. Without it such an
	// attempt fails as unsupported before ffmpeg starts.
	AllowSoftware bool `json:"allow_software,omitempty"`
}

// Image is one decoded image.
type Image struct {
	// Seconds is the media time the frame was asked for.
	Seconds float64 `json:"seconds"`
	JPEG    []byte  `json:"jpeg"`
}

// The tone-map chains chapter thumbnails have always used, kept byte for
// byte so existing thumbnails keep their look. They are not the playback
// chains in tonemap. The software chains need zscale; the VAAPI chain also
// serves QSV, which decodes through VAAPI, and ends in system memory.
const (
	softwareToneMapBT2390 = "tonemapx=tonemap=bt2390,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p"
	softwareToneMapHable  = "zscale=t=linear:npl=100,format=gbrpf32le,tonemap=hable,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p"
	vaapiToneMapDownload  = "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,procamp_vaapi=b=16:c=1,tonemap_vaapi=format=nv12:p=bt709:t=bt709:m=bt709,hwdownload,format=nv12"
)

const maxImageWidth = 7680

func (o ImageOutput) validate() error {
	if o.Width != 0 && (o.Width < 2 || o.Width > maxImageWidth || o.Width%2 != 0) {
		return fmt.Errorf("image width %d must be 0 or even and between 2 and %d", o.Width, maxImageWidth)
	}
	return nil
}

// softwareToneMaps reports whether attempt tone maps an image in software on
// the accelerator accel.
func (r Request) softwareToneMaps(attempt Attempt, accel string) bool {
	return r.Images != nil && r.Images.ToneMap != nil && (!attempt.Hardware || framesInSystemMemory(accel))
}

// toneMapResolver picks the software tone-map chain at most once per run, when
// the first attempt that needs it starts, so a hardware attempt that succeeds
// never loads capabilities and a failed choice is not retried by the next
// attempt.
type toneMapResolver struct {
	done    bool
	filter  string
	failure *AttemptError
}

func (t *toneMapResolver) resolve(ctx context.Context, r Runner) (string, *AttemptError) {
	if !t.done {
		t.filter, t.failure = r.selectSoftwareToneMap(ctx)
		t.done = true
	}
	if t.failure != nil {
		failure := *t.failure
		return "", &failure
	}
	return t.filter, nil
}

// selectSoftwareToneMap picks the software tone-map chain the binary offers:
// Jellyfin's tonemapx (BT.2390) when it is listed, else the standard tonemap
// filter's Hable curve. Both need zscale.
func (r Runner) selectSoftwareToneMap(ctx context.Context) (string, *AttemptError) {
	load := r.Capabilities
	if load == nil {
		load = LoadCapabilities
	}
	caps, err := load(ctx, r.FFmpegPath)
	if err != nil {
		return "", &AttemptError{Reason: ReasonCapabilities, Err: err}
	}
	switch {
	case !caps.HasFilter("zscale"):
		return "", &AttemptError{Reason: ReasonUnsupported, Err: errors.New("configured FFmpeg lacks the required zscale filter")}
	case caps.HasFilter(tonemap.SoftwareFilterBT2390):
		return softwareToneMapBT2390, nil
	case caps.HasFilter(tonemap.SoftwareFilterHable):
		return softwareToneMapHable, nil
	}
	return "", &AttemptError{Reason: ReasonUnsupported, Err: errors.New("configured FFmpeg lacks the required tonemapx or tonemap filter")}
}

// prepareImage settles what an image attempt on hw needs before ffmpeg
// starts: the software tone-map chain, and the arguments.
func (r Runner) prepareImage(ctx context.Context, req Request, attempt Attempt, hw hardwareDecode, toneMap *toneMapResolver) ([]string, *AttemptError) {
	softwareToneMap := ""
	if req.softwareToneMaps(attempt, r.HWAccel) {
		if !req.Images.ToneMap.AllowSoftware {
			return nil, &AttemptError{Reason: ReasonUnsupported, Err: errors.New("software HDR tone mapping is disabled")}
		}
		filter, failure := toneMap.resolve(ctx, r)
		if failure != nil {
			return nil, failure
		}
		softwareToneMap = filter
	}
	args, err := buildImageArgs(req, attempt, hw, softwareToneMap)
	if err != nil {
		return nil, &AttemptError{Reason: ReasonArgs, Err: err}
	}
	return args, nil
}

// buildImageArgs builds the arguments of an At request with an Images
// output. They keep the layout chapter thumbnails have always used: log
// level error, hardware decode options, an accurate input seek, and one MJPEG
// frame on stdout. softwareToneMap is the software tone-map chain, needed
// when the attempt tone maps in software.
func buildImageArgs(req Request, attempt Attempt, hw hardwareDecode, softwareToneMap string) ([]string, error) {
	if req.At == nil || req.Images == nil {
		return nil, errors.New("images need a single-frame sampling mode")
	}
	toneMap := req.Images.ToneMap != nil
	args := []string{hideBanner, logLevelOption, errorLogLevel}
	if req.Threads > 0 {
		args = append(args, "-threads", strconv.Itoa(req.Threads))
	}
	if attempt.Hardware {
		decode, err := hardwareDecodeArgs(hw, false)
		if err != nil {
			return nil, err
		}
		args = append(args, decode...)
	}
	var filter string
	switch {
	case attempt.Hardware && !framesInSystemMemory(hw.Accel):
		filter = hwDownloadFilter
		if toneMap {
			filter = vaapiToneMapDownload
		}
	case toneMap && softwareToneMap == "":
		return nil, errors.New("HDR image extraction requires a software tone-map filter")
	default:
		filter = softwareToneMap
	}
	if width := req.Images.Width; width > 0 {
		scale := fmt.Sprintf("scale=%d:-2", width)
		if filter == "" {
			filter = scale
		} else {
			filter += "," + scale
		}
	}
	args = append(args,
		"-ss", fmt.Sprintf("%.3f", req.At.Seconds),
		"-i", req.Input,
	)
	if filter != "" {
		args = append(args, "-vf", filter)
	}
	return append(args,
		"-frames:v", "1",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-",
	), nil
}

// image runs an image attempt. The output is a single MJPEG frame, so
// ffmpeg's whole stdout is the image.
func (a attemptRun) image(req Request, args []string) (Result, *AttemptError) {
	stdout, failure := a.exec(req, args, nil, true)
	if failure != nil {
		return Result{}, failure
	}
	if stdout.Len() == 0 {
		return Result{}, &AttemptError{Decoder: a.decoder, Reason: ReasonEmpty, Err: errors.New("ffmpeg wrote no image")}
	}
	return Result{Decoder: a.decoder, Images: []Image{{Seconds: req.At.Seconds, JPEG: stdout.Bytes()}}}, nil
}
