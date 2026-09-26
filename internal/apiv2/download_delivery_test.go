package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

// Embed the unused lifecycle interface: the real handler may call only the
// transfer methods for a raw route, never create/delete/status operations.
type downloadDeliveryDomain struct {
	handlers.DownloadService
	user                int
	profile, device, id string
	filter              catalogpkg.AccessFilter
	err                 error
	partial             bool
}

func (s *downloadDeliveryDomain) ServeFile(_ context.Context, w http.ResponseWriter, r *http.Request, user int, profile, device, id string, filter catalogpkg.AccessFilter) error {
	s.user = user
	s.profile = profile
	s.device = device
	s.id = id
	s.filter = filter
	if s.err != nil {
		return s.err
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", `attachment; filename="Synthetic.mp4"`)
	http.ServeContent(w, r, "Synthetic.mp4", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), strings.NewReader("0123456789"))
	return nil
}
func (s *downloadDeliveryDomain) ServeArtwork(_ context.Context, w http.ResponseWriter, _ *http.Request, user int, profile, device, id, kind string, filter catalogpkg.AccessFilter) error {
	s.user = user
	s.profile = profile
	s.device = device
	s.id = id
	s.filter = filter
	if s.err != nil {
		w.Header().Set("Content-Length", "100")
		w.Header().Set("Content-Type", "image/png")
		if s.partial {
			_, _ = w.Write([]byte("partial"))
		}
		return s.err
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write([]byte("image"))
	return nil
}
func (s *downloadDeliveryDomain) ServeSubtitle(_ context.Context, w http.ResponseWriter, _ *http.Request, user int, profile, device, id, ref string, filter catalogpkg.AccessFilter) error {
	s.user = user
	s.profile = profile
	s.device = device
	s.id = id
	s.filter = filter
	if s.err != nil {
		return s.err
	}
	w.Header().Set("Content-Type", "text/vtt")
	_, _ = w.Write([]byte("WEBVTT"))
	return nil
}
func TestDownloadRawDelivery(t *testing.T) {
	domain := &downloadDeliveryDomain{}
	deps := pilotDeps(nil, nil)
	deps.DownloadDelivery = handlers.NewDownloadHandler(domain)
	h := newTestHandler(t, deps)
	viewer := with(with(bearer(memberToken), "X-Profile-Id", "p-owner"), "X-Silo-Device-Id", "device-one")
	path := Prefix + "/downloads/entry/file"
	rec := do(t, h, "GET", path, "", with(viewer, "Range", "bytes=2-4"))
	if rec.Code != 206 || rec.Body.String() != "234" || rec.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	if domain.user != 1 || domain.profile != "p-owner" || domain.device != "device-one" || domain.id != "entry" || domain.filter.ProfileID != "p-owner" {
		t.Fatalf("%+v", domain)
	}
	rec = do(t, h, "GET", path, "", with(viewer, "Range", "bytes=0-1,8-9"))
	if rec.Code != 206 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "multipart/byteranges") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "HEAD", path, "", viewer)
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "10" {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	modified := rec.Header().Get("Last-Modified")
	rec = do(t, h, "GET", path, "", with(viewer, "If-Modified-Since", modified))
	if rec.Code != 304 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "GET", path, "", with(viewer, "Range", "bytes=99-"))
	if rec.Code != 416 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "GET", path+"-proxy", "", viewer)
	if rec.Code != 200 || rec.Body.String() != "0123456789" {
		t.Fatalf("local fallback: %d %s", rec.Code, rec.Body.String())
	}
	domain.err = catalogpkg.ErrItemNotFound
	rec = do(t, h, "GET", path, "", viewer)
	if rec.Code != 404 || rec.Header().Get("Content-Type") != problemContentType {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	domain.err = downloads.ErrDownloadNotActive
	rec = do(t, h, "GET", path, "", viewer)
	if rec.Code != 409 {
		t.Fatalf("%d", rec.Code)
	}
	domain.err = nil
	for _, asset := range []string{"artwork/poster", "subtitles/external:0"} {
		rec = do(t, h, "GET", Prefix+"/downloads/entry/"+asset, "", viewer)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", asset, rec.Code, rec.Body.String())
		}
		rec = do(t, h, "GET", Prefix+"/downloads/entry/"+asset, "", with(bearer(memberToken), "X-Profile-Id", "p-owner"))
		if rec.Code != 422 {
			t.Fatalf("device required: %d", rec.Code)
		}
	}
	rec = do(t, h, "GET", path, "", nil)
	if rec.Code != 401 {
		t.Fatalf("auth: %d", rec.Code)
	}
}
func TestDownloadRawAssetFailureBoundaries(t *testing.T) {
	domain := &downloadDeliveryDomain{err: errors.New("synthetic upstream failure")}
	deps := pilotDeps(nil, nil)
	deps.DownloadDelivery = handlers.NewDownloadHandler(domain)
	h := newTestHandler(t, deps)
	viewer := with(with(bearer(memberToken), "X-Profile-Id", "p-owner"), "X-Silo-Device-Id", "device")
	path := Prefix + "/downloads/entry/artwork/poster"
	rec := do(t, h, "GET", path, "", viewer)
	if rec.Code != 500 || rec.Header().Get("Content-Length") != "" || rec.Header().Get("Content-Type") != problemContentType || strings.Contains(rec.Body.String(), "synthetic upstream") {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	domain.partial = true
	rec = do(t, h, "GET", path, "", viewer)
	if rec.Code != 200 || rec.Body.String() != "partial" {
		t.Fatalf("appended problem: %d %s", rec.Code, rec.Body.String())
	}
}
