package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeAdminSubtitleDelete struct {
	calls    int
	revision *int64
	err      error
}

func (f *fakeAdminSubtitleDelete) DeleteAdminSubtitle(_ context.Context, _ int, revision *int64) error {
	f.calls++
	f.revision = revision
	return f.err
}
func TestAdminSubtitleDeleteContract(t *testing.T) {
	metadata := fixtureAdminSubtitleMetadata()
	deletion := &fakeAdminSubtitleDelete{}
	deps := requestDeps(fixtureRequests())
	deps.AdminSubtitleMetadata = metadata
	deps.AdminSubtitleDelete = deletion
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/subtitles/9"
	read := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	tag := read.Header().Get("ETag")
	requireProblem(t, do(t, h, http.MethodDelete, path, "", actingRequestAdmin), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", `"stale"`)), TypePreconditionFailed)
	requireProblem(t, do(t, h, http.MethodDelete, path, "", nil), TypeAuthenticationRequired)
	if deletion.calls != 0 {
		t.Fatal("refused request reached deletion")
	}
	res := do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", tag))
	if res.Code != 204 || res.Body.Len() != 0 || deletion.revision == nil || *deletion.revision != metadata.row.Revision {
		t.Fatalf("delete: %d %s %+v", res.Code, res.Body, deletion.revision)
	}
	metadata.row.Revision++
	deletion.err = &subtitles.SubtitleRevisionConflict{Current: &metadata.row}
	read = do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	tag = read.Header().Get("ETag")
	res = do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", tag))
	requireProblem(t, res, TypePreconditionFailed)
	if res.Header().Get("ETag") != tag {
		t.Fatal("race lost current validator")
	}
	for _, tc := range []struct {
		err  error
		kind ProblemType
	}{{subtitles.ErrSubtitleNotFound, TypeNotFound}, {subtitles.ErrSubtitleGuardedDeletionUnavailable, TypeDependencyUnavailable}, {errors.New("PRIVATE lost successful reply"), TypeInternalError}} {
		deletion.err = tc.err
		res = do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", tag))
		requireProblem(t, res, tc.kind)
		if strings.Contains(res.Body.String(), "PRIVATE") {
			t.Fatal("private failure escaped")
		}
	}
	deletion.err = nil
	res = do(t, h, http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", "*"))
	if res.Code != 204 || deletion.revision != nil {
		t.Fatal("wildcard did not request existence deletion")
	}
	deps.AdminSubtitleDelete = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodDelete, path, "", with(actingRequestAdmin, "If-Match", tag)), TypeDependencyUnavailable)
}
