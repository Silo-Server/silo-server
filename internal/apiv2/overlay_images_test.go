package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/overlays"
)

var testOverlayRef = strings.Repeat("ab", 32) + ".webp"

type fakeOverlayImages struct {
	storage            bool
	images             []overlays.Image
	listErr, err       error
	uploads, deletes   int
	name, contentType  string
	size               int
	deletedRef, gotRef string
	data               []byte
}

func (f *fakeOverlayImages) HasStorage() bool { return f.storage }
func (f *fakeOverlayImages) Upload(_ context.Context, name, contentType string, data []byte) (overlays.Image, error) {
	f.uploads++
	f.name, f.contentType, f.size = name, contentType, len(data)
	if f.err != nil {
		return overlays.Image{}, f.err
	}
	return overlays.Image{Ref: testOverlayRef, Name: name, Width: 300, Height: 120, Bytes: 900, CreatedAt: time.Unix(1, 0)}, nil
}
func (f *fakeOverlayImages) List(context.Context) ([]overlays.Image, error) {
	return f.images, f.listErr
}
func (f *fakeOverlayImages) Delete(_ context.Context, ref string) error {
	f.deletes++
	f.deletedRef = ref
	return f.err
}
func (f *fakeOverlayImages) Get(_ context.Context, ref string) ([]byte, error) {
	f.gotRef = ref
	if f.err != nil {
		return nil, f.err
	}
	return f.data, nil
}

