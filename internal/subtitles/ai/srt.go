package ai

import "github.com/Silo-Server/silo-server/internal/subtitles"

// ParseCues keeps the AI package API stable while using the shared parser.
func ParseCues(data []byte) ([]SubtitleCue, error) {
	return subtitles.ParseCues(data)
}

// SerializeSRT keeps the AI package API stable while using the shared writer.
func SerializeSRT(cues []SubtitleCue) []byte {
	return subtitles.SerializeSRT(cues)
}
