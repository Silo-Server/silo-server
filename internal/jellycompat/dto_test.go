package jellycompat

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Audio and subtitle streams carry Jellyfin's flag labels; clients such as
// Wholphin compose track names from them.
func TestMediaStreamFlagLabels(t *testing.T) {
	for _, tc := range []struct {
		stream     mediaStreamDTO
		wantLabels bool
	}{
		{mediaStreamDTO{Type: "Subtitle", IsExternal: true}, true},
		{mediaStreamDTO{Type: "Audio"}, true},
		{mediaStreamDTO{Type: "Video"}, false},
	} {
		body, err := json.Marshal(tc.stream)
		if err != nil {
			t.Fatal(err)
		}
		hasLabel := strings.Contains(string(body), `"LocalizedExternal":"External"`) && strings.Contains(string(body), `"LocalizedDefault":"Default"`)
		if hasLabel != tc.wantLabels {
			t.Errorf("%s stream labels=%v: %s", tc.stream.Type, hasLabel, body)
		}
		// Jellyfin sets the forced/undefined/hearing-impaired labels on subtitles only.
		if strings.Contains(string(body), `"LocalizedForced"`) != (tc.stream.Type == "Subtitle") {
			t.Errorf("%s stream subtitle-only labels: %s", tc.stream.Type, body)
		}
		// The Jellyfin 12 SDK requires IsOriginal on every stream.
		if !strings.Contains(string(body), `"IsOriginal":false`) {
			t.Errorf("%s stream missing IsOriginal: %s", tc.stream.Type, body)
		}
	}
}

// jellyfin-sdk-kotlin requires SplashscreenEnabled; without it Jellyfin for
// Android TV discards the branding response.
func TestBrandingConfigurationIncludesSplashscreenEnabled(t *testing.T) {
	rec := httptest.NewRecorder()
	(&SystemHandler{}).HandleBrandingConfiguration(rec, httptest.NewRequest("GET", "/Branding/Configuration", nil))
	if !strings.Contains(rec.Body.String(), `"SplashscreenEnabled":false`) {
		t.Fatalf("branding: %s", rec.Body.String())
	}
}
