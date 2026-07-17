package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
	if !resp.EffectivePlayMethod || !resp.IsJellyfinClient {
		t.Fatalf("capabilities must advertise both fields: %+v", resp)
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
