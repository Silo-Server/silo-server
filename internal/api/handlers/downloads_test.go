package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	policyengine "github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// fakeDownloadService is a programmable DownloadService for handler tests. It
// records the identity (user/profile/device) it was called with so tests can
// assert the handler threads header-only device authority correctly.
type fakeDownloadService struct {
	capability     downloads.Capability
	created        *downloads.Download
	createErr      error
	series         []*downloads.Download
	seriesID       string
	seriesErr      error
	list           []*downloads.Download
	listErr        error
	deleteErr      error
	patchErr       error
	serveErr       error
	serveBody      string
	directBody     string
	directErr      error
	manifest       *downloads.OfflineManifest
	batchManifests []*downloads.OfflineManifest
	batchSkipped   []downloads.SkippedManifest
	manifestErr    error
	artworkErr     error
	subtitleErr    error

	gotCreateReq     downloads.CreateRequest
	gotSeriesReq     downloads.CreateRequest
	gotList          identityCall
	gotServe         identityCall
	gotDelete        identityCall
	gotPatch         identityCall
	gotPatchStatus   string
	gotManifest      identityCall
	gotBatchManifest identityCall
	gotArtwork       identityCall
	gotArtworkKind   string
	gotSubtitle      identityCall
	gotSubtitleRef   string
	gotDirectFormat  string
	gotDirectFileID  int

	// Season download + series-monitoring (subscription) fakes.
	season       []*downloads.Download
	seasonID     string
	seasonErr    error
	gotSeasonReq downloads.CreateRequest
	gotSeasonNum int
	seasonCalled bool

	subResult      *downloads.SubscriptionResult
	subList        []*downloads.Subscription
	sub            *downloads.Subscription
	subErr         error
	subDeleteErr   error
	gotSubReq      downloads.SubscriptionRequest
	gotSubPatch    downloads.SubscriptionPatch
	gotSubIdent    identityCall
	syncRegistered int
}

type identityCall struct {
	userID     int
	profileID  string
	deviceID   string
	downloadID string
}

type proxyDownloadService struct {
	*fakeDownloadService
	directTarget  *downloads.FileTarget
	managedTarget *downloads.FileTarget
	resolveErr    error
}

func (s *proxyDownloadService) ResolveDirectFile(context.Context, int, int, string, catalog.AccessFilter) (*downloads.FileTarget, error) {
	return s.directTarget, s.resolveErr
}

func (s *proxyDownloadService) ResolveManagedFile(context.Context, int, string, string, string, catalog.AccessFilter) (*downloads.FileTarget, error) {
	return s.managedTarget, s.resolveErr
}

func (f *fakeDownloadService) Capability(context.Context, int) (downloads.Capability, error) {
	return f.capability, nil
}

func (f *fakeDownloadService) Create(_ context.Context, _ int, req downloads.CreateRequest, _ catalog.AccessFilter) (*downloads.Download, error) {
	f.gotCreateReq = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.created, nil
}

func (f *fakeDownloadService) CreateSeries(_ context.Context, _ int, req downloads.CreateRequest, _ catalog.AccessFilter) ([]*downloads.Download, string, []downloads.SkippedDownload, error) {
	f.gotSeriesReq = req
	if f.seriesErr != nil {
		return nil, "", nil, f.seriesErr
	}
	return f.series, f.seriesID, nil, nil
}

func (f *fakeDownloadService) CreateSeason(_ context.Context, _ int, req downloads.CreateRequest, seasonNumber int, _ catalog.AccessFilter) ([]*downloads.Download, string, []downloads.SkippedDownload, error) {
	f.gotSeasonReq = req
	f.gotSeasonNum = seasonNumber
	f.seasonCalled = true
	if f.seasonErr != nil {
		return nil, "", nil, f.seasonErr
	}
	return f.season, f.seasonID, nil, nil
}

