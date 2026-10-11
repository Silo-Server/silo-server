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

func TestHandlePreviewValidConfig(t *testing.T) {
	h := &SectionHandler{previewFetcher: &stubPreviewFetcher{
		items: []*models.MediaItem{{ContentID: "abc"}},
		total: 1,
	}}
	body, _ := json.Marshal(map[string]any{
		"section_type": string(sections.SectionRecentlyAdded),
		"config":       map[string]any{},
		"item_limit":   10,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sections/preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandlePreview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

// The frozen /api/v1 preview refuses a rule /api/v2 added, as it did when the
// fetcher could not parse it; the v2 preview takes the same config.
func TestAdminSectionPreviewKeepsTheV1RuleVocabulary(t *testing.T) {
	h := &SectionHandler{previewFetcher: &stubPreviewFetcher{
		items: []*models.MediaItem{{ContentID: "abc"}},
		total: 1,
	}}
	config := json.RawMessage(`{"match":"all","groups":[{"match":"all","rules":[{"field":"title","op":"begins_with","value":"the "}]}]}`)
	body, _ := json.Marshal(map[string]any{
		"section_type": string(sections.SectionCustomFilter),
		"config":       config,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sections/preview", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandlePreview(rec, req)

	if rec.Code != http.StatusInternalServerError || !bytes.Contains(rec.Body.Bytes(), []byte("preview_failed")) {
		t.Fatalf("v1 preview: status %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := h.PreviewAdminSection(context.Background(), AdminSectionPreviewRequest{SectionType: string(sections.SectionCustomFilter), Config: config}); err != nil {
		t.Fatalf("v2 preview: %v", err)
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

// Without an image signer the preview still answers, just without posters.
func TestAdminSectionPreviewWithoutSignerOmitsPosters(t *testing.T) {
	h := &SectionHandler{previewFetcher: &stubPreviewFetcher{items: []*models.MediaItem{{ContentID: "a", PosterPath: "/poster/original.jpg"}}, total: 1}}
	result, err := h.PreviewAdminSection(t.Context(), AdminSectionPreviewRequest{SectionType: string(sections.SectionRecentlyAdded), Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || len(result.PosterURLs) != 0 {
		t.Fatalf("preview without signer = %+v", result)
	}
}

// The frozen /api/v1 preview keeps its body: no signing, no new fields.
func TestHandlePreviewV1BodyUnchanged(t *testing.T) {
	resolver := &countingSectionImageResolver{}
	detail := &catalog.DetailService{}
	detail.SetImageResolver(resolver)
	h := &SectionHandler{DetailSvc: detail, previewFetcher: &stubPreviewFetcher{items: []*models.MediaItem{{ContentID: "a", PosterPath: "/poster/original.jpg"}}, total: 1}}
	body, _ := json.Marshal(map[string]any{"section_type": string(sections.SectionRecentlyAdded), "config": map[string]any{}})
	rec := httptest.NewRecorder()
	h.HandlePreview(rec, httptest.NewRequest(http.MethodPost, "/api/admin/sections/preview", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["items"] == nil || got["total_count"] == nil || resolver.batchCalls+resolver.singleCalls != 0 {
		t.Fatalf("v1 preview body changed: %s (signing calls %d)", rec.Body.String(), resolver.batchCalls+resolver.singleCalls)
	}
}

// Items that share a poster ask the signer for it once, in item order, the
// way a live row collects its paths.
func TestAdminSectionPreviewSignsEachPosterOnce(t *testing.T) {
	resolver := &countingSectionImageResolver{}
	detail := &catalog.DetailService{}
	detail.SetImageResolver(resolver)
	h := &SectionHandler{DetailSvc: detail, previewFetcher: &stubPreviewFetcher{
		items: []*models.MediaItem{
			{ContentID: "b", PosterPath: "/b/original.jpg"},
			{ContentID: "a", PosterPath: "/a/original.jpg"},
			{ContentID: "a-again", PosterPath: "/a/original.jpg"},
			{ContentID: "none"},
		},
		total: 4,
	}}

	result, err := h.PreviewAdminSection(t.Context(), AdminSectionPreviewRequest{SectionType: string(sections.SectionRecentlyAdded), Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/b/w500.jpg", "/a/w500.jpg"}; !slices.Equal(resolver.paths, want) {
		t.Fatalf("signed paths = %v, want %v", resolver.paths, want)
	}
	want := map[string]string{"b": "resolved:/b/w500.jpg", "a": "resolved:/a/w500.jpg", "a-again": "resolved:/a/w500.jpg"}
	if !maps.Equal(result.PosterURLs, want) {
		t.Fatalf("poster URLs = %v, want %v", result.PosterURLs, want)
	}
}
