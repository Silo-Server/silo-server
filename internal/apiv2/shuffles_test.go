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