func (f *fakeDownloadService) CreateSubscription(_ context.Context, _ int, req downloads.SubscriptionRequest, _ catalog.AccessFilter) (*downloads.SubscriptionResult, error) {
	f.gotSubReq = req
	if f.subErr != nil {
		return nil, f.subErr
	}
	return f.subResult, nil
}

func (f *fakeDownloadService) ListSubscriptions(_ context.Context, userID int, profileID, deviceID string) ([]*downloads.Subscription, error) {
	f.gotSubIdent = identityCall{userID: userID, profileID: profileID, deviceID: deviceID}
	return f.subList, f.subErr
}

func (f *fakeDownloadService) GetSubscription(_ context.Context, userID int, profileID, deviceID, id string) (*downloads.Subscription, error) {
	f.gotSubIdent = identityCall{userID, profileID, deviceID, id}
	if f.subErr != nil {
		return nil, f.subErr
	}
	return f.sub, nil
}

func (f *fakeDownloadService) UpdateSubscription(_ context.Context, userID int, profileID, deviceID, id string, patch downloads.SubscriptionPatch, _ catalog.AccessFilter) (*downloads.SubscriptionResult, error) {
	f.gotSubIdent = identityCall{userID, profileID, deviceID, id}
	f.gotSubPatch = patch
	if f.subErr != nil {
		return nil, f.subErr
	}
	return f.subResult, nil
}

func (f *fakeDownloadService) DeleteSubscription(_ context.Context, userID int, profileID, deviceID, id string) error {
	f.gotSubIdent = identityCall{userID, profileID, deviceID, id}
	return f.subDeleteErr
}

func (f *fakeDownloadService) SyncSubscriptions(_ context.Context, userID int, profileID, deviceID string, _ catalog.AccessFilter) (int, error) {
	f.gotSubIdent = identityCall{userID: userID, profileID: profileID, deviceID: deviceID}
	if f.subErr != nil {
		return 0, f.subErr
	}
	return f.syncRegistered, nil
}

func (f *fakeDownloadService) ServeDirect(_ context.Context, w http.ResponseWriter, _ *http.Request, _, fileID int, format string, _ catalog.AccessFilter) error {
	f.gotDirectFileID = fileID
	f.gotDirectFormat = format
	if f.directBody != "" {
		_, _ = w.Write([]byte(f.directBody))
	}
	if f.directErr != nil {
		return f.directErr
	}
	_, _ = w.Write([]byte("served"))
	return nil
}

func (f *fakeDownloadService) ServeFile(_ context.Context, w http.ResponseWriter, _ *http.Request, userID int, profileID, deviceID, downloadID string, _ catalog.AccessFilter) error {
	f.gotServe = identityCall{userID, profileID, deviceID, downloadID}
	if f.serveBody != "" {
		_, _ = w.Write([]byte(f.serveBody))
	}
	if f.serveErr != nil {
		return f.serveErr
	}
	_, _ = w.Write([]byte("served"))
	return nil
}

func (f *fakeDownloadService) List(_ context.Context, userID int, profileID, deviceID string) ([]*downloads.Download, error) {
	f.gotList = identityCall{userID: userID, profileID: profileID, deviceID: deviceID}
	return f.list, f.listErr
}

func (f *fakeDownloadService) Delete(_ context.Context, userID int, profileID, deviceID, downloadID string) error {
	f.gotDelete = identityCall{userID, profileID, deviceID, downloadID}
	return f.deleteErr
}

func (f *fakeDownloadService) PatchStatus(_ context.Context, userID int, profileID, deviceID, downloadID, status string) error {
	f.gotPatch = identityCall{userID, profileID, deviceID, downloadID}
	f.gotPatchStatus = status
	return f.patchErr
}

func (f *fakeDownloadService) BuildManifest(_ context.Context, userID int, profileID, deviceID, downloadID string, _ catalog.AccessFilter) (*downloads.OfflineManifest, error) {
	f.gotManifest = identityCall{userID, profileID, deviceID, downloadID}
	if f.manifestErr != nil {
		return nil, f.manifestErr
	}
	return f.manifest, nil
}

