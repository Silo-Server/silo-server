package nfo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

func writeImageFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fake-image-bytes"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
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

func TestGetImagesFindsSidecarArtwork(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film (2020).mkv")
	writeImageFile(t, moviePath)
	writeImageFile(t, filepath.Join(dir, "poster.jpg"))
	writeImageFile(t, filepath.Join(dir, "fanart.png"))
	writeImageFile(t, filepath.Join(dir, "clearlogo.webp"))

	p := NewProvider()
	images, err := p.GetImages(context.Background(), metadata.ImageRequest{
		ContentType:               "movie",
		RepresentativeFilePath:    moviePath,
		AllGroupFilePaths:         []string{moviePath},
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	byType := imagesByType(images)
	if len(byType) != 3 {
		t.Fatalf("expected poster/backdrop/logo, got %d images: %+v", len(images), images)
	}
	poster := byType[metadata.ImagePoster]
	if poster.URL != "file://"+filepath.Join(dir, "poster.jpg") {
		t.Fatalf("poster URL = %q", poster.URL)
	}
	if poster.Rating != 0 {
		t.Fatalf("poster rating = %v, want 0", poster.Rating)
	}
	if poster.ProviderID != "nfo" {
		t.Fatalf("poster provider = %q", poster.ProviderID)
	}
	if got := byType[metadata.ImageBackdrop].URL; got != "file://"+filepath.Join(dir, "fanart.png") {
		t.Fatalf("backdrop URL = %q", got)
	}
	if got := byType[metadata.ImageLogo].URL; got != "file://"+filepath.Join(dir, "clearlogo.webp") {
		t.Fatalf("logo URL = %q", got)
	}
}

func TestGetImagesFilenamePrecedence(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film.mkv")
	writeImageFile(t, moviePath)
	// poster beats folder and cover; folder beats cover.
	writeImageFile(t, filepath.Join(dir, "poster.png"))
	writeImageFile(t, filepath.Join(dir, "folder.jpg"))
	writeImageFile(t, filepath.Join(dir, "cover.jpg"))
	// backdrop names: fanart beats backdrop/background.
	writeImageFile(t, filepath.Join(dir, "background.jpg"))
	writeImageFile(t, filepath.Join(dir, "fanart.jpg"))
	// logo: logo beats clearlogo.
	writeImageFile(t, filepath.Join(dir, "clearlogo.png"))
	writeImageFile(t, filepath.Join(dir, "logo.png"))

	p := NewProvider()
	images, err := p.GetImages(context.Background(), metadata.ImageRequest{
		ContentType:               "movie",
		RepresentativeFilePath:    moviePath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	byType := imagesByType(images)
	if got := byType[metadata.ImagePoster].URL; got != "file://"+filepath.Join(dir, "poster.png") {
		t.Fatalf("poster URL = %q, want poster.png", got)
	}
	if got := byType[metadata.ImageBackdrop].URL; got != "file://"+filepath.Join(dir, "fanart.jpg") {
		t.Fatalf("backdrop URL = %q, want fanart.jpg", got)
	}
	if got := byType[metadata.ImageLogo].URL; got != "file://"+filepath.Join(dir, "logo.png") {
		t.Fatalf("logo URL = %q, want logo.png", got)
	}
}

func TestGetImagesBasenameScopedArtwork(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film (2020).mkv")
	writeImageFile(t, moviePath)
	writeImageFile(t, filepath.Join(dir, "Film (2020)-poster.jpg"))
	writeImageFile(t, filepath.Join(dir, "Film (2020)-fanart.jpg"))
	writeImageFile(t, filepath.Join(dir, "Film (2020)-logo.png"))

	p := NewProvider()
	// No directory search paths: basename-scoped art still applies.
	images, err := p.GetImages(context.Background(), metadata.ImageRequest{
		ContentType:            "movie",
		RepresentativeFilePath: moviePath,
	})
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	byType := imagesByType(images)
	if got := byType[metadata.ImagePoster].URL; got != "file://"+filepath.Join(dir, "Film (2020)-poster.jpg") {
		t.Fatalf("poster URL = %q", got)
	}
	if got := byType[metadata.ImageBackdrop].URL; got != "file://"+filepath.Join(dir, "Film (2020)-fanart.jpg") {
		t.Fatalf("backdrop URL = %q", got)
	}
	if got := byType[metadata.ImageLogo].URL; got != "file://"+filepath.Join(dir, "Film (2020)-logo.png") {
		t.Fatalf("logo URL = %q", got)
	}
}

func TestGetImagesFlatFolderGenericArtSuppressed(t *testing.T) {
	// Multi-movie flat folder: the directory is not a safe sidecar search
	// path (canUseObservedRootForDirectorySidecars suppression upstream), so
	// generic names like folder.jpg must not attach to any movie in it.
	dir := t.TempDir()
	movieA := filepath.Join(dir, "Movie A.mkv")
	writeImageFile(t, movieA)
	writeImageFile(t, filepath.Join(dir, "Movie B.mkv"))
	writeImageFile(t, filepath.Join(dir, "folder.jpg"))

	p := NewProvider()
	images, err := p.GetImages(context.Background(), metadata.ImageRequest{
		ContentType:            "movie",
		RepresentativeFilePath: movieA,
		AllGroupFilePaths:      []string{movieA},
		// PrimarySidecarSearchPaths deliberately empty.
	})
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	if len(images) != 0 {
		t.Fatalf("expected no images for flat multi-movie folder, got %+v", images)
	}
}

func TestGetImagesWorksWithoutNFO(t *testing.T) {
	// Documented bonus: local art applies even with no .nfo present.
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film.mkv")
	writeImageFile(t, moviePath)
	writeImageFile(t, filepath.Join(dir, "poster.jpg"))

	p := NewProvider()
	images, err := p.GetImages(context.Background(), metadata.ImageRequest{
		ContentType:               "movie",
		RepresentativeFilePath:    moviePath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	if len(images) != 1 || images[0].Type != metadata.ImagePoster {
		t.Fatalf("expected exactly the poster, got %+v", images)
	}
}

func TestGetImagesExtensionSet(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film.mkv")
	writeImageFile(t, moviePath)
	// .gif and .bmp are not in the accepted extension set.
	writeImageFile(t, filepath.Join(dir, "poster.gif"))
	writeImageFile(t, filepath.Join(dir, "fanart.bmp"))
	writeImageFile(t, filepath.Join(dir, "logo.jpeg"))

	p := NewProvider()
	images, err := p.GetImages(context.Background(), metadata.ImageRequest{
		ContentType:               "movie",
		RepresentativeFilePath:    moviePath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	if len(images) != 1 || images[0].Type != metadata.ImageLogo {
		t.Fatalf("expected only logo.jpeg, got %+v", images)
	}
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

	p := NewProvider()
	images, err := p.GetImages(context.Background(), metadata.ImageRequest{
		ContentType:               "movie",
		RepresentativeFilePath:    moviePath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	if len(images) != 0 {
		t.Fatalf("expected symlinked/non-regular sidecars rejected, got %+v", images)
	}
}

func TestGetImagesRejectsOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film.mkv")
	writeImageFile(t, moviePath)
	oversized := filepath.Join(dir, "poster.jpg")
	f, err := os.Create(oversized)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(maxLocalArtworkBytes + 1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	p := NewProvider()
	images, err := p.GetImages(context.Background(), metadata.ImageRequest{
		ContentType:               "movie",
		RepresentativeFilePath:    moviePath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	if len(images) != 0 {
		t.Fatalf("expected oversized sidecar rejected, got %+v", images)
	}
}
