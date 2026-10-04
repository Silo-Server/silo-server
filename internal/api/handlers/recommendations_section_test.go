package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

func TestDiscoverRowSectionKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		rowType  string
		label    string
		cluster  int
		wantKind string
		wantKey  string
	}{
		{"for-you main", "cluster", "For You", 0, recommendations.SectionKindForYouMain, ""},
		{"cluster row", "cluster", "Because you enjoy Drama", 2, recommendations.SectionKindCluster, "2"},
		{"cluster zero", "cluster", "Because you enjoy Action", 0, recommendations.SectionKindCluster, "0"},
		{"similar users", "similar_users_liked", "Users Like You Also Enjoyed", 0, recommendations.SectionKindSimilarUsers, ""},
		{"popular", "popular", "Popular on This Server", 0, recommendations.SectionKindPopular, ""},
		{"recently added", "recently_added", "Recently Added", 0, recommendations.SectionKindRecentlyAdded, ""},
		{"top rated", "top_rated", "Top Rated", 0, recommendations.SectionKindTopRated, ""},
		{"highly rated", "top_rated", "Highly Rated in Your Library", 0, recommendations.SectionKindTopRated, ""},
		{"genre warm", "genre_sampler", "Popular in Drama", 0, recommendations.SectionKindGenre, "Drama"},
		{"genre cold", "genre_sampler", "Top Sci-Fi", 0, recommendations.SectionKindGenre, "Sci-Fi"},
		{"unknown row", "watch_tonight", "Watch Tonight", 0, "", ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotKind, gotKey := discoverRowSectionKey(tc.rowType, tc.label, tc.cluster)
			if gotKind != tc.wantKind || gotKey != tc.wantKey {
				t.Fatalf(
					"discoverRowSectionKey(%q,%q,%d) = (%q,%q), want (%q,%q)",
					tc.rowType, tc.label, tc.cluster, gotKind, gotKey, tc.wantKind, tc.wantKey,
				)
			}
		})
	}
}

type stubSectionReader struct {
	stubRecommendationsReader
	row *recommendations.ForYouRow
	err error
}

func (s stubSectionReader) GetSection(
	context.Context,
	int,
	string,
	string,
	string,
	int,
	catalog.AccessFilter,
) (*recommendations.ForYouRow, error) {
	return s.row, s.err
}

func TestRecommendationsHandleSection_ReturnsEnrichedRow(t *testing.T) {
	t.Parallel()

	handler := NewRecommendationsHandler(nil, stubSectionReader{
		row: &recommendations.ForYouRow{
			Type:  "genre_sampler",
			Label: "Popular in Drama",
			Items: []recommendations.ScoredItem{{MediaItemID: "movie-1"}},
		},
	}, nil, nil, nil, false)
	handler.Fetcher = stubDiscoverFetcher{
		items: []*models.MediaItem{{
			ContentID: "movie-1",
			Type:      "movie",
			Title:     "Movie One",
			Genres:    []string{"Drama"},
			Status:    "matched",
		}},
	}

	req := httptest.NewRequest(http.MethodGet, "/recommendations/section/genre/Drama", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("kind", "genre")
	rctx.URLParams.Add("key", "Drama")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 7})
	ctx = apimw.SetProfileID(ctx, "profile-1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.HandleSection(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp sectionDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Kind != "genre" || resp.Key != "Drama" {
		t.Fatalf("kind/key = %q/%q, want genre/Drama", resp.Kind, resp.Key)
	}
	if resp.Label != "Popular in Drama" {
		t.Fatalf("label = %q, want Popular in Drama", resp.Label)
	}
	if len(resp.Items) != 1 || resp.Items[0].ContentID != "movie-1" {
		t.Fatalf("items = %+v, want [movie-1]", resp.Items)
	}
}

func TestRecommendationsHandleSection_ReturnsEmptyWhenRowMissing(t *testing.T) {
	t.Parallel()

	handler := NewRecommendationsHandler(nil, stubSectionReader{row: nil}, nil, nil, nil, false)
	handler.Fetcher = stubDiscoverFetcher{}

	req := httptest.NewRequest(http.MethodGet, "/recommendations/section/popular", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("kind", "popular")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 7})
	ctx = apimw.SetProfileID(ctx, "profile-1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.HandleSection(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp sectionDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Kind != "popular" {
		t.Fatalf("kind = %q, want popular", resp.Kind)
	}
	if len(resp.Items) != 0 {
		t.Fatalf("items = %+v, want empty", resp.Items)
	}
}