func (f *fakeDownloadService) BuildBatchManifests(_ context.Context, userID int, profileID, deviceID, batchID string, _ catalog.AccessFilter) ([]*downloads.OfflineManifest, []downloads.SkippedManifest, error) {
	f.gotBatchManifest = identityCall{userID, profileID, deviceID, batchID}
	if f.manifestErr != nil {
		return nil, nil, f.manifestErr
	}
	return f.batchManifests, f.batchSkipped, nil
}

func (f *fakeDownloadService) ServeArtwork(_ context.Context, w http.ResponseWriter, _ *http.Request, userID int, profileID, deviceID, downloadID, kind string, _ catalog.AccessFilter) error {
	f.gotArtwork = identityCall{userID, profileID, deviceID, downloadID}
	f.gotArtworkKind = kind
	if f.artworkErr != nil {
		return f.artworkErr
	}
	_, _ = w.Write([]byte("img"))
	return nil
}

func (f *fakeDownloadService) ServeSubtitle(_ context.Context, w http.ResponseWriter, _ *http.Request, userID int, profileID, deviceID, downloadID, ref string, _ catalog.AccessFilter) error {
	f.gotSubtitle = identityCall{userID, profileID, deviceID, downloadID}
	f.gotSubtitleRef = ref
	if f.subtitleErr != nil {
		return f.subtitleErr
	}
	_, _ = w.Write([]byte("WEBVTT"))
	return nil
}

// downloadTestRequest builds a request with auth claims and optional profile +
// device identity (as the viewer-access middleware / client headers would set).
func downloadTestRequest(method, target string, body []byte, userID int, profileID, deviceID string) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if userID != 0 {
		ctx := apimw.SetClaims(r.Context(), &auth.Claims{
			UserID:    userID,
			Role:      "user",
			TokenType: auth.TokenTypeAccess,
		})
		if profileID != "" {
			ctx = apimw.SetProfileID(ctx, profileID)
		}
		r = r.WithContext(ctx)
	}
	if deviceID != "" {
		r.Header.Set("X-Silo-Device-Id", deviceID)
	}
	return r
}

// A transfer that fails after the file's headers and first bytes went out
// aborts the response instead of appending a JSON 500, and logs only when the
// failure is the server's: a client that left has canceled the request.
func TestHandleDirectDownloadAbortsAfterResponseCommitted(t *testing.T) {
	routes := map[string]func(*DownloadHandler, http.ResponseWriter, *http.Request){
		"direct": (*DownloadHandler).HandleDirectDownload,
		"proxy":  (*DownloadHandler).HandleDirectDownloadViaProxy,
	}
	for name, handle := range routes {
		for _, clientLeft := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/client_left=%v", name, clientLeft), func(t *testing.T) {
				var logs bytes.Buffer
				previous := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
				t.Cleanup(func() { slog.SetDefault(previous) })

				svc := &fakeDownloadService{
					directBody: "partial",
					directErr:  fmt.Errorf("serving download: %w", downloads.ErrResponseCommitted),
				}
				req := downloadTestRequest(http.MethodGet, "/direct-download?file_id=42", nil, 7, "", "")
				if clientLeft {
					ctx, cancel := context.WithCancel(req.Context())
					cancel()
					req = req.WithContext(ctx)
				}
				rec := httptest.NewRecorder()
				func() {
					defer func() {
						if got := recover(); got != http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
							t.Fatalf("panic = %v, want http.ErrAbortHandler", got)
						}
					}()
					handle(NewDownloadHandler(svc), rec, req)
				}()

				if rec.Body.String() != "partial" {
					t.Fatalf("body = %q, want the partial file only", rec.Body.String())
				}
				logged := strings.Contains(logs.String(), "direct download failed after the response started")
				if logged == clientLeft || strings.Contains(logs.String(), "level=ERROR") {
					t.Fatalf("client left = %v, logs = %q", clientLeft, logs.String())
				}
			})
		}
	}
}

