package mediasample

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// SheetsOutput asks for the sampled frames tiled into JPEG sprite sheets,
// the images seek-bar previews (trickplay) are cut from. Each sample fills
// one cell, left to right and top to bottom, and a sheet holds Columns by
// Rows cells; the last sheet keeps its full size, with black cells after the
// last sample. Frames are scaled to exactly TileWidth by TileHeight, so the
// caller picks the height that keeps the display aspect ratio, and converted
// to full-range BT.601 4:2:0, the colors a JPEG decoder assumes.
type SheetsOutput struct {
	TileWidth  int `json:"tile_width"`
	TileHeight int `json:"tile_height"`
	Columns    int `json:"columns"`
	Rows       int `json:"rows"`
	// Quality is the JPEG quality, 1 to 100.
	Quality int `json:"quality"`
	// UseInputAspect replaces TileHeight with the probed display height for
	// TileWidth, including a 90-degree display matrix. Result reports the
	// actual tile height. Without it the caller's exact dimensions apply.
	UseInputAspect bool `json:"use_input_aspect,omitzero"`
	// ToneMap converts an HDR source to SDR, after scaling, so software tone
	// mapping only handles thumbnail-sized frames. Nil keeps the decoded
	// colors.
	ToneMap *ToneMap `json:"tone_map,omitempty"`
}

func (o SheetsOutput) forDisplayAspect(aspect float64) SheetsOutput {
	if o.UseInputAspect && aspect > 0 && finite(aspect) {
		o.TileHeight = min(max(2*int(math.Round(float64(o.TileWidth)/aspect/2)), minSheetTile), maxSheetTile)
	}
	return o
}

// Sheet is one sprite sheet.
type Sheet struct {
	// Index counts the request's sheets from zero.
	Index int `json:"index"`
	// Thumbnails is how many of its cells hold a sample.
	Thumbnails int    `json:"thumbnails"`
	JPEG       []byte `json:"jpeg"`
}

// SheetFrames counts how a Sheets output's cells were filled.
type SheetFrames struct {
	// Decoded cells show a frame decoded for their sample.
	Decoded int `json:"decoded"`
	// Filled cells had no frame of their own and show a neighbor's.
	Filled int `json:"filled"`
	// Refined cells show a frame a repair found (see sheet_repair.go): one
	// the first read missed, or one nearer the sample's time than the
	// keyframe the previous cell already shows. They count as decoded too.
	Refined int `json:"refined,omitzero"`
}

// Bounds of a SheetsOutput. A sheet of the widest tiles in the largest grid
// stays within a size every JPEG decoder takes.
const (
	minSheetTile    = 16
	maxSheetTile    = 1920
	maxSheetCells   = 16
	maxSheetPixels  = 16384
	maxFilledShare  = 0.1
	sheetMetadataID = "sheets"
	// maxSheetsRunPixels bounds the tile pixels of one run, which holds
	// every tile it decodes, about 1.5 bytes a pixel, until its repairs are
	// done. Callers chunk longer files into several runs.
	maxSheetsRunPixels = 64 << 20
)

func (o SheetsOutput) validate() error {
	for _, size := range []int{o.TileWidth, o.TileHeight} {
		if size < minSheetTile || size > maxSheetTile || size%2 != 0 {
			return fmt.Errorf("sheet tile size %dx%d must be even and between %d and %d", o.TileWidth, o.TileHeight, minSheetTile, maxSheetTile)
		}
	}
	if o.Columns < 1 || o.Columns > maxSheetCells || o.Rows < 1 || o.Rows > maxSheetCells {
		return fmt.Errorf("sheet grid %dx%d must be between 1 and %d on each side", o.Columns, o.Rows, maxSheetCells)
	}
	if o.Columns*o.TileWidth > maxSheetPixels || o.Rows*o.TileHeight > maxSheetPixels {
		return fmt.Errorf("sheet of %dx%d tiles of %dx%d exceeds %d pixels on a side", o.Columns, o.Rows, o.TileWidth, o.TileHeight, maxSheetPixels)
	}
	if o.Quality < 1 || o.Quality > 100 {
		return fmt.Errorf("sheet quality %d is outside 1..100", o.Quality)
	}
	if o.ToneMap != nil {
		return o.ToneMap.validate()
	}
	return nil
}

