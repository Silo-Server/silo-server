package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type handshakePlugin struct {
	init      *pluginv1.InitAuthorizeRequest
	exchange  *pluginv1.ExchangeCodeRequest
	exchanges int
}

func (f *handshakePlugin) InitAuthorize(_ context.Context, in *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error) {
	f.init = in
	return &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://provider.example/authorize"}, nil
}
func (f *handshakePlugin) ExchangeCode(_ context.Context, in *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error) {
	f.exchange = in
	f.exchanges++
	return &pluginv1.AuthenticateResponse{ExternalSubject: "subject"}, nil
}

type handshakeCompleter struct {
	in    auth.OAuthLoginInput
	calls int
	links int
}

func (f *handshakeCompleter) ResolveOAuthLogin(_ context.Context, in auth.OAuthLoginInput) (*models.User, int64, error) {
	f.in = in
	f.calls++
	return &models.User{ID: 1}, 0, nil
}

func (f *handshakeCompleter) OpenOAuthSession(_ context.Context, _ auth.OAuthSessionDB, c auth.OAuthCompletion) (*auth.TokenPair, error) {
	return &auth.TokenPair{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresIn: 60, SessionID: "s1", User: &models.User{ID: c.UserID}}, nil
}

func (f *handshakeCompleter) LinkOAuthIdentity(_ context.Context, in auth.OAuthLoginInput) (*models.User, error) {
	f.in = in
	f.links++
	return &models.User{ID: in.LinkingUserID}, nil
}

type handshakeRig struct {
	h         http.Handler
	plugin    *handshakePlugin
	completer *handshakeCompleter
}

func newHandshakeRig(t *testing.T) *handshakeRig {
	t.Helper()
	rig := &handshakeRig{plugin: new(handshakePlugin), completer: new(handshakeCompleter)}
	svc := auth.NewOAuthHandler(auth.OAuthHandlerDeps{
		Store: auth.NewInMemoryOAuthStore(), StateSecret: []byte("synthetic-state-secret"), HostBaseURL: "https://silo.example",
		ResolveClient: func(_ context.Context, id int) (auth.OAuthClient, string, error) {
			if id != 3 {
				return nil, "", context.Canceled
			}
			return rig.plugin, "fixture", nil
		},
		LoginCompleter: rig.completer,
		ServerID:       func(context.Context) (string, error) { return fixtureServerID, nil },
		RevokeSession:  func(context.Context, string) error { return nil },
	})
	deps := pilotDeps(nil, nil)
	deps.OAuth = svc
	deps.Accounts = fakeAccounts{users: map[int]handlers.UserView{1: {ID: 1, Username: "laura", Email: "laura@example.test", Role: "user", Permissions: []string{}}}}
	rig.h = newTestHandler(t, deps)
	return rig
}

func (rig *handshakeRig) serve(method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("User-Agent", "Synthetic browser")
	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, req)
	return rec
}

func binderCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if strings.HasPrefix(c.Name, "silo_oauth_") && c.MaxAge > 0 {
			return c
		}
	}
	t.Fatalf("no binding cookie: %v", rec.Header())
	return nil
}