func TestHandleDirectDownloadViaProxyRedirectsToProxy(t *testing.T) {
	const secret = "download-proxy-secret"
	maxJobs := 1
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	svc := &proxyDownloadService{
		fakeDownloadService: &fakeDownloadService{},
		directTarget:        &downloads.FileTarget{Path: "/media/movie.mkv", MediaFileID: 42, ProxyEligible: true},
	}
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{URL: proxy.URL + "/", Enabled: true, Healthy: true, MaxJobs: &maxJobs}})
	planner := nodepool.NewPlanner(proxies, nodepool.NewTranscodePool())
	h := NewDownloadHandler(svc)
	h.SetProxyDelivery(planner, func() string { return secret })
	rec := httptest.NewRecorder()
	h.HandleDirectDownloadViaProxy(rec, downloadTestRequest(http.MethodHead, "/direct-download-proxy?file_id=42", nil, 7, "", ""))

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307 (body: %s)", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	claims, err := streamtoken.Verify(strings.TrimPrefix(location, proxy.URL+"/downloads/file/"), secret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.MediaPath != "/media/movie.mkv" || claims.MediaFileID != 42 {
		t.Fatalf("claims = %+v", claims)
	}
	if svc.gotDirectFileID != 0 {
		t.Fatalf("local ServeDirect called for file %d", svc.gotDirectFileID)
	}
	// HEAD never becomes an active proxy transfer, so it must return the only
	// available slot immediately rather than waiting for reservation expiry.
	if plan := planner.PlanDownload("after-head"); plan.ProxyNode == nil {
		t.Fatal("HEAD request retained the proxy's only job slot")
	}
	planner.ReleaseSession("after-head")
}

func TestHandleDirectDownloadFallsBackWithoutHealthyProxy(t *testing.T) {
	svc := &proxyDownloadService{
		fakeDownloadService: &fakeDownloadService{},
		directTarget:        &downloads.FileTarget{Path: "/media/movie.mkv", MediaFileID: 42, ProxyEligible: true},
	}
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{URL: "https://proxy.example", Enabled: true, Healthy: false}})
	h := NewDownloadHandler(svc)
	h.SetProxyDelivery(nodepool.NewPlanner(proxies, nodepool.NewTranscodePool()), func() string { return "secret" })
	rec := httptest.NewRecorder()
	h.HandleDirectDownloadViaProxy(rec, downloadTestRequest(http.MethodGet, "/direct-download-proxy?file_id=42", nil, 7, "", ""))

	if rec.Code != http.StatusOK || svc.gotDirectFileID != 42 {
		t.Fatalf("status = %d file = %d body = %s", rec.Code, svc.gotDirectFileID, rec.Body.String())
	}
}

func TestHandleDirectDownloadFallsBackWhenProxyCannotServePath(t *testing.T) {
	proxy := httptest.NewServer(http.NotFoundHandler())
	defer proxy.Close()
	svc := &proxyDownloadService{
		fakeDownloadService: &fakeDownloadService{},
		directTarget:        &downloads.FileTarget{Path: "/media/movie.mkv", MediaFileID: 42, ProxyEligible: true},
	}
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{URL: proxy.URL, Enabled: true, Healthy: true}})
	h := NewDownloadHandler(svc)
	h.SetProxyDelivery(nodepool.NewPlanner(proxies, nodepool.NewTranscodePool()), func() string { return "secret" })
	rec := httptest.NewRecorder()
	h.HandleDirectDownloadViaProxy(rec, downloadTestRequest(http.MethodGet, "/direct-download-proxy?file_id=42", nil, 7, "", ""))

	if rec.Code != http.StatusOK || svc.gotDirectFileID != 42 {
		t.Fatalf("status = %d file = %d body = %s", rec.Code, svc.gotDirectFileID, rec.Body.String())
	}
}

