package artworkkey

import (
	"slices"
	"strings"
	"testing"
)

// TestUploadImageTypesAreValidPortableTypes keeps upload artwork addressable by
// the one grammar: a type that cannot appear in a portable key would be stored
// somewhere no reader parses.
func TestUploadImageTypesAreValidPortableTypes(t *testing.T) {
	revision := strings.Repeat("cd", 32)
	for _, imageType := range UploadImageTypes() {
		if err := validatePortableImageType(imageType); err != nil {
			t.Fatalf("upload image type %q is not a valid portable type: %v", imageType, err)
		}
		key := PortableKey(imageType, revision, OriginalVariant, ".webp")
		info, ok := ParsePortableKey(key)
		if !ok {
			t.Fatalf("portable key %q for %q does not round-trip", key, imageType)
		}
		if info.ImageType != imageType {
			t.Fatalf("parsed image type = %q, want %q", info.ImageType, imageType)
		}
		if !strings.HasPrefix(key, PortableObjectsPrefix+"/") {
			t.Fatalf("upload key %q is outside the portable objects namespace", key)
		}
		if !IsUploadImageType(imageType) {
			t.Fatalf("IsUploadImageType(%q) = false", imageType)
		}
	}
}

// TestUploadImageTypesAreDistinctFromCatalogTypes guards the namespace split:
// an upload sharing a type with catalog artwork would share revisions with it,
// and deleting one owner's image could take the other's.
func TestUploadImageTypesAreDistinctFromCatalogTypes(t *testing.T) {
	for _, uploadType := range UploadImageTypes() {
		if slices.Contains(ImageTypes(), uploadType) {
			t.Fatalf("upload type %q is also a catalog artwork type", uploadType)
		}
	}
	for _, catalogType := range ImageTypes() {
		if IsUploadImageType(catalogType) {
			t.Fatalf("catalog type %q reports as an upload type", catalogType)
		}
	}
}

// TestUploadVariantLadders pins each upload ladder. Garbage collection expands
// a trigger-queued revision through VariantNames, so a ladder narrower than
// what the upload path writes leaves orphans behind.
func TestUploadVariantLadders(t *testing.T) {
	tests := []struct {
		imageType string
		want      []string
	}{
		{imageType: ImageTypeLibraryPoster, want: []string{"original", "w500", "w300"}},
		{imageType: ImageTypeCollectionPoster, want: []string{"original", "w500", "w300"}},
		{imageType: ImageTypeCollectionBackdrop, want: []string{"original", "w1280", "w300"}},
		{imageType: ImageTypeAvatar, want: []string{"original", "w256"}},
	}
	for _, tt := range tests {
		t.Run(tt.imageType, func(t *testing.T) {
			got := VariantNames(tt.imageType)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("VariantNames(%q) = %#v, want %#v", tt.imageType, got, tt.want)
			}
		})
	}
}

// TestUploadObjectKeysExpandTheWholeRevision is what the collector relies on
// for rows queued by a displacement trigger, which carry no manifest.
func TestUploadObjectKeysExpandTheWholeRevision(t *testing.T) {
	revision := strings.Repeat("ef", 32)
	for _, imageType := range UploadImageTypes() {
		original := PortableKey(imageType, revision, OriginalVariant, ".webp")
		keys := ObjectKeys(original, imageType)

		want := len(VariantNames(imageType)) + 1 // ladder plus manifest
		if len(keys) != want {
			t.Fatalf("ObjectKeys(%q) = %#v, want %d keys", imageType, keys, want)
		}
		if !slices.Contains(keys, PortableManifestKey(imageType, revision)) {
			t.Fatalf("ObjectKeys(%q) omits the manifest: %#v", imageType, keys)
		}
		if !slices.Contains(keys, original) {
			t.Fatalf("ObjectKeys(%q) omits the original: %#v", imageType, keys)
		}
		// The key the collector records must stay scheme-free, or the revision
		// tracker refuses it.
		for _, key := range keys {
			if strings.Contains(key, "://") {
				t.Fatalf("object key %q carries a scheme", key)
			}
		}
	}
}

func TestCollectionImageType(t *testing.T) {
	tests := []struct {
		slot string
		want string
		ok   bool
	}{
		{slot: "poster", want: ImageTypeCollectionPoster, ok: true},
		{slot: "Backdrop", want: ImageTypeCollectionBackdrop, ok: true},
		{slot: " poster ", want: ImageTypeCollectionPoster, ok: true},
		{slot: "logo", ok: false},
		{slot: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := CollectionImageType(tt.slot)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("CollectionImageType(%q) = (%q, %v), want (%q, %v)", tt.slot, got, ok, tt.want, tt.ok)
		}
	}
}

func TestAuxiliaryArtworkKeyClassification(t *testing.T) {
	if !IsBrandingKey("branding/wordmark/revision.webp") || IsBrandingKey("artwork/v1/objects/poster/aa/revision/original.webp") {
		t.Fatal("branding key classification is incorrect")
	}
	for _, key := range []string{
		"library-posters/7.jpg",
		"collection-images/admin/poster/original.webp",
		"user-collection-images/personal/poster/original.webp",
		"profile-avatars/12/main/original.webp",
	} {
		if !IsLegacyUploadKey(key) {
			t.Fatalf("legacy upload key %q was not recognized", key)
		}
	}
	if IsLegacyUploadKey("branding/favicon/revision.ico") {
		t.Fatal("branding key was classified as a legacy upload")
	}
}
