package nfo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// artSourcePath returns the file path a fingerprinted file:// source names
// (see sidecarPath), or any other source unchanged.
func artSourcePath(t *testing.T, source string) string {
	t.Helper()
	if !strings.HasPrefix(source, "file://") {
		return source
	}
	return sidecarPath(t, source)
}

func TestArtResolverSource(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	movieDir := filepath.Join(root, "Movies", "Film (2001)")
	writeTestFile(t, filepath.Join(movieDir, "art", "poster.jpg"), "img")
	writeTestFile(t, filepath.Join(movieDir, ".actors", "Lead.png"), "img")
	writeTestFile(t, filepath.Join(movieDir, "notes.txt"), "text")
	writeTestFile(t, filepath.Join(movieDir, "empty.jpg"), "")
	writeTestFile(t, filepath.Join(root, "Movies", "outside.jpg"), "img")
	if err := os.Symlink(filepath.Join(root, "Movies", "outside.jpg"), filepath.Join(movieDir, "linked.jpg")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	// A symlinked directory inside the movie folder that leads out of it.
	writeTestFile(t, filepath.Join(root, "Elsewhere", "poster.jpg"), "img")
	if err := os.Symlink(filepath.Join(root, "Elsewhere"), filepath.Join(movieDir, "escape")); err != nil {
		t.Fatalf("Symlink dir: %v", err)
	}
	resolver := newArtResolver(newSidecarFinder(context.Background()), filepath.Join(movieDir, "movie.nfo"), []string{movieDir})

	for ref, want := range map[string]string{
		"art/poster.jpg":     filepath.Join(movieDir, "art", "poster.jpg"),
		"./.actors/Lead.png": filepath.Join(movieDir, ".actors", "Lead.png"),
		filepath.Join(movieDir, ".actors", "Lead.png"): filepath.Join(movieDir, ".actors", "Lead.png"),
		"../outside.jpg": "", // outside the item's folders
		filepath.Join(root, "Movies", "outside.jpg"): "",
		"linked.jpg":           "", // symlinked leaf
		"missing.jpg":          "",
		"notes.txt":            "", // not an image
		"empty.jpg":            "",
		`C:\Movies\poster.jpg`: "",
		"https://image.example.org/t/p/original/a.jpg":        "nfo+https://image.example.org/t/p/original/a.jpg",
		"HTTP://image.example.org/a.jpg":                      "nfo+http://image.example.org/a.jpg",
		"https://user:secret@example.org/a.jpg":               "",
		"http://192.168.1.10/a.jpg":                           "",
		"http://127.0.0.1/a.jpg":                              "",
		"http://[::1]/a.jpg":                                  "",
		"http://localhost:8080/a.jpg":                         "",
		"http://10.0.0.5:32400/photo/a.jpg":                   "",
		"http://93.184.216.34/a.jpg":                          "nfo+http://93.184.216.34/a.jpg",
		"smb://nas/share/poster.jpg":                          "",
		"image://http%3a%2f%2fexample.org%2fa.jpg/":           "",
		"https://" + strings.Repeat("a", 3000) + ".org/a.jpg": "",
		"   ": "",
	} {
		if got := resolver.source(ref); artSourcePath(t, got) != want {
			t.Errorf("source(%q) = %q, want %q", ref, got, want)
		}
	}
}

// NFO-named artwork fills the slots no sidecar file fills; a sidecar file of
// the same kind wins over it.
func TestGetImagesAddsNFOArtForEmptySlots(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "Film (2001)")
	moviePath := filepath.Join(dir, "Film.mkv")
	writeTestFile(t, moviePath, "video")
	writeTestFile(t, filepath.Join(dir, "poster.jpg"), "sidecar")
	writeTestFile(t, filepath.Join(dir, "extra", "backdrop-local.jpg"), "img")
	writeTestFile(t, filepath.Join(dir, "Film.nfo"), `<movie>
  <title>Film</title>
  <thumb aspect="poster">https://img.example.org/poster.jpg</thumb>
  <thumb aspect="clearlogo">http://192.168.1.2/logo.png</thumb>
  <thumb aspect="clearlogo">https://img.example.org/logo.png</thumb>
  <thumb aspect="banner">banner.jpg</thumb>
  <fanart><thumb>missing.jpg</thumb><thumb>extra/backdrop-local.jpg</thumb></fanart>
  <thumb aspect="poster" season="1">https://img.example.org/season1.jpg</thumb>
</movie>`)

	images, err := NewProvider().GetImages(context.Background(), metadata.ImageRequest{
		ContentType:               "movie",
		RepresentativeFilePath:    moviePath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	got := make(map[metadata.ImageType]string)
	for _, img := range images {
		if _, dup := got[img.Type]; dup {
			t.Errorf("two images of type %v: %+v", img.Type, images)
		}
		if img.ProviderID != "nfo" {
			t.Errorf("image %+v, want the nfo provider", img)
		}
		got[img.Type] = img.URL
	}
	// A URL comes back as a local-metadata source, which waits in the source
	// column for the image cache instead of being served.
	expected := map[metadata.ImageType]string{
		metadata.ImagePoster:   filepath.Join(dir, "poster.jpg"),
		metadata.ImageBackdrop: filepath.Join(dir, "extra", "backdrop-local.jpg"),
		metadata.ImageLogo:     "nfo+https://img.example.org/logo.png",
	}
	if len(got) != len(expected) {
		t.Fatalf("images = %+v, want %+v", got, expected)
	}
	for imageType, w := range expected {
		if artSourcePath(t, got[imageType]) != w {
			t.Errorf("type %v = %+v, want %+v", imageType, got[imageType], w)
		}
	}
}

// An NFO's artwork applies to a series too, from its tvshow.nfo, and a
// season gallery request gets none of the item art.
func TestGetImagesNFOArtForSeriesAndSeasonGallery(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "Show")
	episode := filepath.Join(root, "Season 01", "Show S01E01.mkv")
	writeTestFile(t, episode, "video")
	writeTestFile(t, filepath.Join(root, "tvshow.nfo"),
		`<tvshow><title>Show</title><thumb>folder-art.png</thumb></tvshow>`)
	writeTestFile(t, filepath.Join(root, "folder-art.png"), "img")

	req := metadata.ImageRequest{
		ContentType:               "series",
		RepresentativeFilePath:    episode,
		AllGroupFilePaths:         []string{episode},
		PrimarySidecarSearchPaths: []string{root},
	}
	images, err := NewProvider().GetImages(context.Background(), req)
	if err != nil {
		t.Fatalf("GetImages: %v", err)
	}
	if len(images) != 1 || images[0].Type != metadata.ImagePoster ||
		artSourcePath(t, images[0].URL) != filepath.Join(root, "folder-art.png") {
		t.Fatalf("images = %+v, want the tvshow.nfo poster", images)
	}

	season := 1
	req.SeasonNumber = &season
	images, err = NewProvider().GetImages(context.Background(), req)
	if err != nil {
		t.Fatalf("GetImages(season): %v", err)
	}
	if len(images) != 0 {
		t.Errorf("season gallery got NFO item art %+v", images)
	}
}

// An actor's <thumb> comes back resolved: a local file as a file:// source,
// a URL as is, and nothing for a reference that names no usable image.
func TestGetMetadataResolvesActorPhotos(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "Film (2001)")
	moviePath := filepath.Join(dir, "Film.mkv")
	writeTestFile(t, moviePath, "video")
	writeTestFile(t, filepath.Join(dir, ".actors", "Local_Lead.jpg"), "img")
	writeTestFile(t, filepath.Join(dir, "movie.nfo"), `<movie>
  <title>Film</title>
  <actor><name>Local Lead</name><thumb>.actors/Local_Lead.jpg</thumb><tmdbid>500</tmdbid></actor>
  <actor><name>Remote</name><thumb>https://image.example.org/t/p/original/r.jpg</thumb></actor>
  <actor><name>Lan</name><thumb>http://192.168.1.20/photo.jpg</thumb></actor>
  <actor><name>Missing</name><thumb>.actors/Missing.jpg</thumb></actor>
  <actor><name>Escape</name><thumb>../../etc/passwd.jpg</thumb></actor>
  <actor><name>None</name></actor>
</movie>`)

	// The parse is cached across calls; resolving one call's photos must
	// not change what the next call resolves.
	provider := NewProvider()
	req := metadata.MetadataRequest{
		ContentType:               "movie",
		FilePath:                  moviePath,
		PrimarySidecarSearchPaths: []string{dir},
	}
	if _, err := provider.GetMetadata(context.Background(), req); err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	result, err := provider.GetMetadata(context.Background(), req)
	if err != nil {
		t.Fatalf("GetMetadata (cached): %v", err)
	}
	want := map[string]string{
		"Local Lead": filepath.Join(dir, ".actors", "Local_Lead.jpg"),
		"Remote":     "nfo+https://image.example.org/t/p/original/r.jpg",
		"Lan":        "",
		"Missing":    "",
		"Escape":     "",
		"None":       "",
	}
	for _, person := range result.People {
		if artSourcePath(t, person.LocalPhotoSource) != want[person.Name] {
			t.Errorf("%s LocalPhotoSource = %q, want %q", person.Name, person.LocalPhotoSource, want[person.Name])
		}
		if person.PhotoPath != "" || person.PhotoSourcePath != "" {
			t.Errorf("%s carries a served photo %q/%q; the NFO's goes in LocalPhotoSource", person.Name, person.PhotoPath, person.PhotoSourcePath)
		}
	}
	if result.People[0].TmdbID != "500" {
		t.Errorf("Local Lead TmdbID = %q, want 500", result.People[0].TmdbID)
	}
}

