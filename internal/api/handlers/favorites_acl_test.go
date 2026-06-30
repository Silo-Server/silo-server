package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestHandleAddFavoriteRejectsWithoutPersonalListGrant(t *testing.T) {
	store := newProfileTestStore(t)
	authorizer := &fakeMediaConsumptionAuthorizer{
		decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"},
	}
	handler := NewPersonalDataHandler(testUserStoreProvider{store: store}, &fakeHistoryItemRepo{
		items: map[string]*models.MediaItem{
			"movie-1": {ContentID: "movie-1", Type: "movie", Title: "Movie"},
		},
	})
	handler.Authorizer = authorizer

	req := newAuthorizedProfileRequestWithRole(http.MethodPut, "/favorites/movie-1", "", "user", "profile-1")
	req = withProfileRouteParam(req, "item_id", "movie-1")
	rr := httptest.NewRecorder()

	handler.HandleAddFavorite(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !authorizer.called {
		t.Fatalf("personal list ACL authorizer was not called")
	}
	if authorizer.request.Action != auth.ActionPersonalListsManage {
		t.Fatalf("action = %q, want %q", authorizer.request.Action, auth.ActionPersonalListsManage)
	}
	ok, err := store.IsFavorite(context.Background(), "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("IsFavorite: %v", err)
	}
	if ok {
		t.Fatalf("favorite was written despite missing grant")
	}
}

func TestHandleAddToWatchlistRejectsWithoutPersonalListGrant(t *testing.T) {
	store := newProfileTestStore(t)
	authorizer := &fakeMediaConsumptionAuthorizer{
		decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"},
	}
	handler := NewPersonalDataHandler(testUserStoreProvider{store: store}, &fakeHistoryItemRepo{
		items: map[string]*models.MediaItem{
			"movie-1": {ContentID: "movie-1", Type: "movie", Title: "Movie"},
		},
	})
	handler.Authorizer = authorizer

	req := newAuthorizedProfileRequestWithRole(http.MethodPut, "/watchlist/movie-1", "", "user", "profile-1")
	req = withProfileRouteParam(req, "item_id", "movie-1")
	rr := httptest.NewRecorder()

	handler.HandleAddToWatchlist(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !authorizer.called {
		t.Fatalf("personal list ACL authorizer was not called")
	}
	if authorizer.request.Action != auth.ActionPersonalListsManage {
		t.Fatalf("action = %q, want %q", authorizer.request.Action, auth.ActionPersonalListsManage)
	}
	ok, err := store.InWatchlist(context.Background(), "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("InWatchlist: %v", err)
	}
	if ok {
		t.Fatalf("watchlist entry was written despite missing grant")
	}
}

func TestHandleRemoveFavoriteRejectsWithoutPersonalListGrant(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.AddFavorite(context.Background(), "profile-1", "movie-1"); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}
	authorizer := &fakeMediaConsumptionAuthorizer{
		decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"},
	}
	handler := NewPersonalDataHandler(testUserStoreProvider{store: store}, &fakeHistoryItemRepo{})
	handler.Authorizer = authorizer

	req := newAuthorizedProfileRequestWithRole(http.MethodDelete, "/favorites/movie-1", "", "user", "profile-1")
	req = withProfileRouteParam(req, "item_id", "movie-1")
	rr := httptest.NewRecorder()

	handler.HandleRemoveFavorite(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	ok, err := store.IsFavorite(context.Background(), "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("IsFavorite: %v", err)
	}
	if !ok {
		t.Fatalf("favorite was removed despite missing grant")
	}
}

func TestHandleRemoveFromWatchlistRejectsWithoutPersonalListGrant(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.AddToWatchlist(context.Background(), "profile-1", "movie-1"); err != nil {
		t.Fatalf("AddToWatchlist: %v", err)
	}
	authorizer := &fakeMediaConsumptionAuthorizer{
		decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"},
	}
	handler := NewPersonalDataHandler(testUserStoreProvider{store: store}, &fakeHistoryItemRepo{})
	handler.Authorizer = authorizer

	req := newAuthorizedProfileRequestWithRole(http.MethodDelete, "/watchlist/movie-1", "", "user", "profile-1")
	req = withProfileRouteParam(req, "item_id", "movie-1")
	rr := httptest.NewRecorder()

	handler.HandleRemoveFromWatchlist(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	ok, err := store.InWatchlist(context.Background(), "profile-1", "movie-1")
	if err != nil {
		t.Fatalf("InWatchlist: %v", err)
	}
	if !ok {
		t.Fatalf("watchlist entry was removed despite missing grant")
	}
}
