package artworkupload

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"sort"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/artworkadopt"
	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

type memoryStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	writes  int
	failOn  string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{objects: make(map[string][]byte)}
}

func (s *memoryStore) WriteImmutable(_ context.Context, key string, data []byte, _ artworkstore.ObjectMetadata) error {
	if err := artworkstore.ValidateKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failOn != "" && key == s.failOn {
		return errors.New("write refused")
	}
	s.objects[key] = append([]byte(nil), data...)
	s.writes++
	return nil
}

func (s *memoryStore) Matches(_ context.Context, key string, data []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.objects[key]
	return ok && bytes.Equal(stored, data), nil
}

func (s *memoryStore) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *memoryStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

type recordingTracker struct {
	calls           int
	originalPath    string
	imageType       string
	objectKeys      []string
	objectsAtCall   int
	countObjectsNow func() int
	err             error
	recordCalls     int
	recordedPath    string
	sourceClass     string
	objects         []artworkstore.ObjectInfo
	retainCalls     int
	retainedPath    string
	retainErr       error
}

func (t *recordingTracker) RetainUntrackedArtworkRevision(_ context.Context, originalPath string) error {
	t.retainCalls++
	t.retainedPath = originalPath
	return t.retainErr
}

func (t *recordingTracker) TrackArtworkRevision(_ context.Context, originalPath, imageType string, objectKeys []string) error {
	t.calls++
	t.originalPath = originalPath
	t.imageType = imageType
	t.objectKeys = append([]string(nil), objectKeys...)
	if t.countObjectsNow != nil {
		t.objectsAtCall = t.countObjectsNow()
	}
	return t.err
}

func (t *recordingTracker) RecordArtworkRevision(_ context.Context, originalPath, sourceClass string, objects []artworkstore.ObjectInfo) error {
	t.recordCalls++
	t.recordedPath = originalPath
	t.sourceClass = sourceClass
	t.objects = append([]artworkstore.ObjectInfo(nil), objects...)
	return t.err
}

func testJPEG(t *testing.T, w, h int, tint uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 3), G: uint8(y * 3), B: tint, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestMaterializeWritesTheCompleteRevision(t *testing.T) {
	store := newMemoryStore()
	materializer := NewMaterializer(store)

	result, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeLibraryPoster,
		Data:      testJPEG(t, 40, 60, 30),
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	info, ok := artworkkey.ParsePortableKey(result.OriginalKey)
	if !ok {
		t.Fatalf("original key %q is not portable", result.OriginalKey)
	}
	if info.ImageType != artworkkey.ImageTypeLibraryPoster {
		t.Fatalf("image type = %q", info.ImageType)
	}
	if info.Revision != result.Revision {
		t.Fatalf("key revision %q != result revision %q", info.Revision, result.Revision)
	}
	if result.Thumbhash == "" {
		t.Fatal("thumbhash is empty")
	}

	// Exactly the ladder the collector will expand, plus the manifest.
	want := artworkkey.ObjectKeys(result.OriginalKey, info.ImageType)
	sort.Strings(want)
	if got := store.keys(); len(got) != len(want) {
		t.Fatalf("stored %#v, want %#v", got, want)
	} else {
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("stored %#v, want %#v", got, want)
			}
		}
	}
	if result.WrittenObjects != len(artworkkey.VariantNames(info.ImageType)) {
		t.Fatalf("written objects = %d", result.WrittenObjects)
	}
}

