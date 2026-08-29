package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stubPlaybackActivitySource records what the handler asked for so the tests
// can assert on parameter handling without a database.
type stubPlaybackActivitySource struct {
	activity    *AdminPlaybackActivity
	err         error
	gotHours    int
	invalidated int
	callCount   int
}

func (s *stubPlaybackActivitySource) Get(_ context.Context, hours int) (*AdminPlaybackActivity, error) {
	s.callCount++
	s.gotHours = hours
	return s.activity, s.err
}

func (s *stubPlaybackActivitySource) Invalidate() { s.invalidated++ }

func TestAssemblePlaybackBuckets(t *testing.T) {
	t.Parallel()

	hourOne := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	hourTwo := time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		rows []playbackActivityRow
		want []AdminPlaybackActivityBucket
	}{
		{
			name: "no rows yields an empty slice",
			rows: nil,
			want: []AdminPlaybackActivityBucket{},
		},
		{
			name: "methods fold into one bucket per hour",
			rows: []playbackActivityRow{
				{Hour: hourOne, PlayMethod: "direct", Count: 4},
				{Hour: hourOne, PlayMethod: "transcode", Count: 2},
				{Hour: hourTwo, PlayMethod: "remux", Count: 1},
			},
			want: []AdminPlaybackActivityBucket{
				{Hour: hourOne, Direct: 4, Transcode: 2},
				{Hour: hourTwo, Remux: 1},
			},
		},
		{
			name: "history and live rows for the same hour and method add up",
			rows: []playbackActivityRow{
				{Hour: hourOne, PlayMethod: "direct", Count: 3},
				{Hour: hourOne, PlayMethod: "direct", Count: 1},
			},
			want: []AdminPlaybackActivityBucket{
				{Hour: hourOne, Direct: 4},
			},
		},
		{
			name: "unknown play methods do not create a phantom series",
			rows: []playbackActivityRow{
				{Hour: hourOne, PlayMethod: "", Count: 9},
				{Hour: hourOne, PlayMethod: "sorcery", Count: 5},
				{Hour: hourOne, PlayMethod: "direct", Count: 1},
			},
			want: []AdminPlaybackActivityBucket{
				{Hour: hourOne, Direct: 1},
			},
		},
		{
			name: "hours arrive in the query's order and keep it",
			rows: []playbackActivityRow{
				{Hour: hourTwo, PlayMethod: "direct", Count: 1},
				{Hour: hourOne, PlayMethod: "direct", Count: 1},
			},
			want: []AdminPlaybackActivityBucket{
				{Hour: hourTwo, Direct: 1},
				{Hour: hourOne, Direct: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := assemblePlaybackBuckets(tt.rows)
			if len(got) != len(tt.want) {
				t.Fatalf("buckets = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("bucket %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCompletionRate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		completed int64
		finalized int64
		want      float64
	}{
		{name: "no finalized sessions is zero, not NaN", finalized: 0, completed: 0, want: 0},
		{name: "live-only window stays zero", finalized: 0, completed: 5, want: 0},
		{name: "everything completed", finalized: 4, completed: 4, want: 1},
		{name: "rounded to four decimals", finalized: 38, completed: 27, want: 0.7105},
		{name: "nothing completed", finalized: 9, completed: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := completionRate(tt.completed, tt.finalized); got != tt.want {
				t.Fatalf("completionRate(%d, %d) = %v, want %v", tt.completed, tt.finalized, got, tt.want)
			}
		})
	}
}

func TestPlaybackActivityBucketSeconds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		hours     int
		want      int
		wantTrunc string
	}{
		{name: "the minimum window is hourly", hours: adminPlaybackActivityMinHours, want: 3600, wantTrunc: "hour"},
		{name: "a day is hourly", hours: 24, want: 3600, wantTrunc: "hour"},
		{name: "two days is the last hourly window", hours: 48, want: 3600, wantTrunc: "hour"},
		{name: "just past two days becomes daily", hours: 49, want: 86400, wantTrunc: "day"},
		{name: "a week is daily", hours: 168, want: 86400, wantTrunc: "day"},
		{name: "the maximum window is daily", hours: adminPlaybackActivityMaxHours, want: 86400, wantTrunc: "day"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := playbackActivityBucketSeconds(tt.hours)
			if got != tt.want {
				t.Fatalf("playbackActivityBucketSeconds(%d) = %d, want %d", tt.hours, got, tt.want)
			}
			if trunc := playbackActivityTruncField(got); trunc != tt.wantTrunc {
				t.Fatalf("playbackActivityTruncField(%d) = %q, want %q", got, trunc, tt.wantTrunc)
			}
		})
	}
}

