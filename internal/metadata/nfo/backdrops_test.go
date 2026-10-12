package nfo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// sidecarNamesOfType returns the discovered sources of one image type as
// paths relative to dir, in the order discovery returned them.
func sidecarNamesOfType(t *testing.T, dir string, images []metadata.RemoteImage, imageType metadata.ImageType) []string {
	t.Helper()
	var names []string
	for _, img := range images {
		if img.Type != imageType {
			continue
		}
		rel, err := filepath.Rel(dir, sidecarPath(t, img.URL))
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, filepath.ToSlash(rel))
	}
	return names
}

func discoverImages(t *testing.T, contentType, mediaPath, dir string) []metadata.RemoteImage {
	t.Helper()
	images, err := NewProvider().GetImages(context.Background(), metadata.ImageRequest{
		ContentType:               contentType,
		RepresentativeFilePath:    mediaPath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	return images
}

func TestGetImagesExtraArtworkNames(t *testing.T) {
	cases := []struct {
		contentType string
		files       []string
		imageType   metadata.ImageType
		want        string // "" when nothing of imageType is found
	}{
		{"movie", []string{"banner.jpg"}, metadata.ImageBanner, "banner.jpg"},
		{"movie", []string{"Media-banner.png", "banner.jpg"}, metadata.ImageBanner, "Media-banner.png"},
		{"series", []string{"banner.jpg"}, metadata.ImageBanner, "banner.jpg"},
		{"movie", []string{"landscape.jpg"}, metadata.ImageThumb, "landscape.jpg"},
		{"movie", []string{"thumb.jpg"}, metadata.ImageThumb, "thumb.jpg"},
		{"movie", []string{"thumb.jpg", "landscape.jpg"}, metadata.ImageThumb, "landscape.jpg"},
		{"movie", []string{"landscape.jpg", "Media-thumb.jpg"}, metadata.ImageThumb, "Media-thumb.jpg"},
		{"series", []string{"Landscape.PNG"}, metadata.ImageThumb, "Landscape.PNG"},
		{"movie", []string{"clearart.png"}, metadata.ImageClearArt, "clearart.png"},
		{"series", []string{"CLEARART.PNG"}, metadata.ImageClearArt, "CLEARART.PNG"},
		// art.jpg is a backdrop, never clear art.
		{"movie", []string{"art.jpg"}, metadata.ImageClearArt, ""},
		{"movie", []string{"disc.png"}, metadata.ImageDisc, "disc.png"},
		{"movie", []string{"cdart.png"}, metadata.ImageDisc, "cdart.png"},
		{"movie", []string{"discart.png"}, metadata.ImageDisc, "discart.png"},
		{"movie", []string{"discart.png", "cdart.png", "disc.png"}, metadata.ImageDisc, "disc.png"},
		{"movie", []string{"discart.png", "cdart.png"}, metadata.ImageDisc, "cdart.png"},
		{"movie", []string{"disc.png", "Media-discart.png"}, metadata.ImageDisc, "Media-discart.png"},
		// A series has no disc.
		{"series", []string{"disc.png"}, metadata.ImageDisc, ""},
	}
	for _, tc := range cases {
		t.Run(tc.contentType+"/"+strings.Join(tc.files, "+"), func(t *testing.T) {
			dir := t.TempDir()
			mediaPath := filepath.Join(dir, "Media.mkv")
			writeImageFile(t, mediaPath)
			for _, file := range tc.files {
				writeImageFile(t, filepath.Join(dir, file))
			}
			got := sidecarNamesOfType(t, dir, discoverImages(t, tc.contentType, mediaPath, dir), tc.imageType)
			var want []string
			if tc.want != "" {
				want = []string{tc.want}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("image type %d = %q, want %q", tc.imageType, got, want)
			}
		})
	}
}

func TestGetImagesBackdropsInDisplayOrder(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Film (2020).mkv")
	writeImageFile(t, mediaPath)
	for _, file := range []string{
		"Film (2020)-fanart.jpg",
		"fanart.jpg", "backdrop.jpg", "art.jpg",
		// fanart-3 and fanart-4 are missing: two gaps, so fanart-5 counts.
		// 6, 7 and 8 are missing: three gaps end the family before fanart-9.
		"fanart-1.jpg", "fanart2.jpg", "fanart-5.jpg", "fanart-9.jpg",
		"background-1.jpg", "art-1.png", "backdrop1.jpg",
		"extrafanart/b.jpg", "extrafanart/A.png", "extrafanart/notes.txt",
		// Jellyfin's backdrops/ folder holds theme videos, not images.
		"backdrops/theme.jpg",
		"clearart.png",
	} {
		path := filepath.Join(dir, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeImageFile(t, path)
	}

	got := sidecarNamesOfType(t, dir, discoverImages(t, "movie", mediaPath, dir), metadata.ImageBackdrop)
	want := []string{
		"Film (2020)-fanart.jpg",
		"fanart.jpg", "backdrop.jpg", "art.jpg",
		"fanart-1.jpg", "fanart2.jpg", "fanart-5.jpg",
		"background-1.jpg", "art-1.png", "backdrop1.jpg",
		"extrafanart/A.png", "extrafanart/b.jpg",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("backdrops =\n%q\nwant\n%q", got, want)
	}
}

func TestGetImagesBackdropsMatchFolderNamesIgnoringCase(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Media.mkv")
	writeImageFile(t, mediaPath)
	for _, file := range []string{"FanArt.JPG", "Fanart-1.jpg", "ExtraFanart/x.JPG"} {
		path := filepath.Join(dir, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeImageFile(t, path)
	}
	got := sidecarNamesOfType(t, dir, discoverImages(t, "series", mediaPath, dir), metadata.ImageBackdrop)
	want := []string{"FanArt.JPG", "Fanart-1.jpg", "ExtraFanart/x.JPG"}
	if !slices.Equal(got, want) {
		t.Fatalf("backdrops = %q, want %q", got, want)
	}
}

func TestGetImagesBackdropsAreCapped(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Media.mkv")
	writeImageFile(t, mediaPath)
	writeImageFile(t, filepath.Join(dir, "fanart.jpg"))
	if err := os.Mkdir(filepath.Join(dir, "extrafanart"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		writeImageFile(t, filepath.Join(dir, "extrafanart", fmt.Sprintf("%02d.jpg", i)))
	}
	got := sidecarNamesOfType(t, dir, discoverImages(t, "movie", mediaPath, dir), metadata.ImageBackdrop)
	if len(got) != metadata.MaxItemBackdrops {
		t.Fatalf("got %d backdrops, want the cap of %d: %q", len(got), metadata.MaxItemBackdrops, got)
	}
	if got[0] != "fanart.jpg" || got[len(got)-1] != "extrafanart/18.jpg" {
		t.Fatalf("backdrops = %q, want fanart.jpg then extrafanart/00..18", got)
	}
}

// extraFanart stops once it has as many usable images as the backdrops still
// have room for, so a large folder is not checked file by file.
func TestExtraFanartStopsAtLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "extrafanart"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"c.jpg", "a.jpg", "b.jpg"} {
		writeImageFile(t, filepath.Join(dir, "extrafanart", name))
	}
	got := newSidecarFinder(context.Background()).extraFanart(dir, 2)
	if len(got) != 2 || !strings.Contains(got[0], "/extrafanart/a.jpg") || !strings.Contains(got[1], "/extrafanart/b.jpg") {
		t.Fatalf("extraFanart = %q, want a.jpg and b.jpg", got)
	}
}

// A flat folder of several movies offers no directory-level search path, so
// only the movie's own "-fanart" sidecar is a backdrop; the folder's numbered
// and extrafanart images belong to no single movie.
func TestGetImagesFlatFolderBackdropsUseBasenameOnly(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Film.mkv")
	writeImageFile(t, mediaPath)
	writeImageFile(t, filepath.Join(dir, "Film-fanart.jpg"))
	writeImageFile(t, filepath.Join(dir, "fanart-1.jpg"))
	images, err := NewProvider().GetImages(context.Background(), metadata.ImageRequest{
		ContentType:            "movie",
		RepresentativeFilePath: mediaPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := sidecarNamesOfType(t, dir, images, metadata.ImageBackdrop)
	if want := []string{"Film-fanart.jpg"}; !slices.Equal(got, want) {
		t.Fatalf("backdrops = %q, want %q", got, want)
	}
}
