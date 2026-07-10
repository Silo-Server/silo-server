// Package rootcheck probes library root paths for reachability: a root is
// reachable when it exists, is a directory, and can be listed. It is shared
// by the scanner's dead-root protection and the admin mount-check endpoint so
// both agree on what "unreachable" means.
package rootcheck

import (
	"errors"
	"os"
)

// Error codes reported by Probe. They are part of the admin mount-check API
// response contract.
const (
	ErrCodeNotFound         = "not_found"
	ErrCodePermissionDenied = "permission_denied"
	ErrCodeNotDirectory     = "not_directory"
	ErrCodeReadFailed       = "read_failed"
	ErrCodeStatFailed       = "stat_failed"
)

// Result describes the outcome of probing a single root path.
type Result struct {
	Path         string
	Reachable    bool
	ErrorCode    string // empty when Reachable
	ErrorMessage string // empty when Reachable
}

// Probe checks that path exists, is a directory, and can be listed.
func Probe(path string) Result {
	res := Result{Path: path, Reachable: true}
	info, err := os.Stat(path)
	switch {
	case err != nil:
		res.Reachable = false
		res.ErrorCode, res.ErrorMessage = classify(err, false)
	case !info.IsDir():
		res.Reachable = false
		res.ErrorCode, res.ErrorMessage = ErrCodeNotDirectory, "Path is not a directory"
	default:
		if _, err := os.ReadDir(path); err != nil {
			res.Reachable = false
			res.ErrorCode, res.ErrorMessage = classify(err, true)
		}
	}
	return res
}

func classify(err error, isRead bool) (string, string) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return ErrCodeNotFound, "Path does not exist"
	case errors.Is(err, os.ErrPermission):
		return ErrCodePermissionDenied, "Permission denied"
	case isRead:
		return ErrCodeReadFailed, "Failed to read directory"
	default:
		return ErrCodeStatFailed, "Failed to stat path"
	}
}