func TestOAuthBrowserHandshakeV2(t *testing.T) {
	rig := newHandshakeRig(t)
	rec := rig.serve("POST", "https://silo.example"+Prefix+"/auth/oauth/3/init?next=%2Fme")
	if rec.Code != 302 || rec.Header().Get("Location") != "https://provider.example/authorize" || rig.plugin.init.GetRedirectUri() != "https://silo.example/api/v2/auth/oauth/3/callback" {
		t.Fatal(rec.Code, rec.Body.String(), rig.plugin.init)
	}
	cookie := binderCookie(t, rec)
	if cookie.Path != "/api/v2/auth/oauth/3/callback" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie = %+v", cookie)
	}
	callback := "https://silo.example" + Prefix + "/auth/oauth/3/callback?state=" + url.QueryEscape(rig.plugin.init.GetState()) + "&code=provider-code"

	// Another browser, holding no binding cookie, cannot finish the flow.
	other := newHandshakeRig(t)
	other.serve("POST", "https://silo.example"+Prefix+"/auth/oauth/3/init")
	rec = other.serve("GET", "https://silo.example"+Prefix+"/auth/oauth/3/callback?state="+url.QueryEscape(other.plugin.init.GetState())+"&code=provider-code")
	if rec.Code != 302 || rec.Header().Get("Location") != "/login?error=oauth_failed&reason=state_invalid" || other.completer.calls != 0 {
		t.Fatal(rec.Code, rec.Header(), other.completer.calls)
	}

	rec = rig.serve("GET", callback, cookie)
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != 302 || location.Path != "/login/oauth-complete" || strings.Contains(location.String(), "synthetic-access") || rig.completer.calls != 1 || rig.completer.in.IP != "192.0.2.1" || rig.plugin.exchange.GetRedirectUri() != rig.plugin.init.GetRedirectUri() {
		t.Fatal(rec.Code, location, rig.completer, rig.plugin.exchange)
	}
	for _, header := range []string{"Cache-Control", "Referrer-Policy"} {
		if rec.Header().Get(header) == "" {
			t.Fatal("missing privacy header")
		}
	}
	code := location.Query().Get("code")
	if code == "" {
		t.Fatal("missing completion code")
	}
	var browser *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.OAuthCompletionCookieName && c.Path == Prefix+"/auth/oauth/complete" {
			browser = c
		}
	}
	if browser == nil || !browser.HttpOnly || !browser.Secure || browser.SameSite != http.SameSiteLaxMode {
		t.Fatalf("completion cookie = %+v", browser)
	}
	inBrowser := map[string]string{"Cookie": browser.Name + "=" + browser.Value}
	body, _ := json.Marshal(map[string]string{"code": code, "code_verifier": fixtureNativeVerifier})
	requireProblem(t, do(t, rig.h, http.MethodPost, Prefix+"/auth/oauth/complete", string(body), inBrowser), TypeInvalidGrant)
	// Login CSRF: another browser given the code cannot redeem it.
	body, _ = json.Marshal(map[string]string{"code": code})
	requireProblem(t, do(t, rig.h, http.MethodPost, Prefix+"/auth/oauth/complete", string(body), nil), TypeInvalidGrant)
	rec = do(t, rig.h, http.MethodPost, Prefix+"/auth/oauth/complete", string(body), inBrowser)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"next":"/me"`) || !strings.Contains(rec.Body.String(), "synthetic-access") || !strings.Contains(rec.Body.String(), `"username":"laura"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, rig.h, "POST", Prefix+"/auth/oauth/complete", string(body), inBrowser), TypeInvalidToken)
	rec = rig.serve("GET", callback, cookie)
	if rec.Code != 302 || !strings.Contains(rec.Header().Get("Location"), "session_expired") || rig.completer.calls != 1 || rig.plugin.exchanges != 1 {
		t.Fatal(rec.Code, rec.Header(), rig.completer.calls, rig.plugin.exchanges)
	}
}

