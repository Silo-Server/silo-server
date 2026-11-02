package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/models"
)

// testDownloadFiles serves media files by ID to the download service; an
// unknown ID reads as missing.
type testDownloadFiles map[int]*models.MediaFile

func (f testDownloadFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	return f[id], nil
}
func (testDownloadFiles) GetByContentID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}
func (testDownloadFiles) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}
func (testDownloadFiles) ListByEpisodeIDs(context.Context, []string) (map[string][]*models.MediaFile, error) {
	return nil, nil
}

type testDownloadUsers struct{ user *models.User }

func (u testDownloadUsers) GetByID(context.Context, int) (*models.User, error) { return u.user, nil }

type testDownloadAccess struct{}

func (testDownloadAccess) EnsureAccessible(context.Context, string, catalog.AccessFilter) error {
	return nil
}

// newTestDownloadService builds the download service the API router hands
// the compat listener, over the given files and one account. enabled is the
// server's Allow downloads setting; allowed is the account's permission.
func newTestDownloadService(enabled, allowed bool, files ...*models.MediaFile) *downloads.Service {
	cfg := config.DownloadConfig{Enabled: enabled}
	user := &models.User{ID: 1, Role: models.RoleUser, DownloadAllowed: &allowed}
	byID := testDownloadFiles{}
	for _, file := range files {
		byID[file.ID] = file
	}
	return downloads.NewService(nil, nil, nil, byID, nil, nil, testDownloadUsers{user: user}, testDownloadAccess{}, nil, &cfg)
}

// newDownloadRouteHandler is the static direct-play fixture with its one
// version at 2160p, served under the given download rules.
func newDownloadRouteHandler(t *testing.T, enabled, allowed bool) (*PlaybackHandler, string, string) {
	t.Helper()
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	file := handler.fileResolver.(testCompatFileResolver).file
	file.Resolution = "2160p"
	handler.downloads = newTestDownloadService(enabled, allowed, file)
	return handler, encodedID, body
}

func serveDownload(handler *PlaybackHandler, method, encodedID string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/Items/"+encodedID+"/Download", nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", encodedID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
	rec := httptest.NewRecorder()
	handler.HandleDownload(rec, req.WithContext(ctx))
	return rec
}

// TestHandleDownload_ServesOriginalFile verifies /Items/{id}/Download streams
// the original media file when the download rules allow it. The route backs
// CanDownload=true, which Infuse requires before it will Direct Play an item.
func TestHandleDownload_ServesOriginalFile(t *testing.T) {
	handler, encodedID, body := newDownloadRouteHandler(t, true, true)

	rec := serveDownload(handler, http.MethodGet, encodedID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != body {
		t.Errorf("expected file content %q; got %q", body, got)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("Content-Disposition = %q, want an attachment", cd)
	}
}

// TestHandleDownload_RefusesWhenDownloadsAreOff covers the issue's two
// refusals: the server's Allow downloads setting is off, or the account may
// not download. HEAD must answer like GET, and neither may send the file.
func TestHandleDownload_RefusesWhenDownloadsAreOff(t *testing.T) {
	for _, tc := range []struct {
		name             string
		enabled, allowed bool
	}{
		{name: "allow downloads off", enabled: false, allowed: true},
		{name: "account not allowed", enabled: true, allowed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, encodedID, body := newDownloadRouteHandler(t, tc.enabled, tc.allowed)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rec := serveDownload(handler, method, encodedID, nil)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("%s status = %d, want 403, body=%s", method, rec.Code, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), body) || rec.Header().Get("Content-Disposition") != "" {
					t.Fatalf("%s refusal leaked the file: headers=%v body=%q", method, rec.Header(), rec.Body.String())
				}
			}
		})
	}
}

