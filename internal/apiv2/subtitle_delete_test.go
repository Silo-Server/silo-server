package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeViewerSubtitleDelete struct {
	row      subtitles.DownloadedSubtitle
	calls    int
	revision int64
	err      error
	access   catalogpkg.AccessFilter
}

func (f *fakeViewerSubtitleDelete) GetViewerSubtitleForDeletion(_ context.Context, a catalogpkg.AccessFilter, _ int) (*subtitles.DownloadedSubtitle, error) {
	f.access = a
	return &f.row, nil
}
func (f *fakeViewerSubtitleDelete) DeleteViewerSubtitle(_ context.Context, a catalogpkg.AccessFilter, _ int, r int64) error {
	f.calls++
	f.revision = r
	f.access = a
	return f.err
}
func TestViewerSubtitleDeleteContract(t *testing.T) {
	deps, _ := catalogDeps(t)
	f := &fakeViewerSubtitleDelete{row: subtitles.DownloadedSubtitle{ID: 9, MediaFileID: 42, Revision: 3, CreatedAt: fixedTime(), S3Key: "PRIVATE_OBJECT", DownloadedBy: new(1)}}
	deps.ViewerSubtitleDelete = f
	h := newTestHandler(t, deps)
	path := Prefix + "/subtitles/stored/9"
	read := do(t, h, http.MethodGet, path+"/metadata", "", viewerHeaders())
	tag := read.Header().Get("ETag")
	if read.Code != 200 || tag == "" || strings.Contains(read.Body.String(), "PRIVATE") || strings.Contains(read.Body.String(), "downloaded_by") {
		t.Fatalf("read %d %s", read.Code, read.Body)
	}
	cached := do(t, h, http.MethodGet, path+"/metadata", "", with(viewerHeaders(), "If-None-Match", tag))
	if cached.Code != 304 {
		t.Fatal(cached.Code)
	}
	requireProblem(t, do(t, h, http.MethodDelete, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodDelete, path, "", viewerHeaders()), TypePreconditionRequired)
	requireProblem(t, do(t, h, http.MethodDelete, path, "", with(viewerHeaders(), "If-Match", `"stale"`)), TypePreconditionFailed)
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/subtitles/stored/0", "", viewerHeaders()), TypeValidationFailed)
	if f.calls != 0 {
		t.Fatal("refusal dispatched")
	}
	deleted := do(t, h, http.MethodDelete, path, "", with(viewerHeaders(), "If-Match", tag))
	if deleted.Code != 204 || deleted.Body.Len() != 0 || f.revision != 3 || f.access.UserID != 1 || f.access.ProfileID != "p-owner" {
		t.Fatalf("delete %d %s", deleted.Code, deleted.Body)
	}
	f.row.Revision++
	requireProblem(t, do(t, h, http.MethodDelete, path, "", with(viewerHeaders(), "If-Match", tag)), TypePreconditionFailed)
	for _, tc := range []struct {
		err  error
		kind ProblemType
	}{{&subtitles.SubtitleRevisionConflict{Current: &f.row}, TypePreconditionFailed}, {subtitles.ErrSubtitleNotFound, TypeNotFound}, {subtitles.ErrSubtitleGuardedDeletionUnavailable, TypeDependencyUnavailable}, {errors.New("PRIVATE lost successful reply"), TypeInternalError}} {
		f.err = tc.err
		before := f.calls
		res := do(t, h, http.MethodDelete, path, "", with(viewerHeaders(), "If-Match", "*"))
		requireProblem(t, res, tc.kind)
		if strings.Contains(res.Body.String(), "PRIVATE") || f.calls != before+1 || f.revision != f.row.Revision {
			t.Fatal("error leaked/retried/wildcard lost CAS")
		}
	}
	deps.ViewerSubtitleDelete = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, path+"/metadata", "", viewerHeaders()), TypeDependencyUnavailable)
}
