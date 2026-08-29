package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
)

// stubArtworkURLs resolves any valid logical key to a recognizable URL.
type stubArtworkURLs struct {
	err  error
	seen []string
}

type stubTargetArtworkURLs struct {
	stubArtworkURLs
	target  artworkurl.Target
	path    string
	variant string
}

func (s *stubTargetArtworkURLs) ResolveTargetURL(_ context.Context, target artworkurl.Target, variant string) (artworkstore.ResolvedURL, error) {
	s.target = target
	s.path = target.Reference
	s.variant = variant
	return artworkstore.ResolvedURL{URL: "https://target.example/" + variant}, nil
}

func (s *stubArtworkURLs) ResolveArtworkURL(_ context.Context, key string) (artworkstore.ResolvedURL, error) {
	s.seen = append(s.seen, key)
	if s.err != nil {
		return artworkstore.ResolvedURL{}, s.err
	}
	if err := artworkstore.ValidateKey(key); err != nil {
		return artworkstore.ResolvedURL{}, err
	}
	return artworkstore.ResolvedURL{URL: "https://cdn.example/" + key}, nil
}

// stubLegacyAvatarStore presigns from the legacy per-profile avatar bucket.
type stubLegacyAvatarStore struct {
	seen []string
	err  error
}

func (s *stubLegacyAvatarStore) DeleteObject(context.Context, string, string) error { return nil }

