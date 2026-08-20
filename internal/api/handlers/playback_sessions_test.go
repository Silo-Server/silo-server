package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestSessionComponentDecisionLabelsCopiedAudioDuringHLSAsRemux(t *testing.T) {
	videoDecision, audioDecision := sessionComponentDecision("transcode", false, "copy")

	if videoDecision != "remux" {
		t.Fatalf("videoDecision = %q, want remux", videoDecision)
	}
	if audioDecision != "remux" {
		t.Fatalf("audioDecision = %q, want remux", audioDecision)
	}
}

// TestEffectivePlayMethodBuckets pins the bucket for every decision pair
// sessionComponentDecision can produce, plus the unknown case.
func TestEffectivePlayMethodBuckets(t *testing.T) {
	cases := []struct {
		name           string
		playMethod     string
		transcodeAudio bool
		targetVideo    string
		want           string
	}{
		{"direct play", "direct", false, "", "direct"},
		{"plain remux", "remux", false, "", "remux"},
		{"audio-only re-encode via remux", "remux", true, "", "audio"},
		{"full video transcode", "transcode", true, "h264", "transcode"},
		{"video transcode with copied audio", "transcode", false, "h264", "transcode"},
		{"video-copy HLS repackage", "transcode", false, "copy", "remux"},
		{"video-copy HLS with audio re-encode", "transcode", true, "copy", "audio"},
		// Unknown play_method (stale row from an older node): the bucket must
		// stay empty rather than inventing a method from transcode_audio.
		{"unknown method with transcode_audio set", "hls", true, "", ""},
		{"empty method", "", false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			video, audio := sessionComponentDecision(tc.playMethod, tc.transcodeAudio, tc.targetVideo)
			if got := effectivePlayMethod(video, audio); got != tc.want {
				t.Fatalf("effectivePlayMethod(%q, %q) = %q, want %q", video, audio, got, tc.want)
			}
		})
	}
}

