//go:build !linux && !darwin

package nfo

import "io/fs"

// changeTime is unavailable on this platform; listings are keyed by
// modification time alone.
func changeTime(fs.FileInfo) int64 { return 0 }
