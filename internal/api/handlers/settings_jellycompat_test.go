package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// TestLegacySettingsAPIRejectsJellycompatKeys pins the removal of the
// jellycompat carve-out: DisplayPreferences blobs live in their own table now,
// so the legacy settings endpoints treat jellycompat:* like any other unknown
// key — refused on read and write, and never surfaced by the list.
func TestLegacySettingsAPIRejectsJellycompatKeys(t *testing.T) {
	const jellycompatKey = "jellycompat:displayprefs:usersettings:emby"

	store := newProfileTestStore(t)
	handler := NewSettingsHandler(testUserStoreProvider{store: store})

	authed := func(req *http.Request) *http.Request {
		ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7, TokenType: auth.TokenTypeAccess})
		return req.WithContext(ctx)
	}

	t.Run("write and read are refused", func(t *testing.T) {
		cases := []struct {
			method string
			body   string
			serve  http.HandlerFunc
		}{
			{http.MethodPut, `{"value":"{}"}`, handler.HandleSetSetting},
			{http.MethodGet, "", handler.HandleGetSetting},
			{http.MethodDelete, "", handler.HandleDeleteSetting},
		}
		for _, tc := range cases {
			req := httptest.NewRequest(tc.method, "/settings/"+jellycompatKey, strings.NewReader(tc.body))
			req = withProfileRouteParam(authed(req), "key", jellycompatKey)
			rec := httptest.NewRecorder()

			tc.serve(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s status = %d, want %d (body=%s)",
					tc.method, rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		}
	})

	t.Run("the list never surfaces a leftover row", func(t *testing.T) {
		// A row that predates the move migration must be invisible, not leaked.
		if err := store.SetSetting(context.Background(), jellycompatKey, `{"SortBy":"SortName"}`); err != nil {
			t.Fatalf("seeding leftover row: %v", err)
		}
		// Positive control so an empty list cannot pass vacuously.
		if err := store.SetSetting(context.Background(), dateFormatSettingKey, "auto"); err != nil {
			t.Fatalf("seeding registered setting: %v", err)
		}

		req := authed(httptest.NewRequest(http.MethodGet, "/settings", nil))
		rec := httptest.NewRecorder()
		handler.HandleListSettings(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
		}
		var resp settingsListResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		sawControl := false
		for _, entry := range resp.Settings {
			if strings.HasPrefix(entry.Key, "jellycompat:") {
				t.Errorf("list surfaced %s", entry.Key)
			}
			if entry.Key == dateFormatSettingKey {
				sawControl = true
			}
		}
		if !sawControl {
			t.Errorf("list omitted the registered control key %s; response=%+v", dateFormatSettingKey, resp)
		}
	})
}
