package branding

import (
	"fmt"
	"slices"

	"github.com/Silo-Server/silo-server/internal/imageutil"
)

// assetSpec describes how one branding image kind is validated, processed, and
// stored.
type assetSpec struct {
	kind       AssetKind
	settingKey string // server_settings key holding the current content ref
	s3Prefix   string // S3 key prefix; object key is "<prefix>/<ref>"
	maxBytes   int64
	// process validates the declared content type and returns the bytes to
	// store, the content type to serve them with, and the file extension used
	// in the content ref.
	process func(data []byte, declaredType string) (out []byte, serveType, ext string, err error)
}

// assetSpecs is the registry of all branding asset kinds. Adding a kind here is
// the only change needed to support a new uploadable asset.
var assetSpecs = map[AssetKind]assetSpec{
	KindWordmark: {
		kind:       KindWordmark,
		settingKey: "branding.wordmark_ref",
		s3Prefix:   "branding/wordmark",
		maxBytes:   8 << 20,
		process:    processWebP(imageutil.GenerateVariants, 640),
	},
	KindWordmarkLight: {
		kind:       KindWordmarkLight,
		settingKey: "branding.wordmark_light_ref",
		s3Prefix:   "branding/wordmark-light",
		maxBytes:   8 << 20,
		process:    processWebP(imageutil.GenerateVariants, 640),
	},
	KindMark: {
		kind:       KindMark,
		settingKey: "branding.mark_ref",
		s3Prefix:   "branding/mark",
		maxBytes:   8 << 20,
		process:    processWebP(imageutil.GenerateSquareVariants, 512),
	},
	KindMarkLight: {
		kind:       KindMarkLight,
		settingKey: "branding.mark_light_ref",
		s3Prefix:   "branding/mark-light",
		maxBytes:   8 << 20,
		process:    processWebP(imageutil.GenerateSquareVariants, 512),
	},
	KindFavicon: {
		kind:       KindFavicon,
		settingKey: "branding.favicon_ref",
		s3Prefix:   "branding/favicon",
		maxBytes:   1 << 20,
		process:    processFaviconPassthrough,
	},
	KindLoginBg: {
		kind:       KindLoginBg,
		settingKey: "branding.login_bg_ref",
		s3Prefix:   "branding/login-bg",
		maxBytes:   12 << 20,
		process:    processWebP(imageutil.GenerateVariants, 2560),
	},
}

// imageUploadTypes is the accepted set for WebP-converted kinds.
var imageUploadTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

// imageVariantFunc matches imageutil.GenerateVariants / GenerateSquareVariants.
type imageVariantFunc func(data []byte, sizes []int) (*imageutil.VariantResult, error)

// processWebP re-encodes any accepted image to a size-capped WebP using the
// given variant generator (width-preserving or square). The aspect ratio is
// preserved by the generator; narrower images are not upscaled.
func processWebP(generate imageVariantFunc, size int) func([]byte, string) ([]byte, string, string, error) {
	return func(data []byte, declaredType string) ([]byte, string, string, error) {
		if !imageUploadTypes[declaredType] {
			return nil, "", "", ErrUnsupportedImage
		}
		res, err := generate(data, []int{size})
		if err != nil {
			return nil, "", "", ErrUnsupportedImage
		}
		return pickVariant(res, fmt.Sprintf("w%d", size)), "image/webp", ".webp", nil
	}
}

// processFaviconPassthrough stores the favicon unchanged (no WebP re-encode) so
// that .ico/.png keep working in browsers that don't render WebP favicons.
func processFaviconPassthrough(data []byte, declaredType string) ([]byte, string, string, error) {
	switch declaredType {
	case "image/png":
		return data, "image/png", ".png", nil
	case "image/webp":
		return data, "image/webp", ".webp", nil
	case "image/x-icon", "image/vnd.microsoft.icon":
		return data, "image/x-icon", ".ico", nil
	case "image/svg+xml":
		return data, "image/svg+xml", ".svg", nil
	default:
		return nil, "", "", ErrUnsupportedImage
	}
}

// pickVariant returns the named variant's bytes, falling back to the re-encoded
// original when the exact width variant is absent.
func pickVariant(res *imageutil.VariantResult, key string) []byte {
	var original []byte
	for _, v := range res.Variants {
		if v.Key == key {
			return v.Data
		}
		if v.Key == "original" {
			original = v.Data
		}
	}
	return original
}

// MaxUploadBytes returns the maximum accepted upload size for a kind, or 0 when
// the kind is unknown.
func MaxUploadBytes(kind AssetKind) int64 {
	if spec, ok := assetSpecs[kind]; ok {
		return spec.maxBytes
	}
	return 0
}

// contentTypeForExt maps a stored ref's extension to the Content-Type used when
// serving it.
func contentTypeForExt(ext string) string {
	switch ext {
	case ".webp":
		return "image/webp"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

// AssetSettingKeys lists the server_settings keys that hold a branding
// asset's content ref, sorted. Backups read them to find the asset objects.
func AssetSettingKeys() []string {
	keys := make([]string, 0, len(assetSpecs))
	for _, spec := range assetSpecs {
		keys = append(keys, spec.settingKey)
	}
	slices.Sort(keys)
	return keys
}

// AssetObjectKey is the blob key of the asset whose ref is stored under
// settingKey, or "" when the key holds no asset or the ref is empty.
func AssetObjectKey(settingKey, ref string) string {
	if ref == "" {
		return ""
	}
	for _, spec := range assetSpecs {
		if spec.settingKey == settingKey {
			return spec.s3Prefix + "/" + ref
		}
	}
	return ""
}