// TestHandleDownload_ServesRequestedVersion: a multi-version item downloads
// the version mediaSourceId names, not the first one.
func TestHandleDownload_ServesRequestedVersion(t *testing.T) {
	handler, encodedID, _ := newDownloadRouteHandler(t, true, true)
	first := handler.fileResolver.(testCompatFileResolver).file
	second := &models.MediaFile{ID: 43, ContentID: first.ContentID, FilePath: filepath.Join(t.TempDir(), "movie-4k.mkv")}
	if err := os.WriteFile(second.FilePath, []byte("second version"), 0o644); err != nil {
		t.Fatal(err)
	}
	detail := handler.content.(*stubContentService).detail
	detail.Versions = append(detail.Versions, catalog.FileVersion{FileID: 43, FilePath: second.FilePath, Container: "mkv"})
	handler.downloads = newTestDownloadService(true, true, first, second)

	req := httptest.NewRequest(http.MethodGet, "/Items/"+encodedID+"/Download?mediaSourceId="+handler.codec.EncodeIntID(EncodedIDMediaSource, 43), nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", encodedID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"})
	rec := httptest.NewRecorder()
	handler.HandleDownload(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK || rec.Body.String() != "second version" {
		t.Fatalf("download = %d %q, want 200 %q", rec.Code, rec.Body.String(), "second version")
	}
}

// TestHandleDownload_AppliesViewerAccess: the viewer's scope reaches the
// download rules, so a file over the profile's quality ceiling is refused.
func TestHandleDownload_AppliesViewerAccess(t *testing.T) {
	handler, encodedID, _ := newDownloadRouteHandler(t, true, true)
	handler.accessFilter = func(context.Context, int, string) catalog.AccessFilter {
		return catalog.AccessFilter{MaxPlaybackQuality: "1080p"}
	}
	if rec := serveDownload(handler, http.MethodGet, encodedID, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a file over the quality ceiling, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleDownload_ResumesWithIfRange keeps the strong validator the route
// sent before it applied the download rules: a matching If-Range gets the rest
// of the file, a stale one the whole file.
func TestHandleDownload_ResumesWithIfRange(t *testing.T) {
	handler, encodedID, body := newDownloadRouteHandler(t, true, true)
	head := serveDownload(handler, http.MethodHead, encodedID, nil)
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", head.Code)
	}
	etag := head.Header().Get("ETag")
	if etag == "" {
		t.Skip("platform has no stable file revision for an ETag")
	}

	rec := serveDownload(handler, http.MethodGet, encodedID, map[string]string{"Range": "bytes=5-", "If-Range": etag})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != body[5:] {
		t.Fatalf("matching If-Range = %d %q, want 206 %q", rec.Code, rec.Body.String(), body[5:])
	}
	rec = serveDownload(handler, http.MethodGet, encodedID, map[string]string{"Range": "bytes=5-", "If-Range": `"stale"`})
	if rec.Code != http.StatusOK || rec.Body.String() != body {
		t.Fatalf("stale If-Range = %d %q, want 200 %q", rec.Code, rec.Body.String(), body)
	}
}

// TestHandleDownload_WithoutDownloadServiceRefuses fails closed when the API
// router built no download service.
func TestHandleDownload_WithoutDownloadServiceRefuses(t *testing.T) {
	handler, encodedID, _ := newDownloadRouteHandler(t, true, true)
	handler.downloads = nil
	if rec := serveDownload(handler, http.MethodGet, encodedID, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", rec.Code, rec.Body.String())
	}
}

// TestStaticStreamIgnoresDownloadRules: Infuse Direct Play uses Static=true
// streams, which stay playback. An account that may not download still plays.
func TestStaticStreamIgnoresDownloadRules(t *testing.T) {
	handler, encodedID, body := newDownloadRouteHandler(t, false, false)

	rec := serveStaticStream(handler, encodedID, "Static=true")
	if rec.Code != http.StatusOK || rec.Body.String() != body {
		t.Fatalf("static stream = %d %q, want 200 %q", rec.Code, rec.Body.String(), body)
	}
}

// TestItemDetail_AdvertisesCanDownload guards against regressing the
// CanDownload flag: Infuse refuses Direct Play (Static=true streaming) of
// items it believes it cannot download, so playable items must advertise it.
func TestItemDetail_AdvertisesCanDownload(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), nil)
	detail := upstreamItemDetail{
		ContentID: "movie-1",
		Type:      "movie",
		Versions: []catalog.FileVersion{{
			FileID:    42,
			Container: "mkv",
			Duration:  3600,
			AddedAt:   time.Now(),
		}},
	}
	dto := m.itemFromDetailWithFields(detail, false, nil, nil)
	if !dto.CanDownload {
		t.Error("playable item detail must advertise CanDownload=true; Infuse requires it for Direct Play")
	}
}
