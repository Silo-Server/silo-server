package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type stubTopActivitySource struct {
	activity    *AdminTopActivity
	err         error
	gotDays     int
	gotLimit    int
	invalidated int
	callCount   int
}

func (s *stubTopActivitySource) Get(_ context.Context, days, limit int) (*AdminTopActivity, error) {
	s.callCount++
	s.gotDays = days
	s.gotLimit = limit
	return s.activity, s.err
}

func (s *stubTopActivitySource) Invalidate() { s.invalidated++ }

func TestHandleGetTopActivityClampsParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		wantDays  int
		wantLimit int
	}{
		{
			name:      "defaults",
			query:     "",
			wantDays:  adminTopActivityDefaultDays,
			wantLimit: adminTopActivityDefaultLimit,
		},
		{
			name:      "explicit values pass through",
			query:     "?days=14&limit=5",
			wantDays:  14,
			wantLimit: 5,
		},
		{
			name:      "zero clamps up",
			query:     "?days=0&limit=0",
			wantDays:  adminTopActivityMinDays,
			wantLimit: adminTopActivityMinLimit,
		},
		{
			name:      "oversized clamps down",
			query:     "?days=999&limit=999",
			wantDays:  adminTopActivityMaxDays,
			wantLimit: adminTopActivityMaxLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source := &stubTopActivitySource{activity: &AdminTopActivity{}}
			handler := &AdminHandler{TopActivitySource: source}
			rec := httptest.NewRecorder()
			handler.HandleGetTopActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/top-activity"+tt.query, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
			}
			if source.gotDays != tt.wantDays || source.gotLimit != tt.wantLimit {
				t.Fatalf("days/limit = %d/%d, want %d/%d", source.gotDays, source.gotLimit, tt.wantDays, tt.wantLimit)
			}
		})
	}
}

func TestHandleGetTopActivityRejectsNonNumericParams(t *testing.T) {
	t.Parallel()

	for _, query := range []string{"?days=week", "?limit=all"} {
		t.Run(query, func(t *testing.T) {
			t.Parallel()

			source := &stubTopActivitySource{activity: &AdminTopActivity{}}
			handler := &AdminHandler{TopActivitySource: source}
			rec := httptest.NewRecorder()
			handler.HandleGetTopActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/top-activity"+query, nil))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
			if source.callCount != 0 {
				t.Fatalf("source was queried %d times for an invalid request", source.callCount)
			}
		})
	}
}

func TestHandleGetTopActivityRefreshInvalidates(t *testing.T) {
	t.Parallel()

	source := &stubTopActivitySource{activity: &AdminTopActivity{}}
	handler := &AdminHandler{TopActivitySource: source}

	rec := httptest.NewRecorder()
	handler.HandleGetTopActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/top-activity?refresh=true", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if source.invalidated != 1 {
		t.Fatalf("invalidated = %d, want 1", source.invalidated)
	}
}

func TestHandleGetTopActivitySourceFailureIs500(t *testing.T) {
	t.Parallel()

	source := &stubTopActivitySource{err: errors.New("boom")}
	handler := &AdminHandler{TopActivitySource: source}
	rec := httptest.NewRecorder()
	handler.HandleGetTopActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/top-activity", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestHandleGetTopActivityWithoutDatabase(t *testing.T) {
	t.Parallel()

	handler := &AdminHandler{}
	rec := httptest.NewRecorder()
	handler.HandleGetTopActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/top-activity", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// A server with no watch history must answer with empty lists rather than
// nulls: the bar-list widgets map over these fields directly.
func TestAdminTopActivityEmptyListsSerializeAsArrays(t *testing.T) {
	t.Parallel()

	source := &stubTopActivitySource{activity: &AdminTopActivity{
		Days:     7,
		Limit:    10,
		Titles:   []AdminTopTitle{},
		Profiles: []AdminTopProfile{},
	}}
	handler := &AdminHandler{TopActivitySource: source}
	rec := httptest.NewRecorder()
	handler.HandleGetTopActivity(rec, httptest.NewRequest(http.MethodGet, "/admin/stats/top-activity", nil))

	var body struct {
		Titles   []AdminTopTitle   `json:"titles"`
		Profiles []AdminTopProfile `json:"profiles"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Titles == nil || body.Profiles == nil {
		t.Fatalf("titles/profiles decoded as null: %s", rec.Body.String())
	}
}

func TestAdminTopActivityProviderInvalidateClearsEveryVariant(t *testing.T) {
	t.Parallel()

	provider, err := NewAdminTopActivityProvider(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(provider.Close)

	keys := []string{
		adminTopActivityCachePrefix + "7&limit=10",
		adminTopActivityCachePrefix + "30&limit=25",
	}
	for _, key := range keys {
		provider.cache.Set(key, &AdminTopActivity{}, time.Minute)
	}

	provider.Invalidate()

	for _, key := range keys {
		if _, ok := provider.cache.Get(key); ok {
			t.Fatalf("%s survived Invalidate", key)
		}
	}
}

func TestAdminTopActivityProviderWithoutPool(t *testing.T) {
	t.Parallel()

	provider, err := NewAdminTopActivityProvider(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	t.Cleanup(provider.Close)

	if _, err := provider.Get(context.Background(), 7, 10); err == nil {
		t.Fatal("expected an error from a provider with no pool")
	}
}
