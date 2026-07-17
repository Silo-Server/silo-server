package nfo

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// Importing the nfo package must register the provider in the builtin
// registry under its capability id — buildProviders resolves the seeded
// 'nfo' chain entry through this single registration point.
func TestPackageImportRegistersBuiltinProvider(t *testing.T) {
	if !slices.Contains(metadata.BuiltinCapabilityIDs(), "nfo") {
		t.Fatalf("BuiltinCapabilityIDs() = %v, want to contain nfo", metadata.BuiltinCapabilityIDs())
	}
}

// The provider implements the narrow IdentityHintProvider contract: a curated
// <uniqueid> becomes a trusted identity hint before Phase-1 search.
func TestProviderIdentityHintsFromNFOFile(t *testing.T) {
	dir := t.TempDir()
	moviePath := filepath.Join(dir, "Obscure Film (2019).mkv")
	nfoBody := `<?xml version="1.0" encoding="UTF-8"?>
<movie>
  <title>Obscure Film</title>
  <year>2019</year>
  <uniqueid type="tmdb">424242</uniqueid>
  <uniqueid type="imdb">tt0424242</uniqueid>
</movie>`
	if err := os.WriteFile(filepath.Join(dir, "movie.nfo"), []byte(nfoBody), 0o644); err != nil {
		t.Fatalf("write nfo: %v", err)
	}

	var hinter metadata.IdentityHintProvider = NewProvider()
	hints := hinter.IdentityHints(context.Background(), metadata.SearchQuery{
		Title:       "Obscure Film",
		ContentType: "movie",
		FilePath:    moviePath,
	})
	if hints["tmdb"] != "424242" {
		t.Errorf("tmdb hint = %q, want 424242", hints["tmdb"])
	}
	if hints["imdb"] != "tt0424242" {
		t.Errorf("imdb hint = %q, want tt0424242", hints["imdb"])
	}

	// A title-only NFO yields no identity hints (the title flows through the
	// defanged search candidate instead).
	titleOnly := t.TempDir()
	if err := os.WriteFile(filepath.Join(titleOnly, "movie.nfo"), []byte(`<movie><title>Home Video</title></movie>`), 0o644); err != nil {
		t.Fatalf("write nfo: %v", err)
	}
	hints = hinter.IdentityHints(context.Background(), metadata.SearchQuery{
		ContentType: "movie",
		FilePath:    filepath.Join(titleOnly, "Home Video.mkv"),
	})
	if len(hints) != 0 {
		t.Errorf("title-only NFO hints = %v, want none", hints)
	}
}
