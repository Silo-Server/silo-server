package jellycompat

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestCompatPlaybackTokenRetainsOriginCredential(t *testing.T) {
	keyAuth, _, _ := adminKeyAuthWithPrimary(t, fixedNow)
	keySession, _, ok := keyAuth.resolveSession(t.Context(), "sa_test")
	if !ok || keySession == nil {
		t.Fatal("API key fixture did not authenticate")
	}
	for _, test := range []struct {
		name    string
		session *Session
		claim   string
		want    any
	}{
		{name: "login", session: &Session{StreamAppUserID: 2, ProfileID: "p1", LoginSessionID: "compat-origin-login"}, claim: "olsid", want: "compat-origin-login"},
		{name: "API key", session: keySession, claim: "oakid", want: float64(1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &PlaybackHandler{JWTSecret: "origin-fixture"}
			method := string(playback.PlayTranscode)
			location, err := h.buildProxyRedirectURL("compat-play", "upstream-play", method, &models.MediaFile{FilePath: "/media/fixture.mkv"}, PlaybackMediaSource{FileID: 42}, test.session, time.Now(), "https://worker.example.test", 0, &nodepool.Node{ID: 3, URL: "https://proxy.example.test"}, netaccess.Path{})
			if err != nil {
				t.Fatal(err)
			}
			claims, err := streamtoken.Verify(proxyTokenFromRedirect(t, location, method), h.JWTSecret)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(claims)
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if got[test.claim] != test.want {
				t.Fatalf("compat token lost origin credential: %s", encoded)
			}
			if claims.SessionID != "upstream-play" {
				t.Fatal("login id replaced playback id")
			}
		})
	}
}
