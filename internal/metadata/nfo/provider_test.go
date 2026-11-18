package nfo

import (
	"context"
	"path/filepath"

	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// A tvshow.nfo sitting next to a movie file must not inject series data into
// a movie item at top priority: GetMetadata carries the same ContentType
// guard Search has.
func TestGetMetadata_TVShowNFOBesideMovieFileDoesNotInject(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Home Movie (2021).mkv")
	writeFile(t, filePath, "video")
	nfoXML := `<tvshow><title>Some Series</title><uniqueid type="tvdb">123</uniqueid></tvshow>`
	writeFile(t, filepath.Join(dir, "tvshow.nfo"), nfoXML)

	p := NewProvider()
	result, err := p.GetMetadata(context.Background(), metadata.MetadataRequest{
		ContentType:               "movie",
		FilePath:                  filePath,
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if result.HasMetadata {
		t.Fatalf("GetMetadata() = %#v, want no metadata (series NFO must not inject into a movie)", result)
	}
}
