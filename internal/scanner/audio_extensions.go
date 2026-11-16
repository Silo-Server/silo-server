package scanner

import (
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/mediacontainer"
)

// audioExtensions is the set of file extensions the audiobook and podcast
// scanner branches recognize (mediacontainer.AudioExtensions). Mirrors
// videoExtensions for video libraries.
var audioExtensions = mediacontainer.AudioExtensions()

// SupportsAudioFile reports whether the given path uses a recognized audio
// file extension.
func SupportsAudioFile(filePath string) bool {
	if filePath == "" {
		return false
	}
	return audioExtensions[strings.ToLower(filepath.Ext(filePath))]
}
