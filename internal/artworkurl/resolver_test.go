package artworkurl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

func testLibraryReference(t *testing.T, signer *Signer) string {
	t.Helper()
	reference, err := signer.LibraryReference(LibraryIdentity{
		Surface:     SurfaceItemPosters,
		Keys:        []string{"movie-1"},
		Fingerprint: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("LibraryReference: %v", err)
	}
	return reference
}

func TestNewResolverRequiresSigner(t *testing.T) {
	if _, err := NewResolver(nil); err == nil {
		t.Fatal("NewResolver accepted no signer")
	}
}

func TestResolverUsesTargetBoundRoute(t *testing.T) {
	signer := testSigner(t, time.Hour)
	resolver, err := NewResolver(signer)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	target := Target{Surface: SurfaceItemPosters, Keys: []string{"movie-1"}, Slot: "poster"}.WithReference(testKey)
	resolved, err := resolver.ResolveTargetURL(context.Background(), target, "w300")
	if err != nil {
		t.Fatalf("ResolveTargetURL: %v", err)
	}
	if !strings.HasPrefix(resolved.URL, RoutePrefix) {
		t.Fatalf("URL = %q, want the resilient API route", resolved.URL)
	}
}

// TestResolveTargetURLRefusesEmptyReference pins the absent-artwork contract:
// a target minted from a catalog row with nothing selected must not produce a
// capability, so batch resolution omits it and the response field stays "" —
// not a URL that can only ever serve the placeholder.
func TestResolveTargetURLRefusesEmptyReference(t *testing.T) {
	resolver, err := NewResolver(testSigner(t, time.Hour))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	target := Target{Surface: SurfaceItemLogos, Keys: []string{"movie-1"}, Slot: "logo"}.WithReference("")
	if _, err := resolver.ResolveTargetURL(context.Background(), target, "w500"); !errors.Is(err, artworkstore.ErrInvalidKey) {
		t.Fatalf("ResolveTargetURL with empty reference = %v, want ErrInvalidKey", err)
	}
	resolved := resolver.ResolveTargetRequests(context.Background(), []TargetRequest{{Target: target, Variant: "w500"}})
	if len(resolved) != 0 {
		t.Fatalf("ResolveTargetRequests minted %d capabilities for an empty reference, want 0", len(resolved))
	}
}

func TestResolveArtworkURLRequiresLibraryReference(t *testing.T) {
	resolver, err := NewResolver(testSigner(t, time.Hour))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	for _, reference := range []string{testKey, "/images/collection-templates/x.jpg", "../../etc/passwd", ""} {
		if _, err := resolver.ResolveArtworkURL(context.Background(), reference); !errors.Is(err, artworkstore.ErrInvalidKey) {
			t.Fatalf("ResolveArtworkURL(%q) error = %v, want ErrInvalidKey", reference, err)
		}
	}
}

func TestResolveArtworkURLsIncludesOnlyLibraryReferences(t *testing.T) {
	signer := testSigner(t, time.Hour)
	resolver, err := NewResolver(signer)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	reference := testLibraryReference(t, signer)

	resolved := resolver.ResolveArtworkURLs(context.Background(), []string{
		reference,
		testKey,
		"/images/collection-templates/x.jpg",
	})
	if len(resolved) != 1 {
		t.Fatalf("resolved %d URLs, want 1: %v", len(resolved), resolved)
	}
	if !strings.HasPrefix(resolved[reference].URL, LibraryRoutePrefix) {
		t.Fatalf("resolved URL = %q, want direct-library route", resolved[reference].URL)
	}
}

func TestResolveArtworkURLsOmitsInvalidLibraryReferences(t *testing.T) {
	resolver, err := NewResolver(testSigner(t, time.Hour))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	malformed := LibraryReferencePrefix + "not-base64"

	if resolved := resolver.ResolveArtworkURLs(context.Background(), []string{malformed}); len(resolved) != 0 {
		t.Fatalf("resolved = %v, want no entries for an invalid library reference", resolved)
	}
}

func TestNilResolverIsInert(t *testing.T) {
	var resolver *Resolver
	if _, err := resolver.ResolveArtworkURL(context.Background(), testKey); err == nil {
		t.Fatal("nil resolver resolved a URL")
	}
	if resolved := resolver.ResolveArtworkURLs(context.Background(), []string{testKey}); len(resolved) != 0 {
		t.Fatalf("nil resolver resolved %v", resolved)
	}
}
