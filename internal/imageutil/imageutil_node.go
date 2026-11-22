//go:build silo_node

// The transcode node build (tag silo_node) links no libvips: a node never
// processes images, so every function here fails or reports false.

package imageutil

import "errors"

// errNodeBuild is what every processing function returns in a node build.
var errNodeBuild = errors.New("imageutil: image processing is not part of a transcode node build")

// NeedsRasterizing reports false in a node build.
func NeedsRasterizing([]byte) bool { return false }

// PixelDataUndecodable reports false in a node build.
func PixelDataUndecodable([]byte) bool { return false }

// SourceUnusable reports false in a node build.
func SourceUnusable(error, []byte) bool { return false }

// GenerateVariants fails in a node build.
func GenerateVariants([]byte, []int) (*VariantResult, error) { return nil, errNodeBuild }

// CheckImage fails in a node build.
func CheckImage([]byte) error { return errNodeBuild }

// EncodeWebPWidth fails in a node build.
func EncodeWebPWidth([]byte, int) ([]byte, error) { return nil, errNodeBuild }

// Rasterize fails in a node build.
func Rasterize([]byte) ([]byte, error) { return nil, errNodeBuild }

// EncodePNGWithin fails in a node build.
func EncodePNGWithin([]byte, int, int) (out []byte, width, height int, err error) {
	return nil, 0, 0, errNodeBuild
}

// EncodeWebPWithin fails in a node build.
func EncodeWebPWithin([]byte, int, int) (out []byte, width, height int, err error) {
	return nil, 0, 0, errNodeBuild
}

// GenerateSquareVariants fails in a node build.
func GenerateSquareVariants([]byte, []int) (*VariantResult, error) { return nil, errNodeBuild }

// CropToPoster fails in a node build.
func CropToPoster([]byte) ([]byte, error) { return nil, errNodeBuild }

// Thumbhash fails in a node build.
func Thumbhash([]byte) (string, error) { return "", errNodeBuild }