func TestHandleDirectDownloadEstablishedRouteKeepsLocalSuccessStatus(t *testing.T) {
	proxyRequests := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	svc := &proxyDownloadService{
		fakeDownloadService: &fakeDownloadService{},
		directTarget:        &downloads.FileTarget{Path: "/media/movie.mkv", MediaFileID: 42, ProxyEligible: true},
	}
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{URL: proxy.URL, Enabled: true, Healthy: true}})
	h := NewDownloadHandler(svc)
	h.SetProxyDelivery(nodepool.NewPlanner(proxies, nodepool.NewTranscodePool()), func() string { return "secret" })
	rec := httptest.NewRecorder()
	h.HandleDirectDownload(rec, downloadTestRequest(http.MethodGet, "/direct-download?file_id=42", nil, 7, "", ""))

	if rec.Code != http.StatusOK || svc.gotDirectFileID != 42 || proxyRequests != 0 {
		t.Fatalf("status = %d file = %d proxy requests = %d", rec.Code, svc.gotDirectFileID, proxyRequests)
	}
}

func TestHandleDirectDownloadViaProxyStaysLocalWhenTargetIneligible(t *testing.T) {
	proxyRequests := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	svc := &proxyDownloadService{
		fakeDownloadService: &fakeDownloadService{},
		directTarget:        &downloads.FileTarget{Path: "/media/movie.mkv", MediaFileID: 42, ProxyEligible: false},
	}
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{URL: proxy.URL, Enabled: true, Healthy: true}})
	h := NewDownloadHandler(svc)
	h.SetProxyDelivery(nodepool.NewPlanner(proxies, nodepool.NewTranscodePool()), func() string { return "secret" })
	rec := httptest.NewRecorder()
	h.HandleDirectDownloadViaProxy(rec, downloadTestRequest(http.MethodGet, "/direct-download-proxy?file_id=42", nil, 7, "", ""))

	if rec.Code != http.StatusOK || svc.gotDirectFileID != 42 || proxyRequests != 0 {
		t.Fatalf("status = %d file = %d proxy requests = %d", rec.Code, svc.gotDirectFileID, proxyRequests)
	}
}

