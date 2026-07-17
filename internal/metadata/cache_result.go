package metadata

import (
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
)

// CachedImageOriginalPath returns the exact stored original key. The fallback
// keeps older ImageCacher test doubles and implementations source compatible
// while callers migrate away from reconstructing object keys themselves.
func CachedImageOriginalPath(result *CacheImageResult) string {
	if result == nil {
		return ""
	}
	if result.OriginalPath != "" {
		return result.OriginalPath
	}
	if result.BasePath == "" {
		return ""
	}
	if strings.Contains(result.BasePath, "/original.") {
		return result.BasePath
	}
	ext := result.Ext
	if ext == "" {
		// Historical default for legacy cachers that predate WebP conversion.
		ext = ".jpg"
	}
	return artworkkey.Original(result.BasePath, "", ext)
}
