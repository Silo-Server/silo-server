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

type fakeSubtitleReads struct {
	fileID int
	access catalogpkg.AccessFilter
	err    error
}

func (f *fakeSubtitleReads) ListStoredSubtitles(_ context.Context, access catalogpkg.AccessFilter, id int) ([]subtitles.DownloadedSubtitle, error) {
	f.fileID, f.access = id, access
	return []subtitles.DownloadedSubtitle{{ID: 7, MediaFileID: id, Provider: "example", Language: "en", Format: subtitles.FormatSRT, S3Key: "PRIVATE_OBJECT", DownloadedBy: new(99), CreatedAt: fixedTime()}}, f.err
}
func (f *fakeSubtitleReads) SearchSubtitles(_ context.Context, access catalogpkg.AccessFilter, id int, _ []string) (*subtitles.SearchResponse, error) {
	f.fileID, f.access = id, access
	return &subtitles.SearchResponse{Results: []subtitles.SubtitleResult{{ID: "opaque-result", Provider: "example", Language: "en", Format: subtitles.FormatSRT}}, Warnings: []string{"PRIVATE_PROVIDER_ERROR"}}, f.err
}
func TestSubtitleReadsV2IdentityAndSafeProjection(t *testing.T) {
	deps, _ := catalogDeps(t)
	f := &fakeSubtitleReads{}
	deps.SubtitleReads = f
	h := newTestHandler(t, deps)
	list := do(t, h, http.MethodGet, Prefix+"/subtitles/42", "", viewerHeaders())
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"id":"7"`) || !strings.Contains(list.Body.String(), `"media_file_id":"42"`) || strings.Contains(list.Body.String(), "PRIVATE") || strings.Contains(list.Body.String(), "downloaded_by") {
		t.Fatalf("stored: %d %s", list.Code, list.Body.String())
	}
	search := do(t, h, http.MethodPost, Prefix+"/subtitles/search", `{"media_file_id":"42","languages":["en"]}`, viewerHeaders())
	if search.Code != 200 || strings.Contains(search.Body.String(), "PRIVATE") || strings.Contains(search.Body.String(), "upload_date") || !strings.Contains(search.Body.String(), "could not complete") {
		t.Fatalf("search: %d %s", search.Code, search.Body.String())
	}
	if f.fileID != 42 || f.access.UserID != 1 || f.access.ProfileID != "p-owner" {
		t.Fatalf("wrong access: %+v", f)
	}
	f.err = errors.New("PRIVATE_DATABASE_ERROR")
	failed := do(t, h, http.MethodGet, Prefix+"/subtitles/42", "", viewerHeaders())
	requireProblem(t, failed, TypeInternalError)
	if strings.Contains(failed.Body.String(), "PRIVATE") {
		t.Fatal("storage error leaked")
	}
}
func TestSubtitleReadsV2RejectBadIDBeforeService(t *testing.T) {
	deps, _ := catalogDeps(t)
	f := &fakeSubtitleReads{}
	deps.SubtitleReads = f
	h := newTestHandler(t, deps)
	for _, id := range []string{"0", "-1", "opaque-invalid"} {
		requireProblem(t, do(t, h, http.MethodGet, Prefix+"/subtitles/"+id, "", viewerHeaders()), TypeValidationFailed)
	}
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/subtitles/search", `{"media_file_id":42,"languages":[]}`, viewerHeaders()), TypeValidationFailed)
	if f.fileID != 0 {
		t.Fatal("invalid ID reached service")
	}
	deps.SubtitleReads = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/subtitles/42", "", viewerHeaders()), TypeDependencyUnavailable)
}
