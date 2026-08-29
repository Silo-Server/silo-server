package imagecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/h2non/bimg"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

var updateGolden = flag.Bool("update", false, "rewrite the portable-format golden file from this machine's encoder")

const goldenPath = "testdata/portable_golden.json"

type goldenFile struct {
	// Note explains the file to whoever hits a failure.
	Note string `json:"note"`
	// RecipeVersion is the recipe these revisions were produced under.
	RecipeVersion string `json:"recipe_version"`
	// Encoder records the encoder build that produced the golden. It is
	// reported on failure but never compared: a different libvips is an
	// explanation, not a verdict.
	Encoder string        `json:"encoder"`
	Entries []goldenEntry `json:"entries"`
}

type goldenEntry struct {
	Fixture      string          `json:"fixture"`
	ImageType    string          `json:"image_type"`
	SourceDigest string          `json:"source_digest"`
	Revision     string          `json:"revision"`
	OriginalKey  string          `json:"original_key"`
	ObjectKeys   []string        `json:"object_keys"`
	Manifest     json.RawMessage `json:"manifest"`
}

// TestPortableFormatGolden runs real fixture images through the real pipeline
// and pins the resulting revisions, keys, and manifests.
//
// This is a single-platform drift gate, not a portability guarantee. Encoded
// output depends on the libvips build, so the spec treats cross-architecture
// byte-identity as best-effort: a differing encoder yields a different,
// non-colliding revision — never corruption, never an overwrite. What this test
// catches is an *accidental* change to the recipe or the format on the
// platform it runs on, which would silently re-address every future write.
//
// A deliberate change bumps artworkkey.PortableRecipeVersion, then:
//
//	go test ./internal/imagecache -run TestPortableFormatGolden -update
func TestPortableFormatGolden(t *testing.T) {
	fixtures := []struct {
		file      string
		imageType metadata.ImageType
	}{
		{"poster.png", metadata.ImagePoster},
		{"backdrop.png", metadata.ImageBackdrop},
		{"logo.png", metadata.ImageLogo},
	}

	got := goldenFile{
		Note: "Pinned artwork/v1 revisions for the fixture images in testdata/fixtures. " +
			"Regenerate with: go test ./internal/imagecache -run TestPortableFormatGolden -update",
		RecipeVersion: artworkkey.PortableRecipeVersion,
		Encoder:       fmt.Sprintf("bimg %s / libvips %s", bimg.Version, bimg.VipsVersion),
	}
	for _, fixture := range fixtures {
		data, err := os.ReadFile(filepath.Join("testdata", "fixtures", fixture.file))
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		store := &mockStore{}
		cacher := newWithHTTPClient(store, nil)
		result, err := cacher.CacheBytes(context.Background(), data, CacheRequest{
			ProviderID:  "golden",
			ContentType: "movies",
			ContentID:   fixture.file,
			ImageType:   fixture.imageType,
		})
		if err != nil {
			t.Fatalf("CacheBytes %s: %v", fixture.file, err)
		}
		sourceDigest := sha256.Sum256(data)
		got.Entries = append(got.Entries, goldenEntry{
			Fixture:      fixture.file,
			ImageType:    metadata.ImageTypeToString(fixture.imageType),
			SourceDigest: hex.EncodeToString(sourceDigest[:]),
			Revision:     result.Revision,
			OriginalKey:  result.OriginalPath,
			ObjectKeys:   artworkkey.ObjectKeys(result.OriginalPath, metadata.ImageTypeToString(fixture.imageType)),
			Manifest:     json.RawMessage(store.objectData(result.ManifestPath)),
		})
	}

	if *updateGolden {
		encoded, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatalf("encode golden: %v", err)
		}
		if err := os.WriteFile(goldenPath, append(encoded, '\n'), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("rewrote %s using %s", goldenPath, got.Encoder)
		return
	}

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (regenerate with -update): %v", err)
	}
	var want goldenFile
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode golden: %v", err)
	}

	if want.RecipeVersion != got.RecipeVersion {
		t.Fatalf("recipe version = %q, golden was recorded under %q; regenerate the golden in the same commit that bumps the recipe",
			got.RecipeVersion, want.RecipeVersion)
	}
	if len(want.Entries) != len(got.Entries) {
		t.Fatalf("golden has %d entries, produced %d; regenerate with -update", len(want.Entries), len(got.Entries))
	}
	for i, wantEntry := range want.Entries {
		gotEntry := got.Entries[i]
		if wantEntry.Fixture != gotEntry.Fixture {
			t.Fatalf("entry %d is %q, golden expects %q", i, gotEntry.Fixture, wantEntry.Fixture)
		}
		if wantEntry.SourceDigest != gotEntry.SourceDigest {
			t.Fatalf("%s: fixture bytes changed (%s, golden %s); the input, not the encoder, drifted",
				gotEntry.Fixture, gotEntry.SourceDigest, wantEntry.SourceDigest)
		}
		if wantEntry.Revision != gotEntry.Revision {
			t.Fatalf("%s: revision = %s, golden = %s.\n"+
				"Either the recipe or format changed deliberately — bump artworkkey.PortableRecipeVersion and rerun with -update — "+
				"or this machine's encoder differs from the one that recorded the golden (%s vs %s here), which is expected drift, not corruption.",
				gotEntry.Fixture, gotEntry.Revision, wantEntry.Revision, want.Encoder, got.Encoder)
		}
		if wantEntry.OriginalKey != gotEntry.OriginalKey {
			t.Errorf("%s: original key = %s, golden = %s", gotEntry.Fixture, gotEntry.OriginalKey, wantEntry.OriginalKey)
		}
		if !slices.Equal(wantEntry.ObjectKeys, gotEntry.ObjectKeys) {
			t.Errorf("%s: object keys = %v, golden = %v", gotEntry.Fixture, gotEntry.ObjectKeys, wantEntry.ObjectKeys)
		}
		var compacted bytes.Buffer
		if err := json.Compact(&compacted, wantEntry.Manifest); err != nil {
			t.Fatalf("%s: compact golden manifest: %v", gotEntry.Fixture, err)
		}
		if !bytes.Equal(compacted.Bytes(), gotEntry.Manifest) {
			t.Errorf("%s: manifest =\n%s\ngolden =\n%s", gotEntry.Fixture, gotEntry.Manifest, compacted.Bytes())
		}
	}
}
