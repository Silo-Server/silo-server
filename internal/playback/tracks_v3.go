package playback

import (
	"fmt"
	"strconv"
	"strings"
)

func TrackIDV3(fileID int, kind string, ordinal int) string {
	return fmt.Sprintf("file:%d:%s:%d", fileID, kind, ordinal)
}

func ParseTrackIDV3(value string) (fileID int, kind string, ordinal int, ok bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 4 || parts[0] != "file" || (parts[2] != "audio" && parts[2] != "subtitle") {
		return 0, "", 0, false
	}
	fileID, ok = parseCanonicalTrackNumberV3(parts[1])
	if !ok || fileID <= 0 {
		return 0, "", 0, false
	}
	ordinal, ok = parseCanonicalTrackNumberV3(parts[3])
	if !ok {
		return 0, "", 0, false
	}
	return fileID, parts[2], ordinal, true
}

// parseCanonicalTrackNumberV3 accepts only the exact decimal form emitted by
// TrackIDV3: digits without a sign and without leading zeros, so every parsed
// identity round-trips byte-for-byte.
func parseCanonicalTrackNumberV3(value string) (int, bool) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}
