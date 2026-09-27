package apiv2

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type fakeAdminAutoscanRewrites struct {
	ids   []string
	value autoscan.RewriteSuggestions
	err   error
}

func (f *fakeAdminAutoscanRewrites) ReadAdminAutoscanRewriteSuggestions(_ context.Context, id string) (autoscan.RewriteSuggestions, error) {
	f.ids = append(f.ids, id)
	return f.value, f.err
}
func TestAdminAutoscanRewriteSuggestions(t *testing.T) {
	f := &fakeAdminAutoscanRewrites{value: autoscan.RewriteSuggestions{Proposed: []autoscan.ProposedRewrite{{From: "/provider/tv", To: "/library/tv", MatchDepth: 1}}, Ambiguous: []autoscan.AmbiguousRoot{{Root: "/provider/movies", Candidates: []string{"/a/movies", "/b/movies"}}}, Unmatched: []string{"/unknown"}, Covered: []string{"/covered"}}}
	deps := pilotDeps(nil, nil)
	deps.AdminAutoscanRewrites = f
	h := NewHandler(deps)
	path := Prefix + "/admin/autoscan/sources/source-1/rewrite-suggestions"
	requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if len(f.ids) != 0 {
		t.Fatal("unauthorized provider read")
	}
	rec := do(t, h, "GET", path, "", bearer(adminToken))
	for _, want := range []string{`"match_depth":1`, `"from":"/provider/tv"`, `"candidates":["/a/movies","/b/movies"]`, `"unmatched":["/unknown"]`, `"covered":["/covered"]`} {
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Fatal(want, rec.Code, rec.Body.String())
		}
	}
	if len(f.ids) != 1 || f.ids[0] != "source-1" {
		t.Fatal(f.ids)
	}
	f.value = autoscan.RewriteSuggestions{}
	rec = do(t, h, "GET", path, "", bearer(adminToken))
	for _, want := range []string{`"proposed":[]`, `"ambiguous":[]`, `"unmatched":[]`, `"covered":[]`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatal(rec.Body.String())
		}
	}
	for _, tc := range []struct {
		err error
		p   ProblemType
	}{{autoscan.ErrNotFound, TypeNotFound}, {autoscan.ErrNoConnection, TypeValidationFailed}, {errors.New("private provider token and body"), TypeInternalError}} {
		f.err = tc.err
		rec = do(t, h, "GET", path, "", bearer(adminToken))
		requireProblem(t, rec, tc.p)
		if strings.Contains(rec.Body.String(), "private provider") {
			t.Fatal(rec.Body.String())
		}
	}
	deps.AdminAutoscanRewrites = nil
	requireProblem(t, do(t, NewHandler(deps), "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
}
