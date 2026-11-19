package apiv2

import (
	"context"
	"net/http"
	"os"
	"time"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

type fakeThemeSongs struct {
	identity themesongs.Identity
	accepted []themesongs.Format
	fail     error
	routed   string
}

func (s *fakeThemeSongs) Discover(_ context.Context, item themesongs.Item, _ bool, _ catalogpkg.AccessFilter) (themesongs.Set, error) {
	return themesongs.Set{OwnerID: item.ID, Items: []themesongs.Song{{ID: "7", Title: "Opening", DurationSeconds: 60, Container: "mp3"}}}, s.fail
}

// Authorize mirrors the service's negotiation over a fixed mp3 theme: an
// mp3-capable client gets the original, an mp4/aac-only client the conversion.
func (s *fakeThemeSongs) Authorize(_ context.Context, identity themesongs.Identity, _, _ string, _ catalogpkg.AccessFilter, accepted []themesongs.Format, _ time.Time) (themesongs.Authorization, error) {
	s.identity, s.accepted = identity, accepted
	if s.fail != nil {
		return themesongs.Authorization{}, s.fail
	}
	file := themesongs.File{Song: themesongs.Song{Container: "mp3"}, AudioCodec: "mp3"}
	delivery, ok := themesongs.Negotiate(file, accepted)
	if !ok {
		return themesongs.Authorization{}, themesongs.ErrNotAcceptable
	}
	authorization := themesongs.Authorization{Grant: "fixture-theme-grant", URL: s.routed, Delivery: delivery, ContentType: themesongs.DeliveryContentType(file, delivery), ExpiresAt: time.Date(2026, 1, 2, 3, 9, 5, 0, time.UTC)}
	return authorization, nil
}

func (s *fakeThemeSongs) OpenGrant(context.Context, string, string, string) (themesongs.File, themesongs.Delivery, *os.File, error) {
	return themesongs.File{}, "", nil, themesongs.ErrGrant
}

func (s *fakeThemeSongs) ServeConverted(http.ResponseWriter, *http.Request, themesongs.File) {}

func (s *fakeThemeSongs) ThemeCapabilities(context.Context) themesongs.Capabilities {
	return themesongs.Capabilities{Transcode: true, ClusterRouting: true}
}

// originalThemeSongs grants every audio request the original file at path.
type originalThemeSongs struct {
	fakeThemeSongs
	path string
}

func (s *originalThemeSongs) OpenGrant(context.Context, string, string, string) (themesongs.File, themesongs.Delivery, *os.File, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return themesongs.File{}, "", nil, err
	}
	file := themesongs.File{Song: themesongs.Song{ID: "7", Title: "Opening", Container: "mp3"}, Size: 64 << 10, Modified: time.Unix(1700000000, 0)}
	return file, themesongs.DeliveryOriginal, f, nil
}

func themeSongsFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "theme_songs_capability", operationID: "getThemeSongsCapability", method: "GET", path: Prefix + "/catalog/themes/capabilities", headers: themeSongHeaders(), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/ThemeSongsCapability", scenario: "A profile can discover routed theme audio, conversion and cluster delivery."},
		{name: "theme_songs_playback", operationID: "createThemeSongPlayback", method: "POST", path: Prefix + "/catalog/items/movie:heat-1995/themes/7/playback", headers: themeSongHeaders(), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ThemePlayback", scenario: "A login and verified profile receive a short-lived, non-cacheable theme playback grant."},
		{name: "theme_songs_playback_converted", operationID: "createThemeSongPlayback", method: "POST", path: Prefix + "/catalog/items/movie:heat-1995/themes/7/playback", headers: themeSongHeaders(), body: `{"accepted_formats":[{"container":"m4a","audio_codec":"aac"}]}`, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ThemePlayback", scenario: "A client that cannot decode the original theme receives a grant for its AAC conversion."},
		{name: "theme_songs_playback_not_acceptable", operationID: "createThemeSongPlayback", method: "POST", path: Prefix + "/catalog/items/movie:heat-1995/themes/7/playback", headers: themeSongHeaders(), body: `{"accepted_formats":[{"container":"ogg","audio_codec":"opus"}]}`, status: 406, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/Problem", scenario: "A client that decodes neither the original theme nor its AAC conversion is refused."},
		{name: "theme_songs_playback_unauthorized", operationID: "createThemeSongPlayback", method: "POST", path: Prefix + "/catalog/items/movie:heat-1995/themes/7/playback", status: 401, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/Problem", scenario: "Theme playback cannot be granted without account authentication."},
	}
}

func themeSongHeaders() map[string]string {
	return with(bearer("tok-events"), "X-Profile-Id", "p-owner")
}
