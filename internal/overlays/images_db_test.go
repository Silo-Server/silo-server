package overlays

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

type memoryImageStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (s *memoryImageStore) Put(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	s.objects[key] = bytes.Clone(data)
	return nil
}

func (s *memoryImageStore) Get(_ context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, blobstore.ObjectInfo{}, blobstore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), blobstore.ObjectInfo{Key: key, Size: int64(len(data))}, nil
}

func (s *memoryImageStore) Delete(_ context.Context, keys []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		delete(s.objects, key)
	}
	return len(keys), nil
}

func testPNG(t *testing.T, width, height int, shade uint8) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.NRGBA{R: shade, G: uint8(x), B: uint8(y), A: 200})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func imageTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// emptyImageLibrary returns a library over an emptied overlay_images table
// and a fresh in-memory store.
func emptyImageLibrary(t *testing.T) (*pgxpool.Pool, *memoryImageStore, *ImageLibrary) {
	t.Helper()
	pool := imageTestPool(t)
	store := new(memoryImageStore)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM overlay_images`) })
	if _, err := pool.Exec(t.Context(), `DELETE FROM overlay_images`); err != nil {
		t.Fatal(err)
	}
	return pool, store, NewImageLibrary(pool, store)
}

func TestImageLibraryUpload(t *testing.T) {
	_, store, lib := emptyImageLibrary(t)
	wide := testPNG(t, 1200, 200, 10)
	img, err := lib.Upload(t.Context(), "  4K Dolby Vision ", mediaTypePNG, wide)
	if err != nil {
		t.Fatal(err)
	}
	if !IsImageRef(img.Ref) || img.Name != "4K Dolby Vision" || img.Width != 600 || img.Height != 100 || img.Bytes <= 0 {
		t.Fatalf("uploaded image = %+v, want a 600x100 WebP named after the trimmed name", img)
	}
	if _, ok := store.objects[imageKeyPrefix+img.Ref]; !ok {
		t.Fatalf("stored keys = %v, want %s", store.objects, imageKeyPrefix+img.Ref)
	}

	// The same bytes again keep one entry and take the new name.
	again, err := lib.Upload(t.Context(), "Renamed", mediaTypePNG, wide)
	if err != nil || again.Ref != img.Ref || again.Name != "Renamed" || !again.CreatedAt.Equal(img.CreatedAt) {
		t.Fatalf("re-upload = %+v, %v; want the same ref renamed", again, err)
	}
	tall, err := lib.Upload(t.Context(), "Tall", mediaTypePNG, testPNG(t, 100, 900, 90))
	if err != nil || tall.Height != 300 || tall.Width != 33 {
		t.Fatalf("tall upload = %+v, %v; want it fitted to 300 high", tall, err)
	}
	small, err := lib.Upload(t.Context(), "Small", mediaTypePNG, testPNG(t, 40, 20, 200))
	if err != nil || small.Width != 40 || small.Height != 20 {
		t.Fatalf("small upload = %+v, %v; want it kept at its size", small, err)
	}
	list, err := lib.List(t.Context())
	if err != nil || len(list) != 3 || list[0].Ref != img.Ref || list[0].Name != "Renamed" {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestImageLibraryGetAndDelete(t *testing.T) {
	_, store, lib := emptyImageLibrary(t)
	wide := testPNG(t, 1200, 200, 10)
	img, err := lib.Upload(t.Context(), "Remux", mediaTypePNG, wide)
	if err != nil {
		t.Fatal(err)
	}
	data, err := lib.Get(t.Context(), img.Ref)
	if err != nil || !bytes.Equal(data, store.objects[imageKeyPrefix+img.Ref]) {
		t.Fatalf("get = %d bytes, %v", len(data), err)
	}

	if err := lib.Delete(t.Context(), img.Ref); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.objects[imageKeyPrefix+img.Ref]; ok {
		t.Fatal("delete left the image bytes in storage")
	}
	if _, err := lib.Get(t.Context(), img.Ref); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("get after delete = %v, want ErrImageNotFound", err)
	}
	if err := lib.Delete(t.Context(), img.Ref); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("second delete = %v, want ErrImageNotFound", err)
	}
	// Bytes in storage do not make a ref current without its row.
	if err := store.Put(t.Context(), imageKeyPrefix+img.Ref, wide); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Get(t.Context(), img.Ref); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("get of an unlisted ref = %v, want ErrImageNotFound", err)
	}
}

func TestImageLibraryRejects(t *testing.T) {
	pool, _, lib := emptyImageLibrary(t)
	pngBytes := testPNG(t, 20, 10, 1)
	for _, tc := range []struct {
		name, label, contentType string
		data                     []byte
		want                     error
	}{
		{"blank name", "   ", mediaTypePNG, pngBytes, ErrInvalidImageName},
		{"long name", string(bytes.Repeat([]byte("x"), MaxImageNameLength+1)), mediaTypePNG, pngBytes, ErrInvalidImageName},
		{"gif", "Logo", "image/gif", pngBytes, ErrUnsupportedImage},
		{"not an image", "Logo", mediaTypePNG, []byte("not an image"), ErrUnsupportedImage},
		{"too large", "Logo", mediaTypePNG, make([]byte, MaxImageUploadBytes+1), ErrImageTooLarge},
	} {
		if _, err := lib.Upload(t.Context(), tc.label, tc.contentType, tc.data); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := NewImageLibrary(pool, nil).Upload(t.Context(), "Logo", mediaTypePNG, pngBytes); !errors.Is(err, ErrImageStorageUnavailable) {
		t.Fatalf("upload without storage = %v", err)
	}

	// A full library refuses a new image but still renames one it holds.
	if _, err := pool.Exec(t.Context(), `INSERT INTO overlay_images (ref, name, width, height, bytes)
		SELECT lpad(to_hex(g), 64, '0') || '.webp', 'fill ' || g, 1, 1, 1 FROM generate_series(1, $1::int) g`, MaxImages); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Upload(t.Context(), "One too many", mediaTypePNG, pngBytes); !errors.Is(err, ErrImageLibraryFull) {
		t.Fatalf("upload into a full library = %v, want ErrImageLibraryFull", err)
	}
	held := fmt.Sprintf("%064x.webp", 1)
	if err := lib.Delete(t.Context(), held); err != nil {
		t.Fatal(err)
	}
	first, err := lib.Upload(t.Context(), "Fits again", mediaTypePNG, pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Upload(t.Context(), "Renamed while full", mediaTypePNG, pngBytes); err != nil {
		t.Fatalf("renaming a held image in a full library = %v", err)
	}
	if err := lib.Delete(t.Context(), first.Ref); err != nil {
		t.Fatal(err)
	}
}
