package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func (testDownloadAccess) EnsureAccessibleIDs(_ context.Context, ids []string, _ catalog.AccessFilter) (map[string]bool, error) {
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	return allowed, nil
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

// TestHandleDownload_WithoutDownloadServiceRefuses fails closed when the API
// router built no download service.
func TestHandleDownload_WithoutDownloadServiceRefuses(t *testing.T) {
	handler, encodedID, _ := newDownloadRouteHandler(t, true, true)
	handler.downloads = nil
	if rec := serveDownload(handler, http.MethodGet, encodedID, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", rec.Code, rec.Body.String())
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
