package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stubTimeseriesSource records what the handler asked for so the tests can
// assert on parameter handling without a database.
type stubTimeseriesSource struct {
	series      *AdminTimeseries
	err         error
	gotHours    int
	invalidated int
	callCount   int
}

func (s *stubTimeseriesSource) Get(_ context.Context, hours int) (*AdminTimeseries, error) {
	s.callCount++
	s.gotHours = hours
	return s.series, s.err
}

func (s *stubTimeseriesSource) Invalidate() { s.invalidated++ }

func int64Ptr(value int64) *int64 { return &value }

func TestAssembleTimeseries(t *testing.T) {
	t.Parallel()

	minuteOne := time.Date(2026, 8, 26, 11, 58, 0, 0, time.UTC)
	minuteTwo := time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC)

	tests := []struct {
		name string
		rows []timeseriesRow
		want []AdminTimeseriesPoint
	}{
		{
			name: "no samples yields an empty slice",
			rows: nil,
			want: []AdminTimeseriesPoint{},
		},
		{
			name: "a full minute maps straight through",
			rows: []timeseriesRow{{
				Bucket:             minuteOne,
				Streams:            int64Ptr(3),
				Direct:             int64Ptr(1),
				Remux:              int64Ptr(0),
				Transcode:          int64Ptr(2),
				EgressKbps:         48_211,
				DownloadEgressKbps: 6_100,
			}},
			want: []AdminTimeseriesPoint{
				{T: minuteOne, Streams: 3, Direct: 1, Remux: 0, Transcode: 2, EgressKbps: 48_211, DownloadEgressKbps: 6_100},
			},
		},
		{
			name: "a minute with only process egress reads as zero streams",
			rows: []timeseriesRow{{Bucket: minuteOne, EgressKbps: 900}},
			want: []AdminTimeseriesPoint{{T: minuteOne, EgressKbps: 900}},
		},
		{
			name: "gaps are left as gaps, not zero-filled",
			rows: []timeseriesRow{
				{Bucket: minuteOne, Streams: int64Ptr(1), EgressKbps: 10},
				{Bucket: minuteTwo, Streams: int64Ptr(2), EgressKbps: 20},
			},
			want: []AdminTimeseriesPoint{
				{T: minuteOne, Streams: 1, EgressKbps: 10},
				{T: minuteTwo, Streams: 2, EgressKbps: 20},
			},
		},
		{
			name: "buckets are reported in UTC",
			rows: []timeseriesRow{{Bucket: minuteOne.In(time.FixedZone("UTC-5", -5*60*60)), Streams: int64Ptr(1)}},
			want: []AdminTimeseriesPoint{{T: minuteOne, Streams: 1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := assembleTimeseries(tt.rows)
			if len(got) != len(tt.want) {
				t.Fatalf("points = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if !got[i].T.Equal(tt.want[i].T) {
					t.Fatalf("point %d time = %s, want %s", i, got[i].T, tt.want[i].T)
				}
				// Equal ignores the location, so the instant matching is not
				// enough: the wire format is UTC, and a dropped .UTC() would
				// serialize an offset the charts do not expect.
				if loc := got[i].T.Location(); loc != time.UTC {
					t.Fatalf("point %d location = %s, want UTC", i, loc)
				}
				if got[i].Streams != tt.want[i].Streams ||
					got[i].Direct != tt.want[i].Direct ||
					got[i].Remux != tt.want[i].Remux ||
					got[i].Transcode != tt.want[i].Transcode ||
					got[i].EgressKbps != tt.want[i].EgressKbps ||
					got[i].DownloadEgressKbps != tt.want[i].DownloadEgressKbps {
					t.Fatalf("point %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSumTransferredBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rows         []timeseriesRow
		wantEgress   int64
		wantDownload int64
	}{
		{
			name: "no samples transfer nothing",
		},
		{
			// One minute at 8 kbps is 8,000 bits/s for 60 s: 60,000 bytes.
			name:       "one minute converts kbps to bytes",
			rows:       []timeseriesRow{{EgressKbpsSum: 8}},
			wantEgress: 60_000,
		},
		{
			// The totals integrate the per-minute sums, never the bucket
			// peaks: a bucket whose peak minute hit 1,000 kbps but whose
			// minutes added up to 1,200 kbps moved 1,200 kbps-minutes.
			name: "buckets integrate the sum, not the peak",
			rows: []timeseriesRow{
				{EgressKbps: 1_000, EgressKbpsSum: 1_200, DownloadEgressKbps: 400, DownloadEgressKbpsSum: 500},
				{EgressKbps: 50, EgressKbpsSum: 50},
			},
			wantEgress:   1_250 * bytesPerKbpsMinute,
			wantDownload: 500 * bytesPerKbpsMinute,
		},
		{
			// A month of minutes at 10 Gbps must not overflow.
			name:       "a busy month fits in int64",
			rows:       []timeseriesRow{{EgressKbpsSum: 10_000_000 * 31 * 24 * 60}},
			wantEgress: 10_000_000 * 31 * 24 * 60 * bytesPerKbpsMinute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			egress, download := sumTransferredBytes(tt.rows)
			if egress != tt.wantEgress || download != tt.wantDownload {
				t.Fatalf("sumTransferredBytes = (%d, %d), want (%d, %d)", egress, download, tt.wantEgress, tt.wantDownload)
			}
		})
	}
}

func TestTimeseriesBucketSeconds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		hours int
		want  int
	}{
		{name: "the minimum window keeps sampled minutes", hours: adminTimeseriesMinHours, want: 60},
		{name: "two hours is the last minute-resolution window", hours: 2, want: 60},
		{name: "just past two hours steps to five minutes", hours: 3, want: 300},
		{name: "a day is five minutes", hours: 24, want: 300},
		{name: "two days is the last five-minute window", hours: 48, want: 300},
		{name: "just past two days steps to half an hour", hours: 49, want: 1800},
		{name: "a week is half an hour", hours: 168, want: 1800},
		{name: "a fortnight is the last half-hour window", hours: 336, want: 1800},
		{name: "just past a fortnight steps to two hours", hours: 337, want: 7200},
		{name: "the maximum window is two hours", hours: adminTimeseriesMaxHours, want: 7200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := timeseriesBucketSeconds(tt.hours)
			if got != tt.want {
				t.Fatalf("timeseriesBucketSeconds(%d) = %d, want %d", tt.hours, got, tt.want)
			}
			// The point budget is the reason the buckets exist; a threshold
			// that drifts past it silently regresses every wide window.
			if points := tt.hours * 3600 / got; points > adminTimeseriesMaxPoints {
				t.Fatalf("a %d-hour window yields %d points at %ds buckets, over the %d budget",
					tt.hours, points, got, adminTimeseriesMaxPoints)
			}
		})
	}
}

func TestHandleGetTimeseriesFreshInstallSerializesCollectingState(t *testing.T) {
	t.Parallel()

	source := &stubTimeseriesSource{series: &AdminTimeseries{
		ResolutionSeconds:   adminTimeseriesResolutionSeconds,
		EgressBytes:         1,
		DownloadEgressBytes: 1,
		Points:              []AdminTimeseriesPoint{},
	}}
	handler := &AdminHandler{TimeseriesSource: source}
	rec := httptest.NewRecorder()
	handler.HandleGetTimeseries(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/timeseries", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		ResolutionSeconds int               `json:"resolution_seconds"`
		Points            []json.RawMessage `json:"points"`
		OldestSampleAt    *string           `json:"oldest_sample_at"`
		EgressBytes       *int64            `json:"egress_bytes"`
		DownloadBytes     *int64            `json:"download_egress_bytes"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Points == nil {
		t.Fatal("points decoded as null; a server with no samples must send []")
	}
	if len(body.Points) != 0 {
		t.Fatalf("points = %v, want empty", body.Points)
	}
	if body.OldestSampleAt != nil {
		t.Fatalf("oldest_sample_at = %v, want null before the first sample", *body.OldestSampleAt)
	}
	if body.ResolutionSeconds != adminTimeseriesResolutionSeconds {
		t.Fatalf("resolution_seconds = %d, want %d", body.ResolutionSeconds, adminTimeseriesResolutionSeconds)
	}
	// /api/v1 is frozen: the window totals are v2-only and must not leak into
	// the bridge contract.
	if body.EgressBytes != nil || body.DownloadBytes != nil {
		t.Fatalf("egress_bytes = %v, download_egress_bytes = %v, want both absent from v1", body.EgressBytes, body.DownloadBytes)
	}
}

func TestAdminTimeseriesProviderInvalidateClearsEveryWindow(t *testing.T) {
	t.Parallel()

	provider, err := NewAdminTimeseriesProvider(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(provider.Close)

	provider.cache.Set(adminTimeseriesCachePrefix+"1", &AdminTimeseries{}, time.Minute)
	provider.cache.Set(adminTimeseriesCachePrefix+"24", &AdminTimeseries{}, time.Minute)

	provider.Invalidate()

	for _, key := range []string{adminTimeseriesCachePrefix + "1", adminTimeseriesCachePrefix + "24"} {
		if _, ok := provider.cache.Get(key); ok {
			t.Fatalf("%s survived Invalidate", key)
		}
	}
}
