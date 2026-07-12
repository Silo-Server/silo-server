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
	fileID, err := strconv.Atoi(parts[1])
	if err != nil || fileID <= 0 {
		return 0, "", 0, false
	}
	ordinal, err = strconv.Atoi(parts[3])
	if err != nil || ordinal < 0 {
		return 0, "", 0, false
	}
	return fileID, parts[2], ordinal, true
}
