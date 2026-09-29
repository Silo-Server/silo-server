package playback

import (
	"math"
	"strconv"
)

// ladderClass is one output resolution class of the bitrate ladder: a 16:9
// box and the least H.264 bitrate at <=30 fps that looks clean at that size.
type ladderClass struct {
	Height, Width int
	FloorKbps     int
}

// bitrateLadder pairs a bitrate budget with the largest output that budget
// encodes well. The floors follow Apple's HLS authoring ladder and Jellyfin's
// ResolutionNormalizer for H.264 at <=30 fps; 480p takes everything below
// 540p's floor. Download presets, automatic streaming quality, and
// jellycompat all read this one table so a given bitrate always means the
// same resolution.
var bitrateLadder = []ladderClass{
	{Height: 2160, Width: 3840, FloorKbps: 20_000},
	{Height: 1080, Width: 1920, FloorKbps: 5_000},
	{Height: 720, Width: 1280, FloorKbps: 2_000},
	{Height: 540, Width: 960, FloorKbps: 1_200},
	{Height: 480, Width: 854, FloorKbps: 0},
}

const (
	codecVP9 = "vp9"
	codecAV1 = "av1"
)

// codecEfficiency is the bitrate a codec needs relative to H.264 for the same
// quality: HEVC and VP9 are about 40% more efficient, AV1 about 50%.
func codecEfficiency(codec string) float64 {
	switch normalizeCodecV3(codec) {
	case transcodeCodecHEVC, codecVP9:
		return 0.6
	case codecAV1:
		return 0.5
	default:
		return 1
	}
}

// LadderClassForBitrate returns the height of the largest ladder class whose
// floor kbps meets once normalized to H.264 at <=30 fps. Higher frame rates
// need more bits per second for the same picture, so above 30 fps the budget
// shrinks by the square root of the frame-rate ratio; more efficient output
// codecs stretch it.
func LadderClassForBitrate(kbps int, frameRate float64, outputCodec string) int {
	equivalent := float64(kbps) / codecEfficiency(outputCodec)
	if frameRate > 30 {
		equivalent /= math.Sqrt(frameRate / 30)
	}
	for _, class := range bitrateLadder {
		if equivalent >= float64(class.FloorKbps) {
			return class.Height
		}
	}
	return bitrateLadder[len(bitrateLadder)-1].Height
}

// ladderClassesFrom returns the ladder classes at or below height, tallest
// first, so a caller can step down when a device cannot decode a class.
func ladderClassesFrom(height int) []ladderClass {
	for i, class := range bitrateLadder {
		if class.Height <= height {
			return bitrateLadder[i:]
		}
	}
	return bitrateLadder[len(bitrateLadder)-1:]
}

// ladderClassForSize is the smallest ladder class whose box holds a frame,
// the class a source already belongs to: 1920x800 is 1080p, 1280x720 is
// 720p. A frame known only by its height is classed by that height, a frame
// larger than every box is the largest class, and an unknown size is 0.
func ladderClassForSize(width, height int) int {
	if height <= 0 {
		return 0
	}
	for i := len(bitrateLadder) - 1; i >= 0; i-- {
		class := bitrateLadder[i]
		if height <= class.Height && (width <= 0 || width <= class.Width) {
			return class.Height
		}
	}
	return bitrateLadder[0].Height
}

// FitLadderBox scales a source into a class's 16:9 box, keeping its aspect
// ratio and never enlarging it. A 3840x1600 scope film fits the 1080p class
// as 1920x800 rather than 2592x1080. Dimensions are even, as H.264 and HEVC
// 4:2:0 encodes require. The encoder is given only the height and derives the
// width as FFmpeg's scale=-2 does, so the width here is computed the same way
// and the height steps down until that width fits the box. An unknown source
// size returns zeros; a source known only by its height keeps a zero width and
// its own height, or the class height when it is taller.
func FitLadderBox(sourceWidth, sourceHeight, classHeight int) (int, int) {
	if sourceHeight <= 0 {
		return 0, 0
	}
	if sourceWidth <= 0 {
		return 0, min(sourceHeight, classHeight)
	}
	boxWidth, boxHeight := classHeight*16/9, classHeight
	for _, class := range bitrateLadder {
		if class.Height == classHeight {
			boxWidth = class.Width
		}
	}
	if sourceWidth <= boxWidth && sourceHeight <= boxHeight {
		return sourceWidth, sourceHeight
	}
	scale := math.Min(float64(boxWidth)/float64(sourceWidth), float64(boxHeight)/float64(sourceHeight))
	height := int(math.Round(float64(sourceHeight)*scale/2)) * 2
	width := scaledEvenWidth(sourceWidth, sourceHeight, height)
	for width > boxWidth && height > 2 {
		height -= 2
		width = scaledEvenWidth(sourceWidth, sourceHeight, height)
	}
	return width, height
}

// encodedFrame is the frame an encode of a box-fit size actually produces:
// an odd height rounds down to even, since 4:2:0 output needs it, and the
// width is then what scale=-2 gives. An even fit is returned unchanged.
func encodedFrame(sourceWidth, sourceHeight, width, height int) (int, int) {
	if width%2 == 0 && height%2 == 0 {
		return width, height
	}
	height &^= 1
	if sourceWidth > 0 && sourceHeight > 0 {
		width = scaledEvenWidth(sourceWidth, sourceHeight, height)
	}
	return width, height
}

// scaledEvenWidth is the width FFmpeg's scale=-2:height gives a source: the
// aspect-preserving width rounded to the nearest even number.
func scaledEvenWidth(sourceWidth, sourceHeight, height int) int {
	return int(math.Round(float64(height)*float64(sourceWidth)/float64(2*sourceHeight))) * 2
}

// heightLabel formats an output height as the resolution label the encoder's
// scale filters parse.
func heightLabel(height int) string {
	return strconv.Itoa(height) + "p"
}
