package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeOAuthClient struct {
	initResp     *pluginv1.InitAuthorizeResponse
	initErr      error
	exchangeResp *pluginv1.AuthenticateResponse
	exchangeErr  error

	gotInit     *pluginv1.InitAuthorizeRequest
	gotExchange *pluginv1.ExchangeCodeRequest
	inits       int
}

func (f *fakeOAuthClient) InitAuthorize(_ context.Context, in *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error) {
	f.gotInit = in
	f.inits++
	return f.initResp, f.initErr
}

func (f *fakeOAuthClient) ExchangeCode(_ context.Context, in *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error) {
	f.gotExchange = in
	return f.exchangeResp, f.exchangeErr
}

type fakeCompleter struct {
	gotInput   OAuthLoginInput
	pair       *TokenPair
	user       *models.User
	identityID int64
	err        error
	logins     int
	opened     []OAuthCompletion
	openErr    error
	linkInput  OAuthLoginInput
	links      int
	linkErr    error
}

func (f *fakeCompleter) ResolveOAuthLogin(_ context.Context, in OAuthLoginInput) (*models.User, int64, error) {
	f.gotInput = in
	f.logins++
	if f.err != nil {
		return nil, 0, f.err
	}
	return f.user, f.identityID, nil
}

func (f *fakeCompleter) OpenOAuthSession(_ context.Context, _ OAuthSessionDB, c OAuthCompletion) (*TokenPair, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	f.opened = append(f.opened, c)
	if f.pair == nil {
		return nil, nil
	}
	pair := *f.pair
	pair.User = f.user
	return &pair, nil
}

func (f *fakeCompleter) LinkOAuthIdentity(_ context.Context, in OAuthLoginInput) (*models.User, error) {
	f.linkInput = in
	f.links++
	if f.linkErr != nil {
		return nil, f.linkErr
	}
	return &models.User{ID: in.LinkingUserID}, nil
}

type fakeOAuthUsers map[int]*models.User

func (f fakeOAuthUsers) GetByID(_ context.Context, id int) (*models.User, error) {
	if user, ok := f[id]; ok {
		return user, nil
	}
	return nil, ErrNotFound
}

type oauthTestRig struct {
	h       *OAuthHandler
	store   *InMemoryOAuthStore
	client  *fakeOAuthClient
	comp    *fakeCompleter
	revoked []string
}

const testServerID = "3f2a9d5e-6b1c-4c7e-9a0d-2f4b8c1e7a35"