func TestOAuthStartBouncesToThePublicOrigin(t *testing.T) {
	rig := newHandshakeRig(t)
	rec := rig.serve("POST", "http://192.168.1.20:8080"+Prefix+"/auth/oauth/3/init?next=%2Fme")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "https://silo.example/api/v2/auth/oauth/3/start?bounce=1&next=%2Fme" || len(rec.Result().Cookies()) != 0 || rig.plugin.init != nil {
		t.Fatal(rec.Code, rec.Header())
	}
	rec = rig.serve("GET", rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://provider.example/authorize" {
		t.Fatal(rec.Code, rec.Header())
	}
	binderCookie(t, rec)

	// A native start on the app's saved LAN address moves to the public
	// origin with only a random flow ID, and the app redirect names the LAN
	// origin as iss.
	native := "code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256&app_state=abc"
	rec = rig.serve("GET", "http://192.168.1.20:8080"+Prefix+"/auth/oauth/3/native/start?"+native)
	moved, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || rec.Code != http.StatusFound || moved.Scheme+"://"+moved.Host+moved.Path != "https://silo.example/api/v2/auth/oauth/3/native/start" ||
		len(moved.Query()) != 1 || moved.Query().Get("flow") == "" || len(rec.Result().Cookies()) != 0 {
		t.Fatal(rec.Code, rec.Header().Get("Location"))
	}
	rec = rig.serve("GET", moved.String())
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://provider.example/authorize" {
		t.Fatal(rec.Code, rec.Header(), rec.Body.String())
	}
	cookie := binderCookie(t, rec)
	rec = rig.serve("GET", "https://silo.example"+Prefix+"/auth/oauth/3/callback?state="+url.QueryEscape(rig.plugin.init.GetState())+"&code=pc", cookie)
	location, _ := url.Parse(rec.Header().Get("Location"))
	if location == nil || location.Query().Get("iss") != "http://192.168.1.20:8080" || location.Query().Get("state") != "abc" || location.Query().Get("code") == "" {
		t.Fatal(rec.Header().Get("Location"))
	}
	// The flow ID opens one flow; an empty or unknown one opens none.
	for _, target := range []string{moved.String(), "https://silo.example" + Prefix + "/auth/oauth/3/native/start?flow=", "https://silo.example" + Prefix + "/auth/oauth/3/native/start?flow=nope"} {
		if rec := rig.serve("GET", target); rec.Code != http.StatusBadRequest || len(rec.Result().Cookies()) != 0 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Fatal(target, rec.Code, rec.Body.String())
		}
	}
}

func TestOAuthNativeHandshakeV2(t *testing.T) {
	rig := newHandshakeRig(t)
	start := "https://silo.example" + Prefix + "/auth/oauth/3/native/start?code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256&app_state=app-1"
	rec := rig.serve("GET", start)
	if rec.Code != 302 || rec.Header().Get("Location") != "https://provider.example/authorize" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	cookie := binderCookie(t, rec)
	// The web start takes no link ticket; web linking starts with an
	// authenticated startAccountIdentityLink.
	if rec := rig.serve("GET", "https://silo.example"+Prefix+"/auth/oauth/3/start?link_ticket=t"); rec.Code != 400 || len(rec.Result().Cookies()) != 0 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = rig.serve("GET", "https://silo.example"+Prefix+"/auth/oauth/3/callback?state="+url.QueryEscape(rig.plugin.init.GetState())+"&code=pc", cookie)
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "org.siloserver.silo:/auth/callback?") {
		t.Fatal(location)
	}
	parsed, _ := url.Parse(location)
	q := parsed.Query()
	if q.Get("state") != "app-1" || q.Get("server") != fixtureServerID || q.Get("code") == "" {
		t.Fatal(q)
	}
	body, _ := json.Marshal(map[string]string{"code": q.Get("code")})
	requireProblem(t, do(t, rig.h, http.MethodPost, Prefix+"/auth/oauth/complete", string(body), nil), TypeInvalidGrant)
	body, _ = json.Marshal(map[string]string{"code": q.Get("code"), "code_verifier": fixtureNativeVerifier})
	rec = do(t, rig.h, http.MethodPost, Prefix+"/auth/oauth/complete", string(body), nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user":{"id":"1"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}

	for _, bad := range []string{
		"code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=plain&app_state=a",
		"code_challenge=short&code_challenge_method=S256&app_state=a",
		"code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256",
	} {
		rec := rig.serve("GET", "https://silo.example"+Prefix+"/auth/oauth/3/native/start?"+bad)
		if rec.Code != 400 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Fatal(bad, rec.Code, rec.Body.String())
		}
	}
}

