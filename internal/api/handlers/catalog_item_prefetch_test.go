package handlers

import (
	"context"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type viewerStateStore struct {
	userstore.UserStore
}

func (viewerStateStore) IsFavorite(context.Context, string, string) (bool, error) { return false, nil }
func (viewerStateStore) InWatchlist(context.Context, string, string) (bool, error) {
	return false, nil
}

type viewerStateStores struct{}

func (viewerStateStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return viewerStateStore{}, nil
}
func (viewerStateStores) Close() error { return nil }

type recordingPromoter struct{ promoted []string }

func (p *recordingPromoter) PromoteWatchlistItem(_ context.Context, _ catalog.AccessFilter, contentID string) {
	p.promoted = append(p.promoted, contentID)
}

// A prefetch warms a client cache for a title the viewer may never open, so it
// must not promote a watchlist entry the way opening the title does.
func TestItemDetailPrefetchDoesNotPromoteWatchlist(t *testing.T) {
	promoter := &recordingPromoter{}
	h := NewCatalogResourceHandler(&ItemsHandler{storeProvider: viewerStateStores{}})
	h.SetWatchlistPromoter(promoter)
	ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: 7})
	viewer := ItemViewer{ProfileID: "p1"}

	h.enrichViewerState(ctx, viewer, &catalog.ItemDetail{ContentID: "movie:heat-1995", Type: "movie"}, true)
	if len(promoter.promoted) != 0 {
		t.Fatalf("prefetch promoted %v", promoter.promoted)
	}

	detail := &catalog.ItemDetail{ContentID: "movie:heat-1995", Type: "movie"}
	h.enrichViewerState(ctx, viewer, detail, false)
	if len(promoter.promoted) != 1 || promoter.promoted[0] != "movie:heat-1995" {
		t.Fatalf("view promoted %v, want the item once", promoter.promoted)
	}
	if detail.UserState == nil {
		t.Fatal("view did not report user_state")
	}
}