func newOAuthRig(t *testing.T) *oauthTestRig {
	t.Helper()
	pState, _ := structpb.NewStruct(map[string]any{"pkce_verifier": "abc"})
	rig := &oauthTestRig{
		store: NewInMemoryOAuthStore(),
		client: &fakeOAuthClient{
			initResp:     &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://idp.example/authorize", ProviderState: pState},
			exchangeResp: &pluginv1.AuthenticateResponse{ExternalSubject: "https://idp.example|ws-1", Email: "u@x.com", DisplayName: "U"},
		},
		comp: &fakeCompleter{
			pair: &TokenPair{AccessToken: "acc.tok", RefreshToken: "ref.tok", ExpiresIn: 900, SessionID: "sess-1"},
			user: &models.User{ID: 7},
		},
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("right password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	users := fakeOAuthUsers{
		7: {ID: 7, Enabled: true, LocalPasswordLoginEnabled: true, PasswordHash: string(hash)},
		8: {ID: 8, Enabled: true, LocalPasswordLoginEnabled: false, PasswordHash: string(hash)},
	}
	rig.h = NewOAuthHandler(OAuthHandlerDeps{
		Store:       rig.store,
		StateSecret: []byte("test-secret"),
		ResolveClient: func(_ context.Context, id int) (OAuthClient, string, error) {
			if id != 42 {
				return nil, "", ErrUnknownAuthInstallation
			}
			return rig.client, "oidc", nil
		},
		LoginCompleter:       rig.comp,
		HostBaseURL:          "https://silo.test",
		StateTTL:             10 * time.Minute,
		FrontendCompletePath: "/login/oauth-complete",
		ServerID:             func(context.Context) (string, error) { return testServerID, nil },
		RevokeSession: func(_ context.Context, id string) error {
			rig.revoked = append(rig.revoked, id)
			return nil
		},
		Users: users,
		ProviderLogout: func(_ context.Context, userID int, redirect string) (string, error) {
			return "https://idp.example/logout?user=" + url.QueryEscape(redirect), nil
		},
	})
	return rig
}

func withInstallID(r *http.Request, id string) *http.Request {
	rc := chi.NewRouteContext()
	rc.URLParams.Add("install_id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
}

// start runs a v1 init on the public origin and returns the response and
// the binding cookie it set.
func (rig *oauthTestRig) start(t *testing.T, target string) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	rig.h.HandleInit(w, withInstallID(httptest.NewRequest(http.MethodPost, target, nil), "42"))
	var binder *http.Cookie
	for _, c := range w.Result().Cookies() {
		if strings.HasPrefix(c.Name, oauthBinderCookiePrefix) {
			binder = c
		}
	}
	return w, binder
}

// callback answers the provider redirect with the given cookie.
func (rig *oauthTestRig) callback(t *testing.T, installID, query string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := withInstallID(httptest.NewRequest(http.MethodGet, "https://silo.test/api/v1/auth/oauth/"+installID+"/callback?"+query, nil), installID)
	if cookie != nil {
		r.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	w := httptest.NewRecorder()
	rig.h.HandleCallback(w, r)
	return w
}

func (rig *oauthTestRig) stateQuery() string {
	return "code=auth-code&state=" + url.QueryEscape(rig.client.gotInit.GetState())
}

func TestOAuthInit_SetsBindingCookieAndRedirects(t *testing.T) {
	rig := newOAuthRig(t)
	w, cookie := rig.start(t, "https://silo.test/api/v1/auth/oauth/42/init?next=/me")
	if w.Code != http.StatusFound || w.Header().Get("Location") != "https://idp.example/authorize" {
		t.Fatalf("code = %d location = %q body = %s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if cookie == nil {
		t.Fatal("no binding cookie")
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode ||
		cookie.Path != "/api/v1/auth/oauth/42/callback" || cookie.MaxAge != 600+3600 || len(cookie.Value) < 43 {
		t.Fatalf("cookie = %+v", cookie)
	}
	if cookie.Name != OAuthBinderCookieName(rig.client.gotInit.GetState()) {
		t.Fatalf("cookie name %q is not keyed by the flow state", cookie.Name)
	}
	if got := len(rig.store.rows); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
	for _, row := range rig.store.rows {
		if row.InstallID != "42" || row.NextURL != "/me" || row.Kind != OAuthFlowWeb || row.LinkingUserID != "" {
			t.Errorf("row = %+v", row)
		}
		if row.BinderHash != oauthBinderHash(cookie.Value) || row.BinderHash == cookie.Value {
			t.Errorf("binder hash %q does not hash the cookie", row.BinderHash)
		}
		if !strings.Contains(string(row.ProviderState), "pkce_verifier") {
			t.Errorf("ProviderState missing PKCE: %s", row.ProviderState)
		}
	}
	if rig.client.gotInit.GetRedirectUri() != "https://silo.test/api/v1/auth/oauth/42/callback" || rig.client.gotInit.GetLinking() {
		t.Errorf("init = %+v", rig.client.gotInit)
	}
}

func TestOAuthInit_BouncesToPublicOriginOnce(t *testing.T) {
	rig := newOAuthRig(t)
	for _, target := range []string{
		"http://192.168.1.20:8080/api/v1/auth/oauth/42/init?next=/me",
		"http://silo.test/api/v1/auth/oauth/42/init?next=/me", // same host, wrong scheme
		"https://silo.test:8443/api/v1/auth/oauth/42/init?next=/me",
	} {
		w, cookie := rig.start(t, target)
		if w.Code != http.StatusTemporaryRedirect || cookie != nil {
			t.Fatalf("%s: code = %d cookie = %v", target, w.Code, cookie)
		}
		want := "https://silo.test/api/v1/auth/oauth/42/init?bounce=1&next=%2Fme"
		if got := w.Header().Get("Location"); got != want {
			t.Fatalf("%s: Location = %q, want %q", target, got, want)
		}
	}
	if rig.client.inits != 0 || len(rig.store.rows) != 0 {
		t.Fatalf("a bounce contacted the plugin (%d) or stored a flow (%d)", rig.client.inits, len(rig.store.rows))
	}
	// A proxy that hides the public origin must not cause a loop: a start
	// already bounced proceeds.
	w, cookie := rig.start(t, "http://internal:8080/api/v1/auth/oauth/42/init?bounce=1&next=/me")
	if w.Code != http.StatusFound || cookie == nil || rig.client.inits != 1 {
		t.Fatalf("bounced start: code = %d cookie = %v inits = %d", w.Code, cookie, rig.client.inits)
	}
	// The default port is the same origin.
	if _, cookie := rig.start(t, "https://SILO.test:443/api/v1/auth/oauth/42/init"); cookie == nil {
		t.Fatal("explicit default port was treated as another origin")
	}
}

func TestOAuthInit_RefusalsComeBeforeTheBounce(t *testing.T) {
	rig := newOAuthRig(t)
	w := httptest.NewRecorder()
	rig.h.HandleInit(w, withInstallID(httptest.NewRequest(http.MethodPost, "http://other/api/v1/auth/oauth/9/init", nil), "9"))
	if w.Code != http.StatusBadGateway || w.Body.String() != "auth plugin unavailable\n" {
		t.Fatalf("code = %d body = %q", w.Code, w.Body.String())
	}
	for _, c := range []string{"", "abc", "-1", "0"} {
		w := httptest.NewRecorder()
		rig.h.HandleInit(w, withInstallID(httptest.NewRequest(http.MethodPost, "/init", nil), c))
		if w.Code != http.StatusBadRequest {
			t.Errorf("install_id=%q → code %d, want 400", c, w.Code)
		}
	}
}

func TestOAuthInit_PluginErrors(t *testing.T) {
	rig := newOAuthRig(t)
	rig.client.initErr = errors.New("upstream db dsn leaked")
	w, cookie := rig.start(t, "https://silo.test/api/v1/auth/oauth/42/init")
	if w.Code != http.StatusBadGateway || cookie != nil || strings.Contains(w.Body.String(), "upstream") {
		t.Fatalf("code = %d cookie = %v body = %q", w.Code, cookie, w.Body.String())
	}
	rig.client.initErr = nil
	rig.client.initResp = &pluginv1.InitAuthorizeResponse{}
	if w, _ := rig.start(t, "https://silo.test/api/v1/auth/oauth/42/init"); w.Code != http.StatusBadGateway {
		t.Fatalf("empty authorize url: code = %d", w.Code)
	}
}

func TestOAuthInit_WithoutPublicURLIsConflict(t *testing.T) {
	rig := newOAuthRig(t)
	rig.h.SetHostBaseURL("")
	if w, _ := rig.start(t, "https://silo.test/api/v1/auth/oauth/42/init"); w.Code != http.StatusConflict {
		t.Fatalf("code = %d", w.Code)
	}
}

// TestOAuthStart_V2FailuresRedirect: a v2 start that fails on the provider
// or the server sends the browser to the login page (web) or the app
// redirect (native) instead of a raw text page; malformed parameters stay
// plain-text 400s. (The v1 init keeps its plain-text answers, above.)
func TestOAuthStart_V2FailuresRedirect(t *testing.T) {
	start := func(rig *oauthTestRig, installID int, native bool, appState string) *httptest.ResponseRecorder {
		req := OAuthStartRequest{InstallID: installID, Prefix: "/api/v2", Native: native}
		if native {
			req.CodeChallenge, req.AppState = nativeChallenge, appState
		}
		w := httptest.NewRecorder()
		rig.h.ServeStart(w, httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/"+strconv.Itoa(installID)+"/start", nil), req, "", http.StatusFound)
		return w
	}
	const webFailure = "/login?error=oauth_failed&reason=provider_unavailable"
	for name, setup := range map[string]func(*oauthTestRig) int{
		"plugin not loaded":     func(*oauthTestRig) int { return 9 },
		"init authorize failed": func(rig *oauthTestRig) int { rig.client.initErr = errors.New("discovery timed out"); return 42 },
		"no public url":         func(rig *oauthTestRig) int { rig.h.SetHostBaseURL(""); return 42 },
	} {
		t.Run(name, func(t *testing.T) {
			rig := newOAuthRig(t)
			installID := setup(rig)
			if w := start(rig, installID, false, ""); w.Code != http.StatusFound || w.Header().Get("Location") != webFailure || len(w.Result().Cookies()) != 0 {
				t.Fatalf("web: code = %d location = %q", w.Code, w.Header().Get("Location"))
			}
			w := start(rig, installID, true, "app-1")
			loc, _ := url.Parse(w.Header().Get("Location"))
			if w.Code != http.StatusFound || loc == nil || loc.Scheme+":"+loc.Path != NativeAppRedirectURI ||
				loc.Query().Get("error") != OAuthReasonProviderUnavailable || loc.Query().Get("state") != "app-1" ||
				loc.Query().Get("iss") != "https://silo.test" {
				t.Fatalf("native: code = %d location = %q", w.Code, w.Header().Get("Location"))
			}
			// Without a valid app_state there is no app redirect to trust.
			if w := start(rig, installID, true, "has space"); w.Code == http.StatusFound {
				t.Fatalf("native with an invalid app_state redirected to %q", w.Header().Get("Location"))
			}
		})
	}
	rig := newOAuthRig(t)
	w := httptest.NewRecorder()
	rig.h.ServeStart(w, httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/42/start", nil),
		OAuthStartRequest{InstallID: 42, Prefix: "/api/v2", Prompt: "consent"}, "", http.StatusFound)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a malformed start: code = %d", w.Code)
	}
}

func TestNormalizeOAuthNext(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"/me", "/me"},
		{"/me?tab=settings#top", "/me?tab=settings#top"},
		{"  /library/3  ", "/library/3"},
		{"/a%20b", "/a%20b"},
		{"//evil.example/path", "/"},
		{"///evil.example", "/"},
		{"/\\evil.example", "/"},
		{"/\\/evil.example", "/"},
		{"\\\\evil.example", "/"},
		{"/%2Fevil.example", "/"},
		{"/%2fevil.example", "/"},
		{"/%5Cevil.example", "/"},
		{"/%5cevil.example", "/"},
		{"/%252Fevil.example", "/"},
		{"/%25252F%25252Fevil.example", "/"},
		{"/%zz", "/"},
		{"/\t/evil.example", "/"},
		{"/\n/evil.example", "/"},
		{"/%09/evil.example", "/"},
		{"/%0A/evil.example", "/"},
		{"https://evil.example/", "/"},
		{"http:/evil.example", "/"},
		{"javascript:alert(1)", "/"},
		{"data:text/html,x", "/"},
		{"evil.example", "/"},
		{"me", "/"},
		{"/" + strings.Repeat("a", maxNextLength), "/"},
	} {
		if got := normalizeOAuthNext(tc.in); got != tc.want {
			t.Errorf("normalizeOAuthNext(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestOAuthCallback_HappyPath_DeliversOneTimeCompletionCode(t *testing.T) {
	rig := newOAuthRig(t)
	if _, cookie := rig.start(t, "https://silo.test/api/v1/auth/oauth/42/init?next=/me"); cookie == nil {
		t.Fatal("no cookie")
	} else {
		wCb := rig.callback(t, "42", rig.stateQuery(), cookie)
		if wCb.Code != http.StatusFound {
			t.Fatalf("callback code = %d body = %s", wCb.Code, wCb.Body.String())
		}
		cleared := false
		for _, c := range wCb.Result().Cookies() {
			cleared = cleared || (c.Name == cookie.Name && c.MaxAge < 0 && c.Path == cookie.Path)
		}
		if !cleared {
			t.Error("callback did not clear the binding cookie")
		}
		loc := wCb.Header().Get("Location")
		if !strings.HasPrefix(loc, "https://silo.test/login/oauth-complete?") || strings.Contains(loc, "acc.tok") {
			t.Fatalf("redirect = %q", loc)
		}
		parsed, _ := url.Parse(loc)
		code := parsed.Query().Get("code")
		if len(code) < 32 {
			t.Fatalf("completion code %q is shorter than 128 bits", code)
		}
		browser := completionCookieValue(t, wCb)

		// Login CSRF: the code alone, in a browser without the completion
		// cookie (a victim's, sent the attacker's code), is refused and
		// stays redeemable by the browser that signed in.
		for _, other := range []string{"", "another-browser"} {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/complete", strings.NewReader(`{"code":"`+code+`"}`))
			if other != "" {
				r.AddCookie(&http.Cookie{Name: OAuthCompletionCookieName, Value: other})
			}
			w := httptest.NewRecorder()
			rig.h.HandleComplete(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("complete in browser %q = %d", other, w.Code)
			}
		}
		if len(rig.revoked) != 0 {
			t.Fatalf("another browser revoked %v", rig.revoked)
		}

		wComplete := httptest.NewRecorder()
		rComplete := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/complete", strings.NewReader(`{"code":"`+code+`"}`))
		rComplete.AddCookie(&http.Cookie{Name: OAuthCompletionCookieName, Value: browser})
		rig.h.HandleComplete(wComplete, rComplete)
		if wComplete.Code != http.StatusOK {
			t.Fatalf("complete code = %d body=%s", wComplete.Code, wComplete.Body.String())
		}
		var completed OAuthCompleteResponse
		if err := json.NewDecoder(wComplete.Body).Decode(&completed); err != nil {
			t.Fatal(err)
		}
		if completed.AccessToken != "acc.tok" || completed.RefreshToken != "ref.tok" || completed.NextURL != "/me" {
			t.Errorf("completed = %+v", completed)
		}
		if rig.comp.gotInput.InstallationID != 42 || rig.comp.gotInput.CapabilityID != "oidc" ||
			rig.comp.gotInput.Response.GetExternalSubject() != "https://idp.example|ws-1" || rig.comp.gotInput.LinkingUserID != 0 {
			t.Errorf("completer input = %+v", rig.comp.gotInput)
		}

		// A second redemption is refused and revokes the session the code
		// issued.
		if _, err := rig.h.Complete(context.Background(), code, "", browser); !errors.Is(err, ErrOAuthCompletionInvalid) {
			t.Fatalf("reuse err = %v", err)
		}
		if len(rig.revoked) != 1 || rig.revoked[0] != "sess-1" {
			t.Fatalf("revoked = %v", rig.revoked)
		}

		// The flow is single use.
		if w := rig.callback(t, "42", rig.stateQuery(), cookie); !strings.Contains(w.Header().Get("Location"), "reason=session_expired") {
			t.Fatalf("replayed callback: %q", w.Header().Get("Location"))
		}
	}
}

func TestOAuthCallback_RequiresTheStartingBrowser(t *testing.T) {
	for name, cookie := range map[string]func(*http.Cookie) *http.Cookie{
		"no cookie":   func(*http.Cookie) *http.Cookie { return nil },
		"wrong value": func(c *http.Cookie) *http.Cookie { return &http.Cookie{Name: c.Name, Value: c.Value + "x"} },
		"other flow's one": func(c *http.Cookie) *http.Cookie {
			return &http.Cookie{Name: oauthBinderCookiePrefix + "0000", Value: c.Value}
		},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newOAuthRig(t)
			_, real := rig.start(t, "https://silo.test/api/v1/auth/oauth/42/init")
			w := rig.callback(t, "42", rig.stateQuery(), cookie(real))
			if got := w.Header().Get("Location"); got != "/login?error=oauth_failed&reason=state_invalid" {
				t.Fatalf("Location = %q", got)
			}
			if rig.client.gotExchange != nil || rig.comp.logins != 0 {
				t.Fatal("a callback without the binding cookie reached the plugin or the completer")
			}
			if len(rig.store.rows) != 0 {
				t.Fatal("the refused flow was left usable")
			}
		})
	}
}

func TestOAuthCallback_FailureReasons(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(*oauthTestRig)
		query    func(*oauthTestRig) string
		install  string
		reason   string
		noLogins bool
	}{
		{name: "tampered state", query: func(*oauthTestRig) string { return "code=x&state=tampered.junk" }, reason: "state_invalid", noLogins: true},
		{name: "install mismatch", install: "99", reason: "state_invalid", noLogins: true},
		{name: "provider denied", query: func(r *oauthTestRig) string {
			return "error=access_denied&state=" + url.QueryEscape(r.client.gotInit.GetState())
		}, reason: "not_permitted", noLogins: true},
		{name: "provider down", query: func(r *oauthTestRig) string {
			return "error=temporarily_unavailable&state=" + url.QueryEscape(r.client.gotInit.GetState())
		}, reason: "provider_unavailable", noLogins: true},
		{name: "provider misconfigured", query: func(r *oauthTestRig) string {
			return "error=invalid_scope&state=" + url.QueryEscape(r.client.gotInit.GetState())
		}, reason: "login_failed", noLogins: true},
		{name: "exchange fails", setup: func(r *oauthTestRig) { r.client.exchangeErr = errors.New("token endpoint secret context") }, reason: "provider_unavailable", noLogins: true},
		{name: "empty subject", setup: func(r *oauthTestRig) { r.client.exchangeResp = &pluginv1.AuthenticateResponse{} }, reason: "login_failed", noLogins: true},
		{name: "unknown failure", setup: func(r *oauthTestRig) { r.comp.err = errors.New("insert users failed: private db detail") }, reason: "login_failed"},
		{name: "not permitted", setup: func(r *oauthTestRig) { r.comp.err = ErrNotPermitted }, reason: "not_permitted"},
		{name: "account required", setup: func(r *oauthTestRig) { r.comp.err = ErrAccountRequired }, reason: "account_required"},
		{name: "email in use", setup: func(r *oauthTestRig) { r.comp.err = ErrEmailInUse }, reason: "email_in_use"},
		{name: "linked elsewhere", setup: func(r *oauthTestRig) { r.comp.err = ErrIdentityLinkedElsewhere }, reason: "identity_linked_elsewhere"},
		{name: "disabled", setup: func(r *oauthTestRig) { r.comp.err = ErrUserDisabled }, reason: "account_disabled"},
		{name: "provider unavailable", setup: func(r *oauthTestRig) { r.comp.err = ErrProviderUnavailable }, reason: "provider_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newOAuthRig(t)
			if tc.setup != nil {
				tc.setup(rig)
			}
			_, cookie := rig.start(t, "https://silo.test/api/v1/auth/oauth/42/init")
			query := rig.stateQuery()
			if tc.query != nil {
				query = tc.query(rig)
			}
			install := "42"
			if tc.install != "" {
				install = tc.install
			}
			w := rig.callback(t, install, query, cookie)
			if w.Code != http.StatusFound {
				t.Fatalf("code = %d", w.Code)
			}
			if got, want := w.Header().Get("Location"), "/login?error=oauth_failed&reason="+tc.reason; got != want {
				t.Fatalf("Location = %q, want %q", got, want)
			}
			if tc.noLogins && rig.comp.logins != 0 {
				t.Fatal("the completer ran")
			}
		})
	}
}

func TestOAuthCallback_RejectsMissingCodeOrState(t *testing.T) {
	rig := newOAuthRig(t)
	for _, query := range []string{"", "code=x", "state=s"} {
		if w := rig.callback(t, "42", query, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%q: code = %d, want 400", query, w.Code)
		}
	}
}

// nativeVerifier and its S256 challenge (RFC 7636 appendix B).
const (
	nativeVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	nativeChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

func (rig *oauthTestRig) nativeStart(t *testing.T, query string) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	q, _ := url.ParseQuery(query)
	req := OAuthStartRequest{InstallID: 42, Prefix: "/api/v2", Native: true,
		CodeChallenge: q.Get("code_challenge"), AppState: q.Get("app_state"), LinkTicket: q.Get("link_ticket")}
	r := httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/42/native/start?"+query, nil)
	w := httptest.NewRecorder()
	rig.h.ServeStart(w, r, req, "", 0)
	var binder *http.Cookie
	for _, c := range w.Result().Cookies() {
		if strings.HasPrefix(c.Name, oauthBinderCookiePrefix) {
			binder = c
		}
	}
	return w, binder
}

// webStart runs a v2 web start on the public origin (the v1 init takes no
// link ticket).
func (rig *oauthTestRig) webStart(t *testing.T, query string) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	q, _ := url.ParseQuery(query)
	req := OAuthStartRequest{InstallID: 42, Prefix: "/api/v2", Next: q.Get("next"), LinkTicket: q.Get("link_ticket")}
	r := httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/42/start?"+query, nil)
	w := httptest.NewRecorder()
	rig.h.ServeStart(w, r, req, rig.h.PublicURL("/api/v2/auth/oauth/42/start", url.Values{"bounce": {"1"}}), http.StatusFound)
	var binder *http.Cookie
	for _, c := range w.Result().Cookies() {
		if strings.HasPrefix(c.Name, oauthBinderCookiePrefix) {
			binder = c
		}
	}
	return w, binder
}

