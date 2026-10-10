package apiv2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/shuffle"
)

type fakeShuffles struct {
	err        error
	owner      shuffle.Owner
	scope      shuffle.Scope
	from, skip string
	deleted    string
}

func (f *fakeShuffles) shuffle() *shuffle.Shuffle {
	return &shuffle.Shuffle{ID: "6f1c2a51-7b8e-4a37-9a55-0f7e1a3c9b10", Scope: shuffle.Scope{Kind: shuffle.ScopeSeason, ID: "season-1"},
		Title: "Season 1", ParentTitle: "Show", CurrentContentID: "movie:heat-1995", NextContentID: "movie:heat-1995",
		CreatedAt: fixedTime(), UpdatedAt: fixedTime()}
}

func (f *fakeShuffles) Create(_ context.Context, owner shuffle.Owner, _ catalogpkg.AccessFilter, scope shuffle.Scope) (*shuffle.Shuffle, error) {
	f.owner, f.scope = owner, scope
	if f.err != nil {
		return nil, f.err
	}
	return f.shuffle(), nil
}

func (f *fakeShuffles) Get(_ context.Context, owner shuffle.Owner, _ catalogpkg.AccessFilter, id string) (*shuffle.Shuffle, error) {
	f.owner = owner
	if f.err != nil {
		return nil, f.err
	}
	return f.shuffle(), nil
}

func (f *fakeShuffles) Advance(_ context.Context, _ shuffle.Owner, _ catalogpkg.AccessFilter, _, from string) (*shuffle.Shuffle, error) {
	f.from = from
	return f.shuffle(), f.err
}

func (f *fakeShuffles) Skip(_ context.Context, _ shuffle.Owner, _ catalogpkg.AccessFilter, _, skip string) (*shuffle.Shuffle, error) {
	f.skip = skip
	return f.shuffle(), f.err
}

func (f *fakeShuffles) Delete(_ context.Context, _ shuffle.Owner, id string) error {
	f.deleted = id
	return f.err
}

func TestShuffleContract(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &fakeShuffles{}
	deps.Shuffles = fake
	h := newTestHandler(t, deps)

	capability := do(t, h, http.MethodGet, Prefix+"/shuffles/capabilities", "", viewerHeaders())
	if capability.Code != 200 || !strings.Contains(capability.Body.String(), `"state":"available"`) ||
		!strings.Contains(capability.Body.String(), `"scope_kinds":["library","series","season","library_collection","user_collection"]`) {
		t.Fatalf("capability %d %s", capability.Code, capability.Body)
	}

	created := do(t, h, http.MethodPost, Prefix+"/shuffles", `{"scope":{"kind":"season","id":"season-1"}}`, viewerHeaders())
	if created.Code != http.StatusCreated || created.Header().Get("Location") != Prefix+"/shuffles/6f1c2a51-7b8e-4a37-9a55-0f7e1a3c9b10" {
		t.Fatalf("create %d %s %v", created.Code, created.Body, created.Header())
	}
	var body Shuffle
	decodeJSON(t, created.Body, &body)
	if body.Scope != (ShuffleScope{Kind: "season", ID: "season-1", Title: "Season 1", ParentTitle: "Show"}) ||
		body.Current.ContentID != "movie:heat-1995" || body.Current.Title != "Heat" || body.Next.ContentID != "movie:heat-1995" {
		t.Fatalf("created body %+v", body)
	}
	if fake.scope != (shuffle.Scope{Kind: shuffle.ScopeSeason, ID: "season-1"}) || fake.owner.ProfileID != "p-owner" || fake.owner.UserID == 0 {
		t.Fatalf("service saw scope %+v owner %+v", fake.scope, fake.owner)
	}

	path := Prefix + "/shuffles/6f1c2a51-7b8e-4a37-9a55-0f7e1a3c9b10"
	if rec := do(t, h, http.MethodGet, path, "", viewerHeaders()); rec.Code != 200 {
		t.Fatalf("get %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodPost, path+"/advance", `{"from_content_id":"movie:heat-1995"}`, viewerHeaders()); rec.Code != 200 || fake.from != "movie:heat-1995" {
		t.Fatalf("advance %d %s from=%q", rec.Code, rec.Body, fake.from)
	}
	if rec := do(t, h, http.MethodPost, path+"/skip", `{"next_content_id":"movie:heat-1995"}`, viewerHeaders()); rec.Code != 200 || fake.skip != "movie:heat-1995" {
		t.Fatalf("skip %d %s skip=%q", rec.Code, rec.Body, fake.skip)
	}
	if rec := do(t, h, http.MethodDelete, path, "", viewerHeaders()); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || fake.deleted != "6f1c2a51-7b8e-4a37-9a55-0f7e1a3c9b10" {
		t.Fatalf("delete %d %q", rec.Code, rec.Body)
	}

	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/shuffles", `{"scope":{"kind":"playlist","id":"1"}}`, viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, path+"/advance", `{}`, viewerHeaders()), TypeValidationFailed)
}

func TestShuffleProblems(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &fakeShuffles{}
	deps.Shuffles = fake
	h := newTestHandler(t, deps)
	create := func() *httptest.ResponseRecorder {
		return do(t, h, http.MethodPost, Prefix+"/shuffles", `{"scope":{"kind":"library","id":"3"}}`, viewerHeaders())
	}

	fake.err = shuffle.ErrScopeNotFound
	requireProblem(t, create(), TypeNotFound)
	fake.err = shuffle.ErrEmpty
	requireProblem(t, create(), TypeConflict)
	fake.err = shuffle.ErrUnsupportedScope
	requireProblem(t, create(), TypeConflict)
	fake.err = shuffle.ErrNotFound
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/shuffles/missing", "", viewerHeaders()), TypeNotFound)

	deps.Shuffles = nil
	h = newTestHandler(t, deps)
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/shuffles", `{"scope":{"kind":"library","id":"3"}}`, viewerHeaders()), TypeDependencyUnavailable)
	capability := do(t, h, http.MethodGet, Prefix+"/shuffles/capabilities", "", viewerHeaders())
	if capability.Code != 200 || !strings.Contains(capability.Body.String(), `"state":"not_configured"`) || !strings.Contains(capability.Body.String(), `"allowed":false`) {
		t.Fatalf("unwired capability %d %s", capability.Code, capability.Body)
	}
}
