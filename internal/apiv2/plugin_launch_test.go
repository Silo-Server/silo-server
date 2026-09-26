package apiv2

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakePluginLaunch struct {
	calls   int
	claims  *auth.Claims
	profile string
	err     error
}

func (f *fakePluginLaunch) PluginLaunchToken(claims *auth.Claims, profileID string) (string, error) {
	f.calls++
	f.claims, f.profile = claims, profileID
	if f.err != nil {
		return "", f.err
	}
	return "plugin-token-" + profileID, nil
}

func pluginLaunchCookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.PluginAccessCookieName {
			return c
		}
	}
	return nil
}

// The v2 launch reissues the v1 cookie on the narrow plugin-content parent
// path with every other attribute unchanged; the path is never /.
func TestPluginLaunchCookieScopeAndAttributes(t *testing.T) {
	f := &fakePluginLaunch{}
	deps := pilotDeps(nil, nil)
	deps.PluginLaunch = f
	h := NewHandler(deps)
	path := Prefix + "/auth/plugin-launch"
	rec := do(t, h, http.MethodPost, path, "", bearer(memberToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"expires_in":300`) || f.calls != 1 || f.profile != "" {
		t.Fatal(rec.Code, rec.Body.String(), f.calls, f.profile)
	}
	raw := rec.Header().Get("Set-Cookie")
	want := "^silo_plugin_access=[^;]+; Path=" + plugins.ContentPrefix + "; Max-Age=300; HttpOnly; SameSite=Lax$"
	if !regexp.MustCompile(want).MatchString(raw) {
		t.Fatalf("Set-Cookie = %q, want match %q", raw, want)
	}
	c := pluginLaunchCookieOf(t, rec)
	if c == nil || c.Path != plugins.ContentPrefix || c.Path == "/" || c.Path == "/api/v1" || !c.HttpOnly || c.Secure || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 300 || c.Value != "plugin-token-" {
		t.Fatalf("cookie = %#v", c)
	}
	if len(rec.Header().Values("Set-Cookie")) != 1 {
		t.Fatal("exactly one cookie is set; the v1-path cookie is not touched here", rec.Header().Values("Set-Cookie"))
	}
	// Behind an HTTPS proxy the cookie gains Secure through the shared seam.
	rec = do(t, h, http.MethodPost, path, "", with(bearer(memberToken), "X-Forwarded-Proto", "https"))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Set-Cookie"), "; Secure") {
		t.Fatal(rec.Code, rec.Header().Get("Set-Cookie"))
	}
	// A declared profile is validated and carried into the token.
	rec = do(t, h, http.MethodPost, path, "", actingRequestAdmin)
	if rec.Code != 200 || f.profile != "p-primary" || f.claims == nil || f.claims.SessionID == "" {
		t.Fatal(rec.Code, rec.Body.String(), f.profile)
	}
}

func TestPluginLaunchRefusals(t *testing.T) {
	f := &fakePluginLaunch{}
	deps := pilotDeps(nil, nil)
	deps.PluginLaunch = f
	h := NewHandler(deps)
	path := Prefix + "/auth/plugin-launch"
	requireProblem(t, do(t, h, http.MethodPost, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, "", with(bearer(memberToken), "X-Profile-Id", "p-missing")), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, path, "", with(bearer(adminToken), "X-Profile-Id", "p-primary-locked")), TypeProfileVerificationRequired)
	if f.calls != 0 {
		t.Fatal("refusal minted a token", f.calls)
	}
	// An API key authenticates but carries no login session: 403, no cookie.
	rec := do(t, h, http.MethodPost, path, "", bearer(apiKeyToken))
	requireProblem(t, rec, TypePermissionDenied)
	if rec.Header().Get("Set-Cookie") != "" || f.calls != 0 {
		t.Fatal("api key must not receive a plugin cookie", rec.Header())
	}
	f.err = errors.New("private signing detail")
	rec = do(t, h, http.MethodPost, path, "", bearer(memberToken))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "private signing") || rec.Header().Get("Set-Cookie") != "" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	deps.PluginLaunch = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, path, "", bearer(memberToken)), TypeDependencyUnavailable)
}
