package artworkkey

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestAdoptionIndexCanonicalRoundTrip(t *testing.T) {
	fp, ok := SourceFingerprint("plugin", "tmdb://poster/42")
	if !ok {
		t.Fatal("plugin reference was not fingerprinted")
	}
	digest := sha256.Sum256([]byte("manifest"))
	index := AdoptionIndex{Fingerprint: fp, ImageType: "poster", ManifestDigest: hex.EncodeToString(digest[:]), RecipeVersion: PortableRecipeVersion, TargetRevision: fp}
	data, err := EncodeAdoptionIndex(index)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAdoptionIndex(data)
	if err != nil || parsed != index {
		t.Fatalf("round trip = %#v, %v", parsed, err)
	}
	want := PortableIndexPrefix + "/" + fp[:2] + "/" + fp + "/poster/" + PortableRecipeVersion + ".json"
	if got := AdoptionIndexKey(fp, "poster", PortableRecipeVersion); got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
}

func TestSourceFingerprintRejectsResolvedHTTP(t *testing.T) {
	for _, source := range []string{"https://cdn.example/poster.jpg", "https://cdn.example/poster.jpg?token=secret"} {
		if _, ok := SourceFingerprint("provider", source); ok {
			t.Fatalf("fingerprinted transport URL %q", source)
		}
	}
	first, _ := ByteSourceFingerprint("upload", []byte("same"))
	second, _ := ByteSourceFingerprint("sidecar", []byte("same"))
	if first == second {
		t.Fatal("identity class did not domain-separate byte fingerprint")
	}
}

func TestGeneratedSourceFingerprintIncludesRecipeAndOrderedInputs(t *testing.T) {
	first, ok := GeneratedSourceFingerprint("collage-v2", []string{"poster-a", "poster-b"})
	if !ok {
		t.Fatal("generated fingerprint was omitted")
	}
	same, _ := GeneratedSourceFingerprint("collage-v2", []string{"poster-a", "poster-b"})
	reordered, _ := GeneratedSourceFingerprint("collage-v2", []string{"poster-b", "poster-a"})
	changedRecipe, _ := GeneratedSourceFingerprint("collage-v3", []string{"poster-a", "poster-b"})
	if first != same || first == reordered || first == changedRecipe {
		t.Fatalf("generated fingerprints: first=%q same=%q reordered=%q changed=%q", first, same, reordered, changedRecipe)
	}
	if _, ok := GeneratedSourceFingerprint("collage-v2", nil); ok {
		t.Fatal("generated fingerprint accepted missing input revisions")
	}
}