func (rig *oauthTestRig) v2Callback(t *testing.T, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/42/callback?"+rig.stateQuery(), nil)
	if cookie != nil {
		r.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	w := httptest.NewRecorder()
	rig.h.ServeCallback(w, r, "/api/v2", 42)
	return w
}

func (rig *oauthTestRig) nativeCallback(t *testing.T, cookie *http.Cookie) *url.URL {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/42/callback?"+rig.stateQuery(), nil)
	if cookie != nil {
		r.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	w := httptest.NewRecorder()
	rig.h.ServeCallback(w, r, "/api/v2", 42)
	if w.Code != http.StatusFound {
		t.Fatalf("callback code = %d", w.Code)
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestOAuthNative_HandsOffToTheAppWithAVerifierBoundCode(t *testing.T) {
	rig := newOAuthRig(t)
	w, cookie := rig.nativeStart(t, "code_challenge="+nativeChallenge+"&code_challenge_method=S256&app_state=app-state-1")
	if w.Code != http.StatusFound || cookie == nil || cookie.Path != "/api/v2/auth/oauth/42/callback" {
		t.Fatalf("start: code = %d cookie = %+v body = %s", w.Code, cookie, w.Body.String())
	}
	for _, row := range rig.store.rows {
		if row.Kind != OAuthFlowNative || row.CodeChallenge != nativeChallenge || row.AppState != "app-state-1" {
			t.Fatalf("row = %+v", row)
		}
	}
	loc := rig.nativeCallback(t, cookie)
	if loc.Scheme != "org.siloserver.silo" || loc.Opaque != "/auth/callback" && loc.Path != "/auth/callback" {
		t.Fatalf("redirect = %s", loc)
	}
	if !strings.HasPrefix(loc.String(), NativeAppRedirectURI+"?") {
		t.Fatalf("redirect = %s", loc)
	}
	q := loc.Query()
	code := q.Get("code")
	if len(code) < 32 || q.Get("state") != "app-state-1" || q.Get("server") != testServerID || q.Get("iss") != "https://silo.test" || q.Get("error") != "" {
		t.Fatalf("redirect query = %v", q)
	}

	for _, verifier := range []string{"", "not-the-verifier-not-the-verifier-not-the-verifier", nativeVerifier + "x"} {
		if _, err := rig.h.Complete(context.Background(), code, verifier, ""); !errors.Is(err, ErrOAuthInvalidGrant) {
			t.Fatalf("verifier %q: err = %v", verifier, err)
		}
	}
	// v1 cannot send a verifier, so it cannot redeem a native code.
	w = httptest.NewRecorder()
	rig.h.HandleComplete(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/complete", strings.NewReader(`{"code":"`+code+`"}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("v1 redemption of a native code: %d", w.Code)
	}
	if len(rig.comp.opened) != 0 {
		t.Fatalf("the callback or a refused redemption opened a session: %+v", rig.comp.opened)
	}
	completion, err := rig.h.Complete(context.Background(), code, nativeVerifier, "")
	if err != nil || completion.AccessToken != "acc.tok" || completion.UserID != 7 || completion.SessionID != "sess-1" {
		t.Fatalf("redeem: %+v, %v", completion, err)
	}
	if len(rig.comp.opened) != 1 || rig.comp.opened[0].UserID != 7 {
		t.Fatalf("redemption opened %+v", rig.comp.opened)
	}
	// A second redemption without the verifier (an intercepted redirect)
	// is refused without touching the session.
	for _, verifier := range []string{"", nativeVerifier + "x"} {
		if _, err := rig.h.Complete(context.Background(), code, verifier, ""); !errors.Is(err, ErrOAuthInvalidGrant) {
			t.Fatalf("reuse without the verifier %q: err = %v", verifier, err)
		}
	}
	if len(rig.revoked) != 0 {
		t.Fatalf("a reuse without the verifier revoked %v", rig.revoked)
	}
	if _, err := rig.h.Complete(context.Background(), code, nativeVerifier, ""); !errors.Is(err, ErrOAuthCompletionInvalid) {
		t.Fatalf("reuse err = %v", err)
	}
	if len(rig.revoked) != 1 || rig.revoked[0] != "sess-1" {
		t.Fatalf("revoked = %v", rig.revoked)
	}
}

func TestOAuthNative_WebCodeRefusesAVerifier(t *testing.T) {
	rig := newOAuthRig(t)
	_, cookie := rig.start(t, "https://silo.test/api/v1/auth/oauth/42/init")
	w := rig.callback(t, "42", rig.stateQuery(), cookie)
	loc, _ := url.Parse(w.Header().Get("Location"))
	code, browser := loc.Query().Get("code"), completionCookieValue(t, w)
	if _, err := rig.h.Complete(context.Background(), code, nativeVerifier, browser); !errors.Is(err, ErrOAuthInvalidGrant) {
		t.Fatalf("err = %v", err)
	}
	if _, err := rig.h.Complete(context.Background(), code, "", browser); err != nil {
		t.Fatalf("the refused verifier consumed the web code: %v", err)
	}
}

func TestOAuthNative_StartValidation(t *testing.T) {
	rig := newOAuthRig(t)
	for _, query := range []string{
		"code_challenge=short&app_state=s",
		"code_challenge=" + strings.Repeat("*", 43) + "&app_state=s",
		"code_challenge=" + nativeChallenge,
		"code_challenge=" + nativeChallenge + "&app_state=" + url.QueryEscape("has space"),
		"code_challenge=" + nativeChallenge + "&app_state=" + strings.Repeat("a", 513),
	} {
		if w, cookie := rig.nativeStart(t, query); w.Code != http.StatusBadRequest || cookie != nil {
			t.Errorf("%q: code = %d", query, w.Code)
		}
	}
	if rig.client.inits != 0 {
		t.Fatal("an invalid native start reached the plugin")
	}
}

func TestOAuthNative_FailureGoesToTheApp(t *testing.T) {
	rig := newOAuthRig(t)
	rig.comp.err = ErrNotPermitted
	_, cookie := rig.nativeStart(t, "code_challenge="+nativeChallenge+"&app_state=s1")
	q := rig.nativeCallback(t, cookie).Query()
	if q.Get("error") != "not_permitted" || q.Get("state") != "s1" || q.Get("server") != testServerID || q.Get("iss") != "https://silo.test" || q.Get("code") != "" {
		t.Fatalf("query = %v", q)
	}
	// Without the binding cookie the browser is not trusted with the app's
	// state: the web login page answers instead.
	rig = newOAuthRig(t)
	rig.nativeStart(t, "code_challenge="+nativeChallenge+"&app_state=s1")
	r := httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/42/callback?"+rig.stateQuery(), nil)
	w := httptest.NewRecorder()
	rig.h.ServeCallback(w, r, "/api/v2", 42)
	if got := w.Header().Get("Location"); got != "/login?error=oauth_failed&reason=state_invalid" {
		t.Fatalf("Location = %q", got)
	}
}

// lanOrigin is a server address an app saved that is not the public URL.
const lanOrigin = "http://10.0.0.5:8080"

// serveStart answers one start request as the transport hands it over.
func (rig *oauthTestRig) serveStart(r *http.Request, req OAuthStartRequest) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	rig.h.ServeStart(w, r, req, "", 0)
	return w
}

// serveStartVia answers a start behind the client IP middleware, which
// resolves the scheme a trusted proxy names.
func (rig *oauthTestRig) serveStartVia(resolver *clientip.Resolver, r *http.Request, req OAuthStartRequest) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	clientip.Middleware(resolver)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.h.ServeStart(w, r, req, "", 0)
	})).ServeHTTP(w, r)
	return w
}

