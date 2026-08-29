package artworkkey

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

// Fixed synthetic variant bytes. They are not real images on purpose: the
// format must be pinned independently of whatever the encoder produces on this
// machine, so these assertions hold on every platform and Go version.
var (
	goldenOriginal = []byte("silo-golden-original-bytes")
	goldenW500     = []byte("silo-golden-w500")
	goldenW300     = []byte("silo-golden-w300-bytes!")
)

func goldenPosterInput() RevisionInput {
	return RevisionInput{
		ImageType: "poster",
		MediaType: "image/webp",
		Ext:       ".webp",
		Variants: []VariantBytes{
			{Name: "w500", Data: goldenW500},
			{Name: OriginalVariant, Data: goldenOriginal},
			{Name: "w300", Data: goldenW300},
		},
	}
}

// TestPortableRevisionGoldenFormat freezes the wire format: the exact revision
// digest and the exact canonical manifest bytes for a fixed variant set. A
// deliberate format or recipe change must bump PortableRecipeVersion (and,
// for the document, PortableFormatVersion) and update these constants in the
// same commit; anything else failing here is accidental drift that would
// silently re-address every future write.
func TestPortableRevisionGoldenFormat(t *testing.T) {
	revision, err := BuildPortableRevision(goldenPosterInput())
	if err != nil {
		t.Fatalf("BuildPortableRevision: %v", err)
	}

	// Independently re-derive the canonical stream here, by hand, rather than
	// calling the implementation: this is what proves the documented stream is
	// what is actually hashed.
	h := sha256.New()
	h.Write([]byte(PortableRecipeVersion))
	h.Write([]byte{0})
	h.Write([]byte("poster"))
	h.Write([]byte{0})
	h.Write([]byte("image/webp"))
	h.Write([]byte{0})
	for _, variant := range []VariantBytes{
		{Name: OriginalVariant, Data: goldenOriginal},
		{Name: "w300", Data: goldenW300},
		{Name: "w500", Data: goldenW500},
	} {
		h.Write([]byte(variant.Name))
		h.Write([]byte{0})
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(variant.Data)))
		h.Write(size[:])
		h.Write(variant.Data)
	}
	if want := hex.EncodeToString(h.Sum(nil)); revision.Revision != want {
		t.Fatalf("revision = %q, want %q (hand-derived canonical stream)", revision.Revision, want)
	}

	shard := revision.Revision[:2]
	wantDir := "artwork/v1/objects/poster/" + shard + "/" + revision.Revision
	if revision.Directory != wantDir {
		t.Fatalf("directory = %q, want %q", revision.Directory, wantDir)
	}
	if revision.OriginalKey != wantDir+"/original.webp" {
		t.Fatalf("original key = %q", revision.OriginalKey)
	}
	if revision.ManifestKey != wantDir+"/manifest.json" {
		t.Fatalf("manifest key = %q", revision.ManifestKey)
	}
	wantKeys := []string{
		wantDir + "/manifest.json",
		wantDir + "/original.webp",
		wantDir + "/w300.webp",
		wantDir + "/w500.webp",
	}
	if got := revision.ObjectKeys(); !slices.Equal(got, wantKeys) {
		t.Fatalf("object keys = %v, want %v", got, wantKeys)
	}

	wantJSON := fmt.Sprintf(
		`{"format_version":1,"image_type":"poster","media_type":"image/webp","recipe_version":"silo-artwork-recipe-v1","revision":"%s","variants":[`+
			`{"digest":"%s","filename":"original.webp","name":"original","size_bytes":26},`+
			`{"digest":"%s","filename":"w300.webp","name":"w300","size_bytes":23},`+
			`{"digest":"%s","filename":"w500.webp","name":"w500","size_bytes":16}]}`,
		revision.Revision,
		hexDigest(goldenOriginal),
		hexDigest(goldenW300),
		hexDigest(goldenW500),
	)
	if string(revision.ManifestJSON) != wantJSON {
		t.Fatalf("manifest JSON =\n%s\nwant\n%s", revision.ManifestJSON, wantJSON)
	}
	if bytes.HasSuffix(revision.ManifestJSON, []byte("\n")) {
		t.Fatal("canonical manifest must not carry a trailing newline")
	}
}

