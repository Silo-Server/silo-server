package artworkstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

// fakeS3 records what the adapter asks the bucket client to do. Its object map
// is keyed by the *logical* key, which is exactly the point: the adapter must
// never pass a bucket-prefixed or otherwise decorated key down.
type fakeS3 struct {
	mu          sync.Mutex
	bucket      string
	objects     map[string][]byte
	puts        []string
	putErr      error
	statErr     error
	contentType string
	headErr     error
	bucketsSeen map[string]int
	listCalls   int
}

type longArtworkListingS3 struct {
	*fakeS3
	pagesBeforeArtwork int
	calls              int
	nonAdvancing       bool
}

func (f *longArtworkListingS3) ListObjectInfosPage(_ context.Context, _, _, cursor string, limit int) ([]s3client.ObjectInfo, string, bool, error) {
	f.calls++
	if f.nonAdvancing {
		return nil, cursor, false, nil
	}
	if f.calls <= f.pagesBeforeArtwork {
		objects := make([]s3client.ObjectInfo, limit)
		for i := range objects {
			objects[i].Key = fmt.Sprintf("unrelated/%06d/%03d", f.calls, i)
		}
		return objects, fmt.Sprintf("cursor-%d", f.calls), false, nil
	}
	return []s3client.ObjectInfo{{Key: testKey}}, "done", true, nil
}

func newFakeS3() *fakeS3 {
	return &fakeS3{
		bucket:      "artwork",
		objects:     map[string][]byte{},
		contentType: "image/webp",
		bucketsSeen: map[string]int{},
	}
}

func TestS3ArtworkEmptinessProofScansPastFormerBudget(t *testing.T) {
	client := &longArtworkListingS3{fakeS3: newFakeS3(), pagesBeforeArtwork: 65}
	store, err := NewS3Store(client)
	if err != nil {
		t.Fatal(err)
	}
	found, err := store.hasArtworkObjects(t.Context())
	if err != nil {
		t.Fatalf("hasArtworkObjects: %v", err)
	}
	if !found {
		t.Fatal("artwork after the former 16,384-object budget was not found")
	}
	if client.calls != 66 {
		t.Fatalf("listing calls = %d, want 66", client.calls)
	}
}

