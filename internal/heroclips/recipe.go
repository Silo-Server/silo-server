// Package heroclips turns a title's trailer into the short clip the home hero
// plays behind its backdrop.
//
// A clip is a derived asset: made once from a trailer that already exists for
// the title, stored like any other artwork, and served to clients as a URL on
// the section item the hero renders. Nothing about it is generated on a page
// load — the whole point is that the hero fetches a finished file, the way the
// backdrop it plays over is a finished image.
//
// This file owns what a clip *is*: the recipe that produced it, and the bounds
// a clip has to stay inside.
package heroclips

import "fmt"

// AlgorithmVersion names how a clip is made. Bump it whenever a change must
// rebuild clips that already exist: a stored row whose recipe differs from the
// current one is regenerated in place, so the version is what makes a recipe
// change take effect without any cleanup of old files.
const AlgorithmVersion = 1

// The recipe's bounds. A clip is a background for a title card, not a
// viewing experience: 1280 wide at CRF 28 keeps a two-minute trailer near ten
// megabytes, which is the whole trailer at a quality nobody scrutinises behind
// a headline and a gradient.
const (
	DefaultWidth = 1280
	MinWidth     = 640
	MaxWidth     = 1920
	// CRF is the H.264 constant rate factor. Higher is smaller and softer.
	CRF = 28
	// AudioBitrate is kept low because the element plays muted until a viewer
	// asks for sound. The track exists so that choice is possible at all.
	AudioBitrate = "128k"
	// MaxDurationSeconds refuses a "trailer" that is really a feature: some
	// providers list full programmes among a title's videos.
	MaxDurationSeconds = 15 * 60
	// MaxBytes refuses a clip larger than a hero should ever download, whatever
	// the source resolved to.
	MaxBytes = 96 << 20
)

// Recipe is what a clip was made with: the settings in force at the time.
type Recipe struct {
	Width int
}

// NewRecipe returns the recipe for a configured width, clamped to the bounds
// and rounded down to an even number, which every H.264 encoder wants.
func NewRecipe(width int) Recipe {
	if width <= 0 {
		width = DefaultWidth
	}
	width = min(max(width, MinWidth), MaxWidth)
	return Recipe{Width: width &^ 1}
}

// String identifies the recipe. It is part of a clip's key and of the row that
// publishes it, so a stored clip whose recipe differs from this one is stale
// and is rebuilt.
func (r Recipe) String() string {
	return fmt.Sprintf("v%d-w%d-crf%d-a%s", AlgorithmVersion, r.Width, CRF, AudioBitrate)
}
