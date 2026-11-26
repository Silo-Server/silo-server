package abs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestABSClientIsNeverNamedAsAnotherApp: the native API records every play
// of a session another app's key opened under playback.GrantedAppClientName,
// so no Audiobookshelf client is recorded under it, whether it names itself
// in a header or in the session's device info; any other name is kept.
// Catches an Audiobookshelf play reading as another app's in the lists that
// show who played what.
func TestABSClientIsNeverNamedAsAnotherApp(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		device       absDeviceInfo
		want         string
	}{
		{"reserved header", "Granted App", absDeviceInfo{}, "Audiobookshelf"},
		{"reserved device name", "Plappa", absDeviceInfo{ClientName: "GRANTED app", ClientVersion: "1"}, "Plappa"},
		{"reserved header padded past the stored length", "Granted App" + strings.Repeat(" ", 200) + "x", absDeviceInfo{}, "Audiobookshelf"},
		{"reserved device name padded with zero-width spaces", "Plappa", absDeviceInfo{ClientName: "Granted App" + strings.Repeat("\u200b", 200) + "x"}, "Plappa"},
		{"reserved header hiding a Hangul filler", "Granted\u3164App", absDeviceInfo{}, "Audiobookshelf"},
		{"look-alike device name, kept on purpose", "Plappa", absDeviceInfo{ClientName: "Gr\u0430nted App"}, "Gr\u0430nted App"},
		{"other name", "Plappa", absDeviceInfo{}, "Plappa"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/session/local", nil)
		r.Header.Set("X-Silo-Client", tc.header)
		if got := absPlaybackClientInfo(r, tc.device).Name; got != tc.want {
			t.Errorf("%s: recorded as %q, want %q", tc.name, got, tc.want)
		}
	}
}
