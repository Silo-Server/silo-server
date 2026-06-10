package scanner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

const maxAudiobookSidecarCoverSize = 8 * 1024 * 1024

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

type audiobookCoverMetadataStore interface {
	GetPosterPath(ctx context.Context, contentID string) (string, error)
	UpdateMetadata(ctx context.Context, contentID string, upd *catalog.MetadataUpdate) error
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

func applyAudiobookSidecarCover(ctx context.Context, store audiobookCoverMetadataStore, cacher audiobookCoverCacher, contentID string, folderPath string) error {
	if store == nil || cacher == nil || contentID == "" || folderPath == "" {
		return nil
	}
	cover, _, err := findSidecarAudiobookCover(folderPath)
	if err != nil || len(cover) == 0 {
		return err
	}
	existingPosterPath, err := store.GetPosterPath(ctx, contentID)
	if err != nil {
		return fmt.Errorf("get audiobook poster path for cover: %w", err)
	}
	if strings.TrimSpace(existingPosterPath) != "" {
		return nil
	}
	basePath, ext, thumbhash, err := cacher.CacheAudiobookCover(ctx, cover, contentID)
	if err != nil {
		return err
	}
	posterPath := strings.TrimRight(basePath, "/") + "/original" + ext
	update := &catalog.MetadataUpdate{PosterPath: &posterPath}
	if thumbhash != "" {
		update.PosterThumbhash = &thumbhash
	}
	return store.UpdateMetadata(ctx, contentID, update)
}

var sidecarAudiobookCoverNames = []string{"cover", "folder", "front", "poster", "thumbnail"}
var sidecarAudiobookCoverExtensions = []string{".jpg", ".jpeg", ".png", ".webp", ".avif", ".gif", ".bmp"}

func findSidecarAudiobookCover(dir string) ([]byte, string, error) {
	if dir == "" {
		return nil, "", nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", err
	}
	byName := make(map[string]string, len(entries))
	for _, entry := range entries {
		if !isRegularDirEntry(entry) {
			continue
		}
		byName[strings.ToLower(entry.Name())] = filepath.Join(dir, entry.Name())
	}
	for _, name := range sidecarAudiobookCoverNames {
		for _, ext := range sidecarAudiobookCoverExtensions {
			path := byName[name+ext]
			if path == "" {
				continue
			}
			data, err := readSidecarAudiobookCover(path)
			if err != nil {
				return nil, path, err
			}
			return data, path, nil
		}
	}
	return nil, "", nil
}

func isRegularDirEntry(entry os.DirEntry) bool {
	if entry == nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
		return false
	}
	info, err := entry.Info()
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

func readSidecarAudiobookCover(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxAudiobookSidecarCoverSize {
		return nil, fmt.Errorf("audiobook sidecar cover too large: %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxAudiobookSidecarCoverSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAudiobookSidecarCoverSize {
		return nil, fmt.Errorf("audiobook sidecar cover too large: %s", path)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("audiobook sidecar cover empty: %s", path)
	}
	return data, nil
}