// nativeStartOn runs a native start that arrives on origin with app_state
// appState (and a link ticket when ticket is not empty).
func (rig *oauthTestRig) nativeStartOn(origin, appState, ticket string) *httptest.ResponseRecorder {
	query := "code_challenge=" + nativeChallenge + "&code_challenge_method=S256&app_state=" + appState
	if ticket != "" {
		query += "&link_ticket=" + ticket
	}
	return rig.serveStart(httptest.NewRequest(http.MethodGet, origin+"/api/v2/auth/oauth/42/native/start?"+query, nil),
		OAuthStartRequest{InstallID: 42, Prefix: "/api/v2", Native: true, CodeChallenge: nativeChallenge, AppState: appState, LinkTicket: ticket})
}

// resumeNative opens a parked native start on the public origin.
func (rig *oauthTestRig) resumeNative(flow string, installID int) *httptest.ResponseRecorder {
	return rig.serveStart(httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/"+strconv.Itoa(installID)+"/native/start?flow="+url.QueryEscape(flow), nil),
		OAuthStartRequest{InstallID: installID, Prefix: "/api/v2", Native: true, Flow: flow})
}

// parkedFlow returns the flow ID of a start that was moved to the public
// origin, failing when the response is anything else.
func parkedFlow(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != http.StatusFound || loc.Scheme+"://"+loc.Host+loc.Path != "https://silo.test/api/v2/auth/oauth/42/native/start" {
		t.Fatalf("not moved to the public origin: code = %d location = %q", w.Code, w.Header().Get("Location"))
	}
	if binderOf(w) != nil {
		t.Fatal("the move set a binding cookie")
	}
	// Only the opaque ID travels in the browser.
	if len(loc.Query()) != 1 || len(loc.Query().Get(OAuthNativeFlowParameter)) != 64 {
		t.Fatalf("move query = %v", loc.Query())
	}
	return loc.Query().Get(OAuthNativeFlowParameter)
}

func binderOf(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if strings.HasPrefix(c.Name, oauthBinderCookiePrefix) && c.MaxAge > 0 {
			return c
		}
	}
	return nil
}

