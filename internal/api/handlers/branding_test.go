package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/branding"
)

type adminBrandingSettings struct {
	values map[string]string
}

func (s adminBrandingSettings) Get(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s adminBrandingSettings) Set(context.Context, string, string) error { return nil }

type adminBrandingAssets struct {
	url     string
	wantKey string
}

func (s *adminBrandingAssets) PutObject(context.Context, string, string, []byte) error {
	return nil
}

func (s *adminBrandingAssets) GetObject(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (s *adminBrandingAssets) Bucket() string { return "public-assets" }

func (s *adminBrandingAssets) PresignGetURL(_ context.Context, bucket, key string, _ time.Duration) (string, error) {
	if bucket != s.Bucket() {
		return "", errors.New("unexpected bucket")
	}
	if key != s.wantKey {
		return "", errors.New("unexpected key")
	}
	return s.url, nil
}

func TestAdminBrandingReturnsConfiguredNameAndPreferredMark(t *testing.T) {
	settings := adminBrandingSettings{values: map[string]string{
		branding.KeyServerName:  "Living Room Silo",
		"branding.mark_ref":     "mark-ref.webp",
		"branding.wordmark_ref": "wordmark-ref.webp",
	}}
	assets := &adminBrandingAssets{
		url:     "https://garage.example.test/branding/mark/mark-ref.webp",
		wantKey: "branding/mark/mark-ref.webp",
	}
	handler := NewBrandingHandler(branding.NewService(settings, assets))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/branding", nil)
	request.Host = "attacker.example"
	recorder := httptest.NewRecorder()

	handler.HandleAdminBranding(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var got adminBrandingResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ServerName != "Living Room Silo" {
		t.Errorf("server_name = %q, want configured name", got.ServerName)
	}
	if got.LogoURL == nil || *got.LogoURL != assets.url {
		t.Errorf("logo_url = %v, want %q", got.LogoURL, assets.url)
	}
	if got.LogoETag == nil || *got.LogoETag != "mark-ref.webp" {
		t.Errorf("logo_etag = %v, want mark ref", got.LogoETag)
	}
}

func TestAdminBrandingFallsBackToWordmark(t *testing.T) {
	settings := adminBrandingSettings{values: map[string]string{
		branding.KeyServerName:  "Cinema Silo",
		"branding.wordmark_ref": "wordmark-ref.webp",
	}}
	assets := &adminBrandingAssets{
		url:     "https://garage.example.test/branding/wordmark/wordmark-ref.webp",
		wantKey: "branding/wordmark/wordmark-ref.webp",
	}
	handler := NewBrandingHandler(branding.NewService(settings, assets))
	recorder := httptest.NewRecorder()

	handler.HandleAdminBranding(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/branding", nil))

	var got adminBrandingResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.LogoURL == nil || *got.LogoURL != assets.url {
		t.Errorf("logo_url = %v, want %q", got.LogoURL, assets.url)
	}
	if got.LogoETag == nil || *got.LogoETag != "wordmark-ref.webp" {
		t.Errorf("logo_etag = %v, want wordmark ref", got.LogoETag)
	}
}

func TestAdminBrandingReturnsNullLogoMetadataWhenMissing(t *testing.T) {
	handler := NewBrandingHandler(branding.NewService(adminBrandingSettings{values: map[string]string{}}, nil))
	recorder := httptest.NewRecorder()

	handler.HandleAdminBranding(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/branding", nil))

	want := `{"server_name":"Silo","logo_url":null,"logo_etag":null}` + "\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestAdminBrandingReturnsNullLogoMetadataForUnsafeStorageURL(t *testing.T) {
	settings := adminBrandingSettings{values: map[string]string{
		"branding.mark_ref": "mark-ref.webp",
	}}
	assets := &adminBrandingAssets{
		url:     "https://garage.example.test/branding/mark/mark-ref.webp?X-Amz-Expires=900&X-Amz-Signature=secret",
		wantKey: "branding/mark/mark-ref.webp",
	}
	handler := NewBrandingHandler(branding.NewService(settings, assets))
	recorder := httptest.NewRecorder()

	handler.HandleAdminBranding(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/branding", nil))

	var got adminBrandingResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.LogoURL != nil || got.LogoETag != nil {
		t.Fatalf("unsafe storage metadata = (%v, %v), want (nil, nil)", got.LogoURL, got.LogoETag)
	}
}
