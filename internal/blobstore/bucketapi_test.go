package blobstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localBucketAPI(t *testing.T) (*BucketAPI, *Filesystem) {
	t.Helper()
	fs, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewBucketAPI(fs), fs
}

func TestBucketAPIRoundTripsIgnoringBucket(t *testing.T) {
	api, fs := localBucketAPI(t)
	ctx := context.Background()
	const key = "diagnostics/7/report.tar.gz"

	if err := api.PutStream(ctx, "whatever-bucket", key, strings.NewReader("bundle"), "application/gzip"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(ctx, key); err != nil {
		t.Fatalf("object not on disk: %v", err)
	}
	// A recorded bucket from another deployment must not change where a local
	// store reads: it has one location.
	data, err := api.GetObject(ctx, "a-bucket-that-never-existed", key)
	if err != nil || string(data) != "bundle" {
		t.Fatalf("read back %q, %v", data, err)
	}
	if api.Bucket() != LocalBucket {
		t.Fatalf("bucket = %q", api.Bucket())
	}
	if err := api.DeleteObject(ctx, "another-bucket", key); err != nil {
		t.Fatal(err)
	}
	// Deleting an absent key is not an error; cleanup runs after failures that
	// may already have removed it.
	if err := api.DeleteObject(ctx, "", key); err != nil {
		t.Fatalf("absent key reported as a failure: %v", err)
	}
}

// A local reader never blocks on the network, so an upload deadline would
// otherwise elapse unnoticed and the object would still be published. The admin
// job runner's upload timeout exists to bound exactly these large writes.
func TestPutStreamStopsOnACanceledContext(t *testing.T) {
	fs, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	const key = "catalog-seeds/2026/09/16/job.json.gz"
	if err := fs.PutStream(ctx, key, strings.NewReader("a large export"), "application/gzip"); err == nil {
		t.Fatal("canceled write completed")
	}
	if _, err := fs.Stat(context.Background(), key); err == nil {
		t.Fatal("canceled write published an object")
	}
}

// Cancellation partway through must not publish a truncated object either: the
// temporary file is discarded rather than renamed.
func TestPutStreamDiscardsAPartialWriteOnCancellation(t *testing.T) {
	fs, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	const key = "diagnostics/1/report.tar.gz"
	// Cancel after the first chunk, so the copy has begun before it stops.
	reader := &cancelAfterFirstRead{cancel: cancel, data: strings.NewReader(strings.Repeat("x", 128*1024))}

	if err := fs.PutStream(ctx, key, reader, "application/gzip"); err == nil {
		t.Fatal("interrupted write completed")
	}
	if _, err := fs.Stat(context.Background(), key); err == nil {
		t.Fatal("interrupted write published a truncated object")
	}
}

type cancelAfterFirstRead struct {
	cancel context.CancelFunc
	data   *strings.Reader
	read   bool
}

func (c *cancelAfterFirstRead) Read(p []byte) (int, error) {
	n, err := c.data.Read(p)
	if !c.read {
		c.read = true
		c.cancel()
	}
	return n, err
}

func TestBucketAPIUploadFileReportsSize(t *testing.T) {
	api, fs := localBucketAPI(t)
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "export.json.gz")
	payload := []byte("a gzipped catalog, notionally")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	size, err := api.UploadFile(ctx, "", "catalog-seeds/2026/09/16/job.json.gz", source, "application/gzip")
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(payload)) {
		t.Fatalf("size = %d, want %d", size, len(payload))
	}
	info, err := fs.Stat(ctx, "catalog-seeds/2026/09/16/job.json.gz")
	if err != nil || info.Size != int64(len(payload)) {
		t.Fatalf("stored object: %+v %v", info, err)
	}
	if _, err := api.UploadFile(ctx, "", "catalog-seeds/missing.json.gz", filepath.Join(t.TempDir(), "absent"), ""); err == nil {
		t.Fatal("missing source file accepted")
	}
}

// Presigning is the one thing a filesystem cannot do. Callers must be able to
// tell that apart from a transient failure, so it reports a sentinel and
// answers the capability question directly.
func TestBucketAPIRefusesToPresign(t *testing.T) {
	api, _ := localBucketAPI(t)
	if _, err := api.PresignGetURL(context.Background(), "", "catalog-seeds/job.json.gz", 0); !errors.Is(err, ErrNoPresign) {
		t.Fatalf("err = %v, want ErrNoPresign", err)
	}
	if api.SupportsPresign() {
		t.Fatal("local storage claims presign support")
	}
}

// Listings are bounded by the prefix a caller owns. On a local backend the root
// is shared, so a diagnostics listing must not see subtitles or artwork.
func TestBucketAPIListingStaysInsideItsPrefix(t *testing.T) {
	api, _ := localBucketAPI(t)
	ctx := context.Background()
	for _, key := range []string{
		"diagnostics/1/a.tar.gz", "diagnostics/2/b.tar.gz",
		"subtitles/42/x.srt", "tmdb/movies/550/poster/original.webp",
	} {
		if err := api.PutObject(ctx, "", key, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := api.ListObjects(ctx, "", "diagnostics/")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("listed %v, want only the diagnostics namespace", keys)
	}
	infos, next, err := api.ListObjectInfosPage(ctx, "", "diagnostics/", "", 1)
	if err != nil || len(infos) != 1 || next == "" {
		t.Fatalf("page = %v next=%q err=%v", infos, next, err)
	}
	if infos[0].LastModified == nil || infos[0].SizeBytes != 1 {
		t.Fatalf("object info lost fields: %+v", infos[0])
	}
}
