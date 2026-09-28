package apiv2

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeAdminSubtitleBytes struct {
	row   *subtitles.DownloadedSubtitle
	data  []byte
	err   error
	calls int
}

func (f *fakeAdminSubtitleBytes) GetAdminSubtitleBytes(_ context.Context, id int) (*subtitles.DownloadedSubtitle, []byte, error) {
	f.calls++
	if id != 9 {
		return nil, nil, subtitles.ErrSubtitleNotFound
	}
	return f.row, f.data, f.err
}
func TestAdminSubtitleBytesContract(t *testing.T) {
	fake := &fakeAdminSubtitleBytes{row: &subtitles.DownloadedSubtitle{ID: 9, Format: subtitles.FormatSRT, ReleaseName: "../危険\\quoted\"\r\n.srt", S3Key: "PRIVATE-key"}, data: []byte{0, 1, 255, 13, 10}}
	deps := requestDeps(fixtureRequests())
	deps.AdminSubtitleBytes = fake
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/subtitles/9/download"
	for _, headers := range []map[string]string{nil, {"Authorization": "Bearer member", "X-Profile-Id": "owner"}} {
		res := do(t, h, http.MethodGet, path, "", headers)
		if res.Code != 401 && res.Code != 403 {
			t.Fatalf("authorization: %d %s", res.Code, res.Body)
		}
	}
	if fake.calls != 0 {
		t.Fatal("unauthorized storage access")
	}
	for _, id := range []string{"0", "-1", "01", "+9", "999999999999999999999999", "abc"} {
		requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/subtitles/"+id+"/download", "", actingRequestAdmin), TypeValidationFailed)
	}
	if fake.calls != 0 {
		t.Fatal("invalid ID reached storage")
	}
	for _, format := range []subtitles.SubtitleFormat{subtitles.FormatSRT, subtitles.FormatVTT, subtitles.FormatASS, subtitles.FormatSSA, subtitles.FormatSUB, "unknown"} {
		fake.row.Format = format
		res := do(t, h, http.MethodGet, path, "", with(with(actingRequestAdmin, "Range", "bytes=0-0"), "If-None-Match", "*"))
		if res.Code != 200 || res.Body.String() != string(fake.data) {
			t.Fatalf("bytes: %d %q", res.Code, res.Body.String())
		}
		if res.Header().Get("Content-Type") != subtitles.SubtitleContentType(format) || res.Header().Get("Content-Length") != "5" || res.Header().Get("Cache-Control") != "no-store" || res.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("headers: %v", res.Header())
		}
		media, params, err := mime.ParseMediaType(res.Header().Get("Content-Disposition"))
		if err != nil || media != "attachment" || strings.ContainsAny(params["filename"], "/\\\r\n") {
			t.Fatalf("unsafe filename: %v %v", params, err)
		}
		if res.Header().Get("Accept-Ranges") != "" || res.Header().Get("ETag") != "" {
			t.Fatal("invented range or conditional support")
		}
	}
	for _, tc := range []struct {
		err  error
		kind ProblemType
	}{{subtitles.ErrSubtitleNotFound, TypeNotFound}, {handlers.ErrAdminSubtitleBytesUnavailable, TypeDependencyUnavailable}, {errors.New("PRIVATE storage failure"), TypeInternalError}} {
		fake.err = tc.err
		res := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
		requireProblem(t, res, tc.kind)
		if strings.Contains(res.Body.String(), "PRIVATE") || res.Header().Get("Content-Disposition") != "" {
			t.Fatal("leaked storage error or attachment headers")
		}
	}
	fake.err = nil
	fake.row = nil
	requireProblem(t, do(t, h, http.MethodGet, path, "", actingRequestAdmin), TypeNotFound)
	deps.AdminSubtitleBytes = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
}