// validateRun checks that the sheets of samples fit maxSheetsRunPixels.
func (o SheetsOutput) validateRun(samples int) error {
	perSheet := o.Columns * o.Rows
	sheets := (samples + perSheet - 1) / perSheet
	if pixels := int64(sheets) * int64(perSheet) * int64(o.TileWidth) * int64(o.TileHeight); pixels > maxSheetsRunPixels {
		return fmt.Errorf("%d sheets of %dx%d tiles of %dx%d exceed %d pixels in one run", sheets, o.Columns, o.Rows, o.TileWidth, o.TileHeight, maxSheetsRunPixels)
	}
	return nil
}

// frameBytes is the size of one raw frame the chain writes: 8-bit 4:2:0.
func (o SheetsOutput) frameBytes() int {
	return o.TileWidth * o.TileHeight * 3 / 2
}

// sheetFullRange converts a scaled frame to full-range BT.601 4:2:0, what
// JFIF decoders read JPEG samples as. Without it a limited-range frame
// looks washed out and a BT.709 one slightly off in hue.
const sheetFullRange = "out_range=full:out_color_matrix=bt601"

// sheetPixelFormat is the format of the raw frames a Sheets run writes.
const sheetPixelFormat = "yuv420p"

// gpuScaleFactor is how many times a tile's size a GPU scales frames to
// before they are downloaded and scaled the rest of the way in software. The
// GPU scalers sample a few source pixels per output pixel, so a direct 4K to
// 300-pixel scale aliases (SSIM 0.94 against an area scale of the same
// frames); scaling to four tiles' size on the GPU and by area after keeps it
// at 0.999 while downloading a fraction of the frame.
const gpuScaleFactor = 4

// sheetsToneMap is how a sheet attempt converts an HDR source. kind is the
// source's base signal when the request named one (ToneMap.Source). software
// is the software chain for scaled frames: kind's chain, or the best-effort
// chain when the request named no kind.
type sheetsToneMap struct {
	kind     tonemap.SourceKind
	software string
}

// buildSheetsGraph returns the video chain of a Sheets output for frames
// decoded on accel (empty for software), converting HDR as toneMap says when
// the output asks for tone mapping.
//
// Software and VideoToolbox frames are scaled first, tone mapped if asked,
// and converted once:
//
//	scale=W:H:flags=area:out_range=full:out_color_matrix=bt601,format=yuv420p
//	scale=W:H:flags=area,<software tone map>,scale=out_range=full:out_color_matrix=bt601,format=yuv420p
//
// GPU surfaces (VAAPI, which QSV decodes into too, and CUDA) are scaled on the
// GPU to gpuScaleFactor tiles, downloaded, and then take the same chain. An
// HDR source whose base signal the request named downloads as 10-bit P010,
// so it is tone mapped in software, on thumbnail-sized frames, exactly as on
// a software attempt: the GPU tone mappers disagree with it and with one
// another (Intel's VAAPI tone mapper crushes PQ midtones and garbles HLG,
// AMD's refuses HDR, neither applies a Dolby Vision RPU), and the frames are
// too small for the cost to matter. A VAAPI attempt without a named signal
// keeps the best-effort GPU tone map images use; CUDA has none.
//
//	scale_vaapi=w=min(iw\,4W):h=min(ih\,4H):format=nv12,hwdownload,format=nv12,scale=W:H:flags=area:out_range=full:…
//	scale_cuda=w=min(iw\,4W):h=min(ih\,4H):interp_algo=bilinear:format=p010,hwdownload,format=p010le,scale=W:H:flags=area,<software tone map>,scale=out_range=full:…
//
// Every chain ends by giving each frame a "sample" entry of -1 unless its
// packet already carries one, so metadata=print logs every frame and the
// log lines up with the raw frames on stdout (see sheets_join.go).
func buildSheetsGraph(out SheetsOutput, accel string, toneMap sheetsToneMap) (string, error) {
	hdr := out.ToneMap != nil
	gpuToneMaps := false
	var filters []string
	if accel != "" && !framesInSystemMemory(accel) {
		format, download := "nv12", hwDownloadFilter
		switch {
		case !hdr:
		case toneMap.kind != "":
			format, download = "p010", "hwdownload,format=p010le"
		case accel == hwAccelNVENC:
			return "", errors.New("CUDA sheets need the source's base signal to tone map")
		default:
			filters = append(filters, vaapiToneMap)
			gpuToneMaps = true
		}
		filters = append(filters, gpuScaleFilter(out, accel, format), download)
	}
	software := ""
	if hdr && !gpuToneMaps {
		if toneMap.software == "" {
			return "", errors.New("HDR sheets require a software tone-map filter")
		}
		software = toneMap.software
	}
	scale := fmt.Sprintf("scale=%d:%d:flags=area", out.TileWidth, out.TileHeight)
	convert := "format=" + sheetPixelFormat
	if software != "" {
		filters = append(filters, scale, software, "scale="+sheetFullRange, convert)
	} else {
		filters = append(filters, scale+":"+sheetFullRange, convert)
	}
	filters = append(filters,
		"metadata=mode=add:key="+sampleMetadataKey+":value=-1",
		filterMetadata+"@"+sheetMetadataID+"=print")
	return strings.Join(filters, ","), nil
}

