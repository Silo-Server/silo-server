package apiv2

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/uploads"
)

type fakePluginUploads struct {
	calls        int
	lastCreate   handlers.PluginChunkedUploadCreateInput
	lastUploadID string
	lastIndex    int
	lastLength   int64
	lastBody     string
	received     map[int]bool
	direct       int
	directType   string
	directName   string
	err          error
}

func (f *fakePluginUploads) session() uploads.SessionInfo {
	n := 0
	for range f.received {
		n++
	}
	return uploads.SessionInfo{ID: "upload-1", Filename: f.lastCreate.Filename, SizeBytes: f.lastCreate.SizeBytes, ChunkSize: 4, TotalChunks: 3, ReceivedChunks: n, ReceivedBytes: int64(n * 4), Complete: n == 3, ExpiresAt: time.Date(2026, 9, 7, 2, 0, 0, 0, time.UTC)}
}
func (f *fakePluginUploads) view() handlers.PluginInstallationView {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	return handlers.PluginInstallationView{ID: 31, PluginID: "org.example.up", Version: "2.0.0", InstallPath: "/plugins/up", Enabled: true, Kind: plugins.KindPlugin, UpdatePolicy: "auto", SourceKind: "external", CreatedAt: at, UpdatedAt: at}
}
func (f *fakePluginUploads) InstallAdminPluginUpload(_ context.Context, r *http.Request) (handlers.PluginInstallationView, error) {
	f.calls++
	f.direct++
	if f.err != nil {
		return handlers.PluginInstallationView{}, f.err
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		return handlers.PluginInstallationView{}, err
	}
	file, header, err := r.FormFile("archive")
	if err != nil {
		return handlers.PluginInstallationView{}, handlers.ErrPluginUploadMissingArchive
	}
	defer func() { _ = file.Close() }()
	f.directType, f.directName = header.Header.Get("Content-Type"), header.Filename
	return f.view(), nil
}
func (f *fakePluginUploads) CreateAdminPluginUpload(_ context.Context, in handlers.PluginChunkedUploadCreateInput) (uploads.SessionInfo, error) {
	f.calls++
	f.lastCreate = in
	f.received = map[int]bool{}
	if f.err != nil {
		return uploads.SessionInfo{}, f.err
	}
	return f.session(), nil
}
func (f *fakePluginUploads) PutAdminPluginUploadChunk(_ context.Context, id string, index int, body io.Reader, length int64) (uploads.SessionInfo, error) {
	f.calls++
	f.lastUploadID, f.lastIndex, f.lastLength = id, index, length
	data, _ := io.ReadAll(body)
	f.lastBody = string(data)
	if f.err != nil {
		return uploads.SessionInfo{}, f.err
	}
	if f.received == nil {
		f.received = map[int]bool{}
	}
	f.received[index] = true
	return f.session(), nil
}
func (f *fakePluginUploads) CompleteAdminPluginUpload(_ context.Context, id string) (handlers.PluginInstallationView, error) {
	f.calls++
	f.lastUploadID = id
	if f.err != nil {
		return handlers.PluginInstallationView{}, f.err
	}
	return f.view(), nil
}
func (f *fakePluginUploads) CancelAdminPluginUpload(_ context.Context, id string) error {
	f.calls++
	f.lastUploadID = id
	return f.err
}

