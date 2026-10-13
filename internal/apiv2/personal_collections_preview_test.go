package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogsvc "github.com/Silo-Server/silo-server/internal/catalog"
)

// previewCollections answers only the smart preview. The embedded lifecycle
// interface stays nil: no other lifecycle method is called through it.
type previewCollections struct {
	personalCollectionLifecycle
	view handlers.PersonalCollectionPreviewView
	req  handlers.PersonalCollectionPreviewRequest
}

func (p *previewCollections) PreviewPersonalCollection(_ context.Context, req handlers.PersonalCollectionPreviewRequest, _ catalogsvc.AccessFilter) (handlers.PersonalCollectionPreviewView, error) {
	p.req = req
	return p.view, nil
}

func fixturePreviewView() handlers.PersonalCollectionPreviewView {
	return handlers.PersonalCollectionPreviewView{Total: 2, Items: []handlers.PersonalCollectionPreviewItemView{
		{ContentID: "movie:heat-1995", Title: "Heat", Type: "movie", PosterURL: "https://img.example.test/heat.jpg"},
		{ContentID: "movie:ronin-1998", Title: "Ronin", Type: "movie"},
	}}
}

func TestPreviewCollectionPosterURLs(t *testing.T) {
	deps, pc, _ := collectionDeps(t)
	preview := &previewCollections{view: fixturePreviewView()}
	deps.PersonalCollections = struct {
		*fakePersonalCollections
		*previewCollections
	}{pc, preview}
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, "/api/v2/collections/preview", `{"query_definition":{"match":"all","groups":[]},"limit":5}`, viewerHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if preview.req.Limit != 5 || !preview.req.WithPosters || string(preview.req.QueryDefinition) != `{"match":"all","groups":[]}` {
		t.Fatalf("request = %+v", preview.req)
	}
	var body struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 2 || len(body.Items) != 2 {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// The item with a poster carries its URL; the one without omits the member.
	if got := body.Items[0]["poster_url"]; got != "https://img.example.test/heat.jpg" {
		t.Fatalf("first poster_url = %v, body %s", got, rec.Body.String())
	}
	if _, ok := body.Items[1]["poster_url"]; ok {
		t.Fatalf("item without a poster has poster_url: %s", rec.Body.String())
	}
}
