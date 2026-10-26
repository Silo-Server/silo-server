// Package trickplay generates seek-bar preview sprite sheets for the media
// files of libraries that opt in, and publishes their manifests. See
// docs/architecture/trickplay.md.
package trickplay

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/config"
)

// AlgorithmVersion names how sheets are made. Bump it when a change must
// regenerate every file's sheets; servers only claim and invalidate rows at
// or below their own version, so a rolling upgrade does not flap.
const AlgorithmVersion = 1

// Settings bounds and defaults. Width is shared with chapter thumbnails.
const (
	DefaultWidth           = config.DefaultPreviewImageWidth
	MinWidth               = config.MinPreviewImageWidth
	MaxWidth               = config.MaxPreviewImageWidth
	DefaultIntervalSeconds = 10
	MinIntervalSeconds     = 5
	MaxIntervalSeconds     = 60
	// Quality is the JPEG quality of every sheet.
	Quality = 80
	// maxSheetWidth keeps a sheet's width within what TV-class decoders take.
	maxSheetWidth = 3200
	// maxGrid is the most tiles a sheet has on a side.
	maxGrid = 10
	// maxSheetRows fits the tallest decoded tile (1920 pixels) within
	// mediasample's 16384-pixel sheet limit, including geometry learned by
	// the execution probe after the grid has been chosen.
	maxSheetRows = 8
)

// Recipe is what a generation is made with: the settings in force when it
// ran.
type Recipe struct {
	Width      int
	IntervalMS int
}

// NewRecipe returns the recipe for the width and interval settings, clamped
// to their bounds.
func NewRecipe(width, intervalSeconds int) Recipe {
	width = min(max(width, MinWidth), MaxWidth) &^ 1
	intervalSeconds = min(max(intervalSeconds, MinIntervalSeconds), MaxIntervalSeconds)
	return Recipe{Width: width, IntervalMS: intervalSeconds * 1000}
}

// String identifies the recipe; a published row whose recipe differs from
// the current one is regenerated.
func (r Recipe) String() string {
	return fmt.Sprintf("v%d/w%d/i%d/q%d", AlgorithmVersion, r.Width, r.IntervalMS, Quality)
}

// Grid is the sheet's columns and rows for the recipe's width: at most ten
// columns within maxSheetWidth, and at most eight rows for tall tiles.
func (r Recipe) Grid() (columns, rows int) {
	side := min(maxGrid, maxSheetWidth/r.Width)
	return side, min(side, maxSheetRows)
}

// TileHeight is the height of a tile for a picture of the given display
// aspect ratio (width over height), rounded to an even number.
func (r Recipe) TileHeight(aspect float64) int {
	if !(aspect > 0) || math.IsInf(aspect, 0) {
		aspect = 16.0 / 9.0
	}
	height := 2 * int(math.Round(float64(r.Width)/aspect/2))
	return min(max(height, 16), 1920)
}

// SampleTimes are the media times of a file's thumbnails: one per interval,
// at the interval's middle, so thumbnail k shows what plays in
// [k*interval, (k+1)*interval). The last one stays a second inside the
// file, and the times are strictly increasing.
func (r Recipe) SampleTimes(durationSeconds float64) []float64 {
	interval := float64(r.IntervalMS) / 1000
	if !(durationSeconds > 0) {
		return nil
	}
	n := int(math.Ceil(durationSeconds / interval))
	times := make([]float64, n)
	for k := range times {
		start := float64(k) * interval
		times[k] = math.Max(start, math.Min(start+interval/2, durationSeconds-1))
		if k > 0 && times[k] <= times[k-1] {
			times[k] = times[k-1] + 0.001
		}
	}
	return times
}

// Bandwidth is the bits per second a client needs to fetch sheets as fast as
// playback moves through them, as Jellyfin computes it: the largest sheet's
// bits spread over the thumbnails a full sheet holds.
func (r Recipe) Bandwidth(sheetBytes []int) int {
	columns, rows := r.Grid()
	perSheetSeconds := float64(columns*rows) * float64(r.IntervalMS) / 1000
	peak := 0
	for _, n := range sheetBytes {
		peak = max(peak, int(math.Ceil(float64(n)*8/perSheetSeconds)))
	}
	return peak
}

// ParseAspectRatio reads a display aspect ratio as ffprobe reports it
// ("16:9", "2.39:1"), or false when it is missing or degenerate ("0:1",
// "N/A").
func ParseAspectRatio(value string) (float64, bool) {
	a, b, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok {
		return 0, false
	}
	x, errX := strconv.ParseFloat(a, 64)
	y, errY := strconv.ParseFloat(b, 64)
	if errX != nil || errY != nil || !(x > 0) || !(y > 0) {
		return 0, false
	}
	return x / y, true
}
