package apiv2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

type directDownloadFixture struct {
	handlers.DownloadService
	calls     int
	err       error
	partial   bool
	committed bool
	access    catalogpkg.AccessFilter
}

func (f *directDownloadFixture) ServeDirect(_ context.Context, w http.ResponseWriter, r *http.Request, user, file int, format string, access catalogpkg.AccessFilter) error {
	f.calls++
	f.access = access
	if user != 1 || file != 42 || format != "original" && format != "" {
		return downloads.ErrDownloadNotAllowed
	}
	if f.partial {
		_, _ = w.Write([]byte("partial"))
		if f.committed {
			return fmt.Errorf("%w: PRIVATE interrupted", downloads.ErrResponseCommitted)
		}
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