// TestMaterializeIsIdempotent proves re-uploading identical bytes converges on
// the same revision without rewriting a single image object.
func TestMaterializeIsIdempotent(t *testing.T) {
	store := newMemoryStore()
	materializer := NewMaterializer(store)
	data := testJPEG(t, 40, 60, 90)

	first, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeCollectionPoster,
		Data:      data,
	})
	if err != nil {
		t.Fatalf("first Materialize: %v", err)
	}
	writesAfterFirst := store.writes

	second, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeCollectionPoster,
		Data:      data,
	})
	if err != nil {
		t.Fatalf("second Materialize: %v", err)
	}

	if first.OriginalKey != second.OriginalKey {
		t.Fatalf("keys diverged: %q vs %q", first.OriginalKey, second.OriginalKey)
	}
	if second.WrittenObjects != 0 || second.ExistingObjects != first.WrittenObjects {
		t.Fatalf("second pass wrote %d and matched %d", second.WrittenObjects, second.ExistingObjects)
	}
	// Nothing at all is rewritten, manifest included.
	if store.writes != writesAfterFirst {
		t.Fatalf("store writes = %d, want %d", store.writes, writesAfterFirst)
	}

	// A revision whose completeness marker was lost is healed by re-uploading.
	delete(store.objects, first.ManifestKey)
	if _, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeCollectionPoster,
		Data:      data,
	}); err != nil {
		t.Fatalf("healing Materialize: %v", err)
	}
	if _, ok := store.objects[first.ManifestKey]; !ok {
		t.Fatal("manifest was not restored")
	}
}

// TestDifferentImageTypesDoNotShareRevisions guards the upload namespace: two
// surfaces holding identical bytes must not answer to the same object, or
// deleting one library's poster could take another surface's artwork with it.
func TestDifferentImageTypesDoNotShareRevisions(t *testing.T) {
	store := newMemoryStore()
	materializer := NewMaterializer(store)
	data := testJPEG(t, 40, 60, 200)

	poster, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeLibraryPoster,
		Data:      data,
	})
	if err != nil {
		t.Fatalf("library poster: %v", err)
	}
	collection, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeCollectionPoster,
		Data:      data,
	})
	if err != nil {
		t.Fatalf("collection poster: %v", err)
	}
	if poster.Revision == collection.Revision {
		t.Fatal("library and collection posters share a revision")
	}
	if poster.Directory == collection.Directory {
		t.Fatalf("both types landed in %q", poster.Directory)
	}
}

// TestTrackingRegistersEveryObjectBeforeTheFirstWrite is the invariant that
// makes a crashed upload reclaimable instead of orphaned.
func TestTrackingRegistersEveryObjectBeforeTheFirstWrite(t *testing.T) {
	store := newMemoryStore()
	tracker := &recordingTracker{countObjectsNow: store.count}
	materializer := NewMaterializer(store)
	materializer.SetRevisionTracker(tracker)

	result, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeAvatar,
		Data:      testJPEG(t, 64, 64, 10),
		Square:    true,
		Track:     true,
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if tracker.calls != 1 {
		t.Fatalf("tracker calls = %d, want 1", tracker.calls)
	}
	if tracker.objectsAtCall != 0 {
		t.Fatalf("%d objects existed when the revision was registered", tracker.objectsAtCall)
	}
	if tracker.originalPath != result.OriginalKey {
		t.Fatalf("tracked %q, stored %q", tracker.originalPath, result.OriginalKey)
	}
	if tracker.imageType != artworkkey.ImageTypeAvatar {
		t.Fatalf("tracked image type = %q", tracker.imageType)
	}
	tracked := append([]string(nil), tracker.objectKeys...)
	sort.Strings(tracked)
	stored := store.keys()
	if len(tracked) != len(stored) {
		t.Fatalf("tracked %#v, stored %#v", tracked, stored)
	}
	for i := range tracked {
		if tracked[i] != stored[i] {
			t.Fatalf("tracked %#v, stored %#v", tracked, stored)
		}
	}
	if tracker.recordCalls != 1 || tracker.recordedPath != result.OriginalKey || tracker.sourceClass != "upload" {
		t.Fatalf("inventory completion = calls:%d path:%q source:%q", tracker.recordCalls, tracker.recordedPath, tracker.sourceClass)
	}
	if len(tracker.objects) != len(stored) {
		t.Fatalf("inventory objects = %d, stored objects = %d", len(tracker.objects), len(stored))
	}
	for _, object := range tracker.objects {
		if object.SizeBytes != int64(len(store.objects[object.Key])) {
			t.Fatalf("inventory size for %s = %d, stored %d", object.Key, object.SizeBytes, len(store.objects[object.Key]))
		}
	}
}

