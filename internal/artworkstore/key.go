package artworkstore

import (
	"path"
	"strings"
	"unicode"
)

func ValidateKey(k string) error {
	if len(k) > 1024 || strings.IndexFunc(k, func(r rune) bool { return unicode.IsControl(r) }) >= 0 {
		return ErrInvalidKey
	}
	if k == "" || strings.HasPrefix(k, "/") || strings.Contains(k, "\\") {
		return ErrInvalidKey
	}
	for _, p := range strings.Split(k, "/") {
		if p == "" || p == "." || p == ".." || strings.HasPrefix(p, ".tmp-") || strings.HasPrefix(p, ".probe-") {
			return ErrInvalidKey
		}
	}
	return nil
}
func MediaType(k string) string {
	switch strings.ToLower(path.Ext(k)) {
	case ".webp":
		return "image/webp"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".avif":
		return "image/avif"
	default:
		return "application/octet-stream"
	}
}
