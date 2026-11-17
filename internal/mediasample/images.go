package mediasample

import (
	"bytes"
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
	// ratio. Zero keeps the source's display size (see pixelAspectFilter);
	// otherwise it must be even.
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
	// Source is the base signal tonemap.ResolveSource classified the input's
	// video as. Only Sheets take it. With it, every attempt, on a GPU too,
	// converts in software with the chain playback converts that signal with
	// in software (tonemap.SoftwareFilter): a Dolby Vision base only through
	// tonemapx, which applies its RPU, an HLG source with the HLG transfer. It
	// therefore needs AllowSoftware, and an attempt the binary has no filter
	// for fails as unsupported before ffmpeg starts. Empty keeps the
	// best-effort chains below, for sources whose catalog facts cannot
	// classify them.
	Source tonemap.SourceKind `json:"source,omitempty"`
}

func (t ToneMap) validate() error {
	if t.Source != "" && !tonemap.ValidSourceKind(t.Source) {
		return fmt.Errorf("tone-map source %q is not a known base signal", t.Source)
	}
	return nil
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
// serves QSV, which decodes through VAAPI. vaapiToneMap leaves VAAPI
// surfaces, which sheets scale on the GPU before the download;
// vaapiToneMapDownload ends in system memory.
const (
	softwareToneMapBT2390 = "tonemapx=tonemap=bt2390,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p"
	softwareToneMapHable  = "zscale=t=linear:npl=100,format=gbrpf32le,tonemap=hable,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p"
	vaapiToneMap          = "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,procamp_vaapi=b=16:c=1,tonemap_vaapi=format=nv12:p=bt709:t=bt709:m=bt709"
	vaapiToneMapDownload  = vaapiToneMap + "," + hwDownloadFilter
)

const maxImageWidth = 7680

// pixelAspectFilter scales an image to its display width, applying the
// stream's sample aspect ratio so a frame from non-square-pixel video is not
// squeezed. ffmpeg's sar is 1 when the stream has none, so a square-pixel or
// unknown-SAR source scales to its own size and ffmpeg passes the frame
// through unchanged. round, because ffmpeg truncates the expression and
// floating point lands some exact widths one short. The width is not rounded
// to even, which would shrink an odd-width square source by a pixel, and no
// setsar follows, which would make ffmpeg write a JFIF header for sources
// without a SAR. A ratio outside 1:4 to 4:1 is taken for bad metadata and
// ignored, so such a frame keeps its coded size: applied, it would ask for a
// frame wider than MJPEG allows or a few pixels wide. The escaped commas keep
// the filtergraph parser from splitting the expression.
const pixelAspectFilter = `scale=if(between(sar\,0.25\,4)\,round(iw*sar)\,iw):ih`

func (o ImageOutput) validate() error {
	if o.Width != 0 && (o.Width < 2 || o.Width > maxImageWidth || o.Width%2 != 0) {
		return fmt.Errorf("image width %d must be 0 or even and between 2 and %d", o.Width, maxImageWidth)
	}
	return nil
}

// softwareToneMaps reports whether attempt tone maps an image or sheet in
// software on the accelerator accel.
func (r Request) softwareToneMaps(attempt Attempt, accel string) bool {
	return r.imageToneMap() != nil && (!attempt.Hardware || framesInSystemMemory(accel))
}

// imageToneMap is the tone mapping an Images or Sheets output asks for.
func (r Request) imageToneMap() *ToneMap {
	switch {
	case r.Images != nil:
		return r.Images.ToneMap
	case r.Sheets != nil:
		return r.Sheets.ToneMap
	}
	return nil
}

// toneMapResolver picks the software tone-map chain at most once per run, when
// the first attempt that needs it starts, so a hardware attempt that succeeds
// never loads capabilities and a failed choice is not retried by the next
// attempt. A run whose request names its source's base signal picks that
// signal's chain instead (see selectSourceToneMap).
type toneMapResolver struct {
	done    bool
	filter  string
	failure *AttemptError
}

// source returns the software chain that converts the base signal kind,
// chosen once per run.
func (t *toneMapResolver) source(ctx context.Context, r Runner, kind tonemap.SourceKind) (string, *AttemptError) {
	if !t.done {
		t.filter, t.failure = r.selectSourceToneMap(ctx, kind)
		t.done = true
	}
	if t.failure != nil {
		failure := *t.failure
		return "", &failure
	}
	return t.filter, nil
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

// selectSourceToneMap picks the chain playback converts the base signal kind
// with in software (tonemap.SoftwareFilter), on a filter the binary lists:
// Jellyfin's tonemapx, the only one that applies a Dolby Vision RPU, else the
// standard tonemap filter's Hable curve for the standard signals. A base that
// is already SDR needs no tone-map filter. Every chain needs zscale and
// sidedata.
func (r Runner) selectSourceToneMap(ctx context.Context, kind tonemap.SourceKind) (string, *AttemptError) {
	load := r.Capabilities
	if load == nil {
		load = LoadCapabilities
	}
	caps, err := load(ctx, r.FFmpegPath)
	if err != nil {
		return "", &AttemptError{Reason: ReasonCapabilities, Err: err}
	}
	switch {
	case !caps.HasFilter("zscale") || !caps.HasFilter("sidedata"):
		return "", &AttemptError{Reason: ReasonUnsupported, Err: errors.New("configured FFmpeg lacks the required zscale or sidedata filter")}
	case tonemap.IsSDRSource(kind):
		return tonemap.SoftwareFilter(kind, ""), nil
	case caps.HasFilter(tonemap.SoftwareFilterBT2390):
		return tonemap.SoftwareFilter(kind, tonemap.SoftwareFilterBT2390), nil
	case kind != tonemap.SourceDolbyVision && caps.HasFilter(tonemap.SoftwareFilterHable):
		return tonemap.SoftwareFilter(kind, tonemap.SoftwareFilterHable), nil
	}
	return "", &AttemptError{Reason: ReasonUnsupported, Err: fmt.Errorf("configured FFmpeg has no tone-map filter that converts a %s source", kind)}
}

// softwareToneMapFilter settles, before ffmpeg starts, the software tone-map
// chain an image or sheet attempt needs, or "" when it needs none.
func (r Runner) softwareToneMapFilter(ctx context.Context, req Request, attempt Attempt, toneMap *toneMapResolver) (string, *AttemptError) {
	if !req.softwareToneMaps(attempt, r.HWAccel) {
		return "", nil
	}
	if !req.imageToneMap().AllowSoftware {
		return "", &AttemptError{Reason: ReasonUnsupported, Err: errors.New("software HDR tone mapping is disabled")}
	}
	return toneMap.resolve(ctx, r)
}

// prepareImage settles what an image attempt on hw needs before ffmpeg
// starts: the software tone-map chain, and the arguments.
func (r Runner) prepareImage(ctx context.Context, req Request, attempt Attempt, hw hardwareDecode, toneMap *toneMapResolver) ([]string, *AttemptError) {
	softwareToneMap, failure := r.softwareToneMapFilter(ctx, req, attempt, toneMap)
	if failure != nil {
		return nil, failure
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
// frame on stdout. The filter chain always ends by applying the pixel aspect
// (pixelAspectFilter), before any width scale. softwareToneMap is the
// software tone-map chain, needed when the attempt tone maps in software.
func buildImageArgs(req Request, attempt Attempt, hw hardwareDecode, softwareToneMap string) ([]string, error) {
	if req.At == nil || req.Images == nil {
		return nil, errors.New("images need a single-frame sampling mode")
	}
	toneMap := req.Images.ToneMap != nil
	args := []string{hideBanner, logLevelOption, errorLogLevel, "-max_pixels", strconv.Itoa(decodeMaxPixels)}
	if req.Threads > 0 {
		args = append(args, "-threads", strconv.Itoa(req.Threads))
	}
	if attempt.Hardware {
		if !SupportsHardwareDecode(hw.Accel) {
			return nil, fmt.Errorf("hardware decode does not support %q", hw.Accel)
		}
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
	if filter == "" {
		filter = pixelAspectFilter
	} else {
		filter += "," + pixelAspectFilter
	}
	if width := req.Images.Width; width > 0 {
		filter += fmt.Sprintf(",scale=%d:-2", width)
	}
	return append(args,
		"-ss", fmt.Sprintf("%.3f", req.At.Seconds),
		"-i", req.Input,
		videoFilterOption, filter,
		"-frames:v", "1",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-",
	), nil
}

// Bounds on one extracted frame, so a crafted file cannot make ffmpeg or the
// caller hold an unbounded frame in memory.
const (
	// MaxImagePixels is the largest frame, in pixels, an image attempt
	// decodes. It equals imageutil's source budget (maxSourcePixels), which
	// every consumer of an extracted frame already enforces, so a frame at
	// its decoded size or wider that yields a result is not refused. The
	// limit is on the decoded size, which decides the decoder's memory: a
	// larger source that pixelAspectFilter would narrow to under the budget
	// is refused.
	MaxImagePixels = 100_000_000
	// decodeMaxPixels is the -max_pixels ffmpeg gets. ffmpeg compares the
	// width rounded up to its stride alignment, up to 64 on x86, so a frame
	// just under MaxImagePixels would be refused at that value. A sixteenth
	// of headroom covers the rounding for any frame at least 1024 wide.
	decodeMaxPixels = MaxImagePixels + MaxImagePixels/16
	// MaxImageBytes is the most one encoded frame may be. The worst case at
	// MaxImagePixels, noise at ffmpeg's default MJPEG quality, is about 25 MB.
	MaxImageBytes = 64 << 20
)

// ErrImageTooLarge means ffmpeg wrote an image over MaxImageBytes.
var ErrImageTooLarge = errors.New("image too large")

// image runs an image attempt. The output is a single MJPEG frame, so
// ffmpeg's whole stdout is the image, up to MaxImageBytes.
func (a attemptRun) image(req Request, args []string) (Result, *AttemptError) {
	stdout := &cappedBuffer{limit: MaxImageBytes}
	failure := a.exec(req, args, nil, stdout)
	// The overflow decides: the refused write closes ffmpeg's stdout, so
	// ffmpeg exits on a broken pipe and exec reports that exit instead.
	if stdout.overflowed {
		return Result{}, &AttemptError{Decoder: a.decoder, Reason: ReasonOutput, Err: fmt.Errorf("%w: ffmpeg wrote more than %d bytes", ErrImageTooLarge, MaxImageBytes)}
	}
	if failure != nil {
		return Result{}, failure
	}
	if stdout.buf.Len() == 0 {
		return Result{}, &AttemptError{Decoder: a.decoder, Reason: ReasonEmpty, Err: errors.New("ffmpeg wrote no image")}
	}
	return Result{Decoder: a.decoder, Images: []Image{{Seconds: req.At.Seconds, JPEG: stdout.buf.Bytes()}}}, nil
}

// cappedBuffer collects up to limit bytes and refuses the write that would
// pass it.
type cappedBuffer struct {
	buf        bytes.Buffer
	limit      int
	overflowed bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.overflowed || b.buf.Len()+len(p) > b.limit {
		b.overflowed = true
		return 0, ErrImageTooLarge
	}
	return b.buf.Write(p)
}