func TestS3ArtworkEmptinessProofRejectsNonAdvancingCursor(t *testing.T) {
	client := &longArtworkListingS3{fakeS3: newFakeS3(), nonAdvancing: true}
	store, err := NewS3Store(client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.hasArtworkObjects(t.Context())
	if err == nil || !strings.Contains(err.Error(), "listing did not advance") {
		t.Fatalf("hasArtworkObjects = %v, want non-advancing cursor error", err)
	}
}

func (f *fakeS3) Bucket() string { return f.bucket }

func (f *fakeS3) ListObjectInfosPage(_ context.Context, bucket, prefix, cursor string, limit int) ([]s3client.ObjectInfo, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(bucket)
	f.listCalls++
	keys := make([]string, 0, len(f.objects))
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) && key > cursor {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	done := len(keys) <= limit
	if !done {
		keys = keys[:limit]
	}
	objects := make([]s3client.ObjectInfo, len(keys))
	next := cursor
	for i, key := range keys {
		objects[i] = s3client.ObjectInfo{Key: key, SizeBytes: int64(len(f.objects[key]))}
		next = key
	}
	return objects, next, done, nil
}

func TestCheckMemoizesZeroPinArtworkEmptinessProof(t *testing.T) {
	client := newFakeS3()
	handle, err := Open(t.Context(), Options{Backend: BackendS3, S3: client, Settings: newFakeSettings()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	client.mu.Lock()
	client.listCalls = 0
	client.mu.Unlock()
	if err := handle.Check(t.Context()); err != nil {
		t.Fatalf("first Check: %v", err)
	}
	handle.expireCheckCacheForTest()
	if err := handle.Check(t.Context()); err != nil {
		t.Fatalf("second Check: %v", err)
	}
	client.mu.Lock()
	listCalls := client.listCalls
	client.mu.Unlock()
	if listCalls != 1 {
		t.Fatalf("zero-pin artwork listing calls = %d, want 1", listCalls)
	}
}

func (f *fakeS3) DeletePrefix(ctx context.Context, bucket, prefix string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(bucket)
	deleted := 0
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			delete(f.objects, key)
			deleted++
		}
	}
	return deleted, ctx.Err()
}

func (f *fakeS3) note(bucket string) {
	f.bucketsSeen[bucket]++
}

func (f *fakeS3) PutObject(_ context.Context, bucket, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(bucket)
	if f.putErr != nil {
		return f.putErr
	}
	f.objects[key] = append([]byte(nil), data...)
	f.puts = append(f.puts, key)
	return nil
}

func (f *fakeS3) GetObjectStream(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(bucket)
	data, ok := f.objects[key]
	if !ok {
		return nil, s3client.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeS3) StatObject(_ context.Context, bucket, key string) (s3client.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(bucket)
	if f.statErr != nil {
		return s3client.ObjectInfo{}, f.statErr
	}
	data, ok := f.objects[key]
	if !ok {
		return s3client.ObjectInfo{}, s3client.ErrNotFound
	}
	modified := time.Unix(1700000000, 0).UTC()
	return s3client.ObjectInfo{
		Key:          key,
		SizeBytes:    int64(len(data)),
		LastModified: &modified,
		ContentType:  f.contentType,
		ETag:         `"abc123"`,
	}, nil
}

func (f *fakeS3) ObjectMatches(_ context.Context, bucket, key string, data []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(bucket)
	stored, ok := f.objects[key]
	return ok && bytes.Equal(stored, data), nil
}

func (f *fakeS3) DeleteObjects(_ context.Context, bucket string, keys []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(bucket)
	for _, key := range keys {
		delete(f.objects, key)
	}
	// Mirrors the real client: already-absent keys still count as deleted.
	return len(keys), nil
}

func (f *fakeS3) HeadBucket(_ context.Context, bucket string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(bucket)
	return f.headErr
}

func newTestS3Store(t *testing.T) (*S3Store, *fakeS3) {
	t.Helper()
	client := newFakeS3()
	store, err := NewS3Store(client)
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	return store, client
}

func TestNewS3StoreRejectsMissingClientOrBucket(t *testing.T) {
	if _, err := NewS3Store(nil); err == nil {
		t.Fatal("NewS3Store(nil) succeeded")
	}
	client := newFakeS3()
	client.bucket = ""
	if _, err := NewS3Store(client); err == nil {
		t.Fatal("NewS3Store with no bucket succeeded")
	}
}

// The bucket is the adapter's private business: every call must carry the
// configured bucket, and callers only ever supply a logical key.
func TestS3StoreKeepsBucketPrivate(t *testing.T) {
	store, client := newTestS3Store(t)
	ctx := context.Background()

	if err := store.WriteImmutable(ctx, testKey, []byte("bytes"), ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable: %v", err)
	}
	if _, err := store.Stat(ctx, testKey); err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if _, err := store.DeleteObjects(ctx, []string{testKey}); err != nil {
		t.Fatalf("DeleteObjects: %v", err)
	}
	if err := store.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(client.bucketsSeen) != 1 || client.bucketsSeen["artwork"] == 0 {
		t.Fatalf("buckets used = %v, want only the configured bucket", client.bucketsSeen)
	}
	if got := client.puts; len(got) != 1 || got[0] != testKey {
		t.Fatalf("put keys = %v, want the logical key unchanged", got)
	}
}

func TestS3StoreRejectsInvalidKeysBeforeAnyRequest(t *testing.T) {
	store, client := newTestS3Store(t)
	ctx := context.Background()

	for _, key := range []string{"", "/absolute", "../escape", "a\\b", "ok/../../nope"} {
		if err := store.WriteImmutable(ctx, key, []byte("x"), ObjectMetadata{}); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("WriteImmutable(%q) = %v, want ErrInvalidKey", key, err)
		}
		if _, err := store.Stat(ctx, key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Stat(%q) = %v, want ErrInvalidKey", key, err)
		}
		if _, err := store.Matches(ctx, key, []byte("x")); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Matches(%q) = %v, want ErrInvalidKey", key, err)
		}
		if _, err := store.DeleteObjects(ctx, []string{key}); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("DeleteObjects(%q) = %v, want ErrInvalidKey", key, err)
		}
	}
	if len(client.bucketsSeen) != 0 {
		t.Fatalf("rejected keys still reached the bucket: %v", client.bucketsSeen)
	}
}

// A missing object must surface as the shared sentinel so callers treat an S3
// miss and a filesystem miss identically.
func TestS3StoreMissingObjectMapsToErrNotFound(t *testing.T) {
	store, _ := newTestS3Store(t)
	ctx := context.Background()

	if _, err := store.Stat(ctx, testKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat = %v, want ErrNotFound", err)
	}
	if _, err := store.Open(ctx, testKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open = %v, want ErrNotFound", err)
	}
}

