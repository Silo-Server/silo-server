package handlers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/h2non/bimg"
	"golang.org/x/time/rate"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

type fixedSidecarArtworkTargets struct {
	state metadata.ArtworkTargetState
	local metadata.ConfinedLocalArtwork
}

func (f *fixedSidecarArtworkTargets) LoadTarget(_ context.Context, target artworkurl.Target) (metadata.ArtworkTargetState, error) {
	state := f.state
	state.Target = target
	return state, nil
}

func (*fixedSidecarArtworkTargets) SignalMissing(context.Context, metadata.ArtworkTargetState) error {
	return nil
}

func (f *fixedSidecarArtworkTargets) ReadSidecar(context.Context, metadata.ArtworkTargetState) (metadata.ConfinedLocalArtwork, error) {
	return f.local, nil
}

func fallbackTestJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1024, 768))
	for y := range 768 {
		for x := range 1024 {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8((x*31 + y*17) % 256),
				G: uint8((x*13 + y*29) % 256),
				B: uint8((x*7 + y*43) % 256),
				A: 255,
			})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatalf("encode fallback JPEG: %v", err)
	}
	return encoded.Bytes()
}

func TestArtworkFallbackGeneratesSizedWebPAndPreservesOriginal(t *testing.T) {
	signer, err := artworkurl.NewSigner(artworkTestSecret, func() time.Duration { return artworkTestTTL })
	if err != nil {
		t.Fatal(err)
	}
	original := fallbackTestJPEG(t)
	targets := &fixedSidecarArtworkTargets{
		state: metadata.ArtworkTargetState{
			Target:      artworkTestTarget,
			SourcePath:  "file:///library/movie/poster.jpg",
			ImageType:   artworkkey.ImageTypePoster,
			Recoverable: true,
		},
		local: metadata.ConfinedLocalArtwork{
			Data: original, MediaType: "image/jpeg", Path: "/library/movie/poster.jpg",
			ModTime: time.Unix(1_700_000_000, 0), Size: int64(len(original)),
		},
	}
	handler := NewArtworkHandler(unavailableArtworkStore{}, signer)
	handler.SetResilientDependencies(targets, nil, nil)

	sized, err := handler.resolveFallback(t.Context(), targets.state, "w92")
	if err != nil {
		t.Fatalf("resolve sized fallback: %v", err)
	}
	if sized.mediaType != "image/webp" {
		t.Fatalf("sized media type = %q, want image/webp", sized.mediaType)
	}
	if len(sized.data) >= len(original) {
		t.Fatalf("sized fallback = %d bytes, want smaller than %d-byte original", len(sized.data), len(original))
	}
	size, err := bimg.NewImage(sized.data).Size()
	if err != nil {
		t.Fatalf("read sized fallback dimensions: %v", err)
	}
	if size.Width > 92 {
		t.Fatalf("sized fallback width = %d, want at most 92", size.Width)
	}

	passthrough, err := handler.resolveFallback(t.Context(), targets.state, artworkkey.OriginalVariant)
	if err != nil {
		t.Fatalf("resolve original fallback: %v", err)
	}
	if passthrough.mediaType != "image/jpeg" || !bytes.Equal(passthrough.data, original) {
		t.Fatalf("original fallback was transformed: media_type=%q bytes_equal=%v", passthrough.mediaType, bytes.Equal(passthrough.data, original))
	}
}

// Recovery bandwidth is scarce and shared: a page of distinct already-cached
// targets must not spend it, or the requests behind them get placeholders.
func TestArtworkFallbackServesEmergencyCacheWithoutChargingSourceLimiter(t *testing.T) {
	signer, err := artworkurl.NewSigner(artworkTestSecret, func() time.Duration { return artworkTestTTL })
	if err != nil {
		t.Fatal(err)
	}
	state := metadata.ArtworkTargetState{
		Target:      artworkTestTarget,
		SourcePath:  "https://images.example.test/poster.jpg",
		ImageType:   artworkkey.ImageTypePoster,
		Recoverable: true,
	}
	handler := NewArtworkHandler(unavailableArtworkStore{}, signer)
	handler.SetResilientDependencies(&fixedSidecarArtworkTargets{state: state}, nil, nil)
	// A limiter that can never issue a token: anything that reaches it fails, so
	// a successful response can only have come from the emergency cache.
	handler.sourceRate = rate.NewLimiter(0, 0)

	cached := fallbackArtwork{data: []byte("emergency poster bytes"), mediaType: "image/jpeg", etag: `"source-cached"`}
	handler.emergency.put(emergencyCacheKey(state.Target.CacheKey(), state.SourcePath, artworkkey.OriginalVariant, ""), cached)

	got, err := handler.resolveFallback(t.Context(), state, artworkkey.OriginalVariant)
	if err != nil {
		t.Fatalf("cached fallback was throttled: %v", err)
	}
	if !bytes.Equal(got.data, cached.data) || !got.cacheHit {
		t.Fatalf("fallback = %q cache_hit=%v, want the cached bytes", got.data, got.cacheHit)
	}

	// Control: an uncached source on the same handler still has to pass the
	// limiter, so the hit above was not just an unlimited limiter.
	uncached := state
	uncached.SourcePath = "https://images.example.test/backdrop.jpg"
	if _, err := handler.resolveFallback(t.Context(), uncached, artworkkey.OriginalVariant); err == nil {
		t.Fatal("uncached source bypassed the exhausted recovery limiter")
	}
}