func TestAdminPluginDirectUpload(t *testing.T) {
	f := new(fakePluginUploads)
	deps := pilotDeps(nil, nil)
	deps.AdminPluginUploads = f
	h := newTestHandler(t, deps)
	path := Prefix + "/admin/plugins/uploads"
	body, ct := posterForm(t, "archive", "application/zip", 64)
	requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(memberToken), "Content-Type", ct)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("unauthorized upload reached the service")
	}
	rec := do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"plugin_id":"org.example.up"`) || f.directType != "application/zip" {
		t.Fatal(rec.Code, rec.Body.String(), f.directType)
	}
	// JSON on the multipart operation is 415; a missing part is a validation failure.
	requireProblem(t, do(t, h, http.MethodPost, path, `{}`, bearer(adminToken)), TypeUnsupportedMediaType)
	body, ct = posterForm(t, "other", "application/zip", 8)
	p := requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.archive" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	f.err = uploads.ErrTooLarge
	body, ct = posterForm(t, "archive", "application/zip", 8)
	requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), TypePayloadTooLarge)
	f.err = errors.New("open plugin archive: zip: not a valid zip file")
	requireProblem(t, do(t, h, http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), TypeInternalError)
	deps.AdminPluginUploads = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, path, body, with(bearer(adminToken), "Content-Type", ct)), TypeDependencyUnavailable)
}

func TestAdminPluginChunkedUpload(t *testing.T) {
	f := new(fakePluginUploads)
	deps := pilotDeps(nil, nil)
	deps.AdminPluginUploads = f
	h := newTestHandler(t, deps)
	base := Prefix + "/admin/plugins/uploads/chunked"
	requireProblem(t, do(t, h, http.MethodPost, base, `{"filename":"p.zip","size_bytes":12}`, bearer(memberToken)), TypePermissionDenied)
	rec := do(t, h, http.MethodPost, base, `{"filename":"p.zip","size_bytes":12,"chunk_size":4}`, bearer(adminToken))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"upload_id":"upload-1"`) || !strings.Contains(rec.Body.String(), `"expires_at":"2026-09-07T02:00:00.000Z"`) || f.lastCreate.ChunkSize != 4 {
		t.Fatal(rec.Code, rec.Body.String(), f.lastCreate)
	}
	requireProblem(t, do(t, h, http.MethodPost, base, `{"filename":"p.zip","size_bytes":0}`, bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, base, `{"filename":"p.zip","size_bytes":12,"chunk_size":2097152}`, bearer(adminToken)), TypeValidationFailed)
	// Chunk PUT is octet-stream only; body forwarded with its length.
	chunk := base + "/upload-1/chunks/1"
	requireProblem(t, do(t, h, http.MethodPut, chunk, `abcd`, bearer(adminToken)), TypeUnsupportedMediaType)
	rec = do(t, h, http.MethodPut, chunk, "abcd", with(bearer(adminToken), "Content-Type", "application/octet-stream"))
	if rec.Code != http.StatusOK || f.lastUploadID != "upload-1" || f.lastIndex != 1 || f.lastBody != "abcd" || f.lastLength != 4 || !strings.Contains(rec.Body.String(), `"received_chunks":1`) {
		t.Fatal(rec.Code, rec.Body.String(), f.lastUploadID, f.lastIndex, f.lastBody, f.lastLength)
	}
	requireProblem(t, do(t, h, http.MethodPut, base+"/upload-1/chunks/-1", "abcd", with(bearer(adminToken), "Content-Type", "application/octet-stream")), TypeValidationFailed)
	// Upload-manager refusals map to problems.
	for _, tc := range []struct {
		err  error
		want ProblemType
	}{{uploads.ErrNotFound, TypeNotFound}, {uploads.ErrExpired, TypeNotFound}, {uploads.ErrChunkBusy, TypeConflict}, {uploads.ErrAlreadyCompleted, TypeConflict}, {uploads.ErrTooLarge, TypePayloadTooLarge}, {errors.Join(uploads.ErrInvalidChunk, errors.New("chunk size must be 4 bytes")), TypeValidationFailed}} {
		f.err = tc.err
		requireProblem(t, do(t, h, http.MethodPut, chunk, "abcd", with(bearer(adminToken), "Content-Type", "application/octet-stream")), tc.want)
	}
	f.err = nil
	// Complete consumes the session; incomplete is 409, gone is 404.
	rec = do(t, h, http.MethodPost, base+"/upload-1/complete", "", bearer(adminToken))
	if rec.Code != http.StatusCreated || f.lastUploadID != "upload-1" || !strings.Contains(rec.Body.String(), `"id":"31"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.err = uploads.ErrIncomplete
	requireProblem(t, do(t, h, http.MethodPost, base+"/upload-1/complete", "", bearer(adminToken)), TypeConflict)
	f.err = uploads.ErrNotFound
	requireProblem(t, do(t, h, http.MethodPost, base+"/upload-1/complete", "", bearer(adminToken)), TypeNotFound)
	f.err = errors.New("open plugin archive: zip: not a valid zip file")
	requireProblem(t, do(t, h, http.MethodPost, base+"/upload-1/complete", "", bearer(adminToken)), TypeInternalError)
	f.err = nil
	rec = do(t, h, http.MethodDelete, base+"/upload-1", "", bearer(adminToken))
	if rec.Code != http.StatusNoContent || f.lastUploadID != "upload-1" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	f.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Plugin uploads not configured"}
	requireProblem(t, do(t, h, http.MethodDelete, base+"/upload-1", "", bearer(adminToken)), TypeDependencyUnavailable)
	deps.AdminPluginUploads = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodPost, base, `{"filename":"p.zip","size_bytes":12}`, bearer(adminToken)), TypeDependencyUnavailable)
}
