package playback

import (
	"crypto/sha256"
	"fmt"
	"os"
)

const directPlayETagVersion = "dsr1"

// DirectPlayEntityTag is the strong validator ServeDirectPlay sends for file,
// or "" when the platform has no stable revision for it. A route that serves
// the same bytes through another writer sends it so If-Range resumes match.
func DirectPlayEntityTag(file *os.File, info os.FileInfo) string {
	revision, ok := directPlayFilesystemRevision(file, info)
	if !ok {
		return ""
	}

	digest := sha256.Sum256([]byte(directPlayETagVersion + "\x00" + revision))
	return fmt.Sprintf("\"%s-%x\"", directPlayETagVersion, digest)
}