// An unknown file_id reaches the handler as the catalog not-found sentinel the
// download service now returns, on every direct-download variant.
func TestHandleDirectDownloadUnknownFileIsNotFound(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run("direct_"+method, func(t *testing.T) {
			svc := &fakeDownloadService{directErr: catalog.ErrItemNotFound}
			h := NewDownloadHandler(svc)
			rec := httptest.NewRecorder()
			h.HandleDirectDownload(rec, downloadTestRequest(method, "/direct-download?file_id=4242", nil, 7, "", ""))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
			}
		})
		t.Run("proxy_"+method, func(t *testing.T) {
			svc := &proxyDownloadService{fakeDownloadService: &fakeDownloadService{}, resolveErr: catalog.ErrItemNotFound}
			h := NewDownloadHandler(svc)
			h.SetProxyDelivery(nodepool.NewPlanner(nodepool.NewProxyPool(), nodepool.NewTranscodePool()), func() string { return "secret" })
			rec := httptest.NewRecorder()
			h.HandleDirectDownloadViaProxy(rec, downloadTestRequest(method, "/direct-download-proxy?file_id=4242", nil, 7, "", ""))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// ceilingOverrideDecider stands in for a custom action override that keeps
// every download grant but narrows the download quality ceiling to 1080p.
type ceilingOverrideDecider struct{}

func (ceilingOverrideDecider) CheckAction(context.Context, policyengine.ActionInput) (policyengine.ActionDecision, policyengine.Meta, error) {
	return policyengine.ActionDecision{Allowed: true, QualityCeiling: "1080p"}, policyengine.Meta{}, nil
}

type singleFileRepo struct {
	downloads.FileResolver
	file *models.MediaFile
}

func (r singleFileRepo) GetByID(context.Context, int) (*models.MediaFile, error) { return r.file, nil }

type singleUserRepo struct{ user *models.User }

func (r singleUserRepo) GetByID(context.Context, int) (*models.User, error) { return r.user, nil }

type openItemAccess struct{}

func (openItemAccess) EnsureAccessible(context.Context, string, catalog.AccessFilter) error {
	return nil
}

type countingLinkIssuer struct{ calls int }

func (i *countingLinkIssuer) GenerateDirectDownloadLinkToken(int, string, string, string, *int, int) (string, time.Time, error) {
	i.calls++
	return "link-token", time.Now().Add(time.Minute), nil
}

// overrideCeilingDownloadService is the real download service over one
// on-disk file of the given resolution, with a 1080p override ceiling.
func overrideCeilingDownloadService(t *testing.T, resolution string) *downloads.Service {
	t.Helper()
	path := filepath.Join(t.TempDir(), "movie-"+resolution+".mkv")
	if err := os.WriteFile(path, []byte("original bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	allowed := true
	cfg := config.DownloadConfig{Enabled: true}
	svc := downloads.NewService(nil, nil, nil,
		singleFileRepo{file: &models.MediaFile{ID: 42, ContentID: "movie", FilePath: path, Resolution: resolution}},
		nil, nil, singleUserRepo{user: &models.User{ID: 7, DownloadAllowed: &allowed}}, openItemAccess{}, nil, &cfg)
	svc.SetActionDecider(ceilingOverrideDecider{})
	return svc
}

// TestDirectDownloadRefusesOriginalAboveOverrideCeiling is the #2234
// regression: every direct-download entry point (GET and HEAD, the
// proxy-aware variant, and link minting) refuses a 2160p original under a
// 1080p override ceiling with the policy denial, and serves nothing.
func TestDirectDownloadRefusesOriginalAboveOverrideCeiling(t *testing.T) {
	svc := overrideCeilingDownloadService(t, "2160p")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run("direct_"+method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			NewDownloadHandler(svc).HandleDirectDownload(rec, downloadTestRequest(method, "/direct-download?file_id=42", nil, 7, "", ""))
			if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "original bytes") {
				t.Fatalf("status = %d body = %q, want 403 without file bytes", rec.Code, rec.Body.String())
			}
		})
		t.Run("proxy_"+method, func(t *testing.T) {
			proxyRequests := 0
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				proxyRequests++
				w.WriteHeader(http.StatusOK)
			}))
			defer proxy.Close()
			proxies := nodepool.NewProxyPool()
			proxies.SetNodes([]*nodepool.Node{{URL: proxy.URL, Enabled: true, Healthy: true}})
			h := NewDownloadHandler(svc)
			h.SetProxyDelivery(nodepool.NewPlanner(proxies, nodepool.NewTranscodePool()), func() string { return "secret" })
			rec := httptest.NewRecorder()
			h.HandleDirectDownloadViaProxy(rec, downloadTestRequest(method, "/direct-download-proxy?file_id=42", nil, 7, "", ""))
			if rec.Code != http.StatusForbidden || rec.Header().Get("Location") != "" || proxyRequests != 0 {
				t.Fatalf("status = %d location = %q proxy requests = %d, want 403 with no redirect", rec.Code, rec.Header().Get("Location"), proxyRequests)
			}
		})
	}
	t.Run("link", func(t *testing.T) {
		issuer := &countingLinkIssuer{}
		h := NewDownloadHandler(svc)
		h.SetDirectDownloadLinks(issuer)
		ctx := apimw.SetProfileID(apimw.SetClaims(context.Background(), &auth.Claims{
			UserID: 7, Role: "user", SessionID: "s1", TokenType: auth.TokenTypeAccess,
		}), "p1")
		if _, _, err := h.CreateDirectDownloadLink(ctx, 42); !errors.Is(err, downloads.ErrDownloadNotAllowed) || issuer.calls != 0 {
			t.Fatalf("CreateDirectDownloadLink() error = %v, issuer calls = %d; want ErrDownloadNotAllowed and no link", err, issuer.calls)
		}
	})

	// The same override leaves a file at the ceiling downloadable.
	rec := httptest.NewRecorder()
	NewDownloadHandler(overrideCeilingDownloadService(t, "1080p")).HandleDirectDownload(rec, downloadTestRequest(http.MethodGet, "/direct-download?file_id=42", nil, 7, "", ""))
	if rec.Code != http.StatusOK || rec.Body.String() != "original bytes" {
		t.Fatalf("1080p original: status = %d body = %q, want 200 with the file", rec.Code, rec.Body.String())
	}
}

