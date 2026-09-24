package apiv2

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/themedelivery"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

type fakeThemeSongs struct {
	identity themesongs.Identity
	accepted []themesongs.Format
	fail     error
	routed   string
}

func (s *fakeThemeSongs) Discover(_ context.Context, id string, _ bool, _ catalogpkg.AccessFilter) (themesongs.Set, error) {
	return themesongs.Set{OwnerID: id, Items: []themesongs.Song{{ID: "7", Title: "Opening", DurationSeconds: 60, Container: "mp3"}}}, s.fail
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

func TestThemeSongsContractAndAuthorization(t *testing.T) {
	deps, _ := catalogDeps(t)
	deps.ViewerAccess = apimw.NewViewerAccessMiddleware(policyResolver{scope: &access.Scope{PolicyRevision: 7}})
	svc := &fakeThemeSongs{}
	deps.ThemeSongs = svc
	h := newTestHandler(t, deps)
	capability := Prefix + "/catalog/themes/capabilities"
	rec := do(t, h, "GET", capability, "", themeSongHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"delivery":"routed"`) || !strings.Contains(rec.Body.String(), `"cluster_routing":true`) || !strings.Contains(rec.Body.String(), `"transcode":true`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	header := themeSongHeaders()
	header["If-None-Match"] = rec.Header().Get("ETag")
	if got := do(t, h, "GET", capability, "", header); got.Code != 304 {
		t.Fatal(got.Code, got.Body.String())
	}
	requireProblem(t, do(t, h, "GET", capability, "", nil), TypeAuthenticationRequired)
	path := Prefix + "/catalog/items/movie:heat-1995/themes/7/playback"
	requireProblem(t, do(t, h, "POST", path, "", nil), TypeAuthenticationRequired)
	if got := do(t, h, "POST", path, "", bearer(memberToken)); got.Code == 200 {
		t.Fatal("profile-less playback accepted")
	}
	rec = do(t, h, "POST", path, "", themeSongHeaders())
	if rec.Code != 200 || svc.identity.UserID == 0 || svc.identity.ProfileID != "p-owner" || svc.identity.SessionID == "" || svc.identity.PolicyRevision != 7 {
		t.Fatal(rec.Code, rec.Body.String(), svc.identity)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("grant was cacheable")
	}
	if len(svc.accepted) != 0 || !strings.Contains(rec.Body.String(), `"delivery":"original"`) || !strings.Contains(rec.Body.String(), `"content_type":"audio/mpeg"`) ||
		!strings.Contains(rec.Body.String(), `/themes/7/audio?token=fixture-theme-grant`) {
		t.Fatal("bodyless request did not keep the original local grant", rec.Body.String(), svc.accepted)
	}
	rec = do(t, h, "POST", path, `{"accepted_formats":[{"container":"m4a","audio_codec":"aac"},{"container":"flac"}]}`, themeSongHeaders())
	if rec.Code != 200 || len(svc.accepted) != 2 || svc.accepted[0] != (themesongs.Format{Container: "m4a", AudioCodec: "aac"}) ||
		!strings.Contains(rec.Body.String(), `"delivery":"converted"`) || !strings.Contains(rec.Body.String(), `"content_type":"audio/mp4"`) {
		t.Fatal("formats were not negotiated", rec.Code, rec.Body.String(), svc.accepted)
	}
	svc.routed = "https://proxy.example/stream/theme/signed"
	rec = do(t, h, "POST", path, "", themeSongHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"url":"https://proxy.example/stream/theme/signed"`) {
		t.Fatal("routed url was not returned verbatim", rec.Code, rec.Body.String())
	}
	svc.routed = ""
	requireProblem(t, do(t, h, "POST", path, `{"accepted_formats":[{"container":"ogg","audio_codec":"opus"}]}`, themeSongHeaders()), TypeNotAcceptable)
	svc.fail = themedelivery.ErrCapacityUnavailable
	requireProblem(t, do(t, h, "POST", path, "", themeSongHeaders()), TypeDependencyUnavailable)
	svc.fail = nil
	rec = do(t, h, "GET", Prefix+"/catalog/items/movie:heat-1995", "", themeSongHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"themes":{"owner_id":"movie:heat-1995"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, err := range []error{themesongs.ErrNotFound, errors.New("theme store unavailable")} {
		svc.fail = err
		rec = do(t, h, "GET", Prefix+"/catalog/items/movie:heat-1995", "", themeSongHeaders())
		if rec.Code != 200 || strings.Contains(rec.Body.String(), `"themes"`) {
			t.Fatal("optional theme lookup broke item detail", rec.Code, rec.Body.String())
		}
	}
	svc.fail = nil
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec = do(t, h, method, Prefix+"/catalog/items/movie:heat-1995/themes/7/audio?token=bad", "", nil)
		if rec.Code != 401 {
			t.Fatal(method, rec.Code, rec.Body.String())
		}
	}
	deps.ThemeSongs = nil
	rec = do(t, newTestHandler(t, deps), "GET", capability, "", themeSongHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"not_configured"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
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
