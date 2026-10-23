package handlers

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

// localStorageCollageFixture wires collage sources the way local artwork
// storage serves them: posters resolve to this server's signed, root-relative
// artwork route, and the store holds the variant each one names.
func localStorageCollageFixture(t *testing.T, sources []string) (*blobstoretest.Memory, *catalog.DetailService, *artworkurl.Signer) {
	t.Helper()
	store := blobstoretest.New()
	signer := artworkurl.NewSigner("collage-test-secret", time.Hour)
	resolver := metadata.NewPluginImageResolver()
	resolver.SetArtworkResolver(artworkurl.NewServerResolver(signer))
	detail := &catalog.DetailService{}
	detail.SetImageResolver(resolver)

	resolved := detail.PresignImageURLs(context.Background(), sources, "poster", "small")
	for i, source := range sources {
		url := resolved[source]
		if !strings.HasPrefix(url, "/api/v2/artwork/") {
			t.Fatalf("source %q resolved to %q, want the server's artwork route", source, url)
		}
		key, ok := signer.SignedKey(url, time.Now())
		if !ok {
			t.Fatalf("resolved URL %q does not verify", url)
		}
		poster := image.NewNRGBA(image.Rect(0, 0, 20, 30))
		for y := range 30 {
			for x := range 20 {
				poster.Set(x, y, color.NRGBA{R: uint8(60 * i), G: 120, B: 200, A: 255})
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, poster); err != nil {
			t.Fatal(err)
		}
		if err := store.Put(context.Background(), key, buf.Bytes()); err != nil {
			t.Fatal(err)
		}
	}
	return store, detail, signer
}

// Local artwork storage resolves posters to root-relative URLs on this
// server, which an HTTP client cannot request ("unsupported protocol
// scheme"), so collages never built there. They are read from the store.
func TestCollageReadsPostersFromLocalArtworkStorage(t *testing.T) {
	sources := []string{
		"local/movies/title-1/0a1b2c3d/poster/original.webp",
		"local/movies/title-2/1b2c3d4e/poster/original.webp",
		"local/movies/title-3/2c3d4e5f/poster/original.webp",
		"local/movies/title-4/3d4e5f60/poster/original.webp",
	}
	store, detail, signer := localStorageCollageFixture(t, sources)
	gen := NewPersonalCollectionCollageGenerator(store, detail, signer, nil)
	if gen == nil {
		t.Fatal("no collage generator")
	}

	const key = "0123456789abcdef"
	path, _, err := gen.ComposeCollectionCollage(t.Context(), "c1", key, sources)
	if err != nil {
		t.Fatalf("compose from local artwork storage: %v", err)
	}
	if path != gen.CollectionCollagePath("c1", key) {
		t.Fatalf("collage stored at %q, want %q", path, gen.CollectionCollagePath("c1", key))
	}
	if _, ok := store.Objects[path]; !ok {
		t.Fatalf("collage %q not in the store", path)
	}

	// The library collection handler composes with the same reader.
	h := &LibraryCollectionHandler{ArtworkStore: store, detailSvc: detail, ArtworkSigner: signer, httpClient: newCollectionImageClient()}
	if _, _, err := h.ComposeCollectionCollage(t.Context(), "c2", key, sources); err != nil {
		t.Fatalf("library collection compose from local artwork storage: %v", err)
	}
}

// A source the store does not hold fails the build, as an unreachable
// download did, so a later read retries it rather than storing a collage
// with a gap.
func TestCollageFromLocalArtworkStorageFailsOnAMissingPoster(t *testing.T) {
	sources := []string{"local/movies/title-1/0a1b2c3d/poster/original.webp"}
	_, detail, signer := localStorageCollageFixture(t, sources)
	gen := NewPersonalCollectionCollageGenerator(blobstoretest.New(), detail, signer, nil)
	if _, _, err := gen.ComposeCollectionCollage(t.Context(), "c1", "0123456789abcdef", sources); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("compose with a poster the store does not hold = %v, want the store's not-found error", err)
	}
}
