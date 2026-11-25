//go:build !darwin && !linux && !windows && !freebsd

package playback

import "os"

func directPlayFilesystemRevision(*os.File, os.FileInfo) (string, bool) {
	return "", false
}
