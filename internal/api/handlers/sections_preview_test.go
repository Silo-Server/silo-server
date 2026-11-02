package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// stubPreviewFetcher is a test double that returns canned items for any inputs.
type stubPreviewFetcher struct {
	items []*models.MediaItem
	total int
}

func (s *stubPreviewFetcher) FetchOne(_ context.Context, _ sections.ResolvedSection, _ *int, _ []int, _ int, _ string, _ catalog.AccessFilter) (sections.SectionWithItems, error) {
	return sections.SectionWithItems{
		Items:      s.items,
		TotalCount: s.total,
	}, nil
}

func TestHandlePreviewRejectsUnknownType(t *testing.T) {
	h := &SectionHandler{}
	body, _ := json.Marshal(map[string]any{
		"section_type": "not_a_real_type",
		"config":       map[string]any{},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sections/preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandlePreview(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

// The v2 admin preview presigns each item's poster exactly as a live row does,
// so an admin peek shows the same image the row will show once saved.
func TestAdminSectionPreviewPresignsPostersLikeLiveRows(t *testing.T) {
	resolver := &countingSectionImageResolver{}
	detail := &catalog.DetailService{}
	detail.SetImageResolver(resolver)
	stored := &models.MediaItem{ContentID: "stored", Type: "movie", PosterPath: "/poster/original.jpg", BackdropPath: "/backdrop/original.jpg", LogoPath: "/logo/original.png"}
	h := &SectionHandler{DetailSvc: detail, previewFetcher: &stubPreviewFetcher{
		items: []*models.MediaItem{stored, {ContentID: "remote", PosterPath: "https://image.example/remote.jpg"}, {ContentID: "bare"}, nil},
		total: 4,
	}}

	result, err := h.PreviewAdminSection(t.Context(), AdminSectionPreviewRequest{SectionType: string(sections.SectionRecentlyAdded), Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 4 || result.TotalCount != 4 {
		t.Fatalf("preview items changed: %+v", result)
	}
	want := map[string]string{"stored": "resolved:/poster/w500.jpg", "remote": "https://image.example/remote.jpg"}
	if !maps.Equal(result.PosterURLs, want) {
		t.Fatalf("poster URLs = %v, want %v", result.PosterURLs, want)
	}
	if resolver.batchCalls != 1 || resolver.singleCalls != 0 || resolver.variant != "featured" {
		t.Fatalf("signing calls batch=%d single=%d variant=%q", resolver.batchCalls, resolver.singleCalls, resolver.variant)
	}
	if !slices.Equal(resolver.paths, []string{"/poster/w500.jpg"}) {
		t.Fatalf("signed paths = %v, want only the poster", resolver.paths)
	}

	live := h.buildSectionsResponse(httptest.NewRequest(http.MethodGet, "/sections", nil), []sections.SectionWithItems{{
		ResolvedSection: sections.ResolvedSection{ID: "row", SectionType: sections.SectionRecentlyAdded},
		Items:           []*models.MediaItem{stored},
	}}, nil)
	if got := live.Sections[0].Items[0].PosterURL; got != result.PosterURLs["stored"] {
		t.Fatalf("live row poster %q, preview poster %q", got, result.PosterURLs["stored"])
	}
}