// TestOAuthNative_StartOffThePublicOriginIsParked: a native start on the
// app's saved LAN address is kept on the server, the browser reaches the
// public origin with only a random single-use ID, and every app redirect of
// the flow names the LAN origin as iss.
func TestOAuthNative_StartOffThePublicOriginIsParked(t *testing.T) {
	rig := newOAuthRig(t)
	ctx := context.Background()
	start := func() *httptest.ResponseRecorder {
		return rig.serveStart(httptest.NewRequest(http.MethodGet, lanOrigin+"/api/v2/auth/oauth/42/native/start?code_challenge="+nativeChallenge+
			"&code_challenge_method=S256&app_state=app-state-lan&prompt=select_account", nil),
			OAuthStartRequest{InstallID: 42, Prefix: "/api/v2", Native: true, CodeChallenge: nativeChallenge, AppState: "app-state-lan", Prompt: OIDCPromptSelectAccount})
	}
	w := start()
	flow := parkedFlow(t, w)
	for _, leak := range []string{nativeChallenge, "app-state-lan", "10.0.0.5", OIDCPromptSelectAccount} {
		if strings.Contains(w.Header().Get("Location"), leak) {
			t.Fatalf("the move carries %q: %s", leak, w.Header().Get("Location"))
		}
	}
	if rig.client.inits != 0 || len(rig.store.rows) != 0 {
		t.Fatal("the move opened a flow")
	}
	parked, ok := rig.store.starts[flow]
	if !ok || parked.StartOrigin != lanOrigin || parked.AppState != "app-state-lan" || parked.Prompt != OIDCPromptSelectAccount ||
		parked.ExpiresAt.After(time.Now().Add(oauthNativeStartTTL)) {
		t.Fatalf("parked start = %+v", parked)
	}
	// Presented anywhere but the public origin, the ID is refused like an
	// unknown one and stays parked for the public origin.
	for _, origin := range []string{lanOrigin, "http://silo.test", "https://other.example"} {
		w := rig.serveStart(httptest.NewRequest(http.MethodGet, origin+"/api/v2/auth/oauth/42/native/start?flow="+flow, nil),
			OAuthStartRequest{InstallID: 42, Prefix: "/api/v2", Native: true, Flow: flow})
		if w.Code != http.StatusBadRequest || binderOf(w) != nil || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") ||
			strings.TrimSpace(w.Body.String()) != nativeStartUnknownMessage {
			t.Fatalf("flow on %s: code = %d type = %q body = %q", origin, w.Code, w.Header().Get("Content-Type"), w.Body.String())
		}
		if _, ok := rig.store.starts[flow]; !ok || rig.client.inits != 0 {
			t.Fatalf("flow on %s consumed the start or opened a flow", origin)
		}
	}

	w = rig.resumeNative(flow, 42)
	cookie := binderOf(w)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "https://idp.example/authorize" || cookie == nil ||
		rig.client.gotInit.GetPrompt() != OIDCPromptSelectAccount {
		t.Fatalf("resume: code = %d location = %q init = %+v", w.Code, w.Header().Get("Location"), rig.client.gotInit)
	}
	q := rig.nativeCallback(t, cookie).Query()
	if q.Get("iss") != lanOrigin || q.Get("state") != "app-state-lan" || q.Get("server") != testServerID || q.Get("code") == "" {
		t.Fatalf("app redirect = %v", q)
	}
	if _, err := rig.h.Complete(ctx, q.Get("code"), nativeVerifier, ""); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	// The ID opens one flow; an unknown or altered ID opens none.
	inits := rig.client.inits
	for name, id := range map[string]string{"used": flow, "unknown": strings.Repeat("0", 64), "altered": flow[:63] + "x"} {
		if w := rig.resumeNative(id, 42); w.Code != http.StatusBadRequest || binderOf(w) != nil {
			t.Fatalf("%s ID: code = %d location = %q", name, w.Code, w.Header().Get("Location"))
		}
	}
	// Presented for another installation, the ID is refused and burned.
	flow = parkedFlow(t, start())
	if w := rig.resumeNative(flow, 43); w.Code != http.StatusBadRequest {
		t.Fatalf("foreign installation: code = %d", w.Code)
	}
	if w := rig.resumeNative(flow, 42); w.Code != http.StatusBadRequest {
		t.Fatalf("after a foreign attempt: code = %d", w.Code)
	}
	// An expired ID is refused.
	flow = parkedFlow(t, start())
	expired := rig.store.starts[flow]
	expired.ExpiresAt = time.Now().Add(-time.Second)
	rig.store.starts[flow] = expired
	if w := rig.resumeNative(flow, 42); w.Code != http.StatusBadRequest {
		t.Fatalf("expired ID: code = %d", w.Code)
	}
	if rig.client.inits != inits || len(rig.store.starts) != 0 {
		t.Fatalf("refused IDs: inits = %d (want %d), parked = %d", rig.client.inits, inits, len(rig.store.starts))
	}
}

// TestOAuthNative_FailuresNameTheStartOrigin: failures and linking flows
// that started on the LAN address name it too.
func TestOAuthNative_FailuresNameTheStartOrigin(t *testing.T) {
	ctx := context.Background()

	// The provider fails after the move.
	rig := newOAuthRig(t)
	rig.client.initErr = errors.New("discovery timed out")
	w := rig.resumeNative(parkedFlow(t, rig.nativeStartOn(lanOrigin, "s1", "")), 42)
	loc, _ := url.Parse(w.Header().Get("Location"))
	if loc == nil || loc.Query().Get("error") != OAuthReasonProviderUnavailable || loc.Query().Get("iss") != lanOrigin || loc.Query().Get("state") != "s1" {
		t.Fatalf("Location = %q", w.Header().Get("Location"))
	}

	// A bad link ticket is answered on the LAN start itself.
	rig = newOAuthRig(t)
	w = rig.nativeStartOn(lanOrigin, "s2", "not-a-ticket")
	loc, _ = url.Parse(w.Header().Get("Location"))
	if loc == nil || loc.Scheme+":"+loc.Path != NativeAppRedirectURI || loc.Query().Get("error") != OAuthReasonSessionExpired ||
		loc.Query().Get("iss") != lanOrigin || loc.Query().Get("state") != "s2" || len(rig.store.starts) != 0 {
		t.Fatalf("Location = %q", w.Header().Get("Location"))
	}

	// A linking flow consumes its ticket on the LAN start, and the link
	// redirect names the LAN origin.
	rig = newOAuthRig(t)
	ticket, err := rig.h.IssueLinkTicket(ctx, 7, 42, "right password")
	if err != nil {
		t.Fatal(err)
	}
	flow := parkedFlow(t, rig.nativeStartOn(lanOrigin, "s3", ticket.Ticket))
	if len(rig.store.tickets) != 0 || strings.Contains(rig.store.starts[flow].StartOrigin, "silo.test") || rig.store.starts[flow].LinkingUserID != 7 {
		t.Fatalf("parked link = %+v, tickets left = %d", rig.store.starts[flow], len(rig.store.tickets))
	}
	cookie := binderOf(rig.resumeNative(flow, 42))
	if cookie == nil || !rig.client.gotInit.GetLinking() || rig.client.gotInit.GetPrompt() != oidcPromptLogin {
		t.Fatalf("link resume: init = %+v", rig.client.gotInit)
	}
	q := rig.nativeCallback(t, cookie).Query()
	if q.Get("link") != "1" || q.Get("iss") != lanOrigin || q.Get("state") != "s3" || q.Get("code") == "" {
		t.Fatalf("link redirect = %v", q)
	}
	if err := rig.h.CompleteLink(ctx, 7, q.Get("code"), nativeVerifier); err != nil || rig.comp.links != 1 {
		t.Fatalf("complete link: %v, links = %d", err, rig.comp.links)
	}
}

// TestOAuthNative_StartOriginComesFromTheRequest: the start origin is the
// origin the request reached. A hostile server that redirects the app's
// start here (a relay) can only send the browser to one of this server's
// own origins, so iss names this server and an app that saved the hostile
// origin refuses the redirect. Forwarding headers count only from a trusted
// proxy.
func TestOAuthNative_StartOriginComesFromTheRequest(t *testing.T) {
	req := OAuthStartRequest{InstallID: 42, Prefix: "/api/v2", Native: true, CodeChallenge: nativeChallenge, AppState: "relayed"}
	relayed := func(origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, origin+"/api/v2/auth/oauth/42/native/start?code_challenge="+nativeChallenge+"&code_challenge_method=S256&app_state=relayed", nil)
		r.Header.Set("Referer", "https://hostile.example/api/v2/auth/oauth/42/native/start")
		r.RemoteAddr = "198.51.100.7:4321"
		return r
	}

	// Relayed to the public origin: the flow opens there and names it.
	rig := newOAuthRig(t)
	w := rig.serveStart(relayed("https://silo.test"), req)
	cookie := binderOf(w)
	if w.Code != http.StatusFound || cookie == nil || len(rig.store.starts) != 0 {
		t.Fatalf("public start: code = %d location = %q", w.Code, w.Header().Get("Location"))
	}
	if got := rig.nativeCallback(t, cookie).Query().Get("iss"); got != "https://silo.test" {
		t.Fatalf("iss = %q", got)
	}

	// Relayed to a LAN address: forged forwarding headers from an untrusted
	// peer do not change the origin.
	rig = newOAuthRig(t)
	r := relayed(lanOrigin)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "hostile.example")
	flow := parkedFlow(t, rig.serveStartVia(clientip.NewResolver(nil), r, req))
	if got := rig.store.starts[flow].StartOrigin; got != lanOrigin {
		t.Fatalf("untrusted forwarding headers: start origin = %q", got)
	}

	// A trusted proxy's scheme counts, with the Host it passes on.
	rig = newOAuthRig(t)
	_, trusted, _ := net.ParseCIDR("198.51.100.0/24")
	r = relayed("http://Media.Home.Arpa:8443")
	r.Header.Set("X-Forwarded-Proto", "https")
	flow = parkedFlow(t, rig.serveStartVia(clientip.NewResolver([]*net.IPNet{trusted}), r, req))
	if got := rig.store.starts[flow].StartOrigin; got != "https://media.home.arpa:8443" {
		t.Fatalf("trusted proxy: start origin = %q", got)
	}
	// The same proxy forwarding the public origin opens the flow directly.
	rig = newOAuthRig(t)
	r = relayed("http://silo.test")
	r.Header.Set("X-Forwarded-Proto", "https")
	if w := rig.serveStartVia(clientip.NewResolver([]*net.IPNet{trusted}), r, req); binderOf(w) == nil || len(rig.store.starts) != 0 {
		t.Fatalf("trusted proxy on the public origin: code = %d location = %q", w.Code, w.Header().Get("Location"))
	}
}

