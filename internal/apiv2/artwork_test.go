package apiv2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
)

func TestArtworkNestedKeyBytesAndRanges(t *testing.T) {
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := "tmdb/movies/123/poster/w500.rev.webp"
	if err := store.Put(t.Context(), key, []byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	signer := artworkurl.NewSigner("test-secret", time.Hour)
	h := NewHandler(Dependencies{ArtworkStore: store, ArtworkSigner: signer})
	u, _ := signer.Sign(key, time.Now())
	got := do(t, h, http.MethodGet, u, "", nil)
	if got.Code != 200 || got.Body.String() != "0123456789" {
		t.Fatalf("GET: %d %s", got.Code, got.Body.String())
	}
	head := do(t, h, http.MethodHead, u, "", nil)
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "10" {
		t.Fatalf("HEAD: %d %v", head.Code, head.Header())
	}
	partial := do(t, h, http.MethodGet, u, "", map[string]string{"Range": "bytes=0-2"})
	if partial.Code != 206 || partial.Body.String() != "012" {
		t.Fatalf("range: %d %s", partial.Code, partial.Body.String())
	}
	cached := do(t, h, http.MethodGet, u, "", map[string]string{"If-None-Match": got.Header().Get("ETag")})
	if cached.Code != 304 {
		t.Fatalf("conditional: %d", cached.Code)
	}
}

// Embedding the store keeps the fake focused on the read boundary exercised here.
type artworkReadFailure struct {
	blobstore.Store
	calls int
}

func (s *artworkReadFailure) Get(context.Context, string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	s.calls++
	return nil, blobstore.ObjectInfo{}, errors.New("storage offline")
}

type artworkRepairRecorder struct {
	paths  []string
	limits []int
}

func (s *artworkRepairRecorder) EnqueueArtworkRepair(_ context.Context, paths []string, limit int) (int, error) {
	s.paths = append(s.paths, paths...)
	s.limits = append(s.limits, limit)
	return len(paths), nil
}
func TestArtworkRejectsInvalidSignaturesBeforeStorage(t *testing.T) {
	signer := artworkurl.NewSigner("test-secret", time.Hour)
	store := &artworkReadFailure{}
	h := NewHandler(Dependencies{ArtworkStore: store, ArtworkSigner: signer})
	valid, _ := signer.Sign("nested/original.rev.webp", time.Now())
	// A revisioned URL holds for a UTC day plus the TTL.
	expired, _ := signer.Sign("nested/original.rev.webp", time.Now().Add(-26*time.Hour))
	traversal, _ := signer.Sign("nested/../original.rev.webp", time.Now())
	for name, u := range map[string]string{
		"missing query":     Prefix + "/artwork/nested/original.rev.webp",
		"missing signature": strings.Split(valid, "&sig=")[0],
		"invalid signature": valid + "tampered",
		"expired":           expired,
		"signed traversal":  traversal,
	} {
		t.Run(name, func(t *testing.T) {
			got := do(t, h, http.MethodGet, u, "", nil)
			if got.Code != http.StatusNotFound {
				t.Fatalf("status = %d, body = %s", got.Code, got.Body.String())
			}
		})
	}
	if store.calls != 0 {
		t.Fatalf("unauthorized storage reads = %d", store.calls)
	}
}
func TestArtworkMissingRevisionEnqueuesOriginalRepair(t *testing.T) {
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	signer := artworkurl.NewSigner("test-secret", time.Hour)
	repair := &artworkRepairRecorder{}
	h := NewHandler(Dependencies{ArtworkStore: store, ArtworkSigner: signer, ArtworkRepair: repair})
	u, _ := signer.Sign("tmdb/movies/123/poster/w500.rev.webp", time.Now())
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		got := do(t, h, method, u, "", nil)
		if got.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d", method, got.Code)
		}
	}
	if len(repair.paths) != 2 {
		t.Fatalf("repair paths = %v", repair.paths)
	}
	for i, p := range repair.paths {
		if p != "tmdb/movies/123/poster/original.rev.webp" || repair.limits[i] != 1 {
			t.Fatalf("repair = %v, limits = %v", repair.paths, repair.limits)
		}
	}
	legacy, _ := signer.Sign("tmdb/movies/123/poster/original.webp", time.Now())
	if got := do(t, h, http.MethodGet, legacy, "", nil); got.Code != http.StatusNotFound {
		t.Fatalf("legacy status = %d", got.Code)
	}
	if len(repair.paths) != 2 {
		t.Fatalf("legacy miss queued repair: %v", repair.paths)
	}
}
func TestArtworkStorageFailure(t *testing.T) {
	store := &artworkReadFailure{}
	signer := artworkurl.NewSigner("test-secret", time.Hour)
	h := NewHandler(Dependencies{ArtworkStore: store, ArtworkSigner: signer})
	u, _ := signer.Sign("nested/original.webp", time.Now())
	got := do(t, h, http.MethodGet, u, "", nil)
	if got.Code != http.StatusServiceUnavailable || store.calls != 1 {
		t.Fatalf("status = %d, reads = %d", got.Code, store.calls)
	}
}
func TestArtworkMutableCachePolicy(t *testing.T) {
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := "nested/original.webp"
	if err := store.Put(t.Context(), key, []byte("image")); err != nil {
		t.Fatal(err)
	}
	signer := artworkurl.NewSigner("test-secret", time.Hour)
	h := NewHandler(Dependencies{ArtworkStore: store, ArtworkSigner: signer})
	u, _ := signer.Sign(key, time.Now())
	got := do(t, h, http.MethodGet, u, "", nil)
	cache := got.Header().Get("Cache-Control")
	if got.Code != http.StatusOK || cache != "private, no-cache" {
		t.Fatalf("status = %d, cache = %q", got.Code, cache)
	}
}

