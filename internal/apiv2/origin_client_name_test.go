package apiv2

import (
	"encoding/json"
	"net/http"
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
// family or none, and keeps a version-shaped version; the person's own
// session keeps its name; any other session that declares the reserved name
// itself (any case, spacing or invisible characters) is recorded with no name
// there; and a look-alike is kept on purpose. Catches an app naming its plays
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
		{"own session declaring the reserved name in the declared header", memberToken, map[string]string{"X-Client-Name": "granted APP", "X-Client-Version": "3.1.0"}, "", "3.1.0"},
		{"device signed in by code declaring the reserved name", deviceLoginToken, map[string]string{"X-Silo-Client": "GRANTED APP"}, "", ""},
		{"own session padding the reserved name past the stored length", memberToken, map[string]string{"X-Silo-Client": playback.GrantedAppClientName + strings.Repeat(" ", 200) + "x"}, "", ""},
		{"own session padding the reserved name with zero-width spaces", memberToken, map[string]string{"X-Silo-Client": playback.GrantedAppClientName + strings.Repeat("\u200b", 200) + "x"}, "", ""},
		// A look-alike is a different name and is kept on purpose: a person's
		// own client can already put any name it likes in the lists.
		{"own session declaring a look-alike letter, kept on purpose", memberToken, map[string]string{"X-Silo-Client": "Gr\u0430nted App"}, "Gr\u0430nted App", ""},
		{"own session hiding a Hangul filler in the name", memberToken, map[string]string{"X-Silo-Client": "Granted\u3164App"}, "", ""},
		{"own session declaring punctuation, kept on purpose", memberToken, map[string]string{"X-Silo-Client": "Granted-App"}, "Granted-App", ""},
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

// TestRequestLogNeverReadsAsAnotherApp: the request log shows the reserved
// client name only for a request the origin gate named, a session another
// app's key opened. A caller that sends it where the gate never runs (a public
// operation, a refused credential) or sends a longer name the log's shorter
// cut turns into it is logged with no client name. Catches the request log
// showing another caller as another app.
func TestRequestLogNeverReadsAsAnotherApp(t *testing.T) {
	buf := captureLogs(t)
	h := newTestHandler(t, pilotDeps(nil, nil))
	reserved := `"client_name":"` + playback.GrantedAppClientName + `"`
	for _, tc := range []struct {
		name, path, sent string
		headers          map[string]string
		logged           bool
	}{
		{"public operation", "/system/info", playback.GrantedAppClientName, nil, false},
		{"refused credential", "/account/me", playback.GrantedAppClientName, nil, false},
		{"name the log's cut turns into it", "/system/info", playback.GrantedAppClientName + strings.Repeat(" ", 60) + "x", nil, false},
		{"another app's session", "/account/me", "Silo Web", bearer(grantedAppToken), true},
	} {
		buf.Reset()
		headers := with(tc.headers, "X-Silo-Client", tc.sent)
		do(t, h, http.MethodGet, Prefix+tc.path, "", headers)
		if got := strings.Contains(buf.String(), reserved); got != tc.logged {
			t.Errorf("%s: logged as another app %v, want %v: %s", tc.name, got, tc.logged, buf.String())
		}
	}
}