func TestPortableRevisionIsOrderIndependent(t *testing.T) {
	first, err := BuildPortableRevision(goldenPosterInput())
	if err != nil {
		t.Fatalf("BuildPortableRevision: %v", err)
	}
	shuffled := goldenPosterInput()
	slices.Reverse(shuffled.Variants)
	second, err := BuildPortableRevision(shuffled)
	if err != nil {
		t.Fatalf("BuildPortableRevision: %v", err)
	}
	if first.Revision != second.Revision {
		t.Fatalf("variant order changed the revision: %q vs %q", first.Revision, second.Revision)
	}
	if !bytes.Equal(first.ManifestJSON, second.ManifestJSON) {
		t.Fatal("variant order changed the manifest bytes")
	}
}

func TestPortableRevisionSeparatesEveryRecipeInput(t *testing.T) {
	base, err := BuildPortableRevision(goldenPosterInput())
	if err != nil {
		t.Fatalf("BuildPortableRevision: %v", err)
	}
	mutations := map[string]func(*RevisionInput){
		"image type": func(in *RevisionInput) { in.ImageType = "thumb" },
		"media type": func(in *RevisionInput) { in.MediaType = "image/avif" },
		"variant bytes": func(in *RevisionInput) {
			in.Variants[0].Data = append(slices.Clone(in.Variants[0].Data), '!')
		},
		"variant name": func(in *RevisionInput) { in.Variants[0].Name = "w501" },
		"ladder": func(in *RevisionInput) {
			in.Variants = append(in.Variants, VariantBytes{Name: "w100", Data: []byte("extra")})
		},
	}
	seen := map[string]string{base.Revision: "base"}
	for name, mutate := range mutations {
		in := goldenPosterInput()
		mutate(&in)
		revision, err := BuildPortableRevision(in)
		if err != nil {
			t.Fatalf("BuildPortableRevision after changing %s: %v", name, err)
		}
		if previous, collision := seen[revision.Revision]; collision {
			t.Fatalf("changing %s collided with %s at revision %s", name, previous, revision.Revision)
		}
		seen[revision.Revision] = name
	}
	// A different extension alone does not change the bytes, so it does not
	// change identity — only the filenames the same revision is stored under.
	in := goldenPosterInput()
	in.Ext = ".avif"
	other, err := BuildPortableRevision(in)
	if err != nil {
		t.Fatalf("BuildPortableRevision: %v", err)
	}
	if other.Revision != base.Revision {
		t.Fatalf("extension changed the revision: %q vs %q", other.Revision, base.Revision)
	}
	if !strings.HasSuffix(other.OriginalKey, "/original.avif") {
		t.Fatalf("original key = %q, want an .avif filename", other.OriginalKey)
	}
}