// TestOAuthNative_StartOnAnUnknownHostIsRefused: whoever sends a native
// start chooses its Host, so a hostile saved server could send the app's
// start here itself with its own origin as Host and get that origin as iss.
// A start on another origin than the public URL is parked only on an origin
// a server outside the local network cannot hold.
func TestOAuthNative_StartOnAnUnknownHostIsRefused(t *testing.T) {
	req := OAuthStartRequest{InstallID: 42, Prefix: "/api/v2", Native: true, CodeChallenge: nativeChallenge, AppState: "relayed"}
	forged := func(origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, origin+"/api/v2/auth/oauth/42/native/start?code_challenge="+nativeChallenge+"&code_challenge_method=S256&app_state=relayed", nil)
		r.RemoteAddr = "198.51.100.7:4321"
		return r
	}
	refused := func(name string, rig *oauthTestRig, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" || binderOf(w) != nil ||
			len(rig.store.starts) != 0 || len(rig.store.rows) != 0 || rig.client.inits != 0 {
			t.Fatalf("%s: code = %d location = %q parked = %d", name, w.Code, w.Header().Get("Location"), len(rig.store.starts))
		}
	}

	// An untrusted peer naming the hostile origin as Host.
	rig := newOAuthRig(t)
	refused("untrusted peer", rig, rig.serveStartVia(clientip.NewResolver(nil), forged("http://hostile.example"), req))

	// A trusted proxy that names the scheme and passes on an unknown Host.
	_, trusted, _ := net.ParseCIDR("198.51.100.0/24")
	rig = newOAuthRig(t)
	r := forged("http://hostile.example")
	r.Header.Set("X-Forwarded-Proto", "https")
	refused("trusted proxy, unknown Host", rig, rig.serveStartVia(clientip.NewResolver([]*net.IPNet{trusted}), r, req))

	// A trusted proxy naming an unknown host in X-Forwarded-Host.
	rig = newOAuthRig(t)
	r = forged("http://10.0.0.5:8080")
	r.Header.Set("X-Forwarded-Host", "hostile.example")
	refused("trusted proxy, unknown X-Forwarded-Host", rig, rig.serveStartVia(clientip.NewResolver([]*net.IPNet{trusted}), r, req))

	// A start whose provider cannot start (here an installation whose
	// plugin does not resolve) is still a plain-text 400, never an app
	// redirect naming the unknown origin as iss.
	rig = newOAuthRig(t)
	failing := req
	failing.InstallID = 43
	refused("provider unavailable", rig, rig.serveStart(forged("http://hostile.example"), failing))

	// A connected network access provider's origin is known.
	rig = newOAuthRig(t)
	rig.h.deps.KnownOrigins = func() []string { return []string{"https://Silo.Tailnet.Example"} }
	flow := parkedFlow(t, rig.serveStart(forged("https://silo.tailnet.example"), req))
	if got := rig.store.starts[flow].StartOrigin; got != "https://silo.tailnet.example" {
		t.Fatalf("provider origin: start origin = %q", got)
	}
}

// TestOAuthNative_TrustedProxyForwardedHost: a reverse proxy that rewrites
// Host to its upstream address and names the client's host in
// X-Forwarded-Host keeps a start on the public URL on the public origin.
func TestOAuthNative_TrustedProxyForwardedHost(t *testing.T) {
	req := OAuthStartRequest{InstallID: 42, Prefix: "/api/v2", Native: true, CodeChallenge: nativeChallenge, AppState: "proxied"}
	_, trusted, _ := net.ParseCIDR("198.51.100.0/24")
	rig := newOAuthRig(t)
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8096/api/v2/auth/oauth/42/native/start?code_challenge="+nativeChallenge+"&code_challenge_method=S256&app_state=proxied", nil)
	r.RemoteAddr = "198.51.100.7:4321"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "silo.test")
	w := rig.serveStartVia(clientip.NewResolver([]*net.IPNet{trusted}), r, req)
	cookie := binderOf(w)
	if w.Code != http.StatusFound || cookie == nil || len(rig.store.starts) != 0 {
		t.Fatalf("start: code = %d location = %q parked = %d", w.Code, w.Header().Get("Location"), len(rig.store.starts))
	}
	if got := rig.nativeCallback(t, cookie).Query().Get("iss"); got != "https://silo.test" {
		t.Fatalf("iss = %q", got)
	}
}

func TestOAuthKnownStartOrigin(t *testing.T) {
	h := NewOAuthHandler(OAuthHandlerDeps{HostBaseURL: "https://silo.test", KnownOrigins: func() []string {
		return []string{"https://media.tailnet.example:443", "not a url"}
	}})
	for origin, want := range map[string]bool{
		"http://10.0.0.5:8080":            true,
		"http://192.168.1.20":             true,
		"http://172.16.4.1:8096":          true,
		"http://127.0.0.1:8096":           true,
		"http://100.101.102.103:8096":     true,
		"http://169.254.10.10":            true,
		"http://[fd00::5]:8096":           true,
		"http://[fe80::1]":                true,
		"http://[::1]:8096":               true,
		"http://nas:8096":                 true,
		"http://nas.local:8096":           true,
		"http://silo.lan":                 true,
		"http://silo.localdomain":         true,
		"https://silo.home.arpa":          true,
		"http://silo.corp.internal":       true,
		"https://media.tailnet.example":   true,
		"http://media.tailnet.example":    false,
		"https://hostile.example":         false,
		"https://hostile.example.local.x": false,
		"http://203.0.113.9:8096":         false,
		"http://[2001:db8::1]":            false,
		"http://100.128.0.1":              false,
		"https://silo.example.ts.net":     false,
		"":                                false,
	} {
		if got := h.knownStartOrigin(origin); got != want {
			t.Errorf("knownStartOrigin(%q) = %v, want %v", origin, got, want)
		}
	}
}

func TestOAuthSessionNativeNeedsStartOrigin(t *testing.T) {
	sess := OAuthSession{State: "s", InstallID: "1", RedirectURI: "https://silo.test/cb", Kind: OAuthFlowNative, CodeChallenge: nativeChallenge, ExpiresAt: time.Now().Add(time.Minute)}
	if err := validateSession(&sess); err == nil {
		t.Fatal("native session without a start origin was accepted")
	}
	sess.StartOrigin = lanOrigin
	if err := validateSession(&sess); err != nil {
		t.Fatalf("native session with a start origin: %v", err)
	}
}

