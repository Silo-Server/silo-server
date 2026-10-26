package catalog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestBuildPlaybackInfo_MarksProbeRejectedFilesUnreadable: a version is
// Unreadable exactly when its file is ProbeRejected (#1810), so a file that
// was never probed, kept stream data from an earlier probe, or probed
// successfully since stays playable.
func TestBuildPlaybackInfo_MarksProbeRejectedFilesUnreadable(t *testing.T) {
	failedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	probedAt := failedAt.Add(time.Hour)
	service := &DetailService{}

	versions, _, _, _, _, _, _ := service.buildPlaybackInfo(context.Background(), []*models.MediaFile{
		{ID: 1, ContentID: "movie-1", FilePath: "/media/empty.mkv", ProbeFailedAt: &failedAt},
		{ID: 2, ContentID: "movie-1", FilePath: "/media/new.mkv"},
		{ID: 3, ContentID: "movie-1", FilePath: "/media/flaky.mkv", ProbeFailedAt: &failedAt, CodecVideo: "h264"},
		{ID: 4, ContentID: "movie-1", FilePath: "/media/replaced.mkv", ProbeFailedAt: &failedAt, ProbeUpdatedAt: &probedAt},
	}, AccessFilter{}, "movie-1")

	want := map[int]bool{1: true, 2: false, 3: false, 4: false}
	if len(versions) != len(want) {
		t.Fatalf("len(versions) = %d, want %d", len(versions), len(want))
	}
	for _, version := range versions {
		if version.Unreadable != want[version.FileID] {
			t.Errorf("file %d: Unreadable = %v, want %v", version.FileID, version.Unreadable, want[version.FileID])
		}
	}

	// The frozen v1 detail serializes FileVersion directly; the flag must not
	// reach it.
	for _, version := range versions {
		data, err := json.Marshal(version)
		if err != nil {
			t.Fatalf("marshal version: %v", err)
		}
		if strings.Contains(strings.ToLower(string(data)), "unreadable") {
			t.Fatalf("FileVersion JSON carries the unreadable flag: %s", data)
		}
	}
}
