package apiv2

import (
	"encoding/json"
	"math"
	"net/http"
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

func TestRequestResponsesCarryDownload(t *testing.T) {
	h := newTestHandler(t, requestDeps(fixtureRequests()))
	type download struct {
		Phase                 string `json:"phase"`
		Percent               *int   `json:"percent"`
		BytesTotal            *int64 `json:"bytes_total"`
		BytesLeft             *int64 `json:"bytes_left"`
		EstimatedCompletionAt string `json:"estimated_completion_at"`
		Downloads             int    `json:"downloads"`
		UpdatedAt             string `json:"updated_at"`
	}
	var got struct {
		Download *download `json:"download"`
		Targets  []struct {
			Download *download `json:"download"`
		} `json:"targets"`
	}
	rec := do(t, h, http.MethodGet, "/api/v2/requests/r-1", "", requestOwner)
	decodeBody(t, rec.Body, &got)
	if rec.Code != 200 || got.Download == nil || len(got.Targets) != 1 || got.Targets[0].Download == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, d := range []*download{got.Download, got.Targets[0].Download} {
		if d.Phase != "downloading" || d.Percent == nil || *d.Percent != 43 || d.BytesTotal == nil || *d.BytesTotal != 4294967296 ||
			d.BytesLeft == nil || *d.BytesLeft != 2448131358 || d.Downloads != 1 ||
			d.EstimatedCompletionAt != "2026-01-02T03:16:05.678Z" || d.UpdatedAt != "2026-01-02T03:04:05.678Z" {
			t.Fatalf("download = %+v in %s", d, rec.Body.String())
		}
	}

	// A request without progress carries no download at all.
	rec = do(t, h, http.MethodGet, "/api/v2/requests/r-2", "", requestOwner)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"download"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestRequestMediaDetailCarriesDownload(t *testing.T) {
	svc := fixtureRequests()
	h := newTestHandler(t, requestDeps(svc))
	rec := do(t, h, http.MethodGet, "/api/v2/requests/detail/movie/949", "", requestOwner)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"download"`) {
		t.Fatalf("without progress: %d %s", rec.Code, rec.Body.String())
	}

	svc.detailDownload = fixtureDownload()
	rec = do(t, h, http.MethodGet, "/api/v2/requests/detail/movie/949", "", requestOwner)
	var got struct {
		Request struct {
			Download *struct {
				Phase   string `json:"phase"`
				Percent *int   `json:"percent"`
			} `json:"download"`
		} `json:"request"`
		Recommendations []struct {
			Request map[string]any `json:"request"`
		} `json:"recommendations"`
	}
	decodeBody(t, rec.Body, &got)
	if rec.Code != 200 || got.Request.Download == nil || got.Request.Download.Phase != "downloading" ||
		got.Request.Download.Percent == nil || *got.Request.Download.Percent != 43 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(got.Recommendations) != 1 || got.Recommendations[0].Request["download"] != nil {
		t.Fatalf("recommendations = %+v, want no download on them", got.Recommendations)
	}
}

func TestRequestStatusAdvertisesDownloadProgress(t *testing.T) {
	h := lifecycleHandler(&fakeLifecycle{}, &fakeWatchLifecycle{})
	rec := do(t, h, http.MethodGet, Prefix+"/requests/status", "", requestOwner)
	var got struct {
		DownloadProgressSupported *bool `json:"download_progress_supported"`
	}
	decodeBody(t, rec.Body, &got)
	if rec.Code != 200 || got.DownloadProgressSupported == nil || !*got.DownloadProgressSupported {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