func TestOAuthLinkTickets(t *testing.T) {
	rig := newOAuthRig(t)
	ctx := context.Background()
	if _, err := rig.h.IssueLinkTicket(ctx, 7, 42, "wrong password"); !errors.Is(err, ErrLinkTicketPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := rig.h.IssueLinkTicket(ctx, 8, 42, "right password"); !errors.Is(err, ErrPasswordLoginDisabled) {
		t.Fatalf("no local password: %v", err)
	}
	if _, err := rig.h.IssueLinkTicket(ctx, 7, 9, "right password"); !errors.Is(err, ErrUnknownAuthInstallation) {
		t.Fatalf("unknown installation: %v", err)
	}
	resolve := rig.h.deps.ResolveClient
	rig.h.deps.ResolveClient = func(context.Context, int) (OAuthClient, string, error) {
		return nil, "", errors.New("plugin is not running")
	}
	if _, err := rig.h.IssueLinkTicket(ctx, 7, 42, "right password"); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("unreachable plugin: %v", err)
	}
	rig.h.deps.ResolveClient = resolve
	ticket, err := rig.h.IssueLinkTicket(ctx, 7, 42, "right password")
	if err != nil || len(ticket.Ticket) < 32 || ticket.ExpiresAt.Before(time.Now().Add(4*time.Minute)) {
		t.Fatalf("ticket = %+v, %v", ticket, err)
	}

	// The web GET start takes no ticket: whoever opens such a URL would get
	// the flow's binding cookie.
	if w, cookie := rig.webStart(t, "next=/settings/account&link_ticket="+ticket.Ticket); w.Code != http.StatusBadRequest || cookie != nil || rig.client.inits != 0 {
		t.Fatalf("web start with a ticket: code = %d cookie = %v inits = %d", w.Code, cookie, rig.client.inits)
	}

	// The web linking flow starts on the account's authenticated request,
	// asks the provider for a fresh sign-in, links, opens no session and
	// returns to next.
	result, err := rig.h.StartLink(ctx, 7, "/api/v2", ticket.Ticket, "/settings/account")
	if err != nil || result.Cookie == nil || result.AuthorizeURL != "https://idp.example/authorize" {
		t.Fatalf("link start: %+v, %v", result, err)
	}
	if !rig.client.gotInit.GetLinking() || rig.client.gotInit.GetPrompt() != "login" {
		t.Fatalf("init = %+v", rig.client.gotInit)
	}
	for _, row := range rig.store.rows {
		if row.LinkingUserID != "7" || row.Kind != OAuthFlowWeb {
			t.Fatalf("row = %+v", row)
		}
	}
	w := rig.v2Callback(t, result.Cookie)
	if got := w.Header().Get("Location"); got != "/settings/account?linked=1" {
		t.Fatalf("Location = %q", got)
	}
	if rig.comp.links != 1 || rig.comp.logins != 0 || rig.comp.linkInput.LinkingUserID != 7 {
		t.Fatalf("links = %d logins = %d input = %+v", rig.comp.links, rig.comp.logins, rig.comp.linkInput)
	}

	// A ticket starts one flow.
	if _, err := rig.h.StartLink(ctx, 7, "/api/v2", ticket.Ticket, "/"); !errors.Is(err, ErrOAuthLinkTicketInvalid) {
		t.Fatalf("reused ticket: %v", err)
	}

	// A browser without the requesting browser's cookie cannot finish the
	// flow, so a victim sent to the provider URL links nothing.
	ticket, _ = rig.h.IssueLinkTicket(ctx, 7, 42, "right password")
	if _, err := rig.h.StartLink(ctx, 7, "/api/v2", ticket.Ticket, "/settings/account"); err != nil {
		t.Fatal(err)
	}
	if got := rig.v2Callback(t, nil).Header().Get("Location"); got != "/login?error=oauth_failed&reason=state_invalid" {
		t.Fatalf("callback in another browser: Location = %q", got)
	}
	if rig.comp.links != 1 {
		t.Fatal("a flow finished in another browser linked an identity")
	}

	// Another account cannot use the ticket, and the attempt burns it.
	ticket, _ = rig.h.IssueLinkTicket(ctx, 7, 42, "right password")
	if _, err := rig.h.StartLink(ctx, 9, "/api/v2", ticket.Ticket, "/"); !errors.Is(err, ErrOAuthLinkTicketInvalid) {
		t.Fatalf("foreign ticket: %v", err)
	}
	if _, err := rig.h.StartLink(ctx, 7, "/api/v2", ticket.Ticket, "/"); !errors.Is(err, ErrOAuthLinkTicketInvalid) {
		t.Fatalf("burned ticket: %v", err)
	}

	// A refused link reports on the return path.
	ticket, _ = rig.h.IssueLinkTicket(ctx, 7, 42, "right password")
	rig.comp.linkErr = ErrIdentityLinkedElsewhere
	result, _ = rig.h.StartLink(ctx, 7, "/api/v2", ticket.Ticket, "/settings/account")
	w = rig.v2Callback(t, result.Cookie)
	if got := w.Header().Get("Location"); got != "/settings/account?error=oauth_link_failed&reason=identity_linked_elsewhere" {
		t.Fatalf("Location = %q", got)
	}

	// A ticket for an installation that is not an OAuth provider is refused.
	ticket, _ = rig.h.IssueLinkTicket(ctx, 7, 42, "right password")
	rig.store.tickets[ticket.Ticket] = OAuthLinkTicket{Ticket: ticket.Ticket, UserID: 7, InstallationID: 43, ExpiresAt: ticket.ExpiresAt}
	if _, err := rig.h.StartLink(ctx, 7, "/api/v2", ticket.Ticket, "/"); !errors.Is(err, ErrUnknownAuthInstallation) {
		t.Fatalf("other installation: %v", err)
	}

	// The native linking flow parks the link under a code bound to the
	// app's challenge; nothing is linked until the ticket's account redeems
	// it with the verifier.
	rig.comp.linkErr = nil
	links := rig.comp.links
	ticket, _ = rig.h.IssueLinkTicket(ctx, 7, 42, "right password")
	_, cookie := rig.nativeStart(t, "code_challenge="+nativeChallenge+"&app_state=s2&link_ticket="+ticket.Ticket)
	if rig.client.gotInit.GetPrompt() != "login" {
		t.Fatalf("native link init = %+v", rig.client.gotInit)
	}
	q := rig.nativeCallback(t, cookie).Query()
	code := q.Get("code")
	if code == "" || q.Get("link") != "1" || q.Get("state") != "s2" || q.Get("server") != testServerID || q.Get("iss") != "https://silo.test" || rig.comp.links != links {
		t.Fatalf("native link redirect = %v, links = %d", q, rig.comp.links)
	}
	if _, err := rig.h.Complete(ctx, code, nativeVerifier, ""); !errors.Is(err, ErrOAuthCompletionInvalid) {
		t.Fatalf("a link code redeemed as a sign-in: %v", err)
	}
	if err := rig.h.CompleteLink(ctx, 7, code, strings.Repeat("x", 43)); !errors.Is(err, ErrOAuthInvalidGrant) {
		t.Fatalf("wrong verifier: %v", err)
	}
	if err := rig.h.CompleteLink(ctx, 7, code, nativeVerifier); err != nil {
		t.Fatalf("complete link: %v", err)
	}
	if rig.comp.links != links+1 || rig.comp.linkInput.LinkingUserID != 7 || rig.comp.linkInput.InstallationID != 42 {
		t.Fatalf("links = %d input = %+v", rig.comp.links, rig.comp.linkInput)
	}
	if err := rig.h.CompleteLink(ctx, 7, code, nativeVerifier); !errors.Is(err, ErrOAuthCompletionInvalid) {
		t.Fatalf("second redemption: %v", err)
	}

	// Another account holding the code and verifier links nothing.
	ticket, _ = rig.h.IssueLinkTicket(ctx, 7, 42, "right password")
	_, cookie = rig.nativeStart(t, "code_challenge="+nativeChallenge+"&app_state=s4&link_ticket="+ticket.Ticket)
	code = rig.nativeCallback(t, cookie).Query().Get("code")
	if err := rig.h.CompleteLink(ctx, 9, code, nativeVerifier); !errors.Is(err, ErrOAuthCompletionInvalid) {
		t.Fatalf("foreign account: %v", err)
	}
	if err := rig.h.CompleteLink(ctx, 7, code, nativeVerifier); !errors.Is(err, ErrOAuthCompletionInvalid) || rig.comp.links != links+1 {
		t.Fatalf("after a foreign attempt: %v, links = %d", err, rig.comp.links)
	}

	// An expired ticket is refused like a used one.
	expired := OAuthLinkTicket{Ticket: "expired-ticket", UserID: 7, InstallationID: 42, ExpiresAt: time.Now().Add(-time.Second)}
	if err := rig.store.InsertLinkTicket(ctx, expired); err != nil {
		t.Fatal(err)
	}
	w, cookie = rig.nativeStart(t, "code_challenge="+nativeChallenge+"&app_state=s3&link_ticket=expired-ticket")
	if cookie != nil {
		t.Fatal("an expired ticket started a flow")
	}
	if loc, _ := url.Parse(w.Header().Get("Location")); loc == nil || loc.Query().Get("error") != "session_expired" || loc.Query().Get("iss") != "https://silo.test" {
		t.Fatalf("expired ticket: Location = %q", w.Header().Get("Location"))
	}
}

// TestOAuthCallback_ExpiredFlowReachesItsClient: a callback whose state
// expired (a slow provider sign-in) in the browser that started the flow
// ends at the flow's own failure location with session_expired; without the
// binding cookie it still ends on the web login with state_invalid.
func TestOAuthCallback_ExpiredFlowReachesItsClient(t *testing.T) {
	expire := func(rig *oauthTestRig) { rig.h.deps.StateTTL = -time.Second }

	t.Run("web", func(t *testing.T) {
		rig := newOAuthRig(t)
		expire(rig)
		_, cookie := rig.webStart(t, "next=/me")
		if got := rig.v2Callback(t, cookie).Header().Get("Location"); got != "/login?error=oauth_failed&reason=session_expired" {
			t.Fatalf("Location = %q", got)
		}
	})

	t.Run("native", func(t *testing.T) {
		rig := newOAuthRig(t)
		expire(rig)
		_, cookie := rig.nativeStart(t, "code_challenge="+nativeChallenge+"&code_challenge_method=S256&app_state=slow-mfa")
		loc := rig.nativeCallback(t, cookie)
		if loc.Scheme+":"+loc.Path != NativeAppRedirectURI || loc.Query().Get("error") != OAuthReasonSessionExpired ||
			loc.Query().Get("state") != "slow-mfa" || loc.Query().Get("code") != "" {
			t.Fatalf("Location = %q", loc)
		}
		if rig.comp.logins != 0 {
			t.Fatal("an expired flow signed in")
		}
	})

	t.Run("web linking", func(t *testing.T) {
		rig := newOAuthRig(t)
		expire(rig)
		ticket, err := rig.h.IssueLinkTicket(context.Background(), 7, 42, "right password")
		if err != nil {
			t.Fatal(err)
		}
		result, err := rig.h.StartLink(context.Background(), 7, "/api/v2", ticket.Ticket, "/settings/account")
		if err != nil {
			t.Fatal(err)
		}
		loc, _ := url.Parse(rig.v2Callback(t, result.Cookie).Header().Get("Location"))
		if loc == nil || loc.Path != "/settings/account" || loc.Query().Get("reason") != OAuthReasonSessionExpired {
			t.Fatalf("Location = %v", loc)
		}
		if rig.comp.links != 0 {
			t.Fatal("an expired flow linked")
		}
	})

	t.Run("other browser", func(t *testing.T) {
		rig := newOAuthRig(t)
		expire(rig)
		rig.nativeStart(t, "code_challenge="+nativeChallenge+"&code_challenge_method=S256&app_state=slow-mfa")
		if got := rig.v2Callback(t, nil).Header().Get("Location"); got != "/login?error=oauth_failed&reason=state_invalid" {
			t.Fatalf("Location = %q", got)
		}
	})
}

// TestOAuthCompletionOpensItsSessionAtRedemption: the callback resolves the
// account and opens no session; the redemption opens it with what the
// callback saw, and a redemption whose session cannot open leaves the code
// redeemable.
func TestOAuthCompletionOpensItsSessionAtRedemption(t *testing.T) {
	rig := newOAuthRig(t)
	ctx := context.Background()
	rig.comp.identityID = 31
	_, cookie := rig.webStart(t, "")
	w := rig.v2Callback(t, cookie)
	loc, _ := url.Parse(w.Header().Get("Location"))
	code, browser := loc.Query().Get("code"), completionCookieValue(t, w)
	if code == "" || rig.comp.logins != 1 || len(rig.comp.opened) != 0 {
		t.Fatalf("callback: code = %q logins = %d opened = %+v", code, rig.comp.logins, rig.comp.opened)
	}
	rig.comp.openErr = ErrUserDisabled
	if _, err := rig.h.Complete(ctx, code, "", browser); !errors.Is(err, ErrOAuthCompletionInvalid) {
		t.Fatalf("redemption of a disabled account: %v", err)
	}
	rig.comp.openErr = nil
	completion, err := rig.h.Complete(ctx, code, "", browser)
	if err != nil || completion.SessionID != "sess-1" || completion.AccessToken != "acc.tok" {
		t.Fatalf("redeem: %+v, %v", completion, err)
	}
	if len(rig.comp.opened) != 1 {
		t.Fatalf("opened = %+v", rig.comp.opened)
	}
	if got := rig.comp.opened[0]; got.UserID != 7 || got.IdentityID != 31 || got.IP != "192.0.2.1" {
		t.Fatalf("session opened with %+v", got)
	}
}

