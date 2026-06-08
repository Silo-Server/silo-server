package scanner

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// audiobookCoverCacher is the narrow slice of imagecache.Cacher the
// audiobook scanner uses. Defined with scalar args (not the imagecache
// request struct) to avoid an import cycle: imagecache imports metadata
// which imports scanner.
type audiobookCoverCacher interface {
	CacheAudiobookCover(ctx context.Context, data []byte, contentID string) (basePath string, ext string, thumbhash string, err error)
}

type ebookCoverCacher interface {
	CacheEbookCover(ctx context.Context, data []byte, contentID string) (basePath string, ext string, thumbhash string, err error)
}

// FFmpegPathFromFFprobe derives the ffmpeg binary path from a configured
// ffprobe path. They live side by side in every silo deployment.
func FFmpegPathFromFFprobe(ffprobePath string) string {
	if ffprobePath == "" {
		return ""
	}
	if i := strings.LastIndex(ffprobePath, "ffprobe"); i >= 0 {
		candidate := ffprobePath[:i] + "ffmpeg" + ffprobePath[i+len("ffprobe"):]
		if candidate != "" && candidate != ffprobePath {
			return candidate
		}
	}
	return ""
}

// ExtractAndUploadAudiobookCover reads the embedded cover image (if any)
// from the given audio file via ffmpeg, pushes it through the silo
// imagecache (resize + thumbhash + S3 upload), and returns the
// poster_path S3 key plus thumbhash. Returns "", "" (no error) when no
// embedded cover exists or the cacher is unavailable.
func ExtractAndUploadAudiobookCover(
	ctx context.Context,
	ffmpegPath string,
	cacher audiobookCoverCacher,
	audioFilePath string,
	contentID string,
) (string, string) {
	if ffmpegPath == "" || cacher == nil || audioFilePath == "" || contentID == "" {
		return "", ""
	}
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-loglevel", "error",
		"-i", audioFilePath,
		"-an", "-map", "0:v?", "-c:v", "mjpeg",
		"-frames:v", "1",
		"-f", "mjpeg",
		"pipe:1",
	)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		slog.Debug("audiobook cover: ffmpeg failed (likely no embedded artwork)",
			"path", audioFilePath, "error", err, "stderr", stderr.String())
		return "", ""
	}
	data := stdout.Bytes()
	if len(data) == 0 {
		return "", ""
	}
	basePath, ext, thumbhash, err := cacher.CacheAudiobookCover(ctx, data, contentID)
	if err != nil {
		slog.Warn("audiobook cover: imagecache upload failed",
			"path", audioFilePath, "error", err)
		return "", ""
	}
	return fmt.Sprintf("%s/original%s", basePath, ext), thumbhash
}