func TestOAuthBrowserHandshakeFailures(t *testing.T) {
	h := NewHandler(Dependencies{OAuth: fakeOAuth{}})
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"POST", "/auth/oauth/0/init", 400}, {"POST", "/auth/oauth/2/init", 502}, {"GET", "/auth/oauth/2/start", 502},
		{"GET", "/auth/oauth/2/native/start?code_challenge_method=S256", 502}, {"GET", "/auth/oauth/3/start?link_ticket=t", 400},
		{"GET", "/auth/oauth/3/callback", 400}, {"GET", "/auth/oauth/bad/callback?state=s&code=c", 400},
	} {
		rec := do(t, h, tc.method, Prefix+tc.path, "", nil)
		if rec.Code != tc.code || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Fatal(tc, rec.Code, rec.Body.String())
		}
	}
	h = NewHandler(Dependencies{})
	requireProblem(t, do(t, h, "POST", Prefix+"/auth/oauth/3/init", "", nil), TypeDependencyUnavailable)
	requireProblem(t, do(t, h, "GET", Prefix+"/auth/oauth/3/native/start", "", nil), TypeDependencyUnavailable)
	rec := do(t, h, "GET", Prefix+"/auth/oauth/capabilities", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":false`) || !strings.Contains(rec.Body.String(), `"native":false`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestOAuthHandshakeCapabilities(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, "GET", Prefix+"/auth/oauth/capabilities", "", nil)
	for _, want := range []string{`"available":true`, `"native":true`, `"linking":true`, `"provider_logout":true`, `"select_account":true`} {
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Fatal(want, rec.Code, rec.Body.String())
		}
	}
	// Directory linking needs no handshake: getExternalSignInCapabilities
	// reports it.
	if strings.Contains(rec.Body.String(), "credentials_linking") {
		t.Fatal(rec.Body.String())
	}

	// Native sign-in needs a public URL to finish on.
	rec = do(t, NewHandler(Dependencies{OAuth: noPublicURLOAuth{}}), "GET", Prefix+"/auth/oauth/capabilities", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":true`) || !strings.Contains(rec.Body.String(), `"native":false`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestAccountIdentityLinkTicket(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	path := Prefix + "/account/identities/link-ticket"
	rec := do(t, h, http.MethodPost, path, `{"installation_id":"3","password":"right password"}`, bearer(memberToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ticket":"9d2c5f0e`) || !strings.Contains(rec.Body.String(), `"expires_at":"2026-01-02T03:09:05.678Z"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	p := requireProblem(t, do(t, h, http.MethodPost, path, `{"installation_id":"3","password":"nope"}`, bearer(memberToken)), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.password" || strings.Contains(p.Detail, "nope") {
		t.Fatalf("problem = %+v", p)
	}
	requireProblem(t, do(t, h, http.MethodPost, path, `{"installation_id":"9","password":"right password"}`, bearer(memberToken)), TypeNotFound)
	// The same refusal as the directory linking operation's.
	requireProblem(t, do(t, h, http.MethodPost, path, `{"installation_id":"3","password":"no local password"}`, bearer(memberToken)), TypeLocalPasswordRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"installation_id":"3","password":"right password"}`, bearer(apiKeyToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"installation_id":"3","password":"right password"}`, bearer(impersonatedToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"installation_id":"3","password":"right password"}`, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"installation_id":"0","password":"right password"}`, bearer(memberToken)), TypeValidationFailed)
}

