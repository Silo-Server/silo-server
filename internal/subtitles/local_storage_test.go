package subtitles

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// Storing a subtitle whose row is here but whose object is gone (a restore
// that left provider downloads out) writes the object back under the row,
// so the subtitle plays again instead of the download "succeeding" empty.
func TestStoreSubtitleRepairsAMissingObject(t *testing.T) {
	fs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(newMockSubtitleRepo(), blobstore.NewByteStore(fs))
	ctx := context.Background()
	req := StoreSubtitleRequest{MediaFileID: 42, Provider: "opensubtitles", Language: "en", Format: FormatSRT,
		Data: []byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n")}
	stored, err := manager.StoreSubtitle(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Delete(ctx, []string{stored.S3Key}); err != nil {
		t.Fatal(err)
	}
	again, err := manager.StoreSubtitle(ctx, req)
	if err != nil || again.ID != stored.ID || again.S3Key != stored.S3Key {
		t.Fatalf("storing again = %+v, %v; want the same row %d", again, err, stored.ID)
	}
	if _, data, err := manager.GetSubtitleContent(ctx, stored.ID); err != nil || string(data) != string(req.Data) {
		t.Fatalf("content after the repair = %q, %v", data, err)
	}
}
