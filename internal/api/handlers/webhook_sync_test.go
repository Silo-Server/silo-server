package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/webhooksync"
)

type fakeWebhookDeliveryService struct {
	resolveErr   error
	resolveCalls int
	processCalls int
	logCalls     int
	body         []byte
}

func (s *fakeWebhookDeliveryService) ResolveWebhook(context.Context, string) (*webhooksync.Connection, error) {
	s.resolveCalls++
	if s.resolveErr != nil {
		return nil, s.resolveErr
	}
	return &webhooksync.Connection{}, nil
}

func (s *fakeWebhookDeliveryService) ProcessWebhook(_ context.Context, _ *webhooksync.Connection, r *http.Request) (*webhooksync.ProcessWebhookResult, error) {
	s.processCalls++
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	s.body = body
	return nil, nil
}

func (s *fakeWebhookDeliveryService) CreateEventLog(context.Context, webhooksync.WebhookEventLog) (*webhooksync.WebhookEventLog, error) {
	s.logCalls++
	return nil, nil
}

type webhookFailingReadCloser struct{ err error }

func (r *webhookFailingReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (*webhookFailingReadCloser) Close() error               { return nil }

func TestRequestWebhookURLWithPrefix(t *testing.T) {
	t.Parallel()

	if got := requestWebhookURLWithPrefix("https://example.com/", legacyPlexSyncPathPrefix, "secret"); got != "https://example.com/api/v1/plex-sync/webhooks/secret" {
		t.Fatalf("unexpected legacy webhook URL: %q", got)
	}
	if got := requestWebhookURLWithPrefix("https://example.com/", webhookSyncPathPrefix, "secret"); got != "https://example.com/api/v1/webhook-sync/webhooks/secret" {
		t.Fatalf("unexpected generic webhook URL: %q", got)
	}
}

func TestToLegacyPlexActorsResponse(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	profileID := "profile-1"
	resp := toLegacyPlexActorsResponse(&webhooksync.ProfileMappingsResponse{
		Mappings: []webhooksync.ProfileMapping{
			{
				ID:               11,
				ConnectionID:     "conn-1",
				ExternalUserID:   "42",
				ExternalUserName: "Alice",
				SiloProfileID:    &profileID,
				CreatedAt:        now,
				UpdatedAt:        now,
			},
		},
		DiscoveredUsers: []webhooksync.DiscoveredUser{
			{ExternalUserID: "42", ExternalUserName: "Alice"},
			{ExternalUserID: "77", ExternalUserName: "Bob"},
		},
		AccountDiscoveryAvailable: true,
	})

	if !resp.AccountDiscoveryAvailable {
		t.Fatalf("expected discovery to be available")
	}
	if len(resp.Mappings) != 1 || resp.Mappings[0].PlexAccountID != 42 || resp.Mappings[0].SiloProfileID != "profile-1" {
		t.Fatalf("unexpected legacy mappings: %#v", resp.Mappings)
	}
	if len(resp.DiscoveredActors) != 2 || resp.DiscoveredActors[1].PlexAccountID != 77 {
		t.Fatalf("unexpected legacy discovered actors: %#v", resp.DiscoveredActors)
	}
}

func TestWebhookSyncEndpointAcceptsExactLimit(t *testing.T) {
	delivery := &fakeWebhookDeliveryService{}
	handler := &WebhookSyncHandler{delivery: delivery}
	router := webhookSyncBodyTestRouter(handler)
	body := strings.Repeat("x", maxWebhookSyncBodyBytes)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook-sync/webhooks/secret", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if delivery.processCalls != 1 || len(delivery.body) != len(body) {
		t.Fatalf("process calls = %d, body length = %d", delivery.processCalls, len(delivery.body))
	}
}

func TestWebhookSyncEndpointPreservesUnknownSecretPrecedence(t *testing.T) {
	delivery := &fakeWebhookDeliveryService{resolveErr: webhooksync.ErrConnectionNotFound}
	handler := &WebhookSyncHandler{delivery: delivery}
	router := webhookSyncBodyTestRouter(handler)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook-sync/webhooks/unknown", strings.NewReader(strings.Repeat("x", maxWebhookSyncBodyBytes+1)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if delivery.resolveCalls != 1 || delivery.processCalls != 0 || delivery.logCalls != 0 {
		t.Fatalf("unknown secret reached delivery: resolve=%d process=%d logs=%d", delivery.resolveCalls, delivery.processCalls, delivery.logCalls)
	}
}

func TestWebhookSyncEndpointRejectsProviderBodiesOverAggregateLimit(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		contentType string
		body        func(t *testing.T) []byte
	}{
		{
			name: "plex multipart ignored file",
			path: "/api/v1/plex-sync/webhooks/secret",
			body: func(t *testing.T) []byte {
				t.Helper()
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				payload, err := writer.CreateFormField("payload")
				if err != nil {
					t.Fatal(err)
				}
				_, _ = payload.Write([]byte(`{"event":"media.stop"}`))
				file, err := writer.CreateFormFile("thumb", "thumb.jpg")
				if err != nil {
					t.Fatal(err)
				}
				_, _ = file.Write(bytes.Repeat([]byte("x"), maxWebhookSyncBodyBytes))
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				return body.Bytes()
			},
		},
		{
			name: "plex multipart payload part",
			path: "/api/v1/plex-sync/webhooks/secret",
			body: func(t *testing.T) []byte {
				t.Helper()
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				payload, err := writer.CreateFormField("payload")
				if err != nil {
					t.Fatal(err)
				}
				_, _ = payload.Write(bytes.Repeat([]byte("x"), maxWebhookSyncBodyBytes))
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				return body.Bytes()
			},
		},
		{
			name:        "emby encoded JSON",
			path:        "/api/v1/webhook-sync/webhooks/secret",
			contentType: "application/x-www-form-urlencoded",
			body:        func(*testing.T) []byte { return bytes.Repeat([]byte("x"), maxWebhookSyncBodyBytes+1) },
		},
		{
			name:        "jellyfin JSON",
			path:        "/api/v1/webhook-sync/webhooks/secret",
			contentType: "application/json",
			body:        func(*testing.T) []byte { return bytes.Repeat([]byte("x"), maxWebhookSyncBodyBytes+1) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			delivery := &fakeWebhookDeliveryService{}
			handler := &WebhookSyncHandler{delivery: delivery}
			router := webhookSyncBodyTestRouter(handler)
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(tc.body(t)))
			req.ContentLength = -1
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
			}
			if delivery.processCalls != 0 || delivery.logCalls != 0 {
				t.Fatalf("oversized delivery mutated state: process=%d logs=%d", delivery.processCalls, delivery.logCalls)
			}
		})
	}
}

func TestWebhookSyncEndpointRejectsUnreadableBody(t *testing.T) {
	delivery := &fakeWebhookDeliveryService{}
	handler := &WebhookSyncHandler{delivery: delivery}
	router := webhookSyncBodyTestRouter(handler)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook-sync/webhooks/secret", nil)
	req.Body = &webhookFailingReadCloser{err: errors.New("read failed")}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if delivery.processCalls != 0 || delivery.logCalls != 0 {
		t.Fatalf("unreadable delivery mutated state: process=%d logs=%d", delivery.processCalls, delivery.logCalls)
	}
}

func webhookSyncBodyTestRouter(handler *WebhookSyncHandler) http.Handler {
	router := chi.NewRouter()
	router.Post("/api/v1/plex-sync/webhooks/{secret}", handler.HandleWebhook)
	router.Post("/api/v1/webhook-sync/webhooks/{secret}", handler.HandleWebhook)
	return router
}
