package diagnostics

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

func localDiagnosticsStore(t *testing.T) ObjectStore {
	t.Helper()
	fs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewLocalObjectStore(fs)
}

func TestLocalObjectStoreRoundTrip(t *testing.T) {
	store := localDiagnosticsStore(t)
	ctx := context.Background()
	key := ObjectPrefix + "7/report.tar.gz"

	if err := store.PutStream(ctx, store.Bucket(), key, strings.NewReader("bundle"), BundleContentType); err != nil {
		t.Fatal(err)
	}
	body, err := store.GetObject(ctx, store.Bucket(), key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(data) != "bundle" {
		t.Fatalf("read back %q: %v", data, err)
	}

	// Cleanup walks the diagnostics namespace by prefix; on a local backend the
	// root also holds artwork and subtitles, which it must not see.
	keys, err := store.ListObjects(ctx, ObjectPrefix)
	if err != nil || len(keys) != 1 || keys[0] != key {
		t.Fatalf("listed %v: %v", keys, err)
	}
	if err := store.DeleteObject(ctx, store.Bucket(), key); err != nil {
		t.Fatal(err)
	}
}

// readyReportBlobLocation treats an empty bucket as storage-unavailable, so a
// local store must still report a non-empty name for reports to be downloadable.
func TestLocalObjectStoreReportsANonEmptyBucket(t *testing.T) {
	if bucket := localDiagnosticsStore(t).Bucket(); strings.TrimSpace(bucket) == "" {
		t.Fatal("local store reported an empty bucket")
	}
	if NewLocalObjectStore(nil) != nil {
		t.Fatal("nil store produced a non-nil interface")
	}
}

// Orphan cleanup and report deletion both rely on IsObjectNotFound to tell an
// already-gone object from a real failure. The local backend returns its own
// sentinel, which must normalize the same way the S3 one does.
func TestLocalObjectStoreNormalizesNotFound(t *testing.T) {
	store := localDiagnosticsStore(t)
	ctx := context.Background()

	_, err := store.GetObject(ctx, store.Bucket(), ObjectPrefix+"1/absent.tar.gz")
	if !IsObjectNotFound(err) {
		t.Fatalf("err = %v, want an object-not-found", err)
	}
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("err = %v, want ErrObjectNotFound", err)
	}
}

// A filesystem cannot presign, so the admin handler falls through to streaming
// the bundle rather than returning a URL.
func TestLocalObjectStoreCannotPresign(t *testing.T) {
	store := localDiagnosticsStore(t)
	url, err := store.PresignGetURL(context.Background(), store.Bucket(), ObjectPrefix+"1/r.tar.gz", time.Minute)
	if err == nil || url != "" {
		t.Fatalf("presign returned %q, %v", url, err)
	}
	if !errors.Is(err, blobstore.ErrNoPresign) {
		t.Fatalf("err = %v, want ErrNoPresign", err)
	}
}
