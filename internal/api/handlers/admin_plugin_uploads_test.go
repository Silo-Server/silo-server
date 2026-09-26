package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/uploads"
)

// The chunked seams run on the real process-local manager: a session is
// created, chunks land in any order, a repeated chunk is a no-op, complete
// refuses until every chunk arrived, cancel of an unknown session succeeds,
// and complete of a full session consumes it before the install step. The
// assembled bytes carry a zip signature with no archive behind it, so the
// install fails at archive parsing and nothing is ever executed.
func TestPluginUploadSeamsOnRealManager(t *testing.T) {
	t.Parallel()
	h := &PluginHandler{
		uploads:       uploads.NewManager(uploads.ManagerOptions{RootDir: t.TempDir(), MaxSize: 64, MaxChunkSize: 8}),
		installations: &plugins.InstallationStore{},
		service:       &plugins.Service{},
	}
	ctx := t.Context()
	var apiErr *APIError
	if _, err := (&PluginHandler{}).CreateAdminPluginUpload(ctx, PluginChunkedUploadCreateInput{Filename: "p.zip", SizeBytes: 8}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("unwired create err = %v", err)
	}
	if _, err := (&PluginHandler{uploads: h.uploads}).CompleteAdminPluginUpload(ctx, "any"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("complete without service must refuse before consuming: %v", err)
	}
	if _, err := h.CreateAdminPluginUpload(ctx, PluginChunkedUploadCreateInput{Filename: "p.zip", SizeBytes: 128, ChunkSize: 4}); !errors.Is(err, uploads.ErrTooLarge) {
		t.Fatalf("oversize err = %v", err)
	}
	session, err := h.CreateAdminPluginUpload(ctx, PluginChunkedUploadCreateInput{Filename: "p.zip", SizeBytes: 10, ChunkSize: 4})
	if err != nil || session.TotalChunks != 3 {
		t.Fatalf("create: %+v %v", session, err)
	}
	if _, err := h.CompleteAdminPluginUpload(ctx, session.ID); !errors.Is(err, uploads.ErrIncomplete) {
		t.Fatalf("early complete err = %v", err)
	}
	if _, err := h.PutAdminPluginUploadChunk(ctx, session.ID, 2, bytes.NewReader([]byte("ij")), 2); err != nil {
		t.Fatalf("last chunk: %v", err)
	}
	if _, err := h.PutAdminPluginUploadChunk(ctx, session.ID, 0, bytes.NewReader([]byte("PK\x03\x04")), 4); err != nil {
		t.Fatalf("chunk 0: %v", err)
	}
	if _, err := h.PutAdminPluginUploadChunk(ctx, session.ID, 0, bytes.NewReader([]byte("zzzz")), 4); err != nil {
		t.Fatalf("repeat chunk 0: %v", err)
	}
	if _, err := h.PutAdminPluginUploadChunk(ctx, session.ID, 1, bytes.NewReader([]byte("ef")), 2); !errors.Is(err, uploads.ErrInvalidChunk) {
		t.Fatalf("short chunk err = %v", err)
	}
	info, err := h.PutAdminPluginUploadChunk(ctx, session.ID, 1, bytes.NewReader([]byte("efgh")), 4)
	if err != nil || !info.Complete || info.ReceivedBytes != 10 {
		t.Fatalf("chunk 1: %+v %v", info, err)
	}
	// Complete consumes the session, then the install fails at archive
	// parsing (zip signature, no archive); the session is gone afterwards.
	if _, err := h.CompleteAdminPluginUpload(ctx, session.ID); err == nil || errors.Is(err, uploads.ErrIncomplete) || errors.Is(err, uploads.ErrNotFound) || errors.As(err, &apiErr) {
		t.Fatalf("complete err = %v, want an archive parse failure", err)
	}
	if _, err := h.CompleteAdminPluginUpload(ctx, session.ID); !errors.Is(err, uploads.ErrNotFound) {
		t.Fatalf("repeat complete err = %v, want consumed session", err)
	}
	if err := h.CancelAdminPluginUpload(ctx, session.ID); err != nil {
		t.Fatalf("cancel after consume: %v", err)
	}
	if err := h.CancelAdminPluginUpload(ctx, "missing"); err != nil {
		t.Fatalf("cancel unknown: %v", err)
	}
	if _, err := h.PutAdminPluginUploadChunk(ctx, session.ID, 0, bytes.NewReader([]byte("abcd")), 4); !errors.Is(err, uploads.ErrNotFound) {
		t.Fatalf("chunk after cancel err = %v", err)
	}
}

// The frozen bridge keeps its v1 answers on the direct upload route: an
// oversize or malformed multipart body is 400 bad_request "Invalid plugin
// upload", and a form without the archive part is 400 "archive upload is
// required". The shared seam reports 413 only to the v2 listener.
func TestHandleUploadInstallationKeepsBridgeStatuses(t *testing.T) {
	t.Parallel()
	h := &PluginHandler{
		uploads:       uploads.NewManager(uploads.ManagerOptions{RootDir: t.TempDir()}),
		installations: &plugins.InstallationStore{},
		service:       &plugins.Service{},
	}
	form := func(field string, size int) (*bytes.Buffer, string) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		part, err := w.CreateFormFile(field, "plugin.zip")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(bytes.Repeat([]byte{0x50}, size)); err != nil {
			t.Fatal(err)
		}
		_ = w.Close()
		return &buf, w.FormDataContentType()
	}
	call := func(body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/plugins/uploads", body)
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		h.HandleUploadInstallation(rec, req)
		return rec
	}
	assertV1 := func(t *testing.T, rec *httptest.ResponseRecorder, message string) {
		t.Helper()
		var body struct{ Error, Message string }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err, rec.Body.String())
		}
		if rec.Code != http.StatusBadRequest || body.Error != "bad_request" || body.Message != message {
			t.Fatalf("status=%d body=%s, want 400 bad_request %q", rec.Code, rec.Body.String(), message)
		}
	}
	body, ct := form("archive", int(maxPluginUploadSize)+1)
	assertV1(t, call(body, ct), "Invalid plugin upload")
	assertV1(t, call(bytes.NewBufferString("not multipart"), ct), "Invalid plugin upload")
	body, ct = form("other", 8)
	assertV1(t, call(body, ct), "archive upload is required")
}
