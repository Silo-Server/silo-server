package metadata

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxLocalArtworkBytes caps sidecar artwork at the 25 MiB the image cache
// allows for provider downloads. NFO discovery skips larger files and the
// image-cache processor re-checks the cap when it reads one.
const MaxLocalArtworkBytes = 25 << 20

// localImageFingerprint matches the "?v=<size>-<mtimeNs>" suffix that
// LocalImageSourceURL appends. Only a trailing match is stripped, so a "?" in
// the file name itself survives.
var localImageFingerprint = regexp.MustCompile(`\?v=\d+--?\d+$`)

// LocalImageSourceURL records a discovered sidecar image as a file:// source
// carrying its size and modification time, for example
// "file:///media/Film/poster.jpg?v=48213-1712345678901234567". Replacing the
// file under the same name changes the fingerprint, so the stored source path
// changes and the image-cache job re-queues instead of keeping the old copy.
func LocalImageSourceURL(path string, size int64, modTime time.Time) string {
	return "file://" + path + "?v=" + strconv.FormatInt(size, 10) + "-" + strconv.FormatInt(modTime.UnixNano(), 10)
}

// localImageSourceFilePath returns the cleaned filesystem path a file://
// source names, without the scheme or fingerprint. ok is false for a source
// that is not local.
func localImageSourceFilePath(source string) (path string, ok bool) {
	source = strings.TrimSpace(source)
	if !isLocalImageSourcePath(source) {
		return "", false
	}
	path = localImageFingerprint.ReplaceAllString(source[len("file://"):], "")
	return filepath.Clean(path), true
}
