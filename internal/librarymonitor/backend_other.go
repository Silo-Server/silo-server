//go:build !linux

package librarymonitor

import (
	"errors"
	"io/fs"
)

// platformSupported reports whether this build can monitor at all. Real-time
// monitoring is Linux only; every visible root reports unsupported_platform.
const platformSupported = false

var errUnsupportedPlatform = errors.New("real-time monitoring needs Linux")

func classifyRoot(string) (fsClass, error) { return fsClass{}, errUnsupportedPlatform }

func linkCount(fs.FileInfo) uint64 { return 1 }

func identityOf(fs.FileInfo) fileID { return fileID{} }

func readMaxUserWatches() int { return 0 }

func readMounts() ([]mountEntry, error) { return nil, nil }

func newInotifyBackend(BackendOptions, inotifyHooks) (Backend, error) {
	return nil, errUnsupportedPlatform
}
