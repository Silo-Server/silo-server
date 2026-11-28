package chapterthumbs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/models"
)

// noPacketsAfter fakes a video whose last packet is at lastPacket: a seek past
// it fails as ffmpeg does, and records every seek. The nth failure has the nth
// of reasons, or chapter_extract_failed past their end.
func noPacketsAfter(lastPacket float64, seeks *[]float64, failure error, reasons []string) func(context.Context, *models.MediaFile, float64, string) ([]byte, string, error) {
	return func(_ context.Context, _ *models.MediaFile, seek float64, _ string) ([]byte, string, error) {
		*seeks = append(*seeks, seek)
		if seek > lastPacket {
			reason := reasonChapterExtractFailed
			if len(reasons) > 0 {
				reason, reasons = reasons[0], reasons[1:]
			}
			return nil, reason, failure
		}
		return []byte(fmt.Sprintf("frame@%.3f", seek)), "", nil
	}
}

var errNoPackets = errors.New("chapter_extract_failed: ffmpeg sampling failed (exit): exit status 234: [out#0/image2pipe @ 0x0] Nothing was written into output file, because at least one of its streams received no packets")

// A chapter whose capture time falls after the last video packet is tried at
// its own start, which has a frame when the chapter starts on or before that
// packet (#5317). One that starts after the video still fails, with both
// errors and the capture time's reason, and a failure of another kind or
// with its own reason is not retried.
func TestExtractChapterFrameRetriesAtTheChapterStart(t *testing.T) {
	// The video's last packet is at 9.958 s, as in the issue's test file.
	const lastPacket = 9.958
	for _, tc := range []struct {
		name      string
		chapter   models.MediaChapter
		failure   error
		reasons   []string
		wantSeeks []float64
		wantFrame string
		wantErr   bool
	}{
		{name: "capture time has a frame", chapter: models.MediaChapter{StartSeconds: 0, EndSeconds: 9.5}, failure: errNoPackets, wantSeeks: []float64{2.375}, wantFrame: "frame@2.375"},
		{name: "start before the last packet", chapter: models.MediaChapter{StartSeconds: 9.5, EndSeconds: 15}, failure: errNoPackets, wantSeeks: []float64{10.875, 9.5}, wantFrame: "frame@9.500"},
		{name: "start after the video", chapter: models.MediaChapter{StartSeconds: 15, EndSeconds: 20}, failure: errNoPackets, wantSeeks: []float64{16.25, 15}, wantErr: true},
		{name: "retry's own reason", chapter: models.MediaChapter{StartSeconds: 15, EndSeconds: 20}, failure: errNoPackets, reasons: []string{reasonChapterExtractFailed, reasonDecodeInvalidData}, wantSeeks: []float64{16.25, 15}, wantErr: true},
		{name: "no frame with its own reason", chapter: models.MediaChapter{StartSeconds: 9.5, EndSeconds: 15}, failure: errNoPackets, reasons: []string{reasonCPUTimeout}, wantSeeks: []float64{10.875}, wantErr: true},
		{name: "empty output", chapter: models.MediaChapter{StartSeconds: 9.5, EndSeconds: 15}, failure: &mediasample.Error{Reason: mediasample.ReasonEmpty}, wantSeeks: []float64{10.875, 9.5}, wantFrame: "frame@9.500"},
		{name: "node's empty output", chapter: models.MediaChapter{StartSeconds: 9.5, EndSeconds: 15}, failure: errors.New("chapter thumbnail remote extract: chapter_extract_failed: ffmpeg sampling failed (empty)"), wantSeeks: []float64{10.875, 9.5}, wantFrame: "frame@9.500"},
		{name: "other failure", chapter: models.MediaChapter{StartSeconds: 9.5, EndSeconds: 15}, failure: errors.New("chapter_extract_failed: exit status 1: Invalid data found when processing input"), wantSeeks: []float64{10.875}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seeks []float64
			s := &Service{extractFrameFunc: noPacketsAfter(lastPacket, &seeks, tc.failure, tc.reasons)}

			frame, reason, err := s.extractChapterFrame(t.Context(), &models.MediaFile{ID: 1}, tc.chapter, "")

			if !slices.Equal(seeks, tc.wantSeeks) {
				t.Fatalf("seeks = %v, want %v", seeks, tc.wantSeeks)
			}
			if tc.wantErr {
				wantReason := reasonChapterExtractFailed
				if len(tc.reasons) > 0 {
					wantReason = tc.reasons[0]
				}
				if err == nil || reason != wantReason {
					t.Fatalf("extractChapterFrame() = %q, %q, %v; want a %s error", frame, reason, err, wantReason)
				}
				if !errors.Is(err, tc.failure) {
					t.Fatalf("error %v does not carry the capture-time failure", err)
				}
				return
			}
			if err != nil || string(frame) != tc.wantFrame || reason != "" {
				t.Fatalf("extractChapterFrame() = %q, %q, %v; want %q", frame, reason, err, tc.wantFrame)
			}
		})
	}
}
