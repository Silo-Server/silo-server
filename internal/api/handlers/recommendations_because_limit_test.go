package handlers

import (
	"context"
	"math"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// limitRecordingEngine never queries candidates or allocates from the requested limit.
type limitRecordingEngine struct {
	fakeRecsEngine
	limit int
}

func (e *limitRecordingEngine) BecauseYouWatched(_ context.Context, _ int, _, _ string, limit int, _ catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	e.becauseCalls++
	e.limit = limit
	return scored("synthetic-neighbor"), nil
}

func TestBecauseWatchedBoundsEngineLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		want  int
	}{
		{"negative defaults", -1, 20},
		{"zero defaults", 0, 20},
		{"minimum", 1, 1},
		{"default", 20, 20},
		{"maximum", 50, 50},
		{"just above maximum", 51, 50},
		{"large", 100000, 50},
		{"max int", math.MaxInt, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &limitRecordingEngine{}
			h := NewRecommendationsHandler(engine, nil, nil, nil, nil, true)
			h.accessibleIDs = fakeAccessibleIDs{}
			items, err := h.BecauseWatched(t.Context(), 7, "synthetic-profile", "synthetic-anchor", tc.limit, catalog.AccessFilter{})
			if err != nil || len(items) != 1 {
				t.Fatalf("BecauseWatched = %v, %v; want one synthetic neighbor", items, err)
			}
			t.Logf("requested=%d admitted=%d want=%d calls=%d", tc.limit, engine.limit, tc.want, engine.becauseCalls)
			if engine.becauseCalls != 1 || engine.limit != tc.want {
				t.Fatalf("engine calls=%d limit=%d; want one call with limit=%d", engine.becauseCalls, engine.limit, tc.want)
			}
		})
	}
}

func TestV1BecauseWatchedBoundsEngineLimit(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"", 50},
		{"-1", 50},
		{"0", 50},
		{"invalid", 50},
		{"99999999999999999999999999999999999999", 50},
		{"1", 1},
		{"20", 20},
		{"50", 50},
		{"51", 50},
		{"100000", 50},
		{strconv.Itoa(math.MaxInt), 50},
	} {
		t.Run("limit="+tc.query, func(t *testing.T) {
			engine := &limitRecordingEngine{}
			h := NewRecommendationsHandler(engine, nil, nil, nil, nil, true)
			h.accessibleIDs = fakeAccessibleIDs{}
			rec := httptest.NewRecorder()
			h.HandleBecauseWatched(rec, restrictedRecsRequest("/api/v1/recommendations/because-watched/synthetic-anchor?limit="+tc.query, "synthetic-anchor"))
			if got := decodeScoredItems(t, rec); len(got) != 1 || got[0] != "synthetic-neighbor" {
				t.Fatalf("response = %v, want the synthetic neighbor", got)
			}
			t.Logf("query=%q admitted=%d want=%d calls=%d", tc.query, engine.limit, tc.want, engine.becauseCalls)
			if engine.becauseCalls != 1 || engine.limit != tc.want {
				t.Fatalf("engine calls=%d limit=%d; want one call with limit=%d", engine.becauseCalls, engine.limit, tc.want)
			}
		})
	}
}