func TestUntrackedMaterializationDisarmsExistingRevisionCandidate(t *testing.T) {
	store := newMemoryStore()
	tracker := &recordingTracker{}
	materializer := NewMaterializer(store)
	materializer.SetRevisionTracker(tracker)

	result, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeLibraryPoster,
		Data:      testJPEG(t, 40, 60, 40),
	})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if tracker.calls != 0 {
		t.Fatalf("tracker called %d times without Track", tracker.calls)
	}
	if tracker.recordCalls != 0 {
		t.Fatalf("inventory recorded %d times without Track", tracker.recordCalls)
	}
	if tracker.retainCalls != 1 || tracker.retainedPath != result.OriginalKey {
		t.Fatalf("untracked retention = calls:%d path:%q, want 1/%q", tracker.retainCalls, tracker.retainedPath, result.OriginalKey)
	}
}

func TestUntrackedAdoptionDisarmsImportedSeed(t *testing.T) {
	store, err := artworkstore.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := testJPEG(t, 64, 64, 42)
	request := Request{ImageType: artworkkey.ImageTypeAvatar, Data: data, Square: true, Track: false}

	first, err := NewMaterializer(store).Materialize(context.Background(), request)
	if err != nil {
		t.Fatalf("seed adoption source materialization: %v", err)
	}
	tracker := &recordingTracker{}
	materializer := NewMaterializer(store)
	materializer.SetRevisionTracker(tracker)
	adopted, err := materializer.Materialize(context.Background(), request)
	if err != nil {
		t.Fatalf("untracked adoption: %v", err)
	}
	if adopted.OriginalKey != first.OriginalKey {
		t.Fatalf("adopted %q, want %q", adopted.OriginalKey, first.OriginalKey)
	}
	if tracker.retainCalls != 1 || tracker.retainedPath != first.OriginalKey {
		t.Fatalf("seed retention = calls:%d path:%q", tracker.retainCalls, tracker.retainedPath)
	}
	if tracker.calls != 0 || tracker.recordCalls != 0 {
		t.Fatalf("untracked adoption used ordinary lifecycle tracking: track=%d record=%d", tracker.calls, tracker.recordCalls)
	}
}

// TestTrackingFailureAbortsTheWrite keeps the ordering meaningful: objects that
// could not be registered must not reach the store.
func TestTrackingFailureAbortsTheWrite(t *testing.T) {
	store := newMemoryStore()
	materializer := NewMaterializer(store)
	materializer.SetRevisionTracker(&recordingTracker{err: errors.New("database down")})

	if _, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeLibraryPoster,
		Data:      testJPEG(t, 40, 60, 50),
		Track:     true,
	}); err == nil {
		t.Fatal("expected the tracking failure to abort the upload")
	}
	if got := store.count(); got != 0 {
		t.Fatalf("%d objects were written despite the tracking failure", got)
	}
}

// TestManifestIsWrittenLast means a revision directory with a manifest is a
// complete one.
func TestManifestIsWrittenLast(t *testing.T) {
	store := newMemoryStore()
	materializer := NewMaterializer(store)
	data := testJPEG(t, 40, 60, 70)

	// Discover the revision without failing, then replay with one image object
	// refused.
	probe := newMemoryStore()
	preview, err := NewMaterializer(probe).Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeLibraryPoster,
		Data:      data,
	})
	if err != nil {
		t.Fatalf("preview Materialize: %v", err)
	}
	store.failOn = preview.VariantKeys["w300"]

	if _, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeLibraryPoster,
		Data:      data,
	}); err == nil {
		t.Fatal("expected the refused object write to fail the upload")
	}
	for _, key := range store.keys() {
		if key == preview.ManifestKey {
			t.Fatal("manifest was written for an incomplete revision")
		}
	}
}

