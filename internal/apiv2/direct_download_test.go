package apiv2

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

type directDownloadFixture struct {
	handlers.DownloadService
	calls   int
	err     error
	partial bool
	access  catalogpkg.AccessFilter
}

func (f *directDownloadFixture) ServeDirect(_ context.Context, w http.ResponseWriter, r *http.Request, user, file int, format string, access catalogpkg.AccessFilter) error {
	f.calls++
	f.access = access
	if user != 1 || file != 42 || format != "original" && format != "" {
		return downloads.ErrDownloadNotAllowed
	}
	if f.partial {
		_, _ = w.Write([]byte("partial"))
		return errors.New("PRIVATE interrupted")
	}
	if f.err != nil {
		return f.err
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", `attachment; filename="fixture.mp4"`)
	http.ServeContent(w, r, "fixture.mp4", time.Unix(1700000000, 0), strings.NewReader("0123456789"))
	return nil
}
func directDownloadTestHandler(t *testing.T, f *directDownloadFixture) http.Handler {
	t.Helper()
	deps, _ := catalogDeps(t)
	h := handlers.NewDownloadHandler(f)
	deps.DirectDownloads = &DirectDownloadHandlers{Original: http.HandlerFunc(h.HandleDirectDownload), Proxy: http.HandlerFunc(h.HandleDirectDownloadViaProxy)}
	return newTestHandler(t, deps)
}
func TestDirectDownloadDelivery(t *testing.T) {
	for _, path := range []string{"/direct-download", "/direct-download-proxy"} {
		t.Run(path, func(t *testing.T) {
			f := new(directDownloadFixture)
			h := directDownloadTestHandler(t, f)
			path = Prefix + path + "?file_id=42&format=original"
			res := do(t, h, "GET", path, "", viewerHeaders())
			if res.Code != 200 || res.Body.String() != "0123456789" {
				t.Fatal(res.Code, res.Body)
			}
			if f.access.UserID != 1 || f.access.ProfileID != "p-owner" {
				t.Fatal(f.access)
			}
			res = do(t, h, "HEAD", path, "", viewerHeaders())
			if res.Code != 200 || res.Body.Len() != 0 || res.Header().Get("Content-Length") != "10" {
				t.Fatal(res.Code, res.Body, res.Header())
			}
			res = do(t, h, "GET", path, "", with(viewerHeaders(), "Range", "bytes=2-4"))
			if res.Code != 206 || res.Body.String() != "234" || res.Header().Get("Content-Range") != "bytes 2-4/10" {
				t.Fatal(res.Code, res.Body, res.Header())
			}
			res = do(t, h, "GET", path, "", with(viewerHeaders(), "Range", "bytes=99-"))
			requireProblem(t, res, TypeForStatus(416))
			if res.Header().Get("Content-Range") != "bytes */10" {
				t.Fatal(res.Header())
			}
			res = do(t, h, "GET", path, "", with(viewerHeaders(), "If-Modified-Since", time.Unix(1700000000, 0).UTC().Format(http.TimeFormat)))
			if res.Code != 304 || res.Body.Len() != 0 {
				t.Fatal(res.Code, res.Body)
			}
			before := f.calls
			requireProblem(t, do(t, h, "GET", path, "", nil), TypeAuthenticationRequired)
			for _, q := range []string{"", "?file_id=0", "?file_id=042", "?file_id=%2B42", "?file_id=42&file_id=43", "?file_id=42&path=PRIVATE", "?file_id=42&format=transcode"} {
				requireProblem(t, do(t, h, "GET", strings.Split(path, "?")[0]+q, "", viewerHeaders()), TypeValidationFailed)
			}
			if f.calls != before {
				t.Fatal("refused query reached service")
			}
			for _, tc := range []struct {
				err  error
				kind ProblemType
			}{{downloads.ErrDownloadNotAllowed, TypePermissionDenied}, {catalogpkg.ErrItemNotFound, TypeNotFound}, {errors.New("PRIVATE backend failure"), TypeInternalError}} {
				f.err = tc.err
				res = do(t, h, "GET", path, "", viewerHeaders())
				requireProblem(t, res, tc.kind)
				if strings.Contains(res.Body.String(), "PRIVATE") {
					t.Fatal(res.Body)
				}
			}
		})
	}
}
func TestDirectDownloadPartialResponseAborts(t *testing.T) {
	f := &directDownloadFixture{partial: true}
	h := directDownloadTestHandler(t, f)
	req := httptest.NewRequest("GET", Prefix+"/direct-download?file_id=42", nil)
	for k, v := range viewerHeaders() {
		req.Header.Set(k, v)
	}
	res := httptest.NewRecorder()
	defer func() {
		if got, ok := recover().(error); !ok || !errors.Is(got, http.ErrAbortHandler) {
			t.Errorf("panic %v", got)
		}
		if res.Body.String() != "partial" || f.calls != 1 {
			t.Errorf("appended error/replayed %s %d", res.Body, f.calls)
		}
	}()
	h.ServeHTTP(res, req)
}

type directProxyFixture struct {
	*directDownloadFixture
	resolveErr error
}

func (f *directProxyFixture) ResolveDirectFile(_ context.Context, user, file int, _ string, a catalogpkg.AccessFilter) (*downloads.FileTarget, error) {
	if user != 1 || file != 42 || a.ProfileID != "p-owner" {
		return nil, downloads.ErrDownloadNotAllowed
	}
	return &downloads.FileTarget{Path: "/fixture/original.mp4", MediaFileID: 42, ProxyEligible: true}, f.resolveErr
}
func (*directProxyFixture) ResolveManagedFile(context.Context, int, string, string, string, catalogpkg.AccessFilter) (*downloads.FileTarget, error) {
	panic("managed download must not run")
}

type directPlannerFixture struct {
	url             string
	plans, releases int
}

func (p *directPlannerFixture) PlanDownloadWith(string, func(*nodepool.Node) bool, ...string) nodepool.Plan {
	p.plans++
	return nodepool.Plan{ProxyNode: &nodepool.Node{URL: p.url}}
}
func (p *directPlannerFixture) ReleaseSession(string) { p.releases++ }
func TestDirectDownloadProxyAuthorityAndReservation(t *testing.T) {
	for _, kind := range []string{"get", "head", "unavailable", "denied"} {
		t.Run(kind, func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "HEAD" {
					t.Error("preflight method", r.Method)
				}
				if kind == "unavailable" {
					w.WriteHeader(503)
				} else {
					w.WriteHeader(200)
				}
			}))
			defer proxy.Close()
			f := &directProxyFixture{directDownloadFixture: new(directDownloadFixture)}
			if kind == "denied" {
				f.resolveErr = downloads.ErrDownloadNotAllowed
			}
			planner := &directPlannerFixture{url: proxy.URL}
			legacy := handlers.NewDownloadHandler(f)
			const secret = "synthetic-direct-proxy-key"
			legacy.SetProxyDelivery(planner, func() string { return secret })
			deps, _ := catalogDeps(t)
			deps.DirectDownloads = &DirectDownloadHandlers{Proxy: http.HandlerFunc(legacy.HandleDirectDownloadViaProxy)}
			h := newTestHandler(t, deps)
			method := "GET"
			if kind == "head" {
				method = "HEAD"
			}
			res := do(t, h, method, Prefix+"/direct-download-proxy?file_id=42", "", viewerHeaders())
			if kind == "denied" {
				requireProblem(t, res, TypePermissionDenied)
				if planner.plans != 0 || f.calls != 0 {
					t.Fatal("denied authority produced target")
				}
				return
			}
			if kind == "unavailable" {
				if res.Code != 200 || res.Body.String() != "0123456789" || planner.releases != 1 || f.calls != 1 {
					t.Fatal(res.Code, res.Body, planner, f.calls)
				}
				return
			}
			if res.Code != 307 || f.calls != 0 {
				t.Fatal(res.Code, res.Body, f.calls)
			}
			claims, err := streamtoken.Verify(strings.TrimPrefix(res.Header().Get("Location"), proxy.URL+"/downloads/file/"), secret)
			if err != nil {
				t.Fatal(err)
			}
			if claims.UserID != 1 || claims.ProfileID != "p-owner" || claims.MediaFileID != 42 || claims.MediaPath != "/fixture/original.mp4" || claims.PlayMethod != streamtoken.PlayMethodDownload {
				t.Fatal("wrong authorized target")
			}
			wantRelease := 0
			if kind == "head" {
				wantRelease = 1
			}
			if planner.releases != wantRelease || planner.plans != 1 {
				t.Fatal(planner)
			}
		})
	}
}

func TestDirectDownloadBrowserTokenAndUnavailable(t *testing.T) {
	f := new(directDownloadFixture)
	h := directDownloadTestHandler(t, f)
	path := Prefix + directDownloadPath + "?file_id=42&token=" + memberToken
	for _, method := range []string{"HEAD", "GET"} {
		res := do(t, h, method, path, "", nil)
		if res.Code != 200 || f.access.UserID != 1 || f.access.ProfileID != "" {
			t.Fatal(res.Code, res.Body, f.access)
		}
	}
	before := f.calls
	requireProblem(t, do(t, h, "GET", Prefix+directDownloadPath+"?file_id=42&token="+expiredToken, "", nil), TypeSessionExpired)
	if f.calls != before {
		t.Fatal("expired token reached file")
	}
	deps, _ := catalogDeps(t)
	requireProblem(t, do(t, newTestHandler(t, deps), "GET", path, "", nil), TypeDependencyUnavailable)
}
