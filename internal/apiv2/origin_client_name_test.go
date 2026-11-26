package apiv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
		{"own session padding the reserved name past the stored length", memberToken, map[string]string{"X-Silo-Client": playback.GrantedAppClientName + strings.Repeat(" ", 200) + "x"}, "", ""},
		{"own session padding the reserved name with zero-width spaces", memberToken, map[string]string{"X-Silo-Client": playback.GrantedAppClientName + strings.Repeat("\u200b", 200) + "x"}, "", ""},
		{"own session declaring a look-alike letter", memberToken, map[string]string{"X-Silo-Client": "Gr\u0430nted App"}, "", ""},
		{"own session hiding a Hangul filler in the name", memberToken, map[string]string{"X-Silo-Client": "Granted\u3164App"}, "", ""},
		{"own session declaring six look-alike letters", memberToken, map[string]string{"X-Silo-Client": "Gr\u0430nt\u0435\u0501 \u0410\u0440\u0440"}, "", ""},
		{"app carrying a name in a version-shaped word", grantedAppToken, map[string]string{"X-Silo-Client": "Silo Web", "X-Silo-Client-Version": "Silo-for-Apple-TV", "X-Silo-Client-Channel": "Silo-for-Apple-TV"}, playback.GrantedAppClientName, ""},
		// The label is the name and version (build and channel in the full
		// label), so a version that carries a name would read "Granted App
		// (Silo for Apple TV)"; it is dropped and the label is "Granted App".
		{"app carrying a name in its version", grantedAppToken, map[string]string{"X-Silo-Client": "Silo Web", "X-Silo-Client-Version": "(Silo for Apple TV)", "X-Silo-Client-Build": "Silo for Apple TV", "X-Silo-Client-Channel": "release"}, playback.GrantedAppClientName, ""},
		{"own session carrying the same version", memberToken, map[string]string{"X-Silo-Client": "Silo Web", "X-Silo-Client-Version": "(Silo for Apple TV)"}, "Silo Web", "(Silo for Apple TV)"},
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
			if tc.token == grantedAppToken && (strings.ContainsAny(fake.caller.ClientVersion+fake.caller.ClientBuild+fake.caller.ClientChannel, "( )") || strings.Contains(fake.caller.ClientVersion+fake.caller.ClientChannel, "Silo")) {
				t.Fatalf("a name reached the label: version %q build %q channel %q", fake.caller.ClientVersion, fake.caller.ClientBuild, fake.caller.ClientChannel)
			}
		})
	}
}

// TestGrantedAppStartBodyCarriesNoNameInItsVersion: a start falls back to the
// body's app version, build and channel when the headers carry none, so for a
// session another app's key opened each is kept only when it looks like a
// version; the person's own session keeps them as sent. Catches a name
// reaching the label beside "Granted App" through the start body.
func TestGrantedAppStartBodyCarriesNoNameInItsVersion(t *testing.T) {
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
	fixture := playbackStartFixture(t)
	context, _ := fixture["client_playback_context"].(map[string]any)
	context["app_version"], context["app_build"], context["app_channel"] = "(Silo for Apple TV)", "1400", "Silo-for-Apple-TV"
	start := playbackJSON(t, fixture)
	for token, want := range map[string]string{grantedAppToken: "", memberToken: "(Silo for Apple TV)"} {
		rec := do(t, h, http.MethodPost, Prefix+"/playback/start", start, requestHeaders(token, map[string]string{"X-Silo-Client": "Silo Web"}))
		if rec.Code < 200 || rec.Code > 299 {
			t.Fatalf("%s: status %d: %s", token, rec.Code, rec.Body.String())
		}
		wantChannel := "Silo-for-Apple-TV"
		if token == grantedAppToken {
			wantChannel = ""
		}
		if got := fake.request.ClientPlaybackContext; got.AppVersion != want || got.AppBuild != "1400" || got.AppChannel != wantChannel {
			t.Fatalf("%s: app version %q build %q channel %q, want %q, the build kept and channel %q", token, got.AppVersion, got.AppBuild, got.AppChannel, want, wantChannel)
		}
	}
}

// TestRequestLogNeverReadsAsAnotherApp: the request log cuts the client name
// shorter than it is stored, so a name the gate kept because its stored form
// does not read as "Granted App" could read as it once cut. The logged value
// is checked again and left out unless it is the reserved name itself, which
// only the gate sets. Catches the request log showing another caller as
// another app.
func TestRequestLogNeverReadsAsAnotherApp(t *testing.T) {
	for sent, want := range map[string]string{
		playback.GrantedAppClientName + strings.Repeat(" ", 60) + "x": "",
		playback.GrantedAppClientName:                                 playback.GrantedAppClientName,
		"Silo Web":                                                    "Silo Web",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Silo-Client", sent)
		if got, _ := clientIdentity(r); got != want {
			t.Errorf("%q logged as %q, want %q", sent, got, want)
		}
	}
}
