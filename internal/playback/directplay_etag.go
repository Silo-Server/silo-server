package playback

import (
	"crypto/sha256"
	"fmt"
	"os"
)

const directPlayETagVersion = "dsr1"

func directPlayEntityTag(file *os.File, info os.FileInfo) string {
	revision, ok := directPlayFilesystemRevision(file, info)
	if !ok {
		return ""
	}

	digest := sha256.Sum256([]byte(directPlayETagVersion + "\x00" + revision))
	return fmt.Sprintf("\"%s-%x\"", directPlayETagVersion, digest)
}
