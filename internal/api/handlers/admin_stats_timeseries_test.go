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

func TestHandleGetTimeseriesClampsHours(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "absent uses the default window", query: "", want: adminTimeseriesDefaultHours},
		{name: "empty uses the default window", query: "?hours=", want: adminTimeseriesDefaultHours},
		{name: "explicit value passes through", query: "?hours=1", want: 1},
		{name: "a week passes through", query: "?hours=168", want: 168},
		{name: "zero clamps up", query: "?hours=0", want: adminTimeseriesMinHours},
		{name: "negative clamps up", query: "?hours=-3", want: adminTimeseriesMinHours},
		{name: "the retention window is the ceiling", query: "?hours=744", want: 744},
		{name: "past retention clamps down", query: "?hours=100000", want: adminTimeseriesMaxHours},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source := &stubTimeseriesSource{series: &AdminTimeseries{}}
			handler := &AdminHandler{TimeseriesSource: source}
			rec := httptest.NewRecorder()
			handler.HandleGetTimeseries(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/timeseries"+tt.query, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
			}
			if source.gotHours != tt.want {
				t.Fatalf("hours = %d, want %d", source.gotHours, tt.want)
			}
		})
	}
}

func TestHandleGetTimeseriesRejectsNonNumericHours(t *testing.T) {
	t.Parallel()

	source := &stubTimeseriesSource{series: &AdminTimeseries{}}
	handler := &AdminHandler{TimeseriesSource: source}
	rec := httptest.NewRecorder()
	handler.HandleGetTimeseries(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/timeseries?hours=lots", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if source.callCount != 0 {
		t.Fatalf("source was queried %d times for an invalid request", source.callCount)
	}
}

func TestHandleGetTimeseriesRefreshInvalidates(t *testing.T) {
	t.Parallel()

	source := &stubTimeseriesSource{series: &AdminTimeseries{}}
	handler := &AdminHandler{TimeseriesSource: source}

	rec := httptest.NewRecorder()
	handler.HandleGetTimeseries(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/timeseries", nil))
	if source.invalidated != 0 {
		t.Fatalf("invalidated = %d on a plain read, want 0", source.invalidated)
	}

	rec = httptest.NewRecorder()
	handler.HandleGetTimeseries(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/timeseries?refresh=1", nil))
	if source.invalidated != 1 {
		t.Fatalf("invalidated = %d after refresh=1, want 1", source.invalidated)
	}
}

func TestHandleGetTimeseriesWithoutDatabase(t *testing.T) {
	t.Parallel()

	handler := &AdminHandler{}
	rec := httptest.NewRecorder()
	handler.HandleGetTimeseries(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/timeseries", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetTimeseriesFreshInstallSerializesCollectingState(t *testing.T) {
	t.Parallel()

	source := &stubTimeseriesSource{series: &AdminTimeseries{
		ResolutionSeconds: adminTimeseriesResolutionSeconds,
		Points:            []AdminTimeseriesPoint{},
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

func TestAdminTimeseriesProviderWithoutPool(t *testing.T) {
	t.Parallel()

	provider, err := NewAdminTimeseriesProvider(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(provider.Close)

	if _, err := provider.Get(context.Background(), 24); err == nil {
		t.Fatal("expected an error from a provider with no pool")
	}
}
