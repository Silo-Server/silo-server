package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestNativePlaybackTokenRetainsOriginCredential(t *testing.T) {
	for _, origin := range []struct {
		name, login string
		key         int64
	}{
		{name: "login", login: "origin-login-session"}, {name: "API key", key: 73},
	} {
		t.Run(origin.name, func(t *testing.T) {
			manager := playback.NewSessionManager(0, 0)
			handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})
			handler.JWTSecret = "origin-credential-fixture"
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
			handler.ItemAccess = allowAllPlaybackItemAccess{}
			claims := &auth.Claims{UserID: 1, Role: "user", SessionID: origin.login, TokenType: auth.TokenTypeAccess, APIKeyID: origin.key}
			if origin.key != 0 {
				claims.TokenType = auth.TokenTypeAPIKey
			}
			ctx := apimw.SetClaims(newAuthorizedPlaybackContext(), claims)
			rec := httptest.NewRecorder()
			handler.HandleStartPlayback(rec, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest()))).WithContext(ctx))
			var response playback.DecisionResponseV3
			if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.PlaybackPlan == nil {
				t.Fatalf("start=%d %s", rec.Code, rec.Body.String())
			}
			parsed, err := url.Parse(response.PlaybackPlan.Stream.URL)
			if err != nil {
				t.Fatal(err)
			}
			token, err := streamtoken.Verify(parsed.Query().Get(streamTokenParam), handler.JWTSecret)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(token)
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if origin.login != "" && got["olsid"] != origin.login || origin.key != 0 && got["oakid"] != float64(origin.key) {
				t.Fatalf("token lost origin credential: %s", encoded)
			}
			card := playback.RecipeCardFromClaims(token)
			wire, _ := json.Marshal(card)
			var durable playback.RecipeCard
			if err := json.Unmarshal(wire, &durable); err != nil {
				t.Fatal(err)
			}
			tm := playback.NewTranscodeManager()
			tm.Sessions = playback.NewSessionManager(0, 0)
			rebuilt := tm.ReconstructSession(ctx, token.SessionID, token.UserID, durable)
			if rebuilt == nil || rebuilt.OriginCredential != token.OriginCredential {
				t.Fatal("reconstruction lost source credential")
			}
			if reminted := identityRecipeCard(rebuilt).ToClaims(); reminted.OriginCredential != token.OriginCredential {
				t.Fatal("reconstructed token changed source credential")
			}
			if token.SessionID == origin.login {
				t.Fatal("login id replaced playback id")
			}
		})
	}
}

func TestDownloadLinkStreamOriginUsesItsBoundLogin(t *testing.T) {
	claims := &auth.Claims{UserID: 1, TokenType: auth.TokenTypeDirectDownloadLink, SessionID: "link-origin-login", FileID: 42}
	origin := streamOriginCredential(apimw.SetClaims(newAuthorizedPlaybackContext(), claims))
	if origin.OriginLoginSessionID != claims.SessionID || origin.OriginAPIKeyID != 0 {
		t.Fatal("file-link download lost its login binding")
	}
}
