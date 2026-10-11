package metadata

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxArtworkSourceBytes caps every artwork source the image cache reads:
// provider downloads (imagecache) and local sidecars. NFO discovery skips a
// larger sidecar and the image-cache processor re-checks the cap when it reads
// one.
const MaxArtworkSourceBytes = 25 << 20

// localImageFingerprint matches the "?v=<size>-<mtime>" suffix that
// LocalImageSourceURL appends. Only a trailing match is stripped, so a "?" in
// the file name itself survives.
var localImageFingerprint = regexp.MustCompile(`\?v=\d+--?\d+$`)

// LocalImageSourceURL records a discovered sidecar image as a file:// source
// carrying its size and modification time in whole seconds, for example
// "file:///media/Film/poster.jpg?v=48213-1712345678". Replacing the file under
// the same name changes the fingerprint, so the stored source path changes and
// the image-cache job re-queues instead of keeping the old copy. Whole seconds
// keep the fingerprint stable across mounts that report finer mtimes with
// different precision (local disk, NFS, SMB).
func LocalImageSourceURL(path string, size int64, modTime time.Time) string {
	return "file://" + path + "?v=" + strconv.FormatInt(size, 10) + "-" + strconv.FormatInt(modTime.Unix(), 10)
}

// localImageSourceFilePath returns the cleaned filesystem path a file://
// source names, without the scheme or fingerprint. ok is false for a source
// that is not local.
func localImageSourceFilePath(source string) (path string, ok bool) {
	source = strings.TrimSpace(source)
	const scheme = "file://"
	if len(source) < len(scheme) || !strings.EqualFold(source[:len(scheme)], scheme) {
		return "", false
	}
	path = localImageFingerprint.ReplaceAllString(source[len(scheme):], "")
	return filepath.Clean(path), true
}
