package nfo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

func writeImageFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fake-image-bytes"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
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
	writeImageFile(t, filepath.Join(dir, "logo.webp"))

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
	if len(byType) != 3 {
		t.Fatalf("expected poster/backdrop/logo, got %+v", images)
	}
	if poster := byType[metadata.ImagePoster]; poster.Rating != 0 || poster.ProviderID != "nfo" {
		t.Fatalf("poster = %+v, want NFO provider and rating 0", poster)
	}
	if got := sidecarPath(t, byType[metadata.ImagePoster].URL); got != filepath.Join(dir, "poster.png") {
		t.Fatalf("poster URL = %q, want poster.png", got)
	}
	if got := sidecarPath(t, byType[metadata.ImageBackdrop].URL); got != filepath.Join(dir, "fanart.jpg") {
		t.Fatalf("backdrop URL = %q, want fanart.jpg", got)
	}
	if got := sidecarPath(t, byType[metadata.ImageLogo].URL); got != filepath.Join(dir, "logo.webp") {
		t.Fatalf("logo URL = %q, want logo.webp", got)
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
	if got := sidecarPath(t, byType[metadata.ImagePoster].URL); got != filepath.Join(dir, "Film (2020)-poster.jpg") {
		t.Fatalf("poster URL = %q", got)
	}
	if got := sidecarPath(t, byType[metadata.ImageBackdrop].URL); got != filepath.Join(dir, "Film (2020)-fanart.jpg") {
		t.Fatalf("backdrop URL = %q", got)
	}
	if got := sidecarPath(t, byType[metadata.ImageLogo].URL); got != filepath.Join(dir, "Film (2020)-logo.png") {
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

func TestGetImagesExtensionSet(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film.mkv")
	writeImageFile(t, moviePath)
	// .gif and Kodi's .tbn (a JPEG) decode in the image pipeline; .bmp does
	// not, so it is not discovered.
	writeImageFile(t, filepath.Join(dir, "poster.gif"))
	writeImageFile(t, filepath.Join(dir, "fanart.bmp"))
	writeImageFile(t, filepath.Join(dir, "logo.tbn"))

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
	if len(byType) != 2 {
		t.Fatalf("expected poster.gif and logo.tbn only, got %+v", images)
	}
	if got := sidecarPath(t, byType[metadata.ImagePoster].URL); got != filepath.Join(dir, "poster.gif") {
		t.Fatalf("poster = %q, want poster.gif", got)
	}
	if got := sidecarPath(t, byType[metadata.ImageLogo].URL); got != filepath.Join(dir, "logo.tbn") {
		t.Fatalf("logo = %q, want logo.tbn", got)
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
	if got := sidecarPath(t, byType[metadata.ImagePoster].URL); got != filepath.Join(dir, "poster.jpg") {
		t.Fatalf("poster = %q, want the image at the cap", got)
	}
	if got := sidecarPath(t, byType[metadata.ImageBackdrop].URL); got != filepath.Join(dir, "backdrop.jpg") {
		t.Fatalf("backdrop = %q, want backdrop.jpg after the oversized fanart.jpg", got)
	}
}

func TestGetImagesMatchesNamesIgnoringCase(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film (2020).MKV")
	writeImageFile(t, moviePath)
	writeImageFile(t, filepath.Join(dir, "Poster.JPG"))
	writeImageFile(t, filepath.Join(dir, "FanArt.Jpeg"))
	writeImageFile(t, filepath.Join(dir, "film (2020)-CLEARLOGO.png"))

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
	// The source keeps the on-disk spelling, which is what the processor reads.
	for imageType, want := range map[metadata.ImageType]string{
		metadata.ImagePoster:   "Poster.JPG",
		metadata.ImageBackdrop: "FanArt.Jpeg",
		metadata.ImageLogo:     "film (2020)-CLEARLOGO.png",
	} {
		if got := sidecarPath(t, byType[imageType].URL); got != filepath.Join(dir, want) {
			t.Errorf("image type %d = %q, want %s", imageType, got, want)
		}
	}
}

func TestGetImagesBasenameArtBeatsGenericNames(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film (2020).mkv")
	writeImageFile(t, moviePath)
	writeImageFile(t, filepath.Join(dir, "poster.jpg"))
	writeImageFile(t, filepath.Join(dir, "Film (2020)-poster.jpg"))
	writeImageFile(t, filepath.Join(dir, "fanart.jpg"))
	writeImageFile(t, filepath.Join(dir, "Film (2020)-fanart.jpg"))
	writeImageFile(t, filepath.Join(dir, "logo.png"))
	writeImageFile(t, filepath.Join(dir, "Film (2020)-clearlogo.png"))

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
	for imageType, want := range map[metadata.ImageType]string{
		metadata.ImagePoster:   "Film (2020)-poster.jpg",
		metadata.ImageBackdrop: "Film (2020)-fanart.jpg",
		metadata.ImageLogo:     "Film (2020)-clearlogo.png",
	} {
		if got := sidecarPath(t, byType[imageType].URL); got != filepath.Join(dir, want) {
			t.Errorf("image type %d = %q, want %s", imageType, got, want)
		}
	}
}

func TestGetImagesKodiAndJellyfinNames(t *testing.T) {
	cases := []struct {
		contentType string
		file        string
		imageType   metadata.ImageType
		found       bool
	}{
		{"movie", "default.jpg", metadata.ImagePoster, true},
		{"movie", "movie.jpg", metadata.ImagePoster, true},
		{"movie", "show.jpg", metadata.ImagePoster, false},
		{"series", "show.jpg", metadata.ImagePoster, true},
		{"series", "movie.jpg", metadata.ImagePoster, false},
		{"series", "default.png", metadata.ImagePoster, true},
		{"movie", "art.jpg", metadata.ImageBackdrop, true},
		{"series", "background.webp", metadata.ImageBackdrop, true},
		{"series", "clearlogo.png", metadata.ImageLogo, true},
	}
	for _, tc := range cases {
		t.Run(tc.contentType+"/"+tc.file, func(t *testing.T) {
			dir := t.TempDir()
			mediaPath := filepath.Join(dir, "Media.mkv")
			writeImageFile(t, mediaPath)
			writeImageFile(t, filepath.Join(dir, tc.file))
			p := NewProvider()
			images, err := p.GetImages(context.Background(), metadata.ImageRequest{
				ContentType:               tc.contentType,
				RepresentativeFilePath:    mediaPath,
				PrimarySidecarSearchPaths: []string{dir},
			})
			if err != nil {
				t.Fatalf("GetImages: %v", err)
			}
			img, ok := imagesByType(images)[tc.imageType]
			if ok != tc.found {
				t.Fatalf("found = %v, want %v (%+v)", ok, tc.found, images)
			}
			if ok && sidecarPath(t, img.URL) != filepath.Join(dir, tc.file) {
				t.Fatalf("URL = %q, want %s", img.URL, tc.file)
			}
		})
	}
}

// A series request lists every episode file, so episode-named art must not
// become series art; only the series folder's own names count.
func TestGetImagesSeriesIgnoresEpisodeBasenameArt(t *testing.T) {
	root := t.TempDir()
	seasonDir := filepath.Join(root, "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	episode := filepath.Join(seasonDir, "Show S01E01.mkv")
	writeImageFile(t, episode)
	writeImageFile(t, filepath.Join(seasonDir, "Show S01E01-poster.jpg"))
	writeImageFile(t, filepath.Join(seasonDir, "Show S01E01-fanart.jpg"))

	p := NewProvider()
	req := metadata.ImageRequest{
		ContentType:               "series",
		RepresentativeFilePath:    episode,
		AllGroupFilePaths:         []string{episode},
		PrimarySidecarSearchPaths: []string{root},
	}
	images, err := p.GetImages(context.Background(), req)
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	if len(images) != 0 {
		t.Fatalf("episode-named art reached the series: %+v", images)
	}

	writeImageFile(t, filepath.Join(root, "Folder.jpg"))
	images, err = p.GetImages(context.Background(), req)
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	if got := sidecarPath(t, imagesByType(images)[metadata.ImagePoster].URL); got != filepath.Join(root, "Folder.jpg") {
		t.Fatalf("series poster = %q, want the series folder's Folder.jpg", got)
	}
}

func TestGetImagesFingerprintChangesWhenFileIsReplaced(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film.mkv")
	writeImageFile(t, moviePath)
	poster := filepath.Join(dir, "poster.jpg")
	writeImageFile(t, poster)
	original := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(poster, original, original); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	p := NewProvider()
	req := metadata.ImageRequest{
		ContentType:               "movie",
		RepresentativeFilePath:    moviePath,
		PrimarySidecarSearchPaths: []string{dir},
	}
	first, err := p.GetImages(context.Background(), req)
	if err != nil || len(first) != 1 {
		t.Fatalf("GetImages = %+v, %v", first, err)
	}
	again, err := p.GetImages(context.Background(), req)
	if err != nil || len(again) != 1 || again[0].URL != first[0].URL {
		t.Fatalf("unchanged file gave a different source: %+v vs %+v", again, first)
	}

	// Replace the poster under the same name: the path stays the same, the
	// source does not, so the cache job re-queues.
	if err := os.WriteFile(poster, []byte("a different, longer image"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	replaced := original.Add(time.Hour)
	if err := os.Chtimes(poster, replaced, replaced); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	after, err := p.GetImages(context.Background(), req)
	if err != nil || len(after) != 1 {
		t.Fatalf("GetImages after replace = %+v, %v", after, err)
	}
	if after[0].URL == first[0].URL {
		t.Fatalf("replaced poster kept source %q", after[0].URL)
	}
	if sidecarPath(t, after[0].URL) != poster {
		t.Fatalf("replaced poster path = %q, want %q", sidecarPath(t, after[0].URL), poster)
	}
}

// With poster.jpg and a stray Poster.JPG side by side, the documented
// spelling wins, as it did before names matched ignoring case.
func TestGetImagesPrefersExactSpelling(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film.mkv")
	writeImageFile(t, moviePath)
	writeImageFile(t, filepath.Join(dir, "Poster.JPG"))
	writeImageFile(t, filepath.Join(dir, "Poster.jpg"))
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
	if got := sidecarPath(t, imagesByType(images)[metadata.ImagePoster].URL); got != filepath.Join(dir, "poster.jpg") {
		t.Fatalf("poster = %q, want poster.jpg", got)
	}
}

// A flat folder of many movies shares one listing across requests instead of
// being listed again for every movie; adding a sidecar changes the folder's
// mtime, so the next request sees it.
func TestGetImagesLargeFolderSeesNewSidecars(t *testing.T) {
	dir := t.TempDir()
	for i := range sharedListingMinEntries {
		writeImageFile(t, filepath.Join(dir, fmt.Sprintf("Movie %03d.mkv", i)))
	}
	// Only a folder that has not changed in the last few seconds is shared.
	settled := time.Now().Add(-time.Hour)
	if err := os.Chtimes(dir, settled, settled); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	moviePath := filepath.Join(dir, "Movie 007.mkv")
	p := NewProvider()
	req := metadata.ImageRequest{ContentType: "movie", RepresentativeFilePath: moviePath}
	images, err := p.GetImages(context.Background(), req)
	if err != nil || len(images) != 0 {
		t.Fatalf("GetImages before adding art = %+v, %v", images, err)
	}

	// The listing is shared while the folder's mtime is unchanged, so a file
	// added without changing it is not seen yet.
	poster := filepath.Join(dir, "MOVIE 007-poster.png")
	writeImageFile(t, poster)
	if err := os.Chtimes(dir, settled, settled); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	images, err = p.GetImages(context.Background(), req)
	if err != nil || len(images) != 0 {
		t.Fatalf("GetImages with an unchanged folder mtime = %+v, %v; want the shared listing", images, err)
	}

	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(dir, later, later); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	images, err = p.GetImages(context.Background(), req)
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	if got := sidecarPath(t, imagesByType(images)[metadata.ImagePoster].URL); got != poster {
		t.Fatalf("poster = %q, want the sidecar added after the first listing", got)
	}
}

// A folder that can be searched but not listed still serves exactly named
// sidecars, as it did before names matched ignoring case.
func TestGetImagesExecuteOnlyFolderUsesExactNames(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root lists any folder")
	}
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Film.mkv")
	writeImageFile(t, moviePath)
	writeImageFile(t, filepath.Join(dir, "poster.jpg"))
	if err := os.Chmod(dir, 0o311); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	p := NewProvider()
	images, err := p.GetImages(context.Background(), metadata.ImageRequest{
		ContentType:               "movie",
		RepresentativeFilePath:    moviePath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	if got := sidecarPath(t, imagesByType(images)[metadata.ImagePoster].URL); got != filepath.Join(dir, "poster.jpg") {
		t.Fatalf("poster = %q, want poster.jpg found by exact name", got)
	}
}
