package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
)

func TestHandleTasteSeedRejectsWithoutPersonalListGrant(t *testing.T) {
	store := newProfileTestStore(t)
	authorizer := &fakeMediaConsumptionAuthorizer{
		decision: auth.AccessDecision{Allowed: false, ReasonCode: "default_deny"},
	}
	handler := NewRecommendationsHandler(nil, nil, testUserStoreProvider{store: store}, nil, nil, false)
	handler.Authorizer = authorizer

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPost,
		"/recommendations/taste-seed",
		`{"item_ids":["movie-1"]}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()

	handler.HandleTasteSeed(rr, req)

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
