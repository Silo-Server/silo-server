package apiv2

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// TestGrantedAppPlaysUnderTheServersClientName: the client name a playback
// start hands the playback service is what its session, its history row and
// the household streams read carry. A session another app's key opened is
// named playback.GrantedAppClientName whatever it declares, in either header
// family or none, and keeps the version it declared; the person's own session
// keeps its name; and any other session that declares the reserved name, or a
// lookalike, is recorded with no name there. Catches an app naming its plays
// after the person's own apps, and the person's own session, or anyone else,
// being recorded as another app. The gate and the header binding are real;
// the playback service is a fake that records the caller.
func TestGrantedAppPlaysUnderTheServersClientName(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &fakePlaybackService{}
	data, err := os.ReadFile("../playback/testdata/protocol_v3/decision_response.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fake.response); err != nil {
		t.Fatal(err)
	}
	deps.Playback = fake
	h := newTestHandler(t, deps)
	start := playbackJSON(t, playbackStartFixture(t))
	for _, tc := range []struct {
		name, token           string
		headers               map[string]string
		wantName, wantVersion string
	}{
		{"app naming a first-party app", grantedAppToken, map[string]string{"X-Silo-Client": "Silo Web", "X-Silo-Client-Version": "2.4.0"}, playback.GrantedAppClientName, "2.4.0"},
		{"app naming one in the declared header", grantedAppToken, map[string]string{"X-Client-Name": "Silo Android TV", "X-Client-Version": "3.1.0", "X-Silo-Client": "Silo Web"}, playback.GrantedAppClientName, "3.1.0"},
		{"app naming none", grantedAppToken, map[string]string{}, playback.GrantedAppClientName, ""},
		{"own session", memberToken, map[string]string{"X-Silo-Client": "Silo Web", "X-Silo-Client-Version": "2.4.0"}, "Silo Web", "2.4.0"},
		{"own session in the declared header", memberToken, map[string]string{"X-Client-Name": "Silo Android TV", "X-Client-Version": "3.1.0"}, "Silo Android TV", "3.1.0"},
		{"own session declaring the reserved name", memberToken, map[string]string{"X-Silo-Client": playback.GrantedAppClientName, "X-Silo-Client-Version": "2.4.0"}, "", ""},
		{"own session declaring a lookalike in the declared header", memberToken, map[string]string{"X-Client-Name": "granted-app", "X-Client-Version": "3.1.0"}, "", "3.1.0"},
		{"device signed in by code declaring the reserved name", deviceLoginToken, map[string]string{"X-Silo-Client": "GRANTED APP"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake.caller = handlers.PlaybackCaller{}
			rec := do(t, h, http.MethodPost, Prefix+"/playback/start", start, requestHeaders(tc.token, tc.headers))
			if rec.Code < 200 || rec.Code > 299 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if fake.caller.ClientName != tc.wantName || fake.caller.ClientVersion != tc.wantVersion {
				t.Fatalf("client %q %q, want %q %q", fake.caller.ClientName, fake.caller.ClientVersion, tc.wantName, tc.wantVersion)
			}
		})
	}
}
