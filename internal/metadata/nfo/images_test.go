package nfo

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

func writeImageFile(t *testing.T, path string) {
	t.Helper()
	writeFile(t, path, "fake-image-bytes")
}

var fingerprintedSource = regexp.MustCompile(`^file://(.+)\?v=\d+--?\d+$`)

// sidecarPath returns the file path a discovered source names, failing the
// test unless the source carries the size+mtime fingerprint.
func sidecarPath(t *testing.T, source string) string {
	t.Helper()
	match := fingerprintedSource.FindStringSubmatch(source)
	if match == nil {
		t.Fatalf("source %q is not a fingerprinted file:// source", source)
	}
	return match[1]
}

func imagesByType(images []metadata.RemoteImage) map[metadata.ImageType]metadata.RemoteImage {
	byType := make(map[metadata.ImageType]metadata.RemoteImage, len(images))
	for _, img := range images {
		if _, ok := byType[img.Type]; !ok {
			byType[img.Type] = img
		}
	}
	return byType
}

func TestGetImagesRejectsSymlinksAndNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film.mkv")
	writeImageFile(t, moviePath)
	target := filepath.Join(dir, "elsewhere.jpg")
	writeImageFile(t, target)
	if err := os.Symlink(target, filepath.Join(dir, "poster.jpg")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "fanart.jpg"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	images := discoverImages(t, "movie", moviePath, dir)
	if len(images) != 0 {
		t.Fatalf("expected symlinked/non-regular sidecars rejected, got %+v", images)
	}
}

func writeSizedFile(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestGetImagesSizeCapMatchesRemoteImages(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film.mkv")
	writeImageFile(t, moviePath)
	// A poster at the 25 MiB cap is used; a backdrop one byte over is skipped
	// and the next backdrop name takes its place.
	writeSizedFile(t, filepath.Join(dir, "poster.jpg"), metadata.MaxArtworkSourceBytes)
	writeSizedFile(t, filepath.Join(dir, "fanart.jpg"), metadata.MaxArtworkSourceBytes+1)
	writeImageFile(t, filepath.Join(dir, "backdrop.jpg"))

	images := discoverImages(t, "movie", moviePath, dir)
	byType := imagesByType(images)
	if got := sidecarPath(t, byType[metadata.ImagePoster].URL); got != filepath.Join(dir, "poster.jpg") {
		t.Fatalf("poster = %q, want the image at the cap", got)
	}
	if got := sidecarPath(t, byType[metadata.ImageBackdrop].URL); got != filepath.Join(dir, "backdrop.jpg") {
		t.Fatalf("backdrop = %q, want backdrop.jpg after the oversized fanart.jpg", got)
	}
}
