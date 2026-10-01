package subtitles

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// A local filesystem store is a first-class subtitle backend, not just a test
// double: an install with no object storage downloads, re-reads, and deletes
// subtitles through it. The mock elsewhere in this package cannot catch a key
// the filesystem would reject or a read that does not survive a real round
// trip.
func TestStoreSubtitleRoundTripsThroughLocalStorage(t *testing.T) {
	root := t.TempDir()
	fs, err := blobstore.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(newMockSubtitleRepo(), blobstore.NewByteStore(fs))
	ctx := context.Background()
	payload := []byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n")

	stored, err := manager.StoreSubtitle(ctx, StoreSubtitleRequest{
		MediaFileID: 42, Provider: "opensubtitles", Language: "en",
		Format: FormatSRT, ReleaseName: "release", Data: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Subtitles own the "subtitles/" prefix. On a local backend that root is
	// shared with artwork and diagnostics, so the prefix is what keeps them
	// apart and the sweep away.
	if !strings.HasPrefix(stored.S3Key, "subtitles/42/") {
		t.Fatalf("key %q is outside the subtitles namespace", stored.S3Key)
	}
	if _, statErr := fs.Stat(ctx, stored.S3Key); statErr != nil {
		t.Fatalf("object not on disk: %v", statErr)
	}

	_, data, err := manager.GetSubtitleContent(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(payload) {
		t.Fatalf("read back %q, want %q", data, payload)
	}

	manager.cleanupSubtitleObject(ctx, stored.S3Key)
	if _, statErr := fs.Stat(ctx, stored.S3Key); statErr == nil {
		t.Fatal("object survived cleanup")
	}
}

// Deleting an object that is already gone is not an error: publication cleanup
// and the deletion path both run after a failure that may have removed it.
func TestLocalSubtitleDeleteToleratesAbsentObject(t *testing.T) {
	fs, err := blobstore.NewFilesystem(filepath.Join(t.TempDir(), "storage"))
	if err != nil {
		t.Fatal(err)
	}
	if err := blobstore.NewByteStore(fs).Delete(context.Background(), "subtitles/1/absent.srt"); err != nil {
		t.Fatalf("absent key reported as a failure: %v", err)
	}
}
