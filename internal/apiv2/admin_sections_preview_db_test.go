package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// prefixImageResolver signs a path by prefixing it, so a test can see which
// stored variant a response presigned.
type prefixImageResolver struct{}

func (prefixImageResolver) ResolveImageURL(_ context.Context, path, variant string) string {
	return "https://img.example/" + variant + "/" + path
}

func (r prefixImageResolver) ResolveImageURLs(ctx context.Context, paths []string, variant string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		out[path] = r.ResolveImageURL(ctx, path, variant)
	}
	return out
}

// TestAdminSectionPreviewPosterMatchesLiveRowDB: a preview over the real
// fetcher returns the same presigned poster the saved row serves the viewer,
// and never the storage key.
func TestAdminSectionPreviewPosterMatchesLiveRowDB(t *testing.T) {
	pool := viewerAccessTestPool(t)
	suffix := time.Now().UnixNano()
	libraryID := seedLibrary(t, pool, fmt.Sprintf("preview-posters-%d", suffix))
	withPoster := fmt.Sprintf("preview-poster-%d-a", suffix)
	without := fmt.Sprintf("preview-poster-%d-b", suffix)
	t.Cleanup(func() {
		mustExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{withPoster, without})
	})
	mustExec(t, pool, `INSERT INTO media_items (content_id, type, title, poster_path, poster_thumbhash) VALUES ($1, 'movie', 'With poster', 'images/preview/poster/original.jpg', 'hash-a'), ($2, 'movie', 'Without poster', '', '')`, withPoster, without)
	mustExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $3), ($2, $3)`, withPoster, without, libraryID)

	detail := &catalogpkg.DetailService{}
	detail.SetImageResolver(prefixImageResolver{})
	svc := handlers.NewSectionHandler(sections.NewRepository(pool), sections.NewFetcher(pool))
	svc.DetailSvc = detail
	deps := scopedViewerDeps(t, &access.Scope{}, svc, &fakeLibraryViews{})
	deps.AdminSections = svc
	h := newTestHandler(t, deps)

	type item struct {
		ContentID       string  `json:"content_id"`
		PosterURL       *string `json:"poster_url"`
		PosterThumbhash string  `json:"poster_thumbhash"`
	}
	postersOf := func(items []item) map[string]*string {
		out := map[string]*string{}
		for _, it := range items {
			out[it.ContentID] = it.PosterURL
		}
		return out
	}

	rec := do(t, h, http.MethodPost, Prefix+"/admin/sections/preview", fmt.Sprintf(`{"section_type":"recently_added","config":{},"item_limit":3,"library_id":"%d"}`, libraryID), bearer(adminToken))
	if rec.Code != 200 {
		t.Fatalf("preview %d %s", rec.Code, rec.Body)
	}
	var preview struct {
		Items []item `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	previewPosters := postersOf(preview.Items)
	signed := previewPosters[withPoster]
	if signed == nil || *signed != "https://img.example/featured/images/preview/poster/w500.jpg" {
		t.Fatalf("preview poster for %s = %v; body %s", withPoster, signed, rec.Body)
	}
	if got, ok := previewPosters[without]; !ok || got != nil {
		t.Fatalf("item without artwork: present=%t poster=%v; body %s", ok, got, rec.Body)
	}

	live := do(t, h, http.MethodGet, fmt.Sprintf("/api/v2/library/%d/sections/default-recently-added/items", libraryID), "", viewerHeaders())
	if live.Code != 200 {
		t.Fatalf("live row %d %s", live.Code, live.Body)
	}
	var row struct {
		Items []item `json:"items"`
	}
	if err := json.Unmarshal(live.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if got := postersOf(row.Items)[withPoster]; got == nil || *got != *signed {
		t.Fatalf("live row poster %v, preview poster %s; body %s", got, *signed, live.Body)
	}
}
