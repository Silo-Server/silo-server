package apiv2

import (
	"context"

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
