package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeSubtitleDownloads struct {
	calls   int
	access  catalogpkg.AccessFilter
	request subtitles.DownloadRequest
	err     error
}

func (f *fakeSubtitleDownloads) DownloadStoredSubtitle(_ context.Context, access catalogpkg.AccessFilter, req subtitles.DownloadRequest) (*subtitles.DownloadedSubtitle, error) {
	f.calls++
	f.access = access
	f.request = req
	return &subtitles.DownloadedSubtitle{ID: 9007199254740993, MediaFileID: req.MediaFileID, Provider: req.ProviderName, Language: req.Language, Format: subtitles.FormatSRT, S3Key: "PRIVATE_OBJECT", DownloadedBy: new(99), CreatedAt: fixedTime()}, f.err
}

const subtitleDownloadFixtureBody = `{"media_file_id":"42","provider":"example","subtitle_id":"opaque-provider-result","language":"en","release_name":"Synthetic","score":80,"hearing_impaired":false}`

func TestSubtitleDownloadV2AuthorityProjectionAndSingleSend(t *testing.T) {
	deps, _ := catalogDeps(t)
	f := new(fakeSubtitleDownloads)
	deps.SubtitleDownloads = f
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodPost, Prefix+"/subtitles/download", subtitleDownloadFixtureBody, viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"9007199254740993"`) || strings.Contains(rec.Body.String(), "PRIVATE") || strings.Contains(rec.Body.String(), "downloaded_by") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if f.calls != 1 || f.access.UserID != 1 || f.access.ProfileID != "p-owner" || f.request.UserID != nil || f.request.SubtitleID != "opaque-provider-result" || f.request.MediaFileID != 42 {
		t.Fatalf("wrong authority/request: %+v", f)
	}
	f.err = errors.New("PRIVATE_UPSTREAM")
	rec = do(t, h, http.MethodPost, Prefix+"/subtitles/download", subtitleDownloadFixtureBody, viewerHeaders())
	requireProblem(t, rec, TypeInternalError)
	if f.calls != 2 || strings.Contains(rec.Body.String(), "PRIVATE") {
		t.Fatal("unexpected retry or leaked failure")
	}
}

// An unregistered provider key reaches the listener as the service's 404, not
// as an opaque upstream failure.
func TestSubtitleDownloadV2UnknownProviderIsNotFound(t *testing.T) {
	deps, _ := catalogDeps(t)
	f := &fakeSubtitleDownloads{err: &handlers.APIError{Status: http.StatusNotFound, Code: "provider_not_found", Message: "Subtitle provider not found"}}
	deps.SubtitleDownloads = f
	body := strings.Replace(subtitleDownloadFixtureBody, `"provider":"example"`, `"provider":"not-registered"`, 1)
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, Prefix+"/subtitles/download", body, viewerHeaders()), TypeNotFound)
	if f.calls != 1 {
		t.Fatalf("service calls = %d, want 1", f.calls)
	}
}

func TestSubtitleDownloadV2RefusesBeforeService(t *testing.T) {
	deps, _ := catalogDeps(t)
	f := new(fakeSubtitleDownloads)
	deps.SubtitleDownloads = f
	h := newTestHandler(t, deps)
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/subtitles/download", subtitleDownloadFixtureBody, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/subtitles/download", strings.Replace(subtitleDownloadFixtureBody, `"42"`, `42`, 1), viewerHeaders()), TypeValidationFailed)
	if f.calls != 0 {
		t.Fatal("invalid request reached provider service")
	}
	deps.SubtitleDownloads = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, Prefix+"/subtitles/download", subtitleDownloadFixtureBody, viewerHeaders()), TypeDependencyUnavailable)
}
