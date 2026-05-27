package subtitles

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	// ProviderUpload identifies user-uploaded subtitles in downloaded_subtitles.
	ProviderUpload = "upload"

	// MaxUploadSize is the maximum allowed size for user-uploaded subtitle files.
	MaxUploadSize = 5 << 20 // 5 MB
)

var allowedUploadFormats = map[string]SubtitleFormat{
	"srt": FormatSRT,
	"vtt": FormatVTT,
	"ass": FormatASS,
	"ssa": FormatSSA,
	"sub": FormatSUB,
}

// SubtitleContentType returns the HTTP content type for a subtitle format.
func SubtitleContentType(format SubtitleFormat) string {
	switch format {
	case FormatVTT:
		return "text/vtt; charset=utf-8"
	case FormatSRT:
		return "application/x-subrip; charset=utf-8"
	case FormatASS, FormatSSA:
		return "text/x-ssa; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// FormatFromFilename returns the subtitle format from a filename extension.
func FormatFromFilename(name string) (SubtitleFormat, error) {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if ext == "" {
		return "", fmt.Errorf("missing file extension")
	}
	format, ok := allowedUploadFormats[ext]
	if !ok {
		return "", fmt.Errorf("unsupported subtitle format: %s", ext)
	}
	return format, nil
}
