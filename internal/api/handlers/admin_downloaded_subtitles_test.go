package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

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

	s3 := &trackingHandlerBlobStore{objects: map[string][]byte{
		"subtitles/42/en_upload_abcd1234.srt": []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"),
	}}
	manager := subtitles.NewManager(repo, s3)
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
	if updated.S3Key != "subtitles/42/en_upload_abcd1234.srt" {
		t.Fatalf("immutable s3 key changed: %q", updated.S3Key)
	}
	if len(s3.putKeys) != 0 {
		t.Fatalf("metadata edit uploaded content: %v", s3.putKeys)
	}
	if len(s3.deletedKeys) != 0 {
		t.Fatalf("metadata edit deleted content: %v", s3.deletedKeys)
	}
}

type trackingHandlerBlobStore struct {
	objects     map[string][]byte
	putKeys     []string
	deletedKeys []string
}

func (c *trackingHandlerBlobStore) Put(_ context.Context, key string, data []byte) error {
	if c.objects == nil {
		c.objects = make(map[string][]byte)
	}
	c.objects[key] = append([]byte(nil), data...)
	c.putKeys = append(c.putKeys, key)
	return nil
}

func (c *trackingHandlerBlobStore) Get(_ context.Context, key string) ([]byte, error) {
	if data, ok := c.objects[key]; ok {
		return append([]byte(nil), data...), nil
	}
	return nil, context.Canceled
}

func (c *trackingHandlerBlobStore) Delete(_ context.Context, key string) error {
	delete(c.objects, key)
	c.deletedKeys = append(c.deletedKeys, key)
	return nil
}
