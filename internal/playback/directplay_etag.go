package playback

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

const directPlayETagVersion = "dsr1"

func directPlayEntityTag(file *os.File, info os.FileInfo) string {
	revision, ok := directPlayFilesystemRevision(file, info)
	if !ok {
		return ""
	}

	hasher := sha256.New()
	_, _ = io.WriteString(hasher, directPlayETagVersion+"\x00"+revision)
	const sampleSize int64 = 64 << 10
	size := info.Size()
	offsets := []int64{0}
	if size > sampleSize {
		offsets = append(offsets, max(0, size/2-sampleSize/2), max(0, size-sampleSize))
	}
	buffer := make([]byte, sampleSize)
	for _, offset := range offsets {
		n, err := file.ReadAt(buffer, offset)
		if n > 0 {
			_, _ = hasher.Write(buffer[:n])
		}
		if err != nil && err != io.EOF {
			return ""
		}
	}
	digest := hasher.Sum(nil)
	return fmt.Sprintf("\"%s-%x\"", directPlayETagVersion, digest)
}
