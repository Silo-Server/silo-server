package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const deliverySRT = "1\r\n00:00:01,000 --> 00:00:02,500\r\nHello\r\n\r\n2\r\n00:00:03,000 --> 00:00:04,000\r\nWorld\r\n"

func TestDeliveryBytesIdentityPassthrough(t *testing.T) {
	data := []byte(deliverySRT)
	for _, timing := range []Timing{{}, {Scale: 1}} {
		got, err := DeliveryBytes(&DownloadedSubtitle{Format: FormatSRT, Timing: timing}, data)
		if err != nil {
			t.Fatal(err)
		}
		if &got[0] != &data[0] {
			t.Fatalf("identity timing %+v copied the bytes", timing)
		}
	}
	if got, err := DeliveryBytes(nil, data); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("nil subtitle: %q %v", got, err)
	}
}

func TestDeliveryBytesUnsupportedFormatServesStoredBytes(t *testing.T) {
	data := []byte("{1}{2}microdvd")
	got, err := DeliveryBytes(&DownloadedSubtitle{ID: 8, Format: FormatSUB, Timing: Timing{OffsetMS: 100}}, data)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("unsupported format: %q %v", got, err)
	}
}

func TestManagerGetDeliveryContent(t *testing.T) {
	blobs := newMockBlobStore()
	blobs.keys["k"] = []byte(deliverySRT)
	manager := NewManager(newMockSubtitleRepo(), blobs)
	got, err := manager.GetDeliveryContent(context.Background(), &DownloadedSubtitle{S3Key: "k", Format: FormatSRT, Timing: Timing{OffsetMS: -1000}})
	if err != nil {
		t.Fatal(err)
	}
	cues, err := ParseCues(got)
	if err != nil || len(cues) != 2 || cues[0].Start != 0 || cues[1].Start != 2*time.Second {
		t.Fatalf("delivered cues: %+v %v", cues, err)
	}
	if !bytes.Equal(blobs.keys["k"], []byte(deliverySRT)) {
		t.Fatal("delivery modified the stored bytes")
	}
}

// The frozen v1 API serializes DownloadedSubtitle directly; timing must not
// appear in it.
func TestDownloadedSubtitleV1JSONShapeUnchanged(t *testing.T) {
	sub := DownloadedSubtitle{
		ID: 1, MediaFileID: 2, Provider: "upload", Language: "en", Format: FormatSRT, ReleaseName: "r",
		Score: 1.5, HearingImpaired: true, Revision: 9, S3Key: "secret", ContentSHA256: "abc",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Timing: Timing{OffsetMS: 1200, Scale: 1.001},
	}
	encoded, err := json.Marshal(sub)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":1,"media_file_id":2,"provider":"upload","language":"en","format":"srt","release_name":"r","score":1.5,"hearing_impaired":true,"created_at":"2026-01-02T03:04:05Z"}`
	if string(encoded) != want {
		t.Fatalf("v1 JSON changed:\n%s\nwant\n%s", encoded, want)
	}
}

func TestManagerTimingPatch(t *testing.T) {
	repo := newMockSubtitleRepo()
	manager := NewManager(repo, newMockBlobStore())
	ctx := context.Background()
	srt, err := manager.Upload(ctx, UploadRequest{MediaFileID: 1, Language: "en", Filename: "a.srt", Data: []byte(deliverySRT)})
	if err != nil {
		t.Fatal(err)
	}

	revision := srt.Revision
	updated, err := manager.UpdateDownloadedSubtitleWithRevision(ctx, srt.ID, SubtitleMetadataPatch{Timing: &Timing{OffsetMS: 750}}, new(revision))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Timing != (Timing{OffsetMS: 750, Scale: 1}) || updated.Revision != revision+1 {
		t.Fatalf("timing patch stored %+v revision %d", updated.Timing, updated.Revision)
	}

	if _, err := manager.UpdateDownloadedSubtitle(ctx, srt.ID, SubtitleMetadataPatch{Timing: &Timing{Scale: 2}}); !errors.Is(err, ErrInvalidTiming) {
		t.Fatalf("out-of-range scale: %v", err)
	}
	if _, err := manager.UpdateDownloadedSubtitle(ctx, srt.ID, SubtitleMetadataPatch{Timing: &Timing{OffsetMS: 700000}}); !errors.Is(err, ErrInvalidTiming) {
		t.Fatalf("out-of-range offset: %v", err)
	}

	sub, err := manager.StoreSubtitle(ctx, StoreSubtitleRequest{MediaFileID: 1, Provider: "p", Language: "en", Format: FormatSUB, Data: []byte("{1}{2}x")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateDownloadedSubtitle(ctx, sub.ID, SubtitleMetadataPatch{Timing: &Timing{OffsetMS: 100}}); !errors.Is(err, ErrTimingUnsupported) {
		t.Fatalf("unsupported format: %v", err)
	}
	current, _ := repo.GetDownloadedSubtitle(ctx, srt.ID)
	if current.Timing != updated.Timing {
		t.Fatalf("rejected patch changed timing: %+v", current.Timing)
	}
}
