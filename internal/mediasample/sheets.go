package mediasample

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
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

// buildSheetsGraph returns the video filter chain of a Sheets output for
// frames decoded on accel (empty for software). softwareToneMap is the
// software tone-map chain, needed when the attempt tone maps in software.
//
// Software and VideoToolbox frames are scaled first, tone mapped if asked,
// and converted once:
//
//	scale=W:H:flags=area:out_range=full:out_color_matrix=bt601,format=yuv420p
//	scale=W:H:flags=area,<tone map>,scale=out_range=full:out_color_matrix=bt601,format=yuv420p
//
// VAAPI surfaces, which QSV decodes into too, are tone mapped on the GPU,
// which needs the source's HDR metadata and so comes first, scaled there, and
// downloaded:
//
//	[<vaapi tone map>,]scale_vaapi=w=W:h=H:format=nv12,hwdownload,format=nv12,scale=…,format=yuv420p
//
// Every chain ends by giving each frame a "sample" entry of -1 unless its
// packet already carries one, so metadata=print logs every frame and the
// log lines up with the raw frames on stdout (see sheets_join.go).
func buildSheetsGraph(out SheetsOutput, accel, softwareToneMap string) (string, error) {
	toneMap := out.ToneMap != nil
	scale := fmt.Sprintf("scale=%d:%d:flags=area", out.TileWidth, out.TileHeight)
	convert := "format=" + sheetPixelFormat
	var filters []string
	switch {
	case accel != "" && !framesInSystemMemory(accel):
		if toneMap {
			filters = append(filters, vaapiToneMap)
		}
		filters = append(filters,
			fmt.Sprintf("scale_vaapi=w=%d:h=%d:format=nv12", out.TileWidth, out.TileHeight),
			hwDownloadFilter,
			"scale="+sheetFullRange, convert)
	case toneMap:
		if softwareToneMap == "" {
			return "", errors.New("HDR sheets require a software tone-map filter")
		}
		filters = append(filters, scale, softwareToneMap, "scale="+sheetFullRange, convert)
	default:
		filters = append(filters, scale+":"+sheetFullRange, convert)
	}
	filters = append(filters,
		"metadata=mode=add:key="+sampleMetadataKey+":value=-1",
		filterMetadata+"@"+sheetMetadataID+"=print")
	return strings.Join(filters, ","), nil
}

// prepareSheets settles the video chain of a sheet attempt before ffmpeg
// starts, choosing the software tone-map chain if the attempt needs one.
func (r Runner) prepareSheets(ctx context.Context, req Request, attempt Attempt, toneMap *toneMapResolver) (string, *AttemptError) {
	softwareToneMap, failure := r.softwareToneMapFilter(ctx, req, attempt, toneMap)
	if failure != nil {
		return "", failure
	}
	accel := ""
	if attempt.Hardware {
		accel = r.HWAccel
	}
	graph, err := buildSheetsGraph(*req.Sheets, accel, softwareToneMap)
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