// TestSessionsCapabilitiesAdvertisesActivityFields pins the feature-detection
// contract: the additive session fields are omitempty on the wire, so this
// endpoint is how independently deployed clients distinguish an older server
// from a supported one reporting an unknown method / non-Jellyfin session.
func TestSessionsCapabilitiesAdvertisesActivityFields(t *testing.T) {
	rr := httptest.NewRecorder()
	(&AdminHandler{}).HandleGetSessionsCapabilities(rr, httptest.NewRequest(http.MethodGet, "/admin/sessions/capabilities", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp playbackSessionsCapabilitiesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if !resp.EffectivePlayMethod || !resp.IsJellyfinClient || !resp.ClientBuild || !resp.ClientChannel {
		t.Fatalf("capabilities must advertise every additive field: %+v", resp)
	}
	want := []string{"direct", "remux", "transcode", "audio"}
	if len(resp.EffectivePlayMethodValues) != len(want) {
		t.Fatalf("bucket vocabulary = %v, want %v", resp.EffectivePlayMethodValues, want)
	}
	for i, v := range want {
		if resp.EffectivePlayMethodValues[i] != v {
			t.Fatalf("bucket vocabulary = %v, want %v", resp.EffectivePlayMethodValues, want)
		}
	}
}

func TestIsJellyfinEcosystemClient(t *testing.T) {
	cases := []struct {
		name       string
		clientName string
		userAgent  string
		want       bool
	}{
		{"jellyfin web by name", "Jellyfin Web", "", true},
		{"findroid by name", "Findroid", "Findroid/0.15", true},
		{"infuse by user agent only", "", "Infuse-Direct/8.4.6", true},
		{"kodi addon by name", "Kodi", "Kodi/21.0", true},
		{"mpv shim by user agent", "", "mpv 0.38.0", true},
		{"native android client", "Silo Android", "okhttp/4.12", false},
		{"generic browser", "", "Mozilla/5.0 (X11) Chrome/120.0 Safari/537.36", false},
		{"no metadata", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isJellyfinEcosystemClient(tc.clientName, tc.userAgent); got != tc.want {
				t.Fatalf("isJellyfinEcosystemClient(%q, %q) = %v, want %v", tc.clientName, tc.userAgent, got, tc.want)
			}
		})
	}
}

func TestPlaybackClientDisplayNameAndroidDevices(t *testing.T) {
	const curlClientLabel = "curl"

	cases := []struct {
		name      string
		userAgent string
		want      string
	}{
		{
			name:      "fire tv stick 4k max",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 11; AFTKRT Build/RS8180.3729N)",
			want:      "Fire TV Stick 4K Max",
		},
		{
			name:      "fire tv stick 4k",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 9; AFTMM Build/PS7279)",
			want:      "Fire TV Stick 4K",
		},
		{
			name:      "fire tv stick 4k second generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 11; AFTKM Build/RS8139)",
			want:      "Fire TV Stick 4K (2nd Gen)",
		},
		{
			name:      "fire tv stick 4k max first generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 9; AFTKA Build/PS7646)",
			want:      "Fire TV Stick 4K Max (1st Gen)",
		},
		{
			name:      "fire tv stick third generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 9; AFTSSS Build/PS7279)",
			want:      "Fire TV Stick (3rd Gen)",
		},
		{
			name:      "fire tv stick lite first generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 9; AFTSS Build/PS7279)",
			want:      "Fire TV Stick Lite (1st Gen)",
		},
		{
			name:      "fire tv stick second generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 5.1; AFTT Build/LVY48F)",
			want:      "Fire TV Stick (2nd Gen)",
		},
		{
			name:      "fire tv first generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 4.2; AFTB Build/JDQ39)",
			want:      "Fire TV (1st Gen)",
		},
		{
			name:      "fire tv second generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 5.1; AFTS Build/LVY48F)",
			want:      "Fire TV (2nd Gen)",
		},
		{
			name:      "fire tv third generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 7.1; AFTN Build/NS6265)",
			want:      "Fire TV (3rd Gen)",
		},
		{
			name:      "fire tv cube second generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 9; AFTR Build/PS7646)",
			want:      "Fire TV Cube (2nd Gen)",
		},
		{
			name:      "fire tv cube first generation",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 7.1; AFTA Build/NS6265)",
			want:      "Fire TV Cube (1st Gen)",
		},
		{
			name:      "unmapped multi-word android model preserved",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 13; Pixel 7 Build/TQ3A)",
			want:      "Android · Pixel 7",
		},
		{
			name:      "shield model",
			userAgent: "Dalvik/2.1.0 (Linux; U; Android 11; SHIELD Android TV Build/RQ1A)",
			want:      "NVIDIA Shield",
		},
		{
			name:      "chrome remains browser label",
			userAgent: "Mozilla/5.0 (X11; Linux x86_64) Chrome/120.0.0.0 Safari/537.36",
			want:      "Chrome 120",
		},
		{
			name:      "non-android build user agent keeps existing fallback",
			userAgent: "curl/8.0 (Linux; Device Build/42)",
			want:      curlClientLabel,
		},
		{
			name:      "explicit client fallback wins over android device model",
			userAgent: "curl/8.0 (Linux; U; Android 13; Pixel 7 Build/TQ3A)",
			want:      curlClientLabel,
		},
		{
			name:      "android substring is not an android platform token",
			userAgent: "Dalvik/2.1.0 (Linux; U; NotAndroid 13; Pixel 7 Build/TQ3A)",
			want:      "Dalvik",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := playbackClientDisplayName("", "", tc.userAgent)
			if got != tc.want {
				t.Fatalf("playbackClientDisplayName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlaybackClientDisplayNameKeepsNamedClientVersionExact(t *testing.T) {
	cases := []struct {
		name          string
		clientName    string
		clientVersion string
		want          string
	}{
		{name: "patch version survives", clientName: "Silo Android TV", clientVersion: "1.0.0", want: "Silo Android TV 1.0.0"},
		{name: "prerelease suffix survives", clientName: "Silo iOS", clientVersion: "2.1.0-rc.3", want: "Silo iOS 2.1.0-rc.3"},
		{name: "no version", clientName: "Silo Android TV", clientVersion: "", want: "Silo Android TV"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := playbackClientDisplayName(tc.clientName, tc.clientVersion, "")
			if got != tc.want {
				t.Fatalf("playbackClientDisplayName(%q, %q, \"\") = %q, want %q", tc.clientName, tc.clientVersion, got, tc.want)
			}
		})
	}
}

func TestPlaybackClientFullDisplayName(t *testing.T) {
	cases := []struct {
		name      string
		client    string
		version   string
		build     string
		channel   string
		userAgent string
		want      string
	}{
		{
			name:    "version build and channel",
			client:  "Silo Android TV",
			version: "1.0.0",
			build:   "5",
			channel: "dev",
			want:    "Silo Android TV 1.0.0 (build 5, dev)",
		},
		{
			name:    "release channel omitted",
			client:  "Silo Android TV",
			version: "1.0.0",
			build:   "5",
			channel: "release",
			want:    "Silo Android TV 1.0.0 (build 5)",
		},
		{
			name:    "release channel omitted case insensitively",
			client:  "Silo Android TV",
			version: "1.0.0",
			build:   "5",
			channel: "Release",
			want:    "Silo Android TV 1.0.0 (build 5)",
		},
		{
			name:    "empty channel omitted",
			client:  "Silo Android TV",
			version: "1.0.0",
			build:   "5",
			want:    "Silo Android TV 1.0.0 (build 5)",
		},
		{
			name:    "channel without build",
			client:  "Silo Android TV",
			version: "1.0.0",
			channel: "dev",
			want:    "Silo Android TV 1.0.0 (dev)",
		},
		{
			name:    "build and channel empty",
			client:  "Silo Android TV",
			version: "1.0.0",
			want:    "Silo Android TV 1.0.0",
		},
		{
			name:   "version empty",
			client: "Silo Android TV",
			want:   "Silo Android TV",
		},
		{
			name:    "opaque build is not parsed",
			client:  "Silo tvOS",
			version: "1.0.0",
			build:   "2026.08.13-abcdef",
			want:    "Silo tvOS 1.0.0 (build 2026.08.13-abcdef)",
		},
		{
			// A client that reports a build but not a name is still named by its
			// build: the user-agent label supplies the product half, and dropping
			// the qualifiers would hide the exact field this surface exists for.
			name:      "no client name keeps build and channel on the user agent label",
			userAgent: "Mozilla/5.0 (X11; Linux x86_64) Chrome/120.0.0.0 Safari/537.36",
			build:     "5",
			channel:   "dev",
			want:      "Chrome 120 (build 5, dev)",
		},
		{
			name:      "no client name and no qualifiers keeps the plain user agent label",
			userAgent: "Mozilla/5.0 (X11; Linux x86_64) Chrome/120.0.0.0 Safari/537.36",
			want:      "Chrome 120",
		},
		{
			name: "no client name and no user agent",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := playbackClientFullDisplayName(tc.client, tc.version, tc.build, tc.channel, tc.userAgent)
			if got != tc.want {
				t.Fatalf("playbackClientFullDisplayName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlaybackClientInfoForStartV3(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		context playback.ClientPlaybackContextV3
		want    playback.ClientInfo
	}{
		{
			name:    "headers win over the body",
			headers: map[string]string{"X-Silo-Client": "Silo iOS", "X-Silo-Client-Version": "2.1.0", "X-Silo-Client-Build": "9", "X-Silo-Client-Channel": "beta"},
			context: playback.ClientPlaybackContextV3{AppVersion: "1.0.0", AppBuild: "1", AppChannel: "release"},
			want:    playback.ClientInfo{Name: "Silo iOS", Version: "2.1.0", Build: "9", Channel: "beta"},
		},
		{
			// The fallback is per field, not per struct: a client may set the
			// name and version headers on every request and still report the
			// build it only knows at start time in the body.
			name:    "body fills only the fields the headers omit",
			headers: map[string]string{"X-Silo-Client": "Silo tvOS", "X-Silo-Client-Version": "2.1.0"},
			context: playback.ClientPlaybackContextV3{AppVersion: "1.0.0", AppBuild: "77", AppChannel: "sideload"},
			want:    playback.ClientInfo{Name: "Silo tvOS", Version: "2.1.0", Build: "77", Channel: "sideload"},
		},
		{
			name:    "body supplies everything but the name",
			headers: map[string]string{"X-Silo-Client": "Silo Android TV"},
			context: playback.ClientPlaybackContextV3{AppVersion: "1.0.0", AppBuild: "5", AppChannel: "dev"},
			want:    playback.ClientInfo{Name: "Silo Android TV", Version: "1.0.0", Build: "5", Channel: "dev"},
		},
		{
			// The regression this guards: the web player reports the literal
			// "web" as its app_version and sends no X-Silo-Client. Taking the
			// body anyway would stamp "web" onto client_version — the one field
			// the contract promises is a marketing version — for every browser
			// session, and onto every route event and decision log with it.
			name:    "nameless client keeps the body out of client_version",
			context: playback.ClientPlaybackContextV3{AppVersion: "web"},
			want:    playback.ClientInfo{},
		},
		{
			name:    "nameless client takes no build or channel either",
			context: playback.ClientPlaybackContextV3{AppVersion: "web", AppBuild: "5", AppChannel: "dev"},
			want:    playback.ClientInfo{},
		},
		{
			name:    "whitespace-only header falls through to the body",
			headers: map[string]string{"X-Silo-Client": "Silo iOS", "X-Silo-Client-Build": "   "},
			context: playback.ClientPlaybackContextV3{AppBuild: " 42 "},
			want:    playback.ClientInfo{Name: "Silo iOS", Build: "42"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
			for name, value := range tc.headers {
				req.Header.Set(name, value)
			}
			got := playbackClientInfoForStartV3(req, tc.context)
			got.UserAgent = ""
			if got != tc.want {
				t.Fatalf("playbackClientInfoForStartV3() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestPlaybackClientInfoFromRequestClampsHeaders pins the bound at the request
// boundary rather than at session creation. The resolved identity is written
// straight to the plan-decision log and to playback_route_events, so a client
// sending a header-sized build would reach both if the clamp only happened
// where the session stamps its fields.
func TestPlaybackClientInfoFromRequestClampsHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	req.Header.Set("X-Silo-Client", "  "+strings.Repeat("N", 200)+"  ")
	req.Header.Set("X-Silo-Client-Version", strings.Repeat("v", 100))
	req.Header.Set("X-Silo-Client-Build", strings.Repeat("b", 100))
	req.Header.Set("X-Silo-Client-Channel", strings.Repeat("c", 100))

	got := playbackClientInfoFromRequest(req)

	for _, tc := range []struct {
		field string
		value string
		want  int
	}{
		{"Name", got.Name, 128},
		{"Version", got.Version, 64},
		{"Build", got.Build, 64},
		{"Channel", got.Channel, 32},
	} {
		if len(tc.value) != tc.want {
			t.Errorf("%s length = %d, want %d", tc.field, len(tc.value), tc.want)
		}
	}
}

// TestNormalizeClientMetadataCountsRunes guards the two ways a clamp can go
// wrong on a client-supplied header: the published bound is JSON Schema
// maxLength, which counts characters, and a byte-wise cut through a multi-byte
// rune yields invalid UTF-8 that Postgres refuses — failing the whole per-node
// session-sync transaction, not just the offending row.
func TestNormalizeClientMetadataCountsRunes(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	// 40 three-byte runes: within the 64-character channel bound as the schema
	// counts it, well past it as bytes.
	req.Header.Set("X-Silo-Client-Channel", strings.Repeat("δ", 40))

	got := playbackClientInfoFromRequest(req)

	if runes := utf8.RuneCountInString(got.Channel); runes != 32 {
		t.Errorf("Channel runes = %d, want the 32-character bound", runes)
	}
	if !utf8.ValidString(got.Channel) {
		t.Errorf("Channel = %q is not valid UTF-8; a text column would reject it", got.Channel)
	}
}

// TestPlaybackClientInfoStripsControlCharacters guards the one malformed value
// that reaches a text column looking perfectly well-formed. A JSON NUL escape in a
// v3 start body decodes to a real NUL, which is valid UTF-8 — so the UTF-8
// repair above leaves it and TrimSpace does not consider it whitespace — but
// Postgres refuses NUL in text. The per-node session upserts share one
// transaction, so one such start would stop every live session on that node
// from reconciling. Headers cannot carry it (net/http rejects bytes below
// 0x20), which is exactly why the body path needs its own guard.
func TestPlaybackClientInfoStripsControlCharacters(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	req.Header.Set("X-Silo-Client", "Silo Android TV")

	got := playbackClientInfoForStartV3(req, playback.ClientPlaybackContextV3{
		AppVersion: "1.0\x00.0",
		AppBuild:   "5\x00",
		AppChannel: "de\nv",
	})

	if want := "1.0.0"; got.Version != want {
		t.Errorf("Version = %q, want %q", got.Version, want)
	}
	if want := "5"; got.Build != want {
		t.Errorf("Build = %q, want %q", got.Build, want)
	}
	if want := "dev"; got.Channel != want {
		t.Errorf("Channel = %q, want %q", got.Channel, want)
	}
	for _, field := range []string{got.Name, got.Version, got.Build, got.Channel} {
		if strings.ContainsFunc(field, unicode.IsControl) {
			t.Errorf("%q still carries a control character; a text column would reject it", field)
		}
	}
}

func TestEnrichPlaybackSessionRowUsesCompatOrigin(t *testing.T) {
	row := playbackSessionRow{
		ClientName:      "Unrecognized Client",
		ClientUserAgent: "Dalvik/2.1.0",
		CompatOrigin:    true,
	}

	enrichPlaybackSessionRow(&row, nil)

	if !row.IsJellyfinClient {
		t.Fatal("compat-origin session must be marked as a Jellyfin client")
	}
}
