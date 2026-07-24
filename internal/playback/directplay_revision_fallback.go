//go:build !darwin && !linux && !windows

package playback

import "os"

func directPlayFilesystemRevision(*os.File, os.FileInfo) (string, bool) {
	return "", false
}
