package apiv2

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

func TestRequestDownloadOf(t *testing.T) {
	updated := fixedTime()
	eta := updated.Add(10 * time.Minute)
	progress := func(total, left int64) *mediarequests.DownloadProgress {
		return &mediarequests.DownloadProgress{Phase: mediarequests.DownloadPhaseDownloading, BytesTotal: total, BytesLeft: left, Downloads: 1, UpdatedAt: updated}
	}
	for _, tc := range []struct {
		name        string
		total, left int64
		want        int
	}{
		{"just started", 1000, 1000, 0},
		{"rounded down", 3, 1, 66},
		{"almost done", 1000, 1, 99},
		{"done", 1000, 0, 100},
		{"left above total", 1000, 5000, 0},
		{"negative left", 1000, -5, 100},
		{"huge", math.MaxInt64, math.MaxInt64 / 2, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := requestDownloadOf(progress(tc.total, tc.left))
			if got.Percent == nil || *got.Percent != tc.want {
				t.Fatalf("percent = %v, want %d", got.Percent, tc.want)
			}
			if got.BytesTotal == nil || *got.BytesTotal != tc.total || got.BytesLeft == nil || *got.BytesLeft < 0 || *got.BytesLeft > tc.total {
				t.Fatalf("bytes = %v/%v, want the total and a left within it", got.BytesTotal, got.BytesLeft)
			}
		})
	}

	if requestDownloadOf(nil) != nil {
		t.Fatal("no progress must map to no download")
	}
	unknown := requestDownloadOf(&mediarequests.DownloadProgress{Phase: mediarequests.DownloadPhaseQueued, Downloads: -1, UpdatedAt: updated, EstimatedCompletion: &eta})
	if unknown.Percent != nil || unknown.BytesTotal != nil || unknown.BytesLeft != nil {
		t.Fatalf("unknown size: %+v, want no percent and no byte counts", unknown)
	}
	if unknown.Phase != "queued" || unknown.Downloads != 0 || unknown.EstimatedCompletionAt == nil || !unknown.EstimatedCompletionAt.Equal(eta) || !unknown.UpdatedAt.Equal(updated) {
		t.Fatalf("unknown size: %+v", unknown)
	}
	raw, err := json.Marshal(requestDownloadOf(progress(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"percent", "bytes_total", "bytes_left", "estimated_completion_at"} {
		if strings.Contains(string(raw), `"`+field+`"`) {
			t.Fatalf("%s: %s must be omitted", raw, field)
		}
	}
}