// gpuScaleFilter scales GPU surfaces of accel to gpuScaleFactor times the
// tile, or the frame's own size if that is smaller, into format, ready for
// hwdownload. The commas inside min() are escaped from the graph parser.
func gpuScaleFilter(out SheetsOutput, accel, format string) string {
	w := fmt.Sprintf("min(iw\\,%d)", gpuScaleFactor*out.TileWidth)
	h := fmt.Sprintf("min(ih\\,%d)", gpuScaleFactor*out.TileHeight)
	if accel == hwAccelNVENC {
		return "scale_cuda=w=" + w + ":h=" + h + ":interp_algo=bilinear:format=" + format
	}
	return "scale_vaapi=w=" + w + ":h=" + h + ":format=" + format
}

// prepareSheets settles the video chain of a sheet attempt before ffmpeg
// starts. A request naming its source's base signal takes that signal's
// software chain on every attempt (see buildSheetsGraph), which needs
// software tone mapping allowed and a filter the binary lists for the
// signal; without them the attempt fails as unsupported, so a Dolby Vision
// base makes no sheets rather than green and magenta ones. Otherwise an
// attempt takes the best-effort chain, which CUDA lacks.
func (r Runner) prepareSheets(ctx context.Context, req Request, attempt Attempt, resolver *toneMapResolver) (string, *AttemptError) {
	accel := ""
	if attempt.Hardware {
		accel = r.HWAccel
	}
	toneMap := sheetsToneMap{}
	switch out := req.Sheets.ToneMap; {
	case out == nil:
	case out.Source != "":
		if !out.AllowSoftware {
			return "", &AttemptError{Reason: ReasonUnsupported, Err: errors.New("software HDR tone mapping is disabled")}
		}
		software, failure := resolver.source(ctx, r, out.Source)
		if failure != nil {
			return "", failure
		}
		toneMap = sheetsToneMap{kind: out.Source, software: software}
	case accel == hwAccelNVENC:
		return "", &AttemptError{Reason: ReasonUnsupported, Err: errors.New("CUDA sheets need the source's base signal to tone map")}
	default:
		software, failure := r.softwareToneMapFilter(ctx, req, attempt, resolver)
		if failure != nil {
			return "", failure
		}
		toneMap.software = software
	}
	graph, err := buildSheetsGraph(*req.Sheets, accel, toneMap)
	if err != nil {
		return "", &AttemptError{Reason: ReasonArgs, Err: err}
	}
	return graph, nil
}

// sheetsOutputArgs are the output options of a Sheets run: the first video
// stream, filtered by graph, as raw frames on stdout. Passthrough keeps
// ffmpeg from dropping or duplicating frames by timestamp: a sampled list's
// timestamps jump back at every entry, and rawvideo would otherwise pick a
// constant frame rate.
func sheetsOutputArgs(graph string) []string {
	return []string{
		mapStreamOption, firstVideoStream, disableAudioOption, disableSubtitlesOption, disableDataOption,
		videoFilterOption, graph,
		"-fps_mode", "passthrough",
		pixelFormatOption, sheetPixelFormat,
		"-f", "rawvideo", "pipe:1",
	}
}