func TestGetMetadataMapsShowFields(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "tvshow.nfo"), `<tvshow>
  <title>Show</title>
  <sorttitle>Show, The</sorttitle>
  <status>Returning Series</status>
  <studio>Network One / Network Two</studio>
  <originallanguage>ja</originallanguage>
  <trailer>plugin://plugin.video.youtube/?action=play_video&amp;videoid=dQw4w9WgXcQ</trailer>
  <ratings><rating name="tvdb" default="true"><value>8.4</value><votes>1200</votes></rating></ratings>
  <rating>8.4</rating>
</tvshow>`)

	result, err := NewProvider().GetMetadata(context.Background(), metadata.MetadataRequest{
		ContentType:               "series",
		PrimarySidecarSearchPaths: []string{root},
	})
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if result.SortTitle != "Show, The" || result.ShowStatus != "returning" || result.OriginalLanguage != "ja" {
		t.Errorf("sort/status/language = %q/%q/%q", result.SortTitle, result.ShowStatus, result.OriginalLanguage)
	}
	if len(result.Networks) != 2 || result.Networks[1] != "Network Two" || len(result.Studios) != 2 {
		t.Errorf("Networks/Studios = %#v / %#v", result.Networks, result.Studios)
	}
	if len(result.Videos) != 1 || result.Videos[0].SiteKey != "dQw4w9WgXcQ" {
		t.Errorf("Videos = %#v", result.Videos)
	}
	if result.Ratings.IMDB != 0 {
		t.Errorf("Ratings.IMDB = %v, want 0: the default rating is TVDB's", result.Ratings.IMDB)
	}
	if got := result.RatingSources["tvdb"]; got.Score != 84 || got.Votes != 1200 || got.Provider != "nfo" {
		t.Errorf("RatingSources[tvdb] = %#v", got)
	}
}
