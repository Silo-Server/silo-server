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
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

type fakeThemeSongs struct {
	identity themesongs.Identity
	fail     error
}

func (s *fakeThemeSongs) Discover(_ context.Context, id string, _ bool, _ catalogpkg.AccessFilter) (themesongs.Set, error) {
	return themesongs.Set{OwnerID: id, Items: []themesongs.Song{{ID: "7", Title: "Opening", DurationSeconds: 60, Container: "mp3"}}}, s.fail
}
func (s *fakeThemeSongs) Mint(_ context.Context, identity themesongs.Identity, owner, id string, _ catalogpkg.AccessFilter, _ time.Time) (string, time.Time, error) {
	s.identity = identity
	return "fixture-theme-grant", time.Date(2026, 1, 2, 3, 9, 5, 0, time.UTC), s.fail
}
func (s *fakeThemeSongs) OpenGrant(context.Context, string, string, string) (themesongs.File, *os.File, error) {
	return themesongs.File{}, nil, themesongs.ErrGrant
}

func TestThemeSongsContractAndAuthorization(t *testing.T) {
	deps, _ := catalogDeps(t)
	deps.ViewerAccess = apimw.NewViewerAccessMiddleware(policyResolver{scope: &access.Scope{PolicyRevision: 7}})
	svc := &fakeThemeSongs{}
	deps.ThemeSongs = svc
	h := newTestHandler(t, deps)
	capability := Prefix + "/catalog/themes/capabilities"
	rec := do(t, h, "GET", capability, "", themeSongHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"delivery":"local_direct_play"`) || !strings.Contains(rec.Body.String(), `"cluster_routing":false`) {
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
		{name: "theme_songs_capability", operationID: "getThemeSongsCapability", method: "GET", path: Prefix + "/catalog/themes/capabilities", headers: themeSongHeaders(), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/ThemeSongsCapability", scenario: "A profile can discover local direct-play theme audio and typed delivery limitations."},
		{name: "theme_songs_playback", operationID: "createThemeSongPlayback", method: "POST", path: Prefix + "/catalog/items/movie:heat-1995/themes/7/playback", headers: themeSongHeaders(), status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/ThemePlayback", scenario: "A login and verified profile receive a short-lived, non-cacheable theme playback grant."},
		{name: "theme_songs_playback_unauthorized", operationID: "createThemeSongPlayback", method: "POST", path: Prefix + "/catalog/items/movie:heat-1995/themes/7/playback", status: 401, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/Problem", scenario: "Theme playback cannot be granted without account authentication."},
	}
}

func themeSongHeaders() map[string]string {
	return with(bearer("tok-events"), "X-Profile-Id", "p-owner")
}