func (s *stubLegacyAvatarStore) ListObjects(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (s *stubLegacyAvatarStore) PresignGetURL(_ context.Context, _, key string, _ time.Duration) (string, error) {
	s.seen = append(s.seen, key)
	if s.err != nil {
		return "", s.err
	}
	return "https://legacy.example/" + key, nil
}

func (s *stubLegacyAvatarStore) Bucket() string { return "private" }

func portableUploadKey(t *testing.T, imageType, variant string) string {
	t.Helper()
	revision := strings.Repeat("ab", 32)
	key := artworkkey.PortableKey(imageType, revision, variant, ".webp")
	if key == "" {
		t.Fatalf("could not build a portable key for %q/%q", imageType, variant)
	}
	return key
}

func TestResolveStoredImageURL(t *testing.T) {
	key := portableUploadKey(t, artworkkey.ImageTypeLibraryPoster, artworkkey.OriginalVariant)
	resolver := &stubArtworkURLs{}

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "empty", path: "", want: ""},
		{name: "blank", path: "   ", want: ""},
		{name: "absolute url passes through", path: "https://image.tmdb.org/x.jpg", want: "https://image.tmdb.org/x.jpg"},
		{name: "bundled asset passes through", path: "/images/collection-templates/a.webp", want: "/images/collection-templates/a.webp"},
		{name: "portable key resolves", path: key, want: "https://cdn.example/" + key},
		{name: "legacy key resolves", path: "library-posters/3.jpg", want: "https://cdn.example/library-posters/3.jpg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveStoredImageURL(context.Background(), resolver, tt.path); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveStoredImageURLWithoutAResolver(t *testing.T) {
	key := portableUploadKey(t, artworkkey.ImageTypeLibraryPoster, artworkkey.OriginalVariant)
	if got := resolveStoredImageURL(context.Background(), nil, key); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	// Pass-through values still work without artwork storage: they are not
	// store objects.
	if got := resolveStoredImageURL(context.Background(), nil, "/images/x.webp"); got != "/images/x.webp" {
		t.Fatalf("got %q", got)
	}
}

// TestResolveStoredImageURLSwallowsFailures keeps a broken image out of the
// response rather than a URL that cannot work.
func TestResolveStoredImageURLSwallowsFailures(t *testing.T) {
	resolver := &stubArtworkURLs{err: errors.New("store offline")}
	key := portableUploadKey(t, artworkkey.ImageTypeLibraryPoster, artworkkey.OriginalVariant)
	if got := resolveStoredImageURL(context.Background(), resolver, key); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestResolveStoredCardImageURLPicksTheCardVariant proves card-sized delivery
// lands on a ladder entry the upload actually wrote.
func TestResolveStoredCardImageURLPicksTheCardVariant(t *testing.T) {
	original := portableUploadKey(t, artworkkey.ImageTypeCollectionPoster, artworkkey.OriginalVariant)
	resolver := &stubArtworkURLs{}

	got := resolveStoredCardImageURL(context.Background(), resolver, original)
	wantKey := portableUploadKey(t, artworkkey.ImageTypeCollectionPoster, "w300")
	if got != "https://cdn.example/"+wantKey {
		t.Fatalf("got %q, want the w300 variant %q", got, wantKey)
	}
	if !containsString(artworkkey.VariantNames(artworkkey.ImageTypeCollectionPoster), "w300") {
		t.Fatal("the collection poster ladder no longer generates w300")
	}
}

func TestResolveProfileAvatarUsesTheArtworkStoreForNewUploads(t *testing.T) {
	original := portableUploadKey(t, artworkkey.ImageTypeAvatar, artworkkey.OriginalVariant)
	resolver := &stubArtworkURLs{}
	legacy := &stubLegacyAvatarStore{}

	source, url := resolveProfileAvatar(context.Background(), resolver, legacy, time.Minute,
		profileAvatarUploadPrefix+original)

	if source != "upload" {
		t.Fatalf("source = %q, want upload", source)
	}
	wantKey := portableUploadKey(t, artworkkey.ImageTypeAvatar, avatarDisplayVariant)
	if url != "https://cdn.example/"+wantKey {
		t.Fatalf("url = %q, want the %s variant", url, avatarDisplayVariant)
	}
	if len(legacy.seen) != 0 {
		t.Fatalf("legacy bucket was consulted for a stored avatar: %#v", legacy.seen)
	}
	if !containsString(artworkkey.VariantNames(artworkkey.ImageTypeAvatar), avatarDisplayVariant) {
		t.Fatalf("the avatar ladder no longer generates %s", avatarDisplayVariant)
	}
}

func TestResolveProfileAvatarTargetRequestsDisplayVariantFromOriginal(t *testing.T) {
	original := portableUploadKey(t, artworkkey.ImageTypeAvatar, artworkkey.OriginalVariant)
	resolver := &stubTargetArtworkURLs{}

	source, url := resolveProfileAvatarTarget(context.Background(), resolver, nil, time.Minute,
		7, "main", profileAvatarUploadPrefix+original)

	if source != avatarSourceUpload || url != "https://target.example/"+avatarDisplayVariant {
		t.Fatalf("resolved avatar = (%q, %q)", source, url)
	}
	if resolver.path != original {
		t.Fatalf("target reference = %q, want stored original %q", resolver.path, original)
	}
	if resolver.variant != avatarDisplayVariant {
		t.Fatalf("target variant = %q, want %q", resolver.variant, avatarDisplayVariant)
	}
	if resolver.target.Surface != artworkurl.SurfaceProfileAvatars || resolver.target.Keys[0] != "7" || resolver.target.Keys[1] != "main" {
		t.Fatalf("target identity = %#v", resolver.target)
	}
}

// TestResolveProfileAvatarKeepsLegacyUploadsWorking is the compatibility half:
// avatars uploaded before the store contract stay in their old bucket.
func TestResolveProfileAvatarKeepsLegacyUploadsWorking(t *testing.T) {
	resolver := &stubArtworkURLs{}
	legacy := &stubLegacyAvatarStore{}

	source, url := resolveProfileAvatar(context.Background(), resolver, legacy, time.Minute,
		profileAvatarUploadPrefix+"profile-avatars/7/main/original.webp")

	if source != "upload" {
		t.Fatalf("source = %q, want upload", source)
	}
	if url != "https://legacy.example/profile-avatars/7/main/w256.webp" {
		t.Fatalf("url = %q", url)
	}
	if len(resolver.seen) != 0 {
		t.Fatalf("artwork store was consulted for a legacy avatar: %#v", resolver.seen)
	}
}

func TestResolveProfileAvatarNonUploadReferences(t *testing.T) {
	tests := []struct {
		name       string
		ref        string
		wantSource string
		wantURL    string
	}{
		{name: "empty", ref: "", wantSource: "none", wantURL: ""},
		{name: "preset", ref: "preset:avatar-3", wantSource: "preset", wantURL: "/profile-avatars/avatar-3.svg"},
		{name: "bare preset", ref: "avatar-3", wantSource: "preset", wantURL: "/profile-avatars/avatar-3.svg"},
		{name: "unknown preset", ref: "preset:nope", wantSource: "none", wantURL: ""},
		{name: "garbage", ref: "not-an-avatar", wantSource: "none", wantURL: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, url := resolveProfileAvatar(context.Background(), &stubArtworkURLs{}, &stubLegacyAvatarStore{}, time.Minute, tt.ref)
			if source != tt.wantSource || url != tt.wantURL {
				t.Fatalf("got (%q, %q), want (%q, %q)", source, url, tt.wantSource, tt.wantURL)
			}
		})
	}
}

// TestProfileAvatarUploadPrefixOffset pins the byte length the artwork
// collector's reference union hard-codes as a SQL substring offset
// (metadata.profileAvatarReferenceSurface). Changing the prefix without
// changing that expression would make live avatars collectable.
func TestProfileAvatarUploadPrefixOffset(t *testing.T) {
	if profileAvatarUploadPrefix != "upload:" || len(profileAvatarUploadPrefix) != 7 {
		t.Fatalf("profileAvatarUploadPrefix = %q; update metadata.profileAvatarReferenceSurface to match",
			profileAvatarUploadPrefix)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
