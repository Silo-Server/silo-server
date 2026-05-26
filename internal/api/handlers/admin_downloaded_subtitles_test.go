package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

func newAdminSubtitleRequest(method, path string, body []byte) *http.Request {
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{
		UserID:    1,
		Role:      "admin",
		TokenType: auth.TokenTypeAccess,
	})
	return req.WithContext(ctx)
}

func withSubtitleRouteParam(req *http.Request, key, value string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

func TestHandlePatchDownloadedSubtitleUpdatesMetadata(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	repo.subtitles[7] = &subtitles.DownloadedSubtitle{
		ID:          7,
		MediaFileID: 42,
		Provider:    subtitles.ProviderUpload,
		Language:    "en",
		Format:      subtitles.FormatSRT,
		ReleaseName: "movie.en.srt",
		S3Key:       "subtitles/42/en_upload_abcd1234.srt",
	}
	repo.byKey["subtitles/42/en_upload_abcd1234.srt"] = repo.subtitles[7]

	s3 := &trackingHandlerS3Client{objects: map[string][]byte{
		"subtitles/42/en_upload_abcd1234.srt": []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"),
	}}
	manager := subtitles.NewManager(repo, s3, "test-bucket")
	handler := NewAdminSubtitleHandler(repo)
	handler.SetDownloadedSubtitleDeps(nil, manager)

	body, err := json.Marshal(map[string]any{
		"language":         "es",
		"release_name":     "movie.es.srt",
		"hearing_impaired": true,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	req := newAdminSubtitleRequest(http.MethodPatch, "/admin/subtitles/7", body)
	req = withSubtitleRouteParam(req, "id", "7")
	rr := httptest.NewRecorder()
	handler.HandlePatchDownloadedSubtitle(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}

	updated := repo.subtitles[7]
	if updated.Language != "es" {
		t.Fatalf("language = %q, want es", updated.Language)
	}
	if updated.ReleaseName != "movie.es.srt" {
		t.Fatalf("release_name = %q, want movie.es.srt", updated.ReleaseName)
	}
	if !updated.HearingImpaired {
		t.Fatal("expected hearing_impaired=true")
	}
	if updated.S3Key == "subtitles/42/en_upload_abcd1234.srt" {
		t.Fatalf("expected migrated s3 key, got %q", updated.S3Key)
	}
	if len(s3.putKeys) != 1 {
		t.Fatalf("putKeys = %d, want 1", len(s3.putKeys))
	}
	if len(s3.deletedKeys) != 1 {
		t.Fatalf("deletedKeys = %d, want 1", len(s3.deletedKeys))
	}
}

func TestHandlePatchDownloadedSubtitleNotFound(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockS3ClientForHandler(), "test-bucket")
	handler := NewAdminSubtitleHandler(repo)
	handler.SetDownloadedSubtitleDeps(nil, manager)

	body := []byte(`{"language":"fr"}`)
	req := newAdminSubtitleRequest(http.MethodPatch, "/admin/subtitles/404", body)
	req = withSubtitleRouteParam(req, "id", "404")
	rr := httptest.NewRecorder()
	handler.HandlePatchDownloadedSubtitle(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestHandleDownloadDownloadedSubtitle(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	content := []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHello\n")
	repo.subtitles[3] = &subtitles.DownloadedSubtitle{
		ID:          3,
		MediaFileID: 10,
		Provider:    subtitles.ProviderUpload,
		Language:    "en",
		Format:      subtitles.FormatVTT,
		ReleaseName: "sample.vtt",
		S3Key:       "subtitles/10/en_upload_deadbeef.vtt",
		CreatedAt:   time.Now(),
	}
	s3 := &trackingHandlerS3Client{objects: map[string][]byte{
		"subtitles/10/en_upload_deadbeef.vtt": content,
	}}
	manager := subtitles.NewManager(repo, s3, "test-bucket")
	handler := NewAdminSubtitleHandler(repo)
	handler.SetDownloadedSubtitleDeps(nil, manager)

	req := newAdminSubtitleRequest(http.MethodGet, "/admin/subtitles/3/download", nil)
	req = withSubtitleRouteParam(req, "id", "3")
	rr := httptest.NewRecorder()
	handler.HandleDownloadDownloadedSubtitle(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/vtt; charset=utf-8" {
		t.Fatalf("content-type = %q", got)
	}
	if !bytes.Contains([]byte(rr.Header().Get("Content-Disposition")), []byte("sample.vtt")) {
		t.Fatalf("content-disposition = %q", rr.Header().Get("Content-Disposition"))
	}
	if !bytes.Equal(rr.Body.Bytes(), content) {
		t.Fatalf("body mismatch")
	}
}

func TestHandleDeleteDownloadedSubtitle(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	repo.subtitles[5] = &subtitles.DownloadedSubtitle{
		ID:          5,
		MediaFileID: 11,
		Provider:    "opensubtitles",
		Language:    "en",
		Format:      subtitles.FormatSRT,
		S3Key:       "subtitles/11/en_opensubtitles_abcd1234.srt",
	}
	repo.byKey[repo.subtitles[5].S3Key] = repo.subtitles[5]
	s3 := &trackingHandlerS3Client{objects: map[string][]byte{
		repo.subtitles[5].S3Key: []byte("subtitle"),
	}}
	manager := subtitles.NewManager(repo, s3, "test-bucket")
	handler := NewAdminSubtitleHandler(repo)
	handler.SetDownloadedSubtitleDeps(nil, manager)

	req := newAdminSubtitleRequest(http.MethodDelete, "/admin/subtitles/5", nil)
	req = withSubtitleRouteParam(req, "id", "5")
	rr := httptest.NewRecorder()
	handler.HandleDeleteDownloadedSubtitle(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if _, ok := repo.subtitles[5]; ok {
		t.Fatal("expected subtitle record deleted")
	}
	if len(s3.deletedKeys) != 1 {
		t.Fatalf("deletedKeys = %d, want 1", len(s3.deletedKeys))
	}
}

type trackingHandlerS3Client struct {
	objects     map[string][]byte
	putKeys     []string
	deletedKeys []string
}

func (c *trackingHandlerS3Client) PutObject(_ context.Context, _, key string, data []byte) error {
	if c.objects == nil {
		c.objects = make(map[string][]byte)
	}
	c.objects[key] = append([]byte(nil), data...)
	c.putKeys = append(c.putKeys, key)
	return nil
}

func (c *trackingHandlerS3Client) GetObject(_ context.Context, _, key string) ([]byte, error) {
	if data, ok := c.objects[key]; ok {
		return append([]byte(nil), data...), nil
	}
	return nil, context.Canceled
}

func (c *trackingHandlerS3Client) DeleteObject(_ context.Context, _, key string) error {
	delete(c.objects, key)
	c.deletedKeys = append(c.deletedKeys, key)
	return nil
}

func TestSubtitleDownloadFilename(t *testing.T) {
	sub := &subtitles.DownloadedSubtitle{
		ID:          9,
		ReleaseName: "../unsafe/path/movie.en.srt",
		Format:      subtitles.FormatSRT,
	}
	got := subtitleDownloadFilename(sub)
	if got != "movie.en.srt" {
		t.Fatalf("filename = %q, want movie.en.srt", got)
	}
	if strconv.Itoa(sub.ID) == "" {
		t.Fatal("unexpected")
	}
}