func overlayImageForm(t *testing.T, name, contentType string, size int) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if name != "" {
		if err := w.WriteField("name", name); err != nil {
			t.Fatal(err)
		}
	}
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="badge.png"`)
	hdr.Set("Content-Type", contentType)
	part, err := w.CreatePart(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte{0x89}, size)); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	return buf.String(), w.FormDataContentType()
}

func TestAdminOverlayImageUpload(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeOverlayImages{storage: true}
	deps.OverlayImages = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/overlay-images"
	body, ct := overlayImageForm(t, "4K Dolby Vision", "image/png", 64)
	requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(memberToken), "Content-Type", ct)), TypePermissionDenied)
	if f.uploads != 0 {
		t.Fatal("unauthorized upload reached the service")
	}
	rec := do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct))
	want := `{"ref":"` + testOverlayRef + `","name":"4K Dolby Vision","url":"/api/v2/overlay-images/` + testOverlayRef + `","width":300,"height":120}`
	if rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != want || rec.Header().Get("Location") != path+"/"+testOverlayRef {
		t.Fatal(rec.Code, rec.Header(), rec.Body.String())
	}
	if f.name != "4K Dolby Vision" || f.contentType != "image/png" || f.size != 64 {
		t.Fatalf("seam got %q %q %d", f.name, f.contentType, f.size)
	}
	// The form's own bounds refuse before the service; the name's are the
	// service's (ErrInvalidImageName below).
	body, ct = overlayImageForm(t, "Logo", "image/gif", 16)
	p := requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), TypeValidationFailed)
	if len(p.Errors) == 0 || p.Errors[0].Location != "body.file" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	body, ct = overlayImageForm(t, "Logo", "image/png", overlays.MaxImageUploadBytes+1)
	requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), TypePayloadTooLarge)
	if f.uploads != 1 {
		t.Fatalf("service saw %d uploads, want the one accepted", f.uploads)
	}

	body, ct = overlayImageForm(t, "Logo", "image/png", 16)
	for _, tc := range []struct {
		err      error
		want     ProblemType
		location string
	}{
		{overlays.ErrInvalidImageName, TypeValidationFailed, "body.name"},
		{overlays.ErrUnsupportedImage, TypeValidationFailed, "body.file"},
		{overlays.ErrImageLibraryFull, TypeConflict, ""},
		{overlays.ErrImageStorageUnavailable, TypeCapabilityNotConfigured, ""},
		{errors.New("disk on fire"), TypeInternalError, ""},
	} {
		f.err = tc.err
		p := requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), tc.want)
		if tc.location != "" && (len(p.Errors) != 1 || p.Errors[0].Location != tc.location) {
			t.Fatalf("%v: errors = %+v", tc.err, p.Errors)
		}
	}
	deps.OverlayImages = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), TypeDependencyUnavailable)
}

func TestAdminOverlayImageDelete(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := &fakeOverlayImages{storage: true}
	deps.OverlayImages = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/overlay-images/" + testOverlayRef
	requireProblem(t, do(t, h, http.MethodDelete, path, "", bearer(memberToken)), TypePermissionDenied)
	rec := do(t, h, http.MethodDelete, path, "", bearer(adminToken))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || f.deletes != 1 || f.deletedRef != testOverlayRef {
		t.Fatal(rec.Code, rec.Body.String(), f.deletes, f.deletedRef)
	}
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/admin/overlay-images/not-a-ref", "", bearer(adminToken)), TypeValidationFailed)
	f.err = overlays.ErrImageNotFound
	requireProblem(t, do(t, h, http.MethodDelete, path, "", bearer(adminToken)), TypeNotFound)
	deps.OverlayImages = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodDelete, path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

func TestOverlayImageServe(t *testing.T) {
	f := &fakeOverlayImages{storage: true, data: []byte("RIFF....WEBP")}
	h := NewHandler(Dependencies{OverlayImages: f})
	path := Prefix + "/overlay-images/" + testOverlayRef
	got := do(t, h, http.MethodGet, path, "", nil)
	if got.Code != http.StatusOK || got.Body.String() != "RIFF....WEBP" || got.Header().Get("Content-Type") != "image/webp" ||
		got.Header().Get("ETag") != `"`+testOverlayRef+`"` || got.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		got.Header().Get("Content-Security-Policy") != overlayImageContentSecurityPolicy || got.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(got.Code, got.Header(), got.Body.String())
	}
	head := do(t, h, http.MethodHead, path, "", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "12" {
		t.Fatal(head.Code, head.Header(), head.Body.String())
	}
	cached := do(t, h, http.MethodGet, path, "", map[string]string{"If-None-Match": `"` + testOverlayRef + `"`})
	if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
		t.Fatal(cached.Code, cached.Header())
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/overlay-images/nope.webp", "", nil), TypeNotFound)
	f.err = overlays.ErrImageNotFound
	requireProblem(t, do(t, h, http.MethodGet, path, "", nil), TypeNotFound)
	requireProblem(t, do(t, NewHandler(Dependencies{}), http.MethodGet, path, "", nil), TypeDependencyUnavailable)
}

func TestOverlayConfigListsImages(t *testing.T) {
	deps := pilotDeps(nil, nil)
	h := newTestHandler(t, deps)
	var body OverlayConfig
	read := func(h http.Handler) {
		t.Helper()
		rec := do(t, h, http.MethodGet, Prefix+"/settings/overlay-config", "", bearer(memberToken))
		if rec.Code != http.StatusOK {
			t.Fatal(rec.Code, rec.Body.String())
		}
		body = OverlayConfig{}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rec.Body.String(), `"images":[`) {
			t.Fatalf("images must always be present: %s", rec.Body.String())
		}
	}
	read(h)
	if len(body.Images) != 0 || body.ImageUploadsAvailable {
		t.Fatalf("unwired library: %+v", body)
	}

	f := &fakeOverlayImages{storage: true, images: []overlays.Image{{Ref: testOverlayRef, Name: "Remux", Width: 200, Height: 80}}}
	deps.OverlayImages = f
	read(newTestHandler(t, deps))
	if len(body.Images) != 1 || body.Images[0] != (OverlayImage{Ref: testOverlayRef, Name: "Remux", URL: Prefix + "/overlay-images/" + testOverlayRef, Width: 200, Height: 80}) || !body.ImageUploadsAvailable {
		t.Fatalf("wired library: %+v", body)
	}
	// A failed listing degrades to no images instead of failing every card.
	f.listErr = errors.New("database unavailable")
	read(newTestHandler(t, deps))
	if len(body.Images) != 0 || !body.ImageUploadsAvailable || !body.Enabled {
		t.Fatalf("degraded listing: %+v", body)
	}

	caps := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/settings/contract/capabilities", "", bearer(memberToken))
	if !strings.Contains(caps.Body.String(), `"supports_overlay_images":true`) {
		t.Fatalf("library wired, capabilities = %s", caps.Body.String())
	}
}
