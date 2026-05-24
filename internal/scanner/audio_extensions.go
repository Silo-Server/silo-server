package scanner

import (
	"path/filepath"
	"strings"
)

// audioExtensions is the set of file extensions the audiobook and podcast
// scanner branches recognize. Mirrors videoExtensions for video libraries.
var audioExtensions = map[string]bool{
	".m4b":  true,
	".m4a":  true,
	".mp3":  true,
	".flac": true,
	".opus": true,
	".ogg":  true,
}

// SupportsAudioFile reports whether the given path uses a recognized audio
// file extension.
func SupportsAudioFile(filePath string) bool {
	if filePath == "" {
		return false
	}
	return audioExtensions[strings.ToLower(filepath.Ext(filePath))]
}
