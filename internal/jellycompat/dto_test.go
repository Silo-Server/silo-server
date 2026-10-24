package jellycompat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/netaccess"
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

// Jellyfin Web picks its saved max bitrate by IsInNetwork, so the answer must
// follow the same client classification playback uses.
func TestEndpointReportsTheClientNetworkLocation(t *testing.T) {
	h := NewSystemHandler(func() *config.Config { return &config.Config{} })
	for _, tc := range []struct {
		name      string
		ip        string
		provider  string
		inNetwork bool
	}{
		{name: "private", ip: "192.168.1.20", inNetwork: true},
		{name: "public", ip: "203.0.113.7"},
		{name: "unresolved"},
		{name: "network access provider", ip: "10.0.0.9", provider: "tailscale"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := clientip.SetContext(context.Background(), tc.ip)
			if tc.provider != "" {
				ctx = netaccess.WithPath(ctx, netaccess.Path{Provider: tc.provider})
			}
			rec := httptest.NewRecorder()
			h.HandleEndpoint(rec, httptest.NewRequest("GET", "/System/Endpoint", nil).WithContext(ctx))
			var got endpointInfoResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.IsInNetwork != tc.inNetwork || got.IsLocal != tc.inNetwork {
				t.Fatalf("endpoint = %+v, want IsLocal and IsInNetwork %v", got, tc.inNetwork)
			}
		})
	}
}