func TestProxyPreflightCachesReachabilityByNodeAndPath(t *testing.T) {
	requests := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	h := NewDownloadHandler(&fakeDownloadService{})
	location := proxy.URL + "/downloads/file/token"
	if !h.proxyCanServe(context.Background(), proxy.URL+"\x00/media/movie.mkv", location) {
		t.Fatal("first preflight failed")
	}
	if !h.proxyCanServe(context.Background(), proxy.URL+"\x00/media/movie.mkv", location) {
		t.Fatal("cached preflight failed")
	}
	if requests != 1 {
		t.Fatalf("preflight requests = %d, want 1", requests)
	}
}

// TestServeDownloadFileRedirectsManagedArtifactsThroughProxies covers the
// managed-download delegate path: an authorized artifact is redirected to a
// healthy proxy with a token scoped to it, a node-local artifact only through
// a proxy in the origin node's group, and the server serves the file itself
// when the locator is incomplete or the proxy refuses the token.
func TestServeDownloadFileRedirectsManagedArtifactsThroughProxies(t *testing.T) { //nolint:funlen // ported unchanged from the retired v1 handler test
	const secret = "download-proxy-secret"
	remoteTarget := &downloads.FileTarget{
		DownloadID:                   "dl-remote",
		ArtifactID:                   "artifact-row",
		Path:                         "/prepared/Movie Final.mp4",
		MediaFileID:                  42,
		OriginNodeURL:                "http://transcode-b.internal:8096",
		OriginNodeGroup:              "host-b",
		OriginArtifactID:             "artifact-remote",
		ExpectedArtifactSize:         123,
		ExpectedExecutionFingerprint: "recipe-fingerprint",
		ProxyEligible:                true,
	}
	cases := []struct {
		name string
		// target is the resolved managed file; proxies lists each proxy's group
		// ("" for none) and the status its preflight answers.
		target      *downloads.FileTarget
		groups      []string
		statuses    []int
		wantProxy   int // index of the proxy redirected to; -1 serves locally
		wantClaims  func(streamtoken.Claims) bool
		wantUnusedA bool // the first proxy must never be contacted
	}{
		{
			name:      "central artifact redirects to the healthy proxy",
			target:    &downloads.FileTarget{Path: "/artifacts/movie.mp4", DownloadID: "dl1", MediaFileID: 42, ProxyEligible: true},
			groups:    []string{""},
			statuses:  []int{http.StatusOK},
			wantProxy: 0,
			wantClaims: func(c streamtoken.Claims) bool {
				return c.PlayMethod == streamtoken.PlayMethodDownload && c.MediaPath == "/artifacts/movie.mp4" &&
					c.UserID == 7 && c.ProfileID == "pA"
			},
		},
		{
			name:      "node-local artifact goes through the origin group's proxy",
			target:    remoteTarget,
			groups:    []string{"host-a", "host-b"},
			statuses:  []int{http.StatusOK, http.StatusOK},
			wantProxy: 1,
			wantClaims: func(c streamtoken.Claims) bool {
				// The proxy fetches from the origin node; it never learns a
				// local media path.
				return c.MediaPath == "" && c.DownloadFilename == "Movie Final.mp4" &&
					c.TranscodeNode == "http://transcode-b.internal:8096" &&
					c.PlayMethod == streamtoken.PlayMethodToneMapDownload &&
					c.DownloadArtifactID == "artifact-remote" && c.DownloadArtifactRowID == "artifact-row" &&
					c.DownloadArtifactSize == 123 && c.DownloadExecutionFingerprint == "recipe-fingerprint" &&
					c.UserID == 7 && c.ProfileID == "pA"
			},
			wantUnusedA: true,
		},
		{
			name:      "remote locator without an origin URL serves locally",
			target:    &downloads.FileTarget{DownloadID: "dl-incomplete", OriginArtifactID: "artifact-without-origin", ProxyEligible: true},
			groups:    []string{""},
			statuses:  []int{http.StatusOK},
			wantProxy: -1,
		},
		{
			name: "proxy that rejects the token falls back to local serving",
			target: &downloads.FileTarget{
				DownloadID: "dl-attested", ArtifactID: "artifact-row", Path: "/prepared/movie.mp4", MediaFileID: 42,
				OriginNodeURL: "http://transcode.internal", OriginArtifactID: "artifact-remote",
				ExpectedArtifactSize: 123, ExpectedExecutionFingerprint: "fingerprint", ProxyEligible: true,
			},
			groups:    []string{""},
			statuses:  []int{http.StatusUnauthorized},
			wantProxy: -1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := make([]int, len(tc.groups))
			urls := make([]string, len(tc.groups))
			nodes := make([]*nodepool.Node, len(tc.groups))
			for i := range tc.groups {
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits[i]++
					if r.Method != http.MethodHead {
						t.Errorf("proxy %d preflight method = %s, want HEAD", i, r.Method)
					}
					w.WriteHeader(tc.statuses[i])
				}))
				t.Cleanup(proxy.Close)
				urls[i] = proxy.URL
				nodes[i] = &nodepool.Node{URL: proxy.URL, Enabled: true, Healthy: true}
				if tc.groups[i] != "" {
					nodes[i].Group = &tc.groups[i]
				}
			}
			proxies := nodepool.NewProxyPool()
			proxies.SetNodes(nodes)
			svc := &proxyDownloadService{fakeDownloadService: &fakeDownloadService{}, managedTarget: tc.target}
			h := NewDownloadHandler(svc)
			h.SetProxyDelivery(nodepool.NewPlanner(proxies, nodepool.NewTranscodePool()), func() string { return secret })

			rec := httptest.NewRecorder()
			req := downloadTestRequest(http.MethodGet, "/downloads/"+tc.target.DownloadID+"/file", nil, 7, "pA", "devA")
			if err := h.ServeDownloadFile(rec, req, tc.target.DownloadID, true); err != nil {
				t.Fatalf("ServeDownloadFile: %v", err)
			}

			if tc.wantProxy < 0 {
				if rec.Code != http.StatusOK || rec.Body.String() != "served" {
					t.Fatalf("response = %d %q, want the local serve", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusTemporaryRedirect {
				t.Fatalf("status = %d, want 307 (body: %s)", rec.Code, rec.Body.String())
			}
			prefix := urls[tc.wantProxy] + "/downloads/file/"
			location := rec.Header().Get("Location")
			if !strings.HasPrefix(location, prefix) {
				t.Fatalf("Location = %q, want proxy %d (%s)", location, tc.wantProxy, prefix)
			}
			claims, err := streamtoken.Verify(strings.TrimPrefix(location, prefix), secret)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.wantClaims(*claims) {
				t.Fatalf("claims = %+v", claims)
			}
			if svc.gotServe != (identityCall{}) {
				t.Fatalf("local serve was called after a redirect: %+v", svc.gotServe)
			}
			if tc.wantUnusedA && hits[0] != 0 {
				t.Fatalf("wrong-group proxy contacted %d times", hits[0])
			}
		})
	}
}