// A revisioned URL holds for its UTC day, so its bytes may stay cached until
// the URL expires.
func TestArtworkRevisionedCachePolicy(t *testing.T) {
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := "nested/w500.rev.webp"
	if err := store.Put(t.Context(), key, []byte("image")); err != nil {
		t.Fatal(err)
	}
	signer := artworkurl.NewSigner("test-secret", time.Hour)
	h := NewHandler(Dependencies{ArtworkStore: store, ArtworkSigner: signer})
	u, expires := signer.Sign(key, time.Now())
	got := do(t, h, http.MethodGet, u, "", nil)
	var maxAge int64
	cache := got.Header().Get("Cache-Control")
	if _, err := fmt.Sscanf(cache, "private, max-age=%d, immutable", &maxAge); err != nil || got.Code != http.StatusOK {
		t.Fatalf("status = %d, cache = %q", got.Code, cache)
	}
	// A few seconds of slack covers a run that crosses midnight UTC between
	// signing and serving, when the URL has only the TTL left.
	if remaining := int64(time.Until(expires).Seconds()); maxAge < 3600-5 || maxAge > remaining+1 {
		t.Fatalf("max-age = %d, URL remaining lifetime = %ds", maxAge, remaining)
	}
}

func TestArtworkRangesOnForwardOnlyStreams(t *testing.T) {
	// S3 bodies are not seekable. Ranges, HEAD, and full reads must still work
	// without buffering the object.
	store := blobstoretest.New()
	key := "tmdb/movies/123/poster/w500.rev.webp"
	if err := store.Put(t.Context(), key, []byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	signer := artworkurl.NewSigner("test-secret", time.Hour)
	h := NewHandler(Dependencies{ArtworkStore: store, ArtworkSigner: signer})
	u, _ := signer.Sign(key, time.Now())
	if got := do(t, h, http.MethodGet, u, "", nil); got.Code != 200 || got.Body.String() != "0123456789" || got.Header().Get("Content-Length") != "10" {
		t.Fatalf("GET: %d %q %v", got.Code, got.Body.String(), got.Header())
	}
	if head := do(t, h, http.MethodHead, u, "", nil); head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "10" {
		t.Fatalf("HEAD: %d %v", head.Code, head.Header())
	}
	partial := do(t, h, http.MethodGet, u, "", map[string]string{"Range": "bytes=3-5"})
	if partial.Code != 206 || partial.Body.String() != "345" || partial.Header().Get("Content-Range") != "bytes 3-5/10" {
		t.Fatalf("range: %d %q %v", partial.Code, partial.Body.String(), partial.Header())
	}
	if tail := do(t, h, http.MethodGet, u, "", map[string]string{"Range": "bytes=-2"}); tail.Code != 206 || tail.Body.String() != "89" {
		t.Fatalf("suffix range: %d %q", tail.Code, tail.Body.String())
	}
	if multi := do(t, h, http.MethodGet, u, "", map[string]string{"Range": "bytes=0-1,5-6"}); multi.Code != 200 || multi.Body.String() != "0123456789" {
		t.Fatalf("multi-range: %d %q", multi.Code, multi.Body.String())
	}
	if bad := do(t, h, http.MethodGet, u, "", map[string]string{"Range": "bytes=20-30"}); bad.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("unsatisfiable range: %d", bad.Code)
	}
}
