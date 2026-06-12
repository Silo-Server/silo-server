package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

func TestWriteServerChannelErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{notifications.ErrServerChannelsDisabled, http.StatusForbidden},
		{notifications.ErrServerChannelNotFound, http.StatusNotFound},
		{notifications.ErrServerChannelLimit, http.StatusUnprocessableEntity},
		{fmt.Errorf("%w: name is required", notifications.ErrServerChannelInvalid), http.StatusBadRequest},
		{fmt.Errorf("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		writeServerChannelError(rec, tc.err)
		if rec.Code != tc.want {
			t.Errorf("writeServerChannelError(%v) = %d, want %d", tc.err, rec.Code, tc.want)
		}
	}
}

// The response shape must never leak the destination URL (a bearer credential
// for Discord webhooks) or the stored signing secret; signing_secret appears
// only when explicitly set by the create/rotate paths.
func TestServerChannelResponseNeverLeaksSecrets(t *testing.T) {
	secret := "ciphertext-secret"
	ch := notifications.ServerChannel{
		ID:                      "ch-1",
		Name:                    "Community",
		Type:                    "discord",
		URLCiphertext:           "enc:v1:secret-url-material",
		URLHost:                 "discord.com",
		SigningSecretCiphertext: &secret,
		Enabled:                 true,
	}
	body, err := json.Marshal(serverChannelToResponse(ch))
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(body)
	if strings.Contains(encoded, "secret-url-material") || strings.Contains(encoded, "ciphertext-secret") {
		t.Fatalf("response leaked ciphertext material: %s", encoded)
	}
	if strings.Contains(encoded, "signing_secret") {
		t.Fatalf("signing_secret must be omitted unless explicitly set: %s", encoded)
	}
	if !strings.Contains(encoded, `"url_host":"discord.com"`) {
		t.Fatalf("url_host missing from response: %s", encoded)
	}

	response := serverChannelToResponse(ch)
	response.SigningSecret = "shown-once"
	body, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"signing_secret":"shown-once"`) {
		t.Fatalf("explicit signing_secret missing: %s", body)
	}
}

// A handler with no notifications system reports 503 instead of panicking.
func TestServerChannelHandlersUnavailableWithoutSystem(t *testing.T) {
	h := NewAdminServerChannelsHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleList(rec, httptest.NewRequest(http.MethodGet, "/admin/notifications/server-channels", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("HandleList without system = %d, want 503", rec.Code)
	}
}