func TestS3StoreOpenReturnsMetadataAndBody(t *testing.T) {
	store, client := newTestS3Store(t)
	ctx := context.Background()
	client.objects[testKey] = []byte("image-bytes")

	object, err := store.Open(ctx, testKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = object.Close() }()

	if object.Info.SizeBytes != int64(len("image-bytes")) {
		t.Errorf("SizeBytes = %d, want %d", object.Info.SizeBytes, len("image-bytes"))
	}
	if object.Info.MediaType != "image/webp" {
		t.Errorf("MediaType = %q, want image/webp", object.Info.MediaType)
	}
	if object.Info.ETag != `"abc123"` {
		t.Errorf("ETag = %q, want the quoted bucket ETag", object.Info.ETag)
	}
	body, err := io.ReadAll(object.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(body) != "image-bytes" {
		t.Errorf("body = %q, want image-bytes", body)
	}
}

// A backend that reports no content type must not make Silo serve artwork
// without one: the key extension is authoritative and the same on every node.
func TestS3StoreStatFallsBackToKeyDerivedMediaType(t *testing.T) {
	store, client := newTestS3Store(t)
	client.contentType = ""
	client.objects[testKey] = []byte("bytes")

	info, err := store.Stat(context.Background(), testKey)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.MediaType != "image/webp" {
		t.Fatalf("MediaType = %q, want image/webp derived from the key", info.MediaType)
	}
}

func TestS3StoreMatchesReportsContentEquality(t *testing.T) {
	store, client := newTestS3Store(t)
	ctx := context.Background()

	matched, err := store.Matches(ctx, testKey, []byte("bytes"))
	if err != nil {
		t.Fatalf("Matches on a missing object: %v", err)
	}
	if matched {
		t.Fatal("a missing object reported a content match")
	}

	client.objects[testKey] = []byte("bytes")
	matched, err = store.Matches(ctx, testKey, []byte("bytes"))
	if err != nil || !matched {
		t.Fatalf("Matches = (%v, %v), want (true, nil)", matched, err)
	}
	matched, err = store.Matches(ctx, testKey, []byte("other"))
	if err != nil || matched {
		t.Fatalf("Matches on different bytes = (%v, %v), want (false, nil)", matched, err)
	}
}

// Overwriting is deliberate on S3: objects written before the client recorded
// checksums report no match and must remain healable.
func TestS3StoreWriteImmutableOverwritesRatherThanRefusing(t *testing.T) {
	store, client := newTestS3Store(t)
	ctx := context.Background()
	client.objects[testKey] = []byte("stale")

	if err := store.WriteImmutable(ctx, testKey, []byte("fresh"), ObjectMetadata{}); err != nil {
		t.Fatalf("WriteImmutable: %v", err)
	}
	if got := string(client.objects[testKey]); got != "fresh" {
		t.Fatalf("stored bytes = %q, want fresh", got)
	}
}

func TestS3StoreDeleteObjectsCountsAbsentKeys(t *testing.T) {
	store, client := newTestS3Store(t)
	ctx := context.Background()
	client.objects[testKey] = []byte("bytes")
	other := siblingKey

	deleted, err := store.DeleteObjects(ctx, []string{testKey, other})
	if err != nil {
		t.Fatalf("DeleteObjects: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2 (an absent key counts, matching the filesystem store)", deleted)
	}
}

func TestS3StoreDeleteObjectsNoKeysMakesNoRequest(t *testing.T) {
	store, client := newTestS3Store(t)
	if deleted, err := store.DeleteObjects(context.Background(), nil); err != nil || deleted != 0 {
		t.Fatalf("DeleteObjects(nil) = (%d, %v), want (0, nil)", deleted, err)
	}
	if len(client.bucketsSeen) != 0 {
		t.Fatal("an empty delete still contacted the bucket")
	}
}

// Transport failures must reach the caller intact. Reporting a network error
// as "absent" would let the reconciler clear rows whose objects are fine.
func TestS3StoreSurfacesBackendErrors(t *testing.T) {
	store, client := newTestS3Store(t)
	ctx := context.Background()

	client.putErr = errors.New("bucket write failed")
	if err := store.WriteImmutable(ctx, testKey, []byte("x"), ObjectMetadata{}); err == nil ||
		!strings.Contains(err.Error(), "bucket write failed") {
		t.Fatalf("WriteImmutable = %v, want the backend failure", err)
	}
	client.putErr = nil

	client.statErr = errors.New("head timed out")
	_, err := store.Stat(ctx, testKey)
	if err == nil || !strings.Contains(err.Error(), "head timed out") {
		t.Fatalf("Stat = %v, want the backend failure", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("a transport failure was reported as a missing object")
	}
	client.statErr = nil

}

func TestS3StoreProbeReportsBucketFailure(t *testing.T) {
	store, client := newTestS3Store(t)
	client.headErr = errors.New("no such bucket")
	err := store.Probe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no such bucket") {
		t.Fatalf("Probe = %v, want the bucket failure", err)
	}
}

func TestS3StoreMaintenancePrefixDeleteIsLegacyOnly(t *testing.T) {
	store, client := newTestS3Store(t)
	client.objects["tmdb/movies/550/poster/original.old.webp"] = []byte("old")
	client.objects[testKey] = []byte("portable")
	deleted, err := store.DeletePrefixMaintenance(context.Background(), "tmdb/movies/550/poster/")
	if err != nil || deleted != 1 {
		t.Fatalf("maintenance delete = (%d, %v)", deleted, err)
	}
	if _, ok := client.objects[testKey]; !ok {
		t.Fatal("maintenance delete removed portable artwork")
	}
	if _, err := store.DeletePrefixMaintenance(context.Background(), "artwork/v1/"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("portable prefix delete error = %v, want ErrInvalidKey", err)
	}
}