func TestBuildPortableRevisionRejectsBadInput(t *testing.T) {
	tests := map[string]func(*RevisionInput){
		"no image type":  func(in *RevisionInput) { in.ImageType = "" },
		"bad image type": func(in *RevisionInput) { in.ImageType = "Poster/../.." },
		"no media type":  func(in *RevisionInput) { in.MediaType = "" },
		"bad media type": func(in *RevisionInput) { in.MediaType = "image webp" },
		"no ext":         func(in *RevisionInput) { in.Ext = "" },
		"bad ext":        func(in *RevisionInput) { in.Ext = ".we bp" },
		"no variants":    func(in *RevisionInput) { in.Variants = nil },
		"no original": func(in *RevisionInput) {
			in.Variants = []VariantBytes{{Name: "w500", Data: []byte("x")}}
		},
		"duplicate variant": func(in *RevisionInput) {
			in.Variants = append(in.Variants, VariantBytes{Name: OriginalVariant, Data: []byte("x")})
		},
		"empty variant": func(in *RevisionInput) {
			in.Variants = append(in.Variants, VariantBytes{Name: "w100"})
		},
		"bad variant name": func(in *RevisionInput) {
			in.Variants = append(in.Variants, VariantBytes{Name: "500w!", Data: []byte("x")})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			in := goldenPosterInput()
			mutate(&in)
			if _, err := BuildPortableRevision(in); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestParsePortableKey(t *testing.T) {
	revision := strings.Repeat("ab", 32)
	dir := "artwork/v1/objects/poster/ab/" + revision

	info, ok := ParsePortableKey(dir + "/w500.webp")
	if !ok {
		t.Fatal("expected a portable key")
	}
	if info.ImageType != "poster" || info.Revision != revision || info.Variant != "w500" || info.Ext != ".webp" || info.IsManifest {
		t.Fatalf("parsed = %+v", info)
	}
	if info.Directory != dir {
		t.Fatalf("directory = %q, want %q", info.Directory, dir)
	}

	manifest, ok := ParsePortableKey(dir + "/manifest.json")
	if !ok || !manifest.IsManifest || manifest.Variant != "" {
		t.Fatalf("manifest parse = %+v ok=%v", manifest, ok)
	}

	rejected := []string{
		"",
		"tmdb/movies/550/poster/original.webp",
		"tmdb://poster/abc.jpg",
		"artwork/v1/objects/poster/ab/" + revision,                                      // no filename
		"artwork/v1/objects/poster/ab/" + revision + "/sub/w500.webp",                   // too deep
		"artwork/v1/objects/poster/zz/" + revision + "/w500.webp",                       // shard does not match
		"artwork/v1/objects/poster/ab/" + strings.ToUpper(revision)[:64] + "/w500.webp", // not lowercase hex
		"artwork/v1/objects/poster/ab/nothex/w500.webp",
		"artwork/v1/objects/POSTER/ab/" + revision + "/w500.webp",
		"artwork/v1/objects/poster/ab/" + revision + "/w500",
		"artwork/v1/objects/poster/ab/" + revision + "/.hidden.webp",
		"artwork/v2/objects/poster/ab/" + revision + "/w500.webp",
	}
	for _, key := range rejected {
		if IsPortableKey(key) {
			t.Errorf("key %q was accepted as portable", key)
		}
	}
}

func TestImageTypeFromKeyServesBothGrammars(t *testing.T) {
	const backdrop = "backdrop"
	revision := strings.Repeat("cd", 32)
	tests := map[string]string{
		"artwork/v1/objects/" + backdrop + "/cd/" + revision + "/w1280.webp": backdrop,
		"artwork/v1/objects/still/cd/" + revision + "/manifest.json":         "still",
		"tmdb/movies/550/poster/original.abc.webp":                           "poster",
		"tmdb/series/1396/seasons/2/episodes/5/still/original.webp":          "still",
		"local/movies/movie-1/deadbeef/logo/w500.webp":                       "logo",
		"tmdb://poster/abc.jpg":                                              "",
		"original.webp":                                                      "",
		"":                                                                   "",
	}
	for key, want := range tests {
		if got := ImageTypeFromKey(key); got != want {
			t.Errorf("ImageTypeFromKey(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestObjectKeysExpandsBothGrammars(t *testing.T) {
	revision := strings.Repeat("ef", 32)
	dir := "artwork/v1/objects/poster/ef/" + revision

	got := ObjectKeys(dir+"/original.webp", "poster")
	want := []string{
		dir + "/manifest.json",
		dir + "/original.webp",
		dir + "/w780.webp",
		dir + "/w500.webp",
		dir + "/w300.webp",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("portable ObjectKeys = %v, want %v", got, want)
	}

	// The type in the key wins: displacement triggers report the column they
	// fired for, which can disagree with what the revision actually is.
	backdropDir := "artwork/v1/objects/backdrop/ef/" + revision
	got = ObjectKeys(backdropDir+"/original.webp", "poster")
	if len(got) != 5 || !slices.Contains(got, backdropDir+"/w1920.webp") {
		t.Fatalf("ObjectKeys = %v, want the backdrop ladder recorded in the key", got)
	}

	// Legacy keys keep their provider-oriented expansion, with no manifest.
	legacy := ObjectKeys("tmdb/movies/550/poster/original.abc123.webp", "poster")
	wantLegacy := []string{
		"tmdb/movies/550/poster/original.abc123.webp",
		"tmdb/movies/550/poster/w780.abc123.webp",
		"tmdb/movies/550/poster/w500.abc123.webp",
		"tmdb/movies/550/poster/w300.abc123.webp",
	}
	if !slices.Equal(legacy, wantLegacy) {
		t.Fatalf("legacy ObjectKeys = %v, want %v", legacy, wantLegacy)
	}

	if got := ObjectKeys(dir+"/manifest.json", "poster"); got != nil {
		t.Fatalf("ObjectKeys(manifest) = %v, want nil", got)
	}
	if got := ObjectKeys("tmdb://poster/abc.jpg", "poster"); got != nil {
		t.Fatalf("ObjectKeys(plugin ref) = %v, want nil", got)
	}
}

func TestPortableKeyHelpersAndRevisionExtraction(t *testing.T) {
	revision := strings.Repeat("12", 32)
	dir := PortableDirectory("poster", revision)
	if dir != "artwork/v1/objects/poster/12/"+revision {
		t.Fatalf("PortableDirectory = %q", dir)
	}
	if got := PortableKey("poster", revision, "w300", "webp"); got != dir+"/w300.webp" {
		t.Fatalf("PortableKey = %q", got)
	}
	if got := PortableManifestKey("poster", revision); got != dir+"/manifest.json" {
		t.Fatalf("PortableManifestKey = %q", got)
	}
	if got := Revision(dir + "/w300.webp"); got != revision {
		t.Fatalf("Revision(portable) = %q, want %q", got, revision)
	}
	if got := Variant(dir+"/original.webp", "w500"); got != dir+"/w500.webp" {
		t.Fatalf("Variant(portable) = %q", got)
	}
	if got := Directory(dir + "/original.webp"); got != dir+"/" {
		t.Fatalf("Directory(portable) = %q", got)
	}
	if got := PortableDirectory("", revision); got != "" {
		t.Fatalf("PortableDirectory(no type) = %q", got)
	}
	if got := PortableDirectory("poster", "ab"); got != "" {
		t.Fatalf("PortableDirectory(short revision) = %q", got)
	}
}

// storeFixture is an in-memory revision directory.
type storeFixture map[string][]byte

func (s storeFixture) read(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := s[key]
	if !ok {
		return nil, fmt.Errorf("no object at %q", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func writtenRevision(t *testing.T) (*PortableRevision, storeFixture) {
	t.Helper()
	revision, err := BuildPortableRevision(goldenPosterInput())
	if err != nil {
		t.Fatalf("BuildPortableRevision: %v", err)
	}
	store := storeFixture{revision.ManifestKey: revision.ManifestJSON}
	for _, variant := range goldenPosterInput().Variants {
		store[revision.VariantKeys[variant.Name]] = variant.Data
	}
	return revision, store
}

func TestReadManifestValidatesStoredObjects(t *testing.T) {
	revision, store := writtenRevision(t)

	manifest, err := ReadManifest(context.Background(), revision.Directory, store.read)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Revision != revision.Revision {
		t.Fatalf("manifest revision = %q, want %q", manifest.Revision, revision.Revision)
	}
	if !slices.Equal(manifest.ObjectKeys(), revision.ObjectKeys()) {
		t.Fatalf("manifest keys = %v, want %v", manifest.ObjectKeys(), revision.ObjectKeys())
	}
}

func TestReadManifestRejectsCorruptDirectories(t *testing.T) {
	tests := map[string]func(*PortableRevision, storeFixture){
		"missing object": func(r *PortableRevision, s storeFixture) {
			delete(s, r.VariantKeys["w300"])
		},
		"missing manifest": func(r *PortableRevision, s storeFixture) {
			delete(s, r.ManifestKey)
		},
		"tampered bytes": func(r *PortableRevision, s storeFixture) {
			s[r.VariantKeys["w300"]] = []byte("swapped-in-content-of-same-ish-size")
		},
		"truncated object": func(r *PortableRevision, s storeFixture) {
			s[r.VariantKeys[OriginalVariant]] = goldenOriginal[:10]
		},
		"forged manifest": func(r *PortableRevision, s storeFixture) {
			// Re-address the same bytes under a different revision and try to
			// pass that manifest off as this directory's.
			forged := goldenPosterInput()
			forged.Variants[0].Data = []byte("different")
			other, err := BuildPortableRevision(forged)
			if err != nil {
				t.Fatalf("BuildPortableRevision: %v", err)
			}
			s[r.ManifestKey] = other.ManifestJSON
		},
		"digest rewritten to match tampered bytes": func(r *PortableRevision, s storeFixture) {
			swapped := []byte("swapped-in-content")
			s[r.VariantKeys["w300"]] = swapped
			manifest, err := ParseManifest(r.ManifestJSON)
			if err != nil {
				t.Fatalf("ParseManifest: %v", err)
			}
			for i := range manifest.Variants {
				if manifest.Variants[i].Name == "w300" {
					manifest.Variants[i].Digest = hexDigest(swapped)
					manifest.Variants[i].SizeBytes = int64(len(swapped))
				}
			}
			encoded, err := EncodeManifest(manifest)
			if err != nil {
				t.Fatalf("EncodeManifest: %v", err)
			}
			s[r.ManifestKey] = encoded
		},
	}
	for name, corrupt := range tests {
		t.Run(name, func(t *testing.T) {
			revision, store := writtenRevision(t)
			corrupt(revision, store)
			if _, err := ReadManifest(context.Background(), revision.Directory, store.read); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestParseManifestIsStrict(t *testing.T) {
	revision, _ := writtenRevision(t)
	valid := string(revision.ManifestJSON)

	tests := map[string]string{
		"unknown field":     strings.Replace(valid, `{"format_version":1,`, `{"source_url":"https://example.com/p.jpg","format_version":1,`, 1),
		"non-canonical":     strings.Replace(valid, `{"format_version":1,`, `{ "format_version": 1,`, 1),
		"reordered fields":  `{"image_type":"poster","format_version":1}`,
		"trailing content":  valid + `{"format_version":1}`,
		"future version":    strings.Replace(valid, `"format_version":1`, `"format_version":2`, 1),
		"empty":             "",
		"unsorted variants": strings.Replace(valid, `"name":"original"`, `"name":"zzz"`, 1),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest([]byte(data)); err == nil {
				t.Fatalf("expected an error for %s", name)
			}
		})
	}
	if _, err := ParseManifest([]byte(valid)); err != nil {
		t.Fatalf("ParseManifest(valid) = %v", err)
	}
	// A trailing newline is tolerated on read: some copy tools add one.
	if _, err := ParseManifest([]byte(valid + "\n")); err != nil {
		t.Fatalf("ParseManifest(trailing newline) = %v", err)
	}
}

func TestValidateManifestObjectsRequiresAReader(t *testing.T) {
	revision, _ := writtenRevision(t)
	manifest, err := ParseManifest(revision.ManifestJSON)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := ValidateManifestObjects(context.Background(), manifest, nil); err == nil {
		t.Fatal("expected an error without a reader")
	}
	if _, err := ReadManifest(context.Background(), "", nil); err == nil {
		t.Fatal("expected an error without a reader")
	}
}