func TestHandleGetPlaybackActivityClampsHours(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "absent uses the default window", query: "", want: adminPlaybackActivityDefaultHours},
		{name: "empty uses the default window", query: "?hours=", want: adminPlaybackActivityDefaultHours},
		{name: "explicit value passes through", query: "?hours=6", want: 6},
		{name: "a week passes through", query: "?hours=168", want: 168},
		{name: "zero clamps up", query: "?hours=0", want: adminPlaybackActivityMinHours},
		{name: "negative clamps up", query: "?hours=-12", want: adminPlaybackActivityMinHours},
		{name: "a month is the ceiling", query: "?hours=744", want: 744},
		{name: "oversized clamps down", query: "?hours=100000", want: adminPlaybackActivityMaxHours},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source := &stubPlaybackActivitySource{activity: &AdminPlaybackActivity{}}
			handler := &AdminHandler{PlaybackActivitySource: source}
			rec := httptest.NewRecorder()
			handler.HandleGetPlaybackActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/playback-activity"+tt.query, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
			}
			if source.gotHours != tt.want {
				t.Fatalf("hours = %d, want %d", source.gotHours, tt.want)
			}
		})
	}
}

func TestHandleGetPlaybackActivityRejectsNonNumericHours(t *testing.T) {
	t.Parallel()

	source := &stubPlaybackActivitySource{activity: &AdminPlaybackActivity{}}
	handler := &AdminHandler{PlaybackActivitySource: source}
	rec := httptest.NewRecorder()
	handler.HandleGetPlaybackActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/playback-activity?hours=soon", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if source.callCount != 0 {
		t.Fatalf("source was queried %d times for an invalid request", source.callCount)
	}
}

func TestHandleGetPlaybackActivityRefreshInvalidates(t *testing.T) {
	t.Parallel()

	source := &stubPlaybackActivitySource{activity: &AdminPlaybackActivity{}}
	handler := &AdminHandler{PlaybackActivitySource: source}

	rec := httptest.NewRecorder()
	handler.HandleGetPlaybackActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/playback-activity", nil))
	if source.invalidated != 0 {
		t.Fatalf("invalidated = %d on a plain read, want 0", source.invalidated)
	}

	rec = httptest.NewRecorder()
	handler.HandleGetPlaybackActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/playback-activity?refresh=1", nil))
	if source.invalidated != 1 {
		t.Fatalf("invalidated = %d after refresh=1, want 1", source.invalidated)
	}
}

func TestHandleGetPlaybackActivityWithoutDatabase(t *testing.T) {
	t.Parallel()

	handler := &AdminHandler{}
	rec := httptest.NewRecorder()
	handler.HandleGetPlaybackActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/playback-activity", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetPlaybackActivityEmptyWindowSerializesArrays(t *testing.T) {
	t.Parallel()

	source := &stubPlaybackActivitySource{activity: &AdminPlaybackActivity{
		Hours:         24,
		BucketSeconds: adminPlaybackActivityHourSeconds,
		Buckets:       []AdminPlaybackActivityBucket{},
	}}
	handler := &AdminHandler{PlaybackActivitySource: source}
	rec := httptest.NewRecorder()
	handler.HandleGetPlaybackActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/playback-activity", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Hours         int               `json:"hours"`
		BucketSeconds int               `json:"bucket_seconds"`
		Buckets       []json.RawMessage `json:"buckets"`
		Reliability   struct {
			CompletionRate float64 `json:"completion_rate"`
		} `json:"reliability"`
		ProfilesActive24h int64 `json:"profiles_active_24h"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Buckets == nil {
		t.Fatal("buckets decoded as null; a data-free server must send []")
	}
	if len(body.Buckets) != 0 || body.ProfilesActive24h != 0 || body.Reliability.CompletionRate != 0 {
		t.Fatalf("unexpected body: %+v", body)
	}
	// The client zero-fills from bucket_seconds, so it has to survive
	// serialization even on a server with nothing to report.
	if body.BucketSeconds != adminPlaybackActivityHourSeconds {
		t.Fatalf("bucket_seconds = %d, want %d", body.BucketSeconds, adminPlaybackActivityHourSeconds)
	}
}

func TestAdminPlaybackActivityProviderInvalidateClearsEveryWindow(t *testing.T) {
	t.Parallel()

	provider, err := NewAdminPlaybackActivityProvider(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(provider.Close)

	provider.cache.Set(adminPlaybackActivityCachePrefix+"1", &AdminPlaybackActivity{Hours: 1}, time.Minute)
	provider.cache.Set(adminPlaybackActivityCachePrefix+"24", &AdminPlaybackActivity{Hours: 24}, time.Minute)

	provider.Invalidate()

	for _, key := range []string{adminPlaybackActivityCachePrefix + "1", adminPlaybackActivityCachePrefix + "24"} {
		if _, ok := provider.cache.Get(key); ok {
			t.Fatalf("%s survived Invalidate", key)
		}
	}
}

func TestAdminPlaybackActivityProviderWithoutPool(t *testing.T) {
	t.Parallel()

	provider, err := NewAdminPlaybackActivityProvider(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(provider.Close)

	if _, err := provider.Get(context.Background(), 24); err == nil {
		t.Fatal("expected an error from a provider with no pool")
	}
}