func TestUntrackedMaterializationRetainsOnlyAfterManifestWrite(t *testing.T) {
	data := testJPEG(t, 40, 60, 71)
	preview, err := NewMaterializer(newMemoryStore()).Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeLibraryPoster,
		Data:      data,
	})
	if err != nil {
		t.Fatalf("preview Materialize: %v", err)
	}

	store := newMemoryStore()
	store.failOn = preview.ManifestKey
	tracker := &recordingTracker{}
	materializer := NewMaterializer(store)
	materializer.SetRevisionTracker(tracker)
	if _, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeLibraryPoster,
		Data:      data,
	}); err == nil {
		t.Fatal("expected the manifest write to fail")
	}
	if tracker.retainCalls != 0 {
		t.Fatalf("untracked revision retained %d times before manifest success", tracker.retainCalls)
	}
}

func TestUntrackedAdoptionRetainsOnlyAfterThumbhash(t *testing.T) {
	store, err := artworkstore.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	requestData := testJPEG(t, 64, 64, 72)
	fingerprint, _ := artworkkey.ByteSourceFingerprint("upload", requestData)
	revision, err := artworkkey.BuildPortableRevision(artworkkey.RevisionInput{
		ImageType: artworkkey.ImageTypeAvatar,
		MediaType: "image/jpeg",
		Ext:       ".jpg",
		Variants: []artworkkey.VariantBytes{
			{Name: artworkkey.OriginalVariant, Data: []byte("not an image")},
		},
	})
	if err != nil {
		t.Fatalf("BuildPortableRevision: %v", err)
	}
	if err := store.WriteImmutable(t.Context(), revision.OriginalKey, []byte("not an image"), artworkstore.ObjectMetadata{MediaType: "image/jpeg"}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteImmutable(t.Context(), revision.ManifestKey, revision.ManifestJSON, artworkstore.ObjectMetadata{MediaType: "application/json"}); err != nil {
		t.Fatal(err)
	}
	if err := artworkadopt.WriteIndex(t.Context(), store, fingerprint, revision.Manifest, revision.ManifestJSON); err != nil {
		t.Fatal(err)
	}

	tracker := &recordingTracker{}
	materializer := NewMaterializer(store)
	materializer.SetRevisionTracker(tracker)
	if _, err := materializer.Materialize(t.Context(), Request{
		ImageType: artworkkey.ImageTypeAvatar,
		Data:      requestData,
		Square:    true,
	}); err == nil {
		t.Fatal("expected adopted thumbhash generation to fail")
	}
	if tracker.retainCalls != 0 {
		t.Fatalf("adopted revision retained %d times before thumbhash success", tracker.retainCalls)
	}
}

func TestMaterializeRejectsBadRequests(t *testing.T) {
	store := newMemoryStore()
	materializer := NewMaterializer(store)
	data := testJPEG(t, 40, 60, 60)

	if _, err := (*Materializer)(nil).Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeLibraryPoster,
		Data:      data,
	}); !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("nil materializer error = %v, want ErrStorageUnavailable", err)
	}
	if NewMaterializer(nil) != nil {
		t.Fatal("NewMaterializer(nil) should yield a nil Materializer")
	}
	if _, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypeLibraryPoster,
	}); err == nil {
		t.Fatal("expected empty data to be rejected")
	}
	// Catalog artwork types belong to the provider pipeline, not here.
	if _, err := materializer.Materialize(context.Background(), Request{
		ImageType: artworkkey.ImageTypePoster,
		Data:      data,
	}); err == nil {
		t.Fatal("expected a catalog image type to be rejected")
	}
	if store.count() != 0 {
		t.Fatal("a rejected request wrote objects")
	}
}