func TestStartAccountIdentityLink(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	path := "https://silo.example.test" + Prefix + "/account/identities/link-start"
	body := `{"link_ticket":"` + fixtureLinkTicket + `","next":"/settings/account"}`
	rec := do(t, h, http.MethodPost, path, body, bearer(memberToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"authorize_url":"https://sso.example.test/authorize?prompt=login`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Path != Prefix+"/auth/oauth/3/callback" {
		t.Fatalf("cookies = %+v", cookies)
	}
	// Only the ticket's own signed-in account, on the public origin.
	requireProblem(t, do(t, h, http.MethodPost, path, body, bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"link_ticket":"other"}`, bearer(memberToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, "http://10.0.0.5:8080"+Prefix+"/account/identities/link-start", body, bearer(memberToken)), TypeConflict)
	requireProblem(t, do(t, h, http.MethodPost, path, body, bearer(apiKeyToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodPost, path, body, bearer(impersonatedToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodPost, path, body, nil), TypeAuthenticationRequired)
}

func TestCompleteAccountIdentityLink(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	path := Prefix + "/account/identities/link-complete"
	body := `{"code":"` + fixtureLinkCode + `","code_verifier":"` + fixtureNativeVerifier + `"}`
	if rec := do(t, h, http.MethodPost, path, body, bearer(memberToken)); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPost, path, `{"code":"`+fixtureLinkCode+`","code_verifier":"`+strings.Repeat("x", 43)+`"}`, bearer(memberToken)), TypeInvalidGrant)
	requireProblem(t, do(t, h, http.MethodPost, path, body, bearer(adminToken)), TypeInvalidToken)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"code":"nope","code_verifier":"`+fixtureNativeVerifier+`"}`, bearer(memberToken)), TypeInvalidToken)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"code":"`+fixtureLinkCode+`"}`, bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, path, body, bearer(apiKeyToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, http.MethodPost, path, body, nil), TypeAuthenticationRequired)
}

func TestGetProviderLogout(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	rec := do(t, h, http.MethodGet, Prefix+"/auth/provider-logout", "", bearer(memberToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"end_session_url":"https://sso.example.test/logout?`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, Prefix+"/auth/provider-logout", "", bearer(adminToken))
	if rec.Code != 200 || rec.Body.String() != `{"end_session_url":""}`+"\n" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// Impersonation sessions and API keys of the linked account get no
	// provider sign-out: it would carry the account's id_token_hint.
	for _, token := range []string{impersonatedToken, apiKeyToken} {
		rec = do(t, h, http.MethodGet, Prefix+"/auth/provider-logout", "", bearer(token))
		if rec.Code != 200 || rec.Body.String() != `{"end_session_url":""}`+"\n" {
			t.Fatal(token, rec.Code, rec.Body.String())
		}
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/auth/provider-logout", "", nil), TypeAuthenticationRequired)
	deps := pilotDeps(nil, nil)
	deps.OAuth = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/auth/provider-logout", "", bearer(memberToken)), TypeDependencyUnavailable)
}

// TestLinkingRefusalsShareProblemTypes: native link completion, link
// tickets and directory linking answer each shared linking refusal with the
// same problem type. A provider's account-disabled denial wraps
// ErrUserDisabled but is account_disabled, not permission_denied.
func TestLinkingRefusalsShareProblemTypes(t *testing.T) {
	for err, want := range map[error]ProblemType{
		auth.ErrLinkTicketPassword:      TypeValidationFailed,
		auth.ErrPasswordLoginDisabled:   TypeLocalPasswordRequired,
		auth.ErrAccountAlreadyLinked:    TypeAlreadyLinked,
		auth.ErrIdentityLinkedElsewhere: TypeIdentityLinkedElsewhere,
		auth.ErrProviderAccountDisabled: TypeAccountDisabled,
		auth.ErrUserDisabled:            TypePermissionDenied,
		auth.ErrNotPermitted:            TypeNotPermitted,
		auth.ErrProviderPasswordExpired: TypeProviderPasswordExpired,
		auth.ErrProviderUnavailable:     TypeProviderUnavailable,
	} {
		for name, render := range map[string]func(error) error{
			"link-complete":    linkCompletionProblem,
			"link-credentials": credentialsLinkProblem,
		} {
			var p *Problem
			if !errors.As(render(err), &p) || p.Type != want.URI() {
				t.Fatalf("%s: %v -> %+v, want %s", name, err, p, want.ID)
			}
			if errors.Is(err, auth.ErrAccountAlreadyLinked) && p.Status != http.StatusConflict {
				t.Fatalf("%s: already-linked status = %d, want409", name, p.Status)
			}
		}
	}
	if !errors.Is(auth.ErrProviderAccountDisabled, auth.ErrUserDisabled) {
		t.Fatal("ErrProviderAccountDisabled no longer wraps ErrUserDisabled; the ordering this test guards is moot")
	}
}