func TestOAuthCompletionExpires(t *testing.T) {
	rig := newOAuthRig(t)
	ctx := context.Background()
	if err := rig.store.InsertCompletion(ctx, OAuthCompletion{Code: "old", UserID: 7,
		BrowserHash: oauthBinderHash("browser"), ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.h.Complete(ctx, "old", "", "browser"); !errors.Is(err, ErrOAuthCompletionInvalid) {
		t.Fatalf("err = %v", err)
	}
	if len(rig.revoked) != 0 || len(rig.comp.opened) != 0 {
		t.Fatalf("an expired code revoked %v and opened %v", rig.revoked, rig.comp.opened)
	}
}

func TestOAuthProviderLogoutURL(t *testing.T) {
	rig := newOAuthRig(t)
	got, err := rig.h.ProviderLogoutURL(context.Background(), 7)
	if err != nil || got != "https://idp.example/logout?user="+url.QueryEscape("https://silo.test/login") {
		t.Fatalf("got %q, %v", got, err)
	}
	rig.h.SetHostBaseURL("")
	if got, _ := rig.h.ProviderLogoutURL(context.Background(), 7); got != "" {
		t.Fatalf("without a public URL: %q", got)
	}
}

// TestOAuthProviderLogoutURLIsBounded: the plugin lookup runs under a
// deadline, so a provider whose discovery hangs cannot hold up the web
// logout that waits for this answer.
func TestOAuthProviderLogoutURLIsBounded(t *testing.T) {
	rig := newOAuthRig(t)
	var deadline time.Time
	rig.h.deps.ProviderLogout = func(ctx context.Context, _ int, _ string) (string, error) {
		var ok bool
		if deadline, ok = ctx.Deadline(); !ok {
			t.Fatal("provider logout lookup has no deadline")
		}
		return "", nil
	}
	if _, err := rig.h.ProviderLogoutURL(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if left := time.Until(deadline); left <= 0 || left > providerLogoutTimeout {
		t.Fatalf("deadline in %v, want within %v", left, providerLogoutTimeout)
	}
}

func TestClientIPUsesResolvedContextAndPreservesIPv6(t *testing.T) {
	tests := []struct {
		name       string
		contextIP  string
		remoteAddr string
		want       string
	}{
		{
			name:       "middleware context wins",
			contextIP:  "2001:db8::7",
			remoteAddr: "10.0.0.1:1234",
			want:       "2001:db8::7",
		},
		{
			name:       "bracketed ipv6 with port",
			remoteAddr: "[::1]:8080",
			want:       "::1",
		},
		{
			name:       "bare ipv6 from middleware remote addr",
			remoteAddr: "::1",
			want:       "::1",
		},
		{
			name:       "ipv4 with port",
			remoteAddr: "192.0.2.10:8080",
			want:       "192.0.2.10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.contextIP != "" {
				r = r.WithContext(clientip.SetContext(r.Context(), tt.contextIP))
			}
			if got := clientIP(r); got != tt.want {
				t.Fatalf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A credentials (LDAP) installation takes no part in the OAuth routes: it
// gets no link ticket and no flow, answered like an unknown installation.
func TestOAuthRoutesRefuseCredentialsInstallations(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	client := func(context.Context) (pluginAuthClient, error) { return nil, errors.New("not reached") }
	ldap := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: 5, CapabilityID: "ldap"}, nil, nil, client)
	oidc := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: 6, CapabilityID: "oidc"}, nil, nil, client)
	svc.SetPluginProviderSource(staticProviderSource{
		{Info: LoginProviderInfo{ID: PluginProviderID(5, "ldap"), Mode: ProviderModeCredentials, InstallationID: 5}, Provider: ldap},
		{Info: LoginProviderInfo{ID: PluginProviderID(6, "oidc"), Mode: ProviderModeOAuth, InstallationID: 6}, Provider: oidc},
	})
	if got := svc.FindOAuthInstallation(5); got != nil {
		t.Fatal("a credentials installation was found as an OAuth provider")
	}
	if got := svc.FindOAuthInstallation(6); got != oidc {
		t.Fatalf("OAuth installation = %v", got)
	}

	rig := newOAuthRig(t)
	rig.h.deps.ResolveClient = func(_ context.Context, id int) (OAuthClient, string, error) {
		if svc.FindOAuthInstallation(id) == nil {
			return nil, "", ErrUnknownAuthInstallation
		}
		return rig.client, "oidc", nil
	}
	if _, err := rig.h.IssueLinkTicket(context.Background(), 7, 5, "right password"); !errors.Is(err, ErrUnknownAuthInstallation) {
		t.Fatalf("link ticket for a credentials installation: %v", err)
	}
}

// TestOAuthNative_IssuerSerialization: iss is serialized in one canonical
// form, so the app can compare it with its saved server's origin.
func TestOAuthNative_IssuerSerialization(t *testing.T) {
	for _, tc := range []struct{ scheme, host, want string }{
		{"https", "silo.test", "https://silo.test"},
		{"HTTPS", "Silo.Example.TEST:443", "https://silo.example.test"},
		{"http", "10.0.0.5:80", "http://10.0.0.5"},
		{"http", "10.0.0.5:8096", "http://10.0.0.5:8096"},
		{"https", "[FE80::1]:8443", "https://[fe80::1]:8443"},
		{"https", "[::1]", "https://[::1]"},
	} {
		if got := serializeOrigin(tc.scheme, tc.host); got != tc.want {
			t.Errorf("serializeOrigin(%q, %q) = %q, want %q", tc.scheme, tc.host, got, tc.want)
		}
	}

	// A start on the public origin of a public URL with a path, a default
	// port and upper case names its origin only.
	rig := newOAuthRig(t)
	rig.h.SetHostBaseURL("HTTPS://Silo.Test:443/media/")
	_, cookie := rig.nativeStart(t, "code_challenge="+nativeChallenge+"&app_state=s1")
	if got := rig.nativeCallback(t, cookie).Query().Get("iss"); got != "https://silo.test" {
		t.Fatalf("iss = %q", got)
	}
	// Without a public URL the request origin issues the failure.
	rig = newOAuthRig(t)
	rig.h.SetHostBaseURL("")
	w := httptest.NewRecorder()
	rig.h.ServeStart(w, httptest.NewRequest(http.MethodGet, "http://10.0.0.5:8080/api/v2/auth/oauth/42/native/start", nil),
		OAuthStartRequest{InstallID: 42, Prefix: "/api/v2", Native: true, CodeChallenge: nativeChallenge, AppState: "s1"}, "", http.StatusFound)
	loc, _ := url.Parse(w.Header().Get("Location"))
	if loc == nil || loc.Query().Get("error") != OAuthReasonProviderUnavailable || loc.Query().Get("iss") != "http://10.0.0.5:8080" {
		t.Fatalf("Location = %q", w.Header().Get("Location"))
	}
}

// completionCookieValue returns the completion cookie a web callback set,
// checking it is set on the complete path of both API versions with the
// binding cookie's attributes.
func completionCookieValue(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	paths := map[string]string{}
	for _, c := range w.Result().Cookies() {
		if c.Name != OAuthCompletionCookieName {
			continue
		}
		if c.Value == "" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.MaxAge <= 0 || c.MaxAge > 180 {
			t.Fatalf("completion cookie = %+v", c)
		}
		paths[c.Path] = c.Value
	}
	v1, v2 := paths["/api/v1/auth/oauth/complete"], paths["/api/v2/auth/oauth/complete"]
	if len(paths) != 2 || v1 == "" || v1 != v2 {
		t.Fatalf("completion cookies = %v", paths)
	}
	return v1
}

func TestSameOriginComparesCanonicalHosts(t *testing.T) {
	cases := []struct {
		scheme, host, base string
		want               bool
	}{
		{"https", "[::1]", "https://[0:0:0:0:0:0:0:1]", true},
		{"https", "[::1]:443", "https://[0:0:0:0:0:0:0:1]", true},
		{"http", "[::1]:8096", "http://[::1]:8096", true},
		{"https", "Silo.Example.test", "https://silo.example.test:443", true},
		{"https", "[::2]", "https://[::1]", false},
		{"http", "silo.example.test", "https://silo.example.test", false},
		{"https", "silo.example.test:8443", "https://silo.example.test", false},
	}
	for _, tc := range cases {
		base, err := url.Parse(tc.base)
		if err != nil {
			t.Fatal(err)
		}
		if got := sameOrigin(tc.scheme, tc.host, base); got != tc.want {
			t.Errorf("sameOrigin(%q, %q, %q) = %v, want %v", tc.scheme, tc.host, tc.base, got, tc.want)
		}
	}
}
