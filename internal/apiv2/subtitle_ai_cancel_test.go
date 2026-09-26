package apiv2

import (
	"context"
	"errors"
	"net/http"
	"testing"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

type fakeSubtitleAICancel struct {
	calls   int
	id      int64
	filter  catalogpkg.AccessFilter
	failure error
}

func (f *fakeSubtitleAICancel) CancelSubtitleAIJob(_ context.Context, filter catalogpkg.AccessFilter, id int64) error {
	f.calls++
	f.id = id
	f.filter = filter
	return f.failure
}
func TestSubtitleAICancelV2(t *testing.T) {
	deps, _ := catalogDeps(t)
	f := new(fakeSubtitleAICancel)
	deps.SubtitleAICancel = f
	h := newTestHandler(t, deps)
	path := Prefix + "/subtitles/ai/jobs/9007199254740993/cancel"
	for range 2 {
		res := do(t, h, http.MethodPost, path, "", viewerHeaders())
		if res.Code != 204 || res.Body.Len() != 0 {
			t.Fatalf("cancel=%d %s", res.Code, res.Body.String())
		}
	}
	if f.calls != 2 || f.id != 9007199254740993 || f.filter.UserID != 1 || f.filter.ProfileID != "p-owner" {
		t.Fatalf("authority=%+v", f)
	}
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/subtitles/ai/jobs/0/cancel", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, "", with(bearer(memberToken), "X-Profile-Id", "p-other")), TypeNotFound)
	if f.calls != 2 {
		t.Fatal("invalid authority or ID reached cancel")
	}
	f.failure = errors.New("PRIVATE database details")
	requireProblem(t, do(t, h, http.MethodPost, path, "", viewerHeaders()), TypeInternalError)
	deps.SubtitleAICancel = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, path, "", viewerHeaders()), TypeDependencyUnavailable)
}
