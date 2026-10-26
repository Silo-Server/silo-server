package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPGOAuthStoreFlowsDB(t *testing.T) {
	env := newExternalSignInEnv(t)
	ctx := t.Context()
	store := NewPGOAuthStore(env.pool, []byte("synthetic-oauth-db-secret"))
	user := env.localAccount(t, "store", models.RoleUser)

	// A flow keeps its binder hash and native fields.
	state := "state-" + env.suffix
	if err := store.Insert(ctx, OAuthSession{State: state, InstallID: "1", RedirectURI: "https://silo.test/cb", BinderHash: "hash",
		Kind: OAuthFlowNative, CodeChallenge: nativeChallenge, AppState: "app", StartOrigin: "http://10.0.0.5:8080", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	sess, err := store.GetAndDelete(ctx, state)
	if err != nil || sess.BinderHash != "hash" || sess.Kind != OAuthFlowNative || sess.CodeChallenge != nativeChallenge || sess.AppState != "app" ||
		sess.StartOrigin != "http://10.0.0.5:8080" {
		t.Fatalf("session = %+v, %v", sess, err)
	}

	// A parked native start is stored under the hash of its ID, opens one
	// flow, and is refused once expired.
	parked := OAuthNativeStart{ID: "flow-" + env.suffix, InstallationID: env.installationID, CodeChallenge: nativeChallenge, AppState: "app",
		Prompt: OIDCPromptSelectAccount, LinkingUserID: user.ID, StartOrigin: "http://10.0.0.5:8080", ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.InsertNativeStart(ctx, parked); err != nil {
		t.Fatal(err)
	}
	var clear int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM oauth_native_starts WHERE flow_hash = $1`, parked.ID).Scan(&clear); err != nil || clear != 0 {
		t.Fatalf("native start stored in the clear: %d %v", clear, err)
	}
	if consumed, err := store.ConsumeNativeStart(ctx, parked.ID); err != nil || consumed.ExpiresAt.Sub(parked.ExpiresAt).Abs() > time.Millisecond {
		t.Fatalf("consume = %+v, %v", consumed, err)
	} else if consumed.ExpiresAt = parked.ExpiresAt; consumed != parked {
		t.Fatalf("consume = %+v", consumed)
	}
	if _, err := store.ConsumeNativeStart(ctx, parked.ID); !errors.Is(err, ErrOAuthNativeStartInvalid) {
		t.Fatalf("second consume: %v", err)
	}
	signIn := OAuthNativeStart{ID: "signin-" + env.suffix, InstallationID: env.installationID, CodeChallenge: nativeChallenge, AppState: "app",
		StartOrigin: "https://[fe80::1]:8443", ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.InsertNativeStart(ctx, signIn); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ConsumeNativeStart(ctx, signIn.ID); err != nil || got.LinkingUserID != 0 || got.Prompt != "" || got.StartOrigin != signIn.StartOrigin {
		t.Fatalf("sign-in start = %+v, %v", got, err)
	}
	expiredStart := OAuthNativeStart{ID: "expired-start-" + env.suffix, InstallationID: env.installationID, CodeChallenge: nativeChallenge, AppState: "app",
		StartOrigin: "http://10.0.0.5:8080", ExpiresAt: time.Now().Add(-time.Second)}
	if err := store.InsertNativeStart(ctx, expiredStart); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeNativeStart(ctx, expiredStart.ID); !errors.Is(err, ErrOAuthNativeStartInvalid) {
		t.Fatalf("expired start: %v", err)
	}
	abandonedStart := expiredStart
	abandonedStart.ID = "abandoned-start-" + env.suffix
	if err := store.InsertNativeStart(ctx, abandonedStart); err != nil {
		t.Fatal(err)
	}

	// A native code: the verifier must fit, a miss leaves it redeemable and
	// opens no session, the redemption opens the session in its own
	// transaction, and reuse names that session.
	sessions := NewSessionRepository(env.pool)
	open := dbSessionOpener(sessions)
	native := OAuthCompletion{Code: "native-" + env.suffix, Kind: OAuthFlowNative, CodeChallenge: nativeChallenge, UserID: user.ID,
		DeviceName: "Synthetic phone", IP: "192.0.2.7", ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.InsertCompletion(ctx, native); err != nil {
		t.Fatal(err)
	}
	for _, verifier := range []string{"", nativeVerifier + "x"} {
		if _, err := store.RedeemCompletion(ctx, native.Code, verifier, "", open); !errors.Is(err, ErrOAuthInvalidGrant) {
			t.Fatalf("verifier %q: %v", verifier, err)
		}
	}
	if n := env.activeSessions(t, user.ID); n != 0 {
		t.Fatalf("refused redemptions opened %d sessions", n)
	}
	// A failing opener rolls the redemption back: no session, code still
	// redeemable.
	failing := func(ctx context.Context, db OAuthSessionDB, c OAuthCompletion) (*TokenPair, error) {
		if _, err := open(ctx, db, c); err != nil {
			return nil, err
		}
		return nil, errors.New("synthetic token failure")
	}
	if _, err := store.RedeemCompletion(ctx, native.Code, nativeVerifier, "", failing); err == nil {
		t.Fatal("a failed session open redeemed the code")
	}
	if n := env.activeSessions(t, user.ID); n != 0 {
		t.Fatalf("a failed redemption left %d sessions", n)
	}
	for _, account := range []*models.User{nil, {ID: user.ID + 1}} {
		malformed := func(ctx context.Context, db OAuthSessionDB, c OAuthCompletion) (*TokenPair, error) {
			pair, err := open(ctx, db, c)
			if err != nil {
				return nil, err
			}
			pair.User = account
			return pair, nil
		}
		if _, err := store.RedeemCompletion(ctx, native.Code, nativeVerifier, "", malformed); err == nil {
			t.Fatal("an opener with a missing or foreign account redeemed the code")
		}
		if n := env.activeSessions(t, user.ID); n != 0 {
			t.Fatalf("a malformed account left %d sessions", n)
		}
	}
	got, err := store.RedeemCompletion(ctx, native.Code, nativeVerifier, "", open)
	if err != nil || got.AccessToken == "" || got.RefreshToken == "" || got.UserID != user.ID || got.SessionID == "" || got.User == nil || got.User.ID != user.ID {
		t.Fatalf("redeem = %+v, %v", got, err)
	}
	opened, err := sessions.GetByID(ctx, got.SessionID)
	if err != nil || opened.UserID != user.ID || opened.DeviceName != native.DeviceName || opened.IPAddress != native.IP {
		t.Fatalf("opened session = %+v, %v", opened, err)
	}
	var ciphertext string
	if err := env.pool.QueryRow(ctx, `SELECT token_ciphertext FROM oauth_completions WHERE code_hash = $1`, oauthCompletionCodeHash(native.Code)).Scan(&ciphertext); err != nil || ciphertext != "" {
		t.Fatalf("tokens stored: %q %v", ciphertext, err)
	}
	// Without the verifier a used native code is not even reported as
	// reused, so an intercepted redirect cannot revoke the app's session.
	if _, err := store.RedeemCompletion(ctx, native.Code, "", "", open); !errors.Is(err, ErrOAuthInvalidGrant) {
		t.Fatalf("reuse without the verifier: %v", err)
	}
	reused, err := store.RedeemCompletion(ctx, native.Code, nativeVerifier, "", open)
	if !errors.Is(err, ErrOAuthCompletionReused) || reused.SessionID != got.SessionID || reused.UserID != user.ID || reused.AccessToken != "" {
		t.Fatalf("reuse = %+v, %v", reused, err)
	}
	if n := env.activeSessions(t, user.ID); n != 1 {
		t.Fatalf("one redemption and a reuse left %d sessions", n)
	}

	// A web code redeems only with the completion cookie of the browser
	// the callback answered (login CSRF: an attacker's own code sent to a
	// victim's browser must not sign the victim in), and a miss leaves it
	// redeemable.
	browser := "browser-" + env.suffix
	web := OAuthCompletion{Code: "web-" + env.suffix, UserID: user.ID, BrowserHash: oauthBinderHash(browser), ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.InsertCompletion(ctx, web); err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{"", "another-browser"} {
		if _, err := store.RedeemCompletion(ctx, web.Code, "", other, open); !errors.Is(err, ErrOAuthCompletionBrowser) {
			t.Fatalf("web code in browser %q: %v", other, err)
		}
	}
	if _, err := store.RedeemCompletion(ctx, web.Code, nativeVerifier, browser, open); !errors.Is(err, ErrOAuthInvalidGrant) {
		t.Fatalf("web code with a verifier: %v", err)
	}
	unbound := OAuthCompletion{Code: "unbound-" + env.suffix, UserID: user.ID, ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.InsertCompletion(ctx, unbound); err == nil {
		t.Fatal("a web completion without a browser binding was stored")
	}
	// Concurrent redemptions of one code: exactly one wins and opens a
	// session, and every reuse names that session.
	var wg sync.WaitGroup
	results := make([]OAuthCompletion, 8)
	errs := make([]error, len(results))
	for i := range results {
		wg.Go(func() { results[i], errs[i] = store.RedeemCompletion(ctx, web.Code, "", browser, open) })
	}
	wg.Wait()
	wins, reuses, winner := 0, 0, ""
	for i, err := range errs {
		switch {
		case err == nil:
			wins++
			winner = results[i].SessionID
		case errors.Is(err, ErrOAuthCompletionReused):
			reuses++
		default:
			t.Fatalf("concurrent redeem: %v", err)
		}
	}
	if wins != 1 || reuses != len(results)-1 {
		t.Fatalf("wins = %d reuses = %d", wins, reuses)
	}
	for i, err := range errs {
		if err != nil && results[i].SessionID != winner {
			t.Fatalf("reuse named session %q, the winner opened %q", results[i].SessionID, winner)
		}
	}
	if n := env.activeSessions(t, user.ID); n != 2 {
		t.Fatalf("two redeemed codes left %d sessions", n)
	}
	// Another browser replaying the used code is refused before reuse is
	// looked at, so it cannot revoke the session the code issued.
	if _, err := store.RedeemCompletion(ctx, web.Code, "", "", open); !errors.Is(err, ErrOAuthCompletionBrowser) {
		t.Fatalf("used web code in another browser: %v", err)
	}

	// Link tickets are stored hashed and start one flow.
	ticket := OAuthLinkTicket{Ticket: "ticket-" + env.suffix, UserID: user.ID, InstallationID: env.installationID, ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.InsertLinkTicket(ctx, ticket); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM oauth_link_tickets WHERE ticket_hash = $1`, ticket.Ticket).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("ticket stored in the clear: %d %v", stored, err)
	}
	if got, err := store.ConsumeLinkTicket(ctx, ticket.Ticket); err != nil || got.UserID != user.ID || got.InstallationID != env.installationID {
		t.Fatalf("consume = %+v, %v", got, err)
	}
	if _, err := store.ConsumeLinkTicket(ctx, ticket.Ticket); !errors.Is(err, ErrOAuthLinkTicketInvalid) {
		t.Fatalf("second consume: %v", err)
	}
	expired := OAuthLinkTicket{Ticket: "expired-" + env.suffix, UserID: user.ID, InstallationID: env.installationID, ExpiresAt: time.Now().Add(-time.Second)}
	if err := store.InsertLinkTicket(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeLinkTicket(ctx, expired.Ticket); !errors.Is(err, ErrOAuthLinkTicketInvalid) {
		t.Fatalf("expired ticket: %v", err)
	}

	// Retention keeps a used code through its reuse window, then drops it.
	recent := OAuthCompletion{Code: "recent-" + env.suffix, UserID: user.ID, BrowserHash: oauthBinderHash(browser), ExpiresAt: time.Now().Add(-time.Minute)}
	old := OAuthCompletion{Code: "old-" + env.suffix, UserID: user.ID, BrowserHash: oauthBinderHash(browser), ExpiresAt: time.Now().Add(-time.Hour)}
	for _, c := range []OAuthCompletion{recent, old} {
		if err := store.InsertCompletion(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.RedeemCompletion(ctx, recent.Code, "", browser, open); !errors.Is(err, ErrOAuthCompletionNotFound) {
		t.Fatalf("expired code redeemed: %v", err)
	}
	if n := env.activeSessions(t, user.ID); n != 2 {
		t.Fatalf("an expired code opened a session: %d sessions", n)
	}
	// An expired flow is kept through its grace, so a late callback can
	// still answer its own client; past it, it goes.
	lateFlow, abandonedFlow := "late-"+env.suffix, "abandoned-"+env.suffix
	for state, expiresAt := range map[string]time.Time{lateFlow: time.Now().Add(-time.Minute), abandonedFlow: time.Now().Add(-OAuthExpiredFlowGrace - time.Minute)} {
		if err := store.Insert(ctx, OAuthSession{State: state, InstallID: "1", RedirectURI: "https://silo.test/cb", BinderHash: "hash", Kind: OAuthFlowWeb, ExpiresAt: expiresAt}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DeleteExpiredFlows(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAndDelete(ctx, lateFlow); err != nil {
		t.Fatalf("flow inside its grace: %v", err)
	}
	if _, err := store.GetAndDelete(ctx, abandonedFlow); !errors.Is(err, ErrOAuthSessionNotFound) {
		t.Fatalf("flow past its grace: %v", err)
	}
	exists := func(code string) bool {
		var n int
		if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM oauth_completions WHERE code_hash = $1`, oauthCompletionCodeHash(code)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	if !exists(recent.Code) || exists(old.Code) {
		t.Fatalf("retention: recent kept = %v, old kept = %v", exists(recent.Code), exists(old.Code))
	}
	var abandonedStarts int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM oauth_native_starts WHERE flow_hash = $1`,
		oauthCompletionCodeHash(abandonedStart.ID)).Scan(&abandonedStarts); err != nil || abandonedStarts != 0 {
		t.Fatalf("expired native start kept by cleanup: %d %v", abandonedStarts, err)
	}
	_, _ = env.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM oauth_completions WHERE code_hash = ANY($1)`,
		[]string{oauthCompletionCodeHash(recent.Code), oauthCompletionCodeHash(native.Code), oauthCompletionCodeHash(web.Code)})
}

// dbSessionOpener opens a bare login session for the completion being
// redeemed, as OpenOAuthSession does, on the redemption's transaction.
func dbSessionOpener(sessions *SessionRepository) OAuthSessionOpener {
	return func(ctx context.Context, db OAuthSessionDB, c OAuthCompletion) (*TokenPair, error) {
		var exec sessionExecQuerier = sessions.pool
		if db != nil {
			exec = db
		}
		id := uuid.NewString()
		if err := sessions.createWithQuerier(ctx, exec, models.AuthSession{ID: id, UserID: c.UserID, DeviceName: c.DeviceName,
			IPAddress: c.IP, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			return nil, err
		}
		return &TokenPair{AccessToken: "access-" + id, RefreshToken: "refresh-" + id, ExpiresIn: 60, SessionID: id, User: &models.User{ID: c.UserID}}, nil
	}
}

// activeSessions counts the account's unrevoked, unexpired login sessions.
func (e *externalSignInEnv) activeSessions(t *testing.T, userID int) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM auth_sessions
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > NOW()`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// dbOAuthPlugin is an OIDC plugin client for the database flow tests.
type dbOAuthPlugin struct {
	mu       sync.Mutex
	subject  string
	init     *pluginv1.InitAuthorizeRequest
	endURL   string
	endErr   error
	endAsked *pluginv1.AuthEndSessionUrlRequest
}

func (p *dbOAuthPlugin) Authenticate(context.Context, *pluginv1.AuthenticateRequest) (*pluginv1.AuthenticateResponse, error) {
	return nil, status.Error(codes.Unimplemented, "oauth only")
}

func (p *dbOAuthPlugin) InitAuthorize(_ context.Context, in *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init = in
	return &pluginv1.InitAuthorizeResponse{AuthorizeUrl: "https://idp.example/authorize"}, nil
}

func (p *dbOAuthPlugin) ExchangeCode(context.Context, *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return &pluginv1.AuthenticateResponse{ExternalSubject: p.subject, Issuer: "https://id.example.test",
		Username: strings.TrimPrefix(p.subject, "https://id.example.test|"), DisplayName: "Flow Person"}, nil
}

func (p *dbOAuthPlugin) EndSessionUrl(_ context.Context, in *pluginv1.AuthEndSessionUrlRequest) (*pluginv1.AuthEndSessionUrlResponse, error) {
	p.endAsked = in
	return &pluginv1.AuthEndSessionUrlResponse{Url: p.endURL}, p.endErr
}

type dbSettings struct{ env *externalSignInEnv }

func (s dbSettings) Get(ctx context.Context, key string) (string, error) {
	var value string
	err := s.env.pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return value, err
}

type oauthDBRig struct {
	env      *externalSignInEnv
	svc      *Service
	plugin   *dbOAuthPlugin
	sessions *SessionRepository
}

// node builds one server process's OAuth handler over the shared database.
func (rig *oauthDBRig) node() *OAuthHandler {
	svc := rig.svc
	return NewOAuthHandler(OAuthHandlerDeps{
		Store:       NewPGOAuthStore(rig.env.pool, []byte("synthetic-oauth-db-secret")),
		StateSecret: []byte("synthetic-state-secret"),
		HostBaseURL: "https://silo.test",
		ResolveClient: func(ctx context.Context, id int) (OAuthClient, string, error) {
			pp := svc.FindOAuthInstallation(id)
			if pp == nil {
				return nil, "", ErrUnknownAuthInstallation
			}
			c, err := pp.OAuthClient(ctx)
			return c, pp.CapabilityID(), err
		},
		LoginCompleter: svc,
		ServerID:       func(context.Context) (string, error) { return testServerID, nil },
		RevokeSession:  svc.Logout,
		Users:          NewUserRepository(rig.env.pool),
		ProviderLogout: svc.ProviderLogoutURL,
	})
}

func newOAuthDBRig(t *testing.T) *oauthDBRig {
	t.Helper()
	env := newExternalSignInEnv(t)
	users := NewUserRepository(env.pool)
	sessions := NewSessionRepository(env.pool)
	rig := &oauthDBRig{env: env, plugin: &dbOAuthPlugin{}, sessions: sessions}
	rig.svc = NewService(NewLocalProvider(users, sessions), NewJWTService("synthetic-jwt-secret-synthetic-jwt-secret", time.Minute, time.Hour), sessions, users, nil, dbSettings{env}, nil)
	provider := NewPluginProviderWithClientFactory(PluginProviderConfig{InstallationID: env.installationID, CapabilityID: "oidc", DisplayName: "Company SSO", AutoProvision: true},
		sessions, env.resolver, func(context.Context) (pluginAuthClient, error) { return rig.plugin, nil })
	rig.svc.SetPluginProviderSource(staticProviderSource{{
		Info:     LoginProviderInfo{ID: PluginProviderID(env.installationID, "oidc"), DisplayName: "Company SSO", Mode: ProviderModeOAuth, InstallationID: env.installationID},
		Provider: provider,
	}})
	return rig
}

// run starts a flow on node a (GET start or native start), answers the
// provider redirect on node b, and returns the redirect b sent.
func (rig *oauthDBRig) run(t *testing.T, a, b *OAuthHandler, req OAuthStartRequest) *url.URL {
	t.Helper()
	req.InstallID, req.Prefix = rig.env.installationID, "/api/v2"
	w := httptest.NewRecorder()
	a.ServeStart(w, httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/x/start", nil), req, "", 0)
	if w.Code != http.StatusFound {
		t.Fatalf("start: %d %s (%s)", w.Code, w.Body.String(), w.Header().Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		cookie = c
	}
	if cookie == nil {
		t.Fatalf("start set no cookie; went to %s", w.Header().Get("Location"))
	}
	r := httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/x/callback?code=c&state="+url.QueryEscape(rig.plugin.init.GetState()), nil)
	r.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	w = httptest.NewRecorder()
	b.ServeCallback(w, r, "/api/v2", rig.env.installationID)
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != http.StatusFound {
		t.Fatalf("callback: %d %v", w.Code, err)
	}
	return loc
}

// callback answers the provider redirect of the flow the plugin last
// started on node b, from a browser holding cookie (nil: another browser).
func (rig *oauthDBRig) callback(t *testing.T, b *OAuthHandler, cookie *http.Cookie) *url.URL {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/x/callback?code=c&state="+url.QueryEscape(rig.plugin.init.GetState()), nil)
	if cookie != nil {
		r.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	w := httptest.NewRecorder()
	b.ServeCallback(w, r, "/api/v2", rig.env.installationID)
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != http.StatusFound {
		t.Fatalf("callback: %d %v", w.Code, err)
	}
	return loc
}

// hasIdentity reports whether subject is linked to any account.
func (rig *oauthDBRig) hasIdentity(t *testing.T, subject string) bool {
	t.Helper()
	var n int
	if err := rig.env.pool.QueryRow(t.Context(), `SELECT count(*) FROM plugin_auth_identities
		WHERE plugin_installation_id = $1 AND external_subject = $2`, rig.env.installationID, subject).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestOAuthNativeFlowAcrossNodesDB(t *testing.T) {
	rig := newOAuthDBRig(t)
	ctx := t.Context()
	rig.plugin.subject = "https://id.example.test|" + rig.env.name("native")
	nodeA, nodeB, nodeC := rig.node(), rig.node(), rig.node()

	loc := rig.run(t, nodeA, nodeB, OAuthStartRequest{Native: true, CodeChallenge: nativeChallenge, AppState: "app-state"})
	q := loc.Query()
	if !strings.HasPrefix(loc.String(), NativeAppRedirectURI+"?") || q.Get("state") != "app-state" || q.Get("server") != testServerID || q.Get("iss") != "https://silo.test" || q.Get("code") == "" {
		t.Fatalf("app redirect = %s", loc)
	}
	if _, err := nodeC.Complete(ctx, q.Get("code"), "", ""); !errors.Is(err, ErrOAuthInvalidGrant) {
		t.Fatalf("without verifier: %v", err)
	}
	completion, err := nodeC.Complete(ctx, q.Get("code"), nativeVerifier, "")
	if err != nil || completion.AccessToken == "" || completion.UserID == 0 || completion.SessionID == "" {
		t.Fatalf("complete = %+v, %v", completion, err)
	}
	identity := rig.env.identityRow(t, rig.plugin.subject)
	if identity.UserID != completion.UserID {
		t.Fatalf("identity user %d, completion user %d", identity.UserID, completion.UserID)
	}
	if valid, err := rig.sessions.IsValid(ctx, completion.SessionID); err != nil || !valid {
		t.Fatalf("session valid = %v, %v", valid, err)
	}
	// Reuse on another node revokes the session the code issued.
	if _, err := nodeA.Complete(ctx, q.Get("code"), nativeVerifier, ""); !errors.Is(err, ErrOAuthCompletionInvalid) {
		t.Fatalf("reuse: %v", err)
	}
	if valid, _ := rig.sessions.IsValid(ctx, completion.SessionID); valid {
		t.Fatal("reuse left the session valid")
	}
}

// TestOAuthUnredeemedCodeLeavesNoSessionDB: a sign-in opens its login
// session only when its completion code is redeemed. A web or native code
// that expires unredeemed, after refused redemptions (another browser, a
// wrong verifier, a disabled account), leaves the account with no session,
// through cleanup and a late redemption; a redeemed code leaves exactly one.
func TestOAuthUnredeemedCodeLeavesNoSessionDB(t *testing.T) {
	rig := newOAuthDBRig(t)
	ctx := t.Context()
	rig.plugin.subject = "https://id.example.test|" + rig.env.name("unredeemed")
	nodeA, nodeB := rig.node(), rig.node()
	store := NewPGOAuthStore(rig.env.pool, []byte("synthetic-oauth-db-secret"))
	expire := func(code string) {
		t.Helper()
		if _, err := rig.env.pool.Exec(ctx, `UPDATE oauth_completions SET expires_at = NOW() - interval '1 hour' WHERE code_hash = $1`,
			oauthCompletionCodeHash(code)); err != nil {
			t.Fatal(err)
		}
	}

	nativeCode := rig.run(t, nodeA, nodeB, OAuthStartRequest{Native: true, CodeChallenge: nativeChallenge, AppState: "unredeemed"}).Query().Get("code")
	user := rig.env.identityRow(t, rig.plugin.subject).UserID
	if nativeCode == "" || user == 0 {
		t.Fatalf("native code %q for user %d", nativeCode, user)
	}
	if n := rig.env.activeSessions(t, user); n != 0 {
		t.Fatalf("the callback opened %d sessions", n)
	}
	if _, err := nodeA.Complete(ctx, nativeCode, nativeVerifier+"x", ""); !errors.Is(err, ErrOAuthInvalidGrant) {
		t.Fatalf("wrong verifier: %v", err)
	}
	webCode := rig.run(t, nodeB, nodeA, OAuthStartRequest{Next: "/me"}).Query().Get("code")
	if webCode == "" {
		t.Fatal("no web code")
	}
	if _, err := nodeB.Complete(ctx, webCode, "", "another-browser"); !errors.Is(err, ErrOAuthCompletionBrowser) {
		t.Fatalf("another browser: %v", err)
	}
	if n := rig.env.activeSessions(t, user); n != 0 {
		t.Fatalf("refused redemptions opened %d sessions", n)
	}

	for _, code := range []string{nativeCode, webCode} {
		expire(code)
	}
	if _, err := nodeA.Complete(ctx, nativeCode, nativeVerifier, ""); !errors.Is(err, ErrOAuthCompletionInvalid) {
		t.Fatalf("late redemption: %v", err)
	}
	if _, err := store.DeleteExpiredFlows(ctx); err != nil {
		t.Fatal(err)
	}
	if n := rig.env.activeSessions(t, user); n != 0 {
		t.Fatalf("expired unredeemed codes left %d active sessions", n)
	}

	// An account disabled between the callback and the redemption opens no
	// session; the code stays redeemable once it is enabled again.
	code := rig.run(t, nodeA, nodeB, OAuthStartRequest{Native: true, CodeChallenge: nativeChallenge, AppState: "redeemed"}).Query().Get("code")
	setEnabled := func(enabled bool) {
		t.Helper()
		if _, err := rig.env.pool.Exec(ctx, `UPDATE users SET enabled = $2 WHERE id = $1`, user, enabled); err != nil {
			t.Fatal(err)
		}
	}
	setEnabled(false)
	if _, err := nodeB.Complete(ctx, code, nativeVerifier, ""); !errors.Is(err, ErrOAuthCompletionInvalid) {
		t.Fatalf("redemption for a disabled account: %v", err)
	}
	if n := rig.env.activeSessions(t, user); n != 0 {
		t.Fatalf("a disabled account got %d sessions", n)
	}
	setEnabled(true)

	// A redeemed code opens exactly one session, through the identity.
	completion, err := nodeB.Complete(ctx, code, nativeVerifier, "")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := rig.sessions.GetByID(ctx, completion.SessionID)
	if err != nil || opened.UserID != user || opened.IdentityID == nil || *opened.IdentityID != rig.env.identityRow(t, rig.plugin.subject).ID {
		t.Fatalf("opened session = %+v, %v", opened, err)
	}
	if n := rig.env.activeSessions(t, user); n != 1 {
		t.Fatalf("one redemption left %d active sessions", n)
	}
}

// TestOAuthWebFlowBrowserBindingDB: a web flow's completion code redeems
// only with the completion cookie the callback set in the browser it
// answered, on any node. An attacker who finishes a sign-in and sends the
// completion link to a victim cannot sign the victim's browser in.
func TestOAuthWebFlowBrowserBindingDB(t *testing.T) {
	rig := newOAuthDBRig(t)
	ctx := t.Context()
	rig.plugin.subject = "https://id.example.test|" + rig.env.name("web")
	nodeA, nodeB, nodeC := rig.node(), rig.node(), rig.node()
	installID := rig.env.installationID

	w := httptest.NewRecorder()
	nodeA.ServeStart(w, httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/x/start", nil),
		OAuthStartRequest{InstallID: installID, Prefix: "/api/v2", Next: "/me"}, "", 0)
	var binder *http.Cookie
	for _, c := range w.Result().Cookies() {
		binder = c
	}
	if w.Code != http.StatusFound || binder == nil {
		t.Fatalf("start: %d %q", w.Code, w.Header().Get("Location"))
	}
	r := httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/x/callback?code=c&state="+url.QueryEscape(rig.plugin.init.GetState()), nil)
	r.AddCookie(&http.Cookie{Name: binder.Name, Value: binder.Value})
	w = httptest.NewRecorder()
	nodeB.ServeCallback(w, r, "/api/v2", installID)
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != http.StatusFound || !strings.HasPrefix(loc.String(), "https://silo.test/login/oauth-complete?") {
		t.Fatalf("callback: %d %q", w.Code, w.Header().Get("Location"))
	}
	code, browser := loc.Query().Get("code"), ""
	for _, c := range w.Result().Cookies() {
		if c.Name == OAuthCompletionCookieName && c.Path == "/api/v2/auth/oauth/complete" {
			browser = c.Value
		}
	}
	if code == "" || browser == "" {
		t.Fatalf("callback set no completion cookie: %v", w.Result().Cookies())
	}

	for _, other := range []string{"", "another-browser"} {
		if _, err := nodeC.Complete(ctx, code, "", other); !errors.Is(err, ErrOAuthCompletionBrowser) {
			t.Fatalf("code in browser %q: %v", other, err)
		}
	}
	completion, err := nodeC.Complete(ctx, code, "", browser)
	if err != nil || completion.AccessToken == "" || completion.NextURL != "/me" {
		t.Fatalf("complete = %+v, %v", completion, err)
	}
	if valid, err := rig.sessions.IsValid(ctx, completion.SessionID); err != nil || !valid {
		t.Fatalf("session valid = %v, %v", valid, err)
	}
	// Another browser replaying the used code cannot revoke the session.
	if _, err := nodeA.Complete(ctx, code, "", ""); !errors.Is(err, ErrOAuthCompletionBrowser) {
		t.Fatalf("replay in another browser: %v", err)
	}
	if valid, _ := rig.sessions.IsValid(ctx, completion.SessionID); !valid {
		t.Fatal("a replay from another browser revoked the session")
	}
}

// TestOAuthNativeStartOriginAcrossNodesDB: a native start that arrives on
// the app's LAN address at one node is parked in the database, opened on the
// public origin at another, and the callback on a third names the LAN
// origin as iss.
func TestOAuthNativeStartOriginAcrossNodesDB(t *testing.T) {
	rig := newOAuthDBRig(t)
	rig.plugin.subject = "https://id.example.test|" + rig.env.name("lan")
	nodeA, nodeB, nodeC := rig.node(), rig.node(), rig.node()
	installID := rig.env.installationID
	req := OAuthStartRequest{InstallID: installID, Prefix: "/api/v2", Native: true, CodeChallenge: nativeChallenge, AppState: "lan-state"}

	w := httptest.NewRecorder()
	nodeA.ServeStart(w, httptest.NewRequest(http.MethodGet, "http://10.0.0.5:8080/api/v2/auth/oauth/x/native/start", nil), req, "", 0)
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != http.StatusFound || loc.Host != "silo.test" || len(loc.Query()) != 1 || len(w.Result().Cookies()) != 0 {
		t.Fatalf("LAN start: %d %q", w.Code, w.Header().Get("Location"))
	}
	flow := loc.Query().Get(OAuthNativeFlowParameter)
	resume := func(node *OAuthHandler) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		node.ServeStart(w, httptest.NewRequest(http.MethodGet, loc.String(), nil),
			OAuthStartRequest{InstallID: installID, Prefix: "/api/v2", Native: true, Flow: flow}, "", 0)
		return w
	}
	w = resume(nodeB)
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		cookie = c
	}
	if w.Code != http.StatusFound || cookie == nil {
		t.Fatalf("resume: %d %q", w.Code, w.Header().Get("Location"))
	}
	if again := resume(nodeA); again.Code != http.StatusBadRequest {
		t.Fatalf("second resume on another node: %d", again.Code)
	}
	q := rig.callback(t, nodeC, cookie).Query()
	if q.Get("iss") != "http://10.0.0.5:8080" || q.Get("state") != "lan-state" || q.Get("code") == "" {
		t.Fatalf("app redirect = %v", q)
	}
	if _, err := nodeA.Complete(t.Context(), q.Get("code"), nativeVerifier, ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

func TestOAuthLinkFlowDB(t *testing.T) {
	rig := newOAuthDBRig(t)
	ctx := t.Context()
	node := rig.node()
	alice := rig.env.localAccount(t, "link-alice", models.RoleUser)
	if _, err := node.IssueLinkTicket(ctx, alice.ID, rig.env.installationID, "wrong"); !errors.Is(err, ErrLinkTicketPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	ticket, err := node.IssueLinkTicket(ctx, alice.ID, rig.env.installationID, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	countSessions := func(userID int) int {
		var n int
		if err := rig.env.pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id = $1`, userID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	rig.plugin.subject = "https://id.example.test|" + rig.env.name("alice-sso")
	started, err := node.StartLink(ctx, alice.ID, "/api/v2", ticket.Ticket, "/settings/account")
	if err != nil {
		t.Fatal(err)
	}
	loc := rig.callback(t, rig.node(), started.Cookie)
	if loc.String() != "/settings/account?linked=1" || !rig.plugin.init.GetLinking() || rig.plugin.init.GetPrompt() != "login" {
		t.Fatalf("link redirect = %s init = %+v", loc, rig.plugin.init)
	}
	if identity := rig.env.identityRow(t, rig.plugin.subject); identity.UserID != alice.ID {
		t.Fatalf("identity linked to %d, want %d", identity.UserID, alice.ID)
	}
	if n := countSessions(alice.ID); n != 0 {
		t.Fatalf("linking opened %d sessions", n)
	}
	reloaded, err := NewUserRepository(rig.env.pool).GetByID(ctx, alice.ID)
	if err != nil || reloaded.LocalPasswordLoginEnabled {
		t.Fatalf("local password after link = %v, %v", reloaded.LocalPasswordLoginEnabled, err)
	}

	// An identity another account holds is refused on the return path.
	bob := rig.env.localAccount(t, "link-bob", models.RoleUser)
	ticket, err = node.IssueLinkTicket(ctx, bob.ID, rig.env.installationID, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	started, err = node.StartLink(ctx, bob.ID, "/api/v2", ticket.Ticket, "/settings/account")
	if err != nil {
		t.Fatal(err)
	}
	loc = rig.callback(t, node, started.Cookie)
	if loc.String() != "/settings/account?error=oauth_link_failed&reason=identity_linked_elsewhere" {
		t.Fatalf("redirect = %s", loc)
	}

	// Provider logout follows the plugin's answer: a plugin whose operator
	// turned provider logout off answers empty.
	if got, err := node.ProviderLogoutURL(ctx, alice.ID); err != nil || got != "" {
		t.Fatalf("plugin answers none: %q %v", got, err)
	}
	rig.plugin.endURL = "https://id.example.test/logout?id_token_hint=x"
	if got, err := node.ProviderLogoutURL(ctx, alice.ID); err != nil || got != rig.plugin.endURL {
		t.Fatalf("plugin answers a URL: %q %v", got, err)
	}
	if rig.plugin.endAsked.GetExternalSubject() != rig.plugin.subject || rig.plugin.endAsked.GetPostLogoutRedirectUri() != "https://silo.test/login" {
		t.Fatalf("asked = %+v", rig.plugin.endAsked)
	}
	if got, _ := node.ProviderLogoutURL(ctx, bob.ID); got != "" {
		t.Fatalf("an unlinked account got %q", got)
	}
	rig.plugin.endURL = "javascript:alert(1)"
	if got, _ := node.ProviderLogoutURL(ctx, alice.ID); got != "" {
		t.Fatalf("an unsafe url passed: %q", got)
	}
	rig.plugin.endURL, rig.plugin.endErr = "", status.Error(codes.Unimplemented, "old plugin")
	if got, err := node.ProviderLogoutURL(ctx, alice.ID); err != nil || got != "" {
		t.Fatalf("old plugin: %q %v", got, err)
	}
}

// TestOAuthForcedLinkDB is the attacker-started link (Vaultwarden
// CVE-2026-47158 class): Mallory requests a link ticket for her own account
// and gets a victim to finish the flow with the victim's provider identity.
// Neither the web nor the native flow may link it to Mallory's account.
func TestOAuthForcedLinkDB(t *testing.T) {
	rig := newOAuthDBRig(t)
	ctx := t.Context()
	nodeA, nodeB := rig.node(), rig.node()
	mallory := rig.env.localAccount(t, "forced-mallory", models.RoleUser)
	victim := rig.env.localAccount(t, "forced-victim", models.RoleUser)
	rig.plugin.subject = "https://id.example.test|" + rig.env.name("victim-sso")

	// Web: the start is Mallory's authenticated request, so the binding
	// cookie is in Mallory's browser. The victim's browser, sent to the
	// provider URL, cannot finish the flow.
	ticket, err := nodeA.IssueLinkTicket(ctx, mallory.ID, rig.env.installationID, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodeA.StartLink(ctx, mallory.ID, "/api/v2", ticket.Ticket, "/"); err != nil {
		t.Fatal(err)
	}
	if loc := rig.callback(t, nodeB, nil); loc.String() != "/login?error=oauth_failed&reason=state_invalid" {
		t.Fatalf("victim's callback = %s", loc)
	}
	if rig.hasIdentity(t, rig.plugin.subject) {
		t.Fatal("a flow finished in the victim's browser linked the victim's identity")
	}

	// The ticket works only for the account it was issued to.
	ticket, _ = nodeA.IssueLinkTicket(ctx, mallory.ID, rig.env.installationID, "correct horse battery")
	if _, err := nodeB.StartLink(ctx, victim.ID, "/api/v2", ticket.Ticket, "/"); !errors.Is(err, ErrOAuthLinkTicketInvalid) {
		t.Fatalf("ticket used by another account: %v", err)
	}

	// The web GET start refuses a ticket outright.
	ticket, _ = nodeA.IssueLinkTicket(ctx, mallory.ID, rig.env.installationID, "correct horse battery")
	w := httptest.NewRecorder()
	nodeA.ServeStart(w, httptest.NewRequest(http.MethodGet, "https://silo.test/api/v2/auth/oauth/x/start", nil),
		OAuthStartRequest{InstallID: rig.env.installationID, Prefix: "/api/v2", LinkTicket: ticket.Ticket}, "", 0)
	if w.Code != http.StatusBadRequest || len(w.Result().Cookies()) != 0 {
		t.Fatalf("web start with a ticket: %d", w.Code)
	}

	// Native: Mallory builds the start URL with her ticket and challenge;
	// the victim's browser runs it. The callback links nothing: it parks
	// the answer for Mallory's app, which never sees the code.
	loc := rig.run(t, nodeA, nodeB, OAuthStartRequest{Native: true, CodeChallenge: nativeChallenge, AppState: "s", LinkTicket: ticket.Ticket})
	code := loc.Query().Get("code")
	if code == "" || loc.Query().Get("link") != "1" || rig.hasIdentity(t, rig.plugin.subject) {
		t.Fatalf("native link callback = %s", loc)
	}
	// The victim's app, holding the code but not Mallory's verifier or
	// session, links nothing either.
	if err := nodeB.CompleteLink(ctx, victim.ID, code, strings.Repeat("v", 43)); !errors.Is(err, ErrOAuthInvalidGrant) {
		t.Fatalf("victim without the verifier: %v", err)
	}
	if err := nodeB.CompleteLink(ctx, victim.ID, code, nativeVerifier); !errors.Is(err, ErrOAuthCompletionInvalid) {
		t.Fatalf("victim's session: %v", err)
	}
	if err := nodeB.CompleteLink(ctx, mallory.ID, code, nativeVerifier); !errors.Is(err, ErrOAuthCompletionInvalid) {
		t.Fatalf("code after a foreign redemption: %v", err)
	}
	if rig.hasIdentity(t, rig.plugin.subject) {
		t.Fatal("the native flow linked the victim's identity")
	}

	// The real owner of a native linking flow: the app redeems its code
	// with its verifier on its own session, on any node.
	ticket, _ = nodeA.IssueLinkTicket(ctx, victim.ID, rig.env.installationID, "correct horse battery")
	loc = rig.run(t, nodeA, nodeB, OAuthStartRequest{Native: true, CodeChallenge: nativeChallenge, AppState: "s", LinkTicket: ticket.Ticket})
	var payload string
	if err := rig.env.pool.QueryRow(ctx, `SELECT payload FROM oauth_pending_links WHERE code_hash = $1`,
		oauthCompletionCodeHash(loc.Query().Get("code"))).Scan(&payload); err != nil || strings.Contains(payload, "victim-sso") {
		t.Fatalf("pending link stored in the clear: %v", err)
	}
	if _, err := rig.node().Complete(ctx, loc.Query().Get("code"), nativeVerifier, ""); !errors.Is(err, ErrOAuthCompletionInvalid) {
		t.Fatalf("a link code redeemed as a sign-in: %v", err)
	}
	if err := rig.node().CompleteLink(ctx, victim.ID, loc.Query().Get("code"), nativeVerifier); err != nil {
		t.Fatalf("complete link: %v", err)
	}
	if identity := rig.env.identityRow(t, rig.plugin.subject); identity.UserID != victim.ID {
		t.Fatalf("identity linked to %d, want %d", identity.UserID, victim.ID)
	}
}