type countingArtworkObjectStore struct {
	ArtworkObjectStore
	opens int
}

func (s *countingArtworkObjectStore) Open(ctx context.Context, key string) (*artworkstore.Object, error) {
	s.opens++
	return s.ArtworkObjectStore.Open(ctx, key)
}

type chapterThumbnailS3 struct {
	objects map[string][]byte
	gets    int
	mu      sync.Mutex
}

func (*chapterThumbnailS3) Bucket() string { return "public" }
func (*chapterThumbnailS3) PutObject(context.Context, string, string, []byte) error {
	return nil
}
func (s *chapterThumbnailS3) GetObjectStream(_ context.Context, _, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, s3client.ErrNotFound
	}
	s.gets++
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (s *chapterThumbnailS3) StatObject(_ context.Context, _, key string) (s3client.ObjectInfo, error) {
	data, ok := s.objects[key]
	if !ok {
		return s3client.ObjectInfo{}, s3client.ErrNotFound
	}
	modified := time.Unix(1_700_000_000, 0)
	return s3client.ObjectInfo{
		Key: key, SizeBytes: int64(len(data)), LastModified: &modified,
		ContentType: "image/webp", ETag: `"chapter"`,
	}, nil
}
func (*chapterThumbnailS3) ObjectMatches(context.Context, string, string, []byte) (bool, error) {
	return false, nil
}
func (*chapterThumbnailS3) DeleteObjects(context.Context, string, []string) (int, error) {
	return 0, nil
}
func (*chapterThumbnailS3) HeadBucket(context.Context, string) error { return nil }
func (*chapterThumbnailS3) PresignGetURL(context.Context, string, string, time.Duration) (string, error) {
	return "", nil
}
func (*chapterThumbnailS3) EffectivePresignTTL(ttl time.Duration) time.Duration { return ttl }
func (*chapterThumbnailS3) ReadURLExpires() bool                                { return true }
func (*chapterThumbnailS3) ListObjectInfosPage(context.Context, string, string, string, int) ([]s3client.ObjectInfo, string, bool, error) {
	return nil, "", true, nil
}
func (*chapterThumbnailS3) DeletePrefix(context.Context, string, string) (int, error) {
	return 0, nil
}

func TestArtworkHandlerReadsChapterTargetFromPublicS3WhenCanonicalStoreIsLocal(t *testing.T) {
	local, err := artworkstore.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	canonical := &countingArtworkObjectStore{ArtworkObjectStore: local}
	signer, err := artworkurl.NewSigner(artworkTestSecret, func() time.Duration { return artworkTestTTL })
	if err != nil {
		t.Fatal(err)
	}

	const originalKey = "chapter-images/42/3/original.webp"
	const variantKey = "chapter-images/42/3/w300.webp"
	want := []byte("chapter thumbnail from public S3")
	client := &chapterThumbnailS3{objects: map[string][]byte{variantKey: want}}
	chapterStore, err := artworkstore.NewS3Store(client)
	if err != nil {
		t.Fatal(err)
	}
	target := artworkurl.Target{
		Surface: artworkurl.SurfaceChapterThumbnails,
		Keys:    []string{"42", "3"},
		Slot:    artworkkey.ImageTypeStill,
	}.WithReference(originalKey)
	targets := &fixedSidecarArtworkTargets{state: metadata.ArtworkTargetState{
		SelectedPath: originalKey,
		ImageType:    artworkkey.ImageTypeStill,
		Protected:    true,
	}}
	handler := NewArtworkHandler(canonical, signer)
	handler.SetChapterThumbnailStore(chapterStore)
	handler.SetResilientDependencies(targets, nil, nil)
	signed, err := signer.SignTarget(target, "w300", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimPrefix(signed.URL, artworkurl.RoutePrefix), "/")
	recorder := httptest.NewRecorder()
	handler.serve(recorder, httptest.NewRequest(http.MethodGet, signed.URL, nil), parts[0], parts[1])

	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), want) {
		t.Fatalf("chapter response = %d %q, want 200 with S3 bytes", recorder.Code, recorder.Body.Bytes())
	}
	if canonical.opens != 0 {
		t.Fatalf("canonical local store opens = %d, want zero", canonical.opens)
	}
	if client.gets != 1 {
		t.Fatalf("public S3 reads = %d, want one", client.gets)
	}
}
