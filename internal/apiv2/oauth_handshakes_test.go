package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
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
}

func (f *handshakeCompleter) CompleteOAuthLogin(_ context.Context, in auth.OAuthLoginInput) (*auth.TokenPair, *models.User, error) {
	f.in = in
	f.calls++
	return &auth.TokenPair{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresIn: 60}, nil, nil
}
func TestOAuthBrowserHandshakeV2(t *testing.T) {
	plugin := new(handshakePlugin)
	completer := new(handshakeCompleter)
	svc := auth.NewOAuthHandler(auth.OAuthHandlerDeps{Store: auth.NewInMemoryOAuthStore(), StateSecret: []byte("synthetic-state-secret"), HostBaseURL: "https://silo.example", ResolveClient: func(context.Context, int) (auth.OAuthClient, string, error) { return plugin, "fixture", nil }, LoginCompleter: completer})
	h := NewHandler(Dependencies{OAuth: svc})
	req := httptest.NewRequest("POST", Prefix+"/auth/oauth/3/init?next=%2Fme", strings.NewReader("ignored=form"))
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 302 || rec.Header().Get("Location") != "https://provider.example/authorize" || plugin.init.GetRedirectUri() != "https://silo.example/api/v2/auth/oauth/3/callback" {
		t.Fatal(rec.Code, rec.Body.String(), plugin.init)
	}
	callback := Prefix + "/auth/oauth/3/callback?state=" + url.QueryEscape(plugin.init.GetState()) + "&code=provider-code"
	req = httptest.NewRequest("GET", callback, nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("User-Agent", "Synthetic browser")
	req.Header.Set("Authorization", "Bearer ignored-ambient-session")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != 302 || location.Path != "/login/oauth-complete" || strings.Contains(location.String(), "synthetic-access") || completer.calls != 1 || completer.in.IP != "192.0.2.1" || plugin.exchange.GetRedirectUri() != plugin.init.GetRedirectUri() {
		t.Fatal(rec.Code, location, completer, plugin.exchange)
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
	body, _ := json.Marshal(map[string]string{"code": code})
	rec = do(t, h, http.MethodPost, Prefix+"/auth/oauth/complete", string(body), nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"next":"/me"`) || !strings.Contains(rec.Body.String(), "synthetic-access") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, "POST", Prefix+"/auth/oauth/complete", string(body), nil), TypeInvalidToken)
	rec = do(t, h, "GET", callback, "", nil)
	if rec.Code != 302 || !strings.Contains(rec.Header().Get("Location"), "session_expired") || completer.calls != 1 || plugin.exchanges != 1 {
		t.Fatal(rec.Code, rec.Header(), completer.calls, plugin.exchanges)
	}
}
func TestOAuthBrowserHandshakeFailures(t *testing.T) {
	h := NewHandler(Dependencies{OAuth: fakeOAuth{}})
	for _, tc := range []struct {
		method, path string
		code         int
	}{{"POST", "/auth/oauth/0/init", 400}, {"POST", "/auth/oauth/2/init", 502}, {"GET", "/auth/oauth/3/callback", 400}, {"GET", "/auth/oauth/bad/callback?state=s&code=c", 400}} {
		rec := do(t, h, tc.method, Prefix+tc.path, "", nil)
		if rec.Code != tc.code || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Fatal(tc, rec.Code, rec.Body.String())
		}
	}
	h = NewHandler(Dependencies{})
	requireProblem(t, do(t, h, "POST", Prefix+"/auth/oauth/3/init", "", nil), TypeDependencyUnavailable)
	rec := do(t, h, "GET", Prefix+"/auth/oauth/capabilities", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
}
