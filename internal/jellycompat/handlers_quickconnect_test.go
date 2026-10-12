package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeQuickConnect is the device sign-in service as the Quick Connect routes
// see it.
type fakeQuickConnect struct {
	started    auth.QuickConnectStartInput
	state      *auth.QuickConnectState
	stateErr   error
	approveErr error
	approved   struct{ code, accessToken, profileID string }
	collect    *auth.DeviceLoginPollResult
	collectErr error
}

func (f *fakeQuickConnect) StartQuickConnect(_ context.Context, input auth.QuickConnectStartInput) (*auth.QuickConnectStart, error) {
	f.started = input
	return &auth.QuickConnectStart{
		Secret: "SECRET", Code: "482177", DeviceName: input.DeviceName, AppName: input.AppName,
		CreatedAt: time.Date(2026, 10, 12, 1, 2, 3, 0, time.UTC),
	}, nil
}

func (f *fakeQuickConnect) QuickConnectState(context.Context, string) (*auth.QuickConnectState, error) {
	return f.state, f.stateErr
}

func (f *fakeQuickConnect) ApproveQuickConnectAsSession(_ context.Context, code, accessToken, profileID string) error {
	f.approved.code, f.approved.accessToken, f.approved.profileID = code, accessToken, profileID
	return f.approveErr
}

func (f *fakeQuickConnect) CollectQuickConnect(ctx context.Context, _ string, persist auth.QuickConnectPersist) (*auth.DeviceLoginPollResult, error) {
	if f.collectErr != nil {
		return nil, f.collectErr
	}
	if f.collect != nil && f.collect.TokenPair != nil && persist != nil {
		if err := persist(ctx, nil, f.collect); err != nil {
			return nil, err
		}
	}
	return f.collect, nil
}

// profileStore serves GetProfile from a fixed list.
type profileStore struct {
	userstore.UserStore
	profiles []userstore.Profile
}

func (s *profileStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	for i := range s.profiles {
		if s.profiles[i].ID == id {
			return &s.profiles[i], nil
		}
	}
	return nil, nil
}

type profileStoreProvider struct{ store *profileStore }

func (p profileStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}
func (p profileStoreProvider) Close() error { return nil }

const quickConnectAuthHeader = `MediaBrowser Client="Jellyfin%20Web", Device="Living%20room", DeviceId="dev-1", Version="10.11.6"`

func quickConnectRouter(t *testing.T, service *fakeQuickConnect) (http.Handler, *SessionStore) {
	t.Helper()
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	store := NewSessionStore(time.Hour, time.Now)
	deps := Dependencies{Config: cfg, SessionStore: store}
	if service != nil {
		deps.DeviceLogin = service
		stores := profileStoreProvider{store: &profileStore{profiles: []userstore.Profile{
			{ID: "kids", Name: "Kids"}, {ID: "alice", Name: "Alice"},
		}}}
		deps.LoginResolver = NewLoginResolver(nil, stores, store, func() string { return "compat-token" }, nil).WithQuickConnect(service)
	}
	return NewRouter(deps), store
}

func serveQuickConnect(router http.Handler, method, target, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeQuickConnectResult(t *testing.T, rec *httptest.ResponseRecorder) quickConnectResult {
	t.Helper()
	var out quickConnectResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestQuickConnectEnabledReflectsService(t *testing.T) {
	off, _ := quickConnectRouter(t, nil)
	if rec := serveQuickConnect(off, http.MethodGet, "/QuickConnect/Enabled", "", nil); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "false" {
		t.Fatalf("Enabled without service = %d %q", rec.Code, rec.Body.String())
	}
	if rec := serveQuickConnect(off, http.MethodPost, "/QuickConnect/Initiate", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("Initiate without service = %d", rec.Code)
	}
	on, _ := quickConnectRouter(t, &fakeQuickConnect{})
	if rec := serveQuickConnect(on, http.MethodGet, "/quickconnect/enabled", "", nil); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "true" {
		t.Fatalf("Enabled = %d %q", rec.Code, rec.Body.String())
	}
}

// TestQuickConnectInitiateReadsAuthorizationHeader: both the POST Jellyfin
// 10.9+ sends and the GET of 10.8 open a request, named from the decoded
// MediaBrowser header, and answer Jellyfin's QuickConnectResult.
func TestQuickConnectInitiateReadsAuthorizationHeader(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		service := &fakeQuickConnect{}
		router, _ := quickConnectRouter(t, service)
		rec := serveQuickConnect(router, method, "/QuickConnect/Initiate", "", map[string]string{"X-Emby-Authorization": quickConnectAuthHeader})
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s Initiate = %d %s, Cache-Control %q", method, rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
		}
		if service.started.DeviceName != "Living room" || service.started.AppName != "Jellyfin Web" {
			t.Fatalf("%s started with %+v", method, service.started)
		}
		got := decodeQuickConnectResult(t, rec)
		want := quickConnectResult{Secret: "SECRET", Code: "482177", DeviceID: "dev-1", DeviceName: "Living room",
			AppName: "Jellyfin Web", AppVersion: "10.11.6", DateAdded: "2026-10-12T01:02:03.0000000Z"}
		if got != want {
			t.Fatalf("%s Initiate = %+v, want %+v", method, got, want)
		}
	}
}

// TestQuickConnectConnectReportsState: pending is not yet authenticated,
// approved and collected are, and anything else is gone. The code comes back
// on every poll and the secret is read in any case, as Jellyfin Web sends
// ?Secret=.
func TestQuickConnectConnectReportsState(t *testing.T) {
	cases := []struct {
		status string
		code   int
		authed bool
	}{
		{auth.DeviceLoginStatusPending, http.StatusOK, false},
		{auth.DeviceLoginStatusApproved, http.StatusOK, true},
		{auth.DeviceLoginStatusConsumed, http.StatusOK, true},
		{auth.DeviceLoginStatusDenied, http.StatusNotFound, false},
		{auth.DeviceLoginStatusExpired, http.StatusNotFound, false},
	}
	for _, tc := range cases {
		service := &fakeQuickConnect{state: &auth.QuickConnectState{Status: tc.status, Code: "482177", DeviceName: "TV", AppName: "Jellyfin Android TV"}}
		router, _ := quickConnectRouter(t, service)
		rec := serveQuickConnect(router, http.MethodGet, "/QuickConnect/Connect?Secret=SECRET", "", nil)
		if rec.Code != tc.code {
			t.Fatalf("%s: Connect = %d %s", tc.status, rec.Code, rec.Body.String())
		}
		if tc.code != http.StatusOK {
			continue
		}
		got := decodeQuickConnectResult(t, rec)
		if got.Authenticated != tc.authed || got.Secret != "SECRET" || got.Code != "482177" || got.DeviceName != "TV" {
			t.Fatalf("%s: Connect = %+v", tc.status, got)
		}
	}

	router, _ := quickConnectRouter(t, &fakeQuickConnect{stateErr: auth.ErrDeviceLoginNotFound})
	if rec := serveQuickConnect(router, http.MethodGet, "/QuickConnect/Connect?secret=nope", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown secret = %d", rec.Code)
	}
	if rec := serveQuickConnect(router, http.MethodGet, "/QuickConnect/Connect", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing secret = %d", rec.Code)
	}
}

// TestQuickConnectAuthorizeApprovesAsTheSession: a signed-in Jellyfin app
// approves for its own account and profile with its login session's token.
// It must be signed in, may not name another user, and a spent budget is 429
// with Retry-After.
func TestQuickConnectAuthorizeApprovesAsTheSession(t *testing.T) {
	service := &fakeQuickConnect{}
	router, store := quickConnectRouter(t, service)
	session := Session{Token: "app-token", StreamAppUserID: 7, StreamAppAccessToken: "native-access", ProfileID: "kids", PseudoUserID: PseudoUserID(7, "kids")}
	if err := store.Put(session); err != nil {
		t.Fatal(err)
	}
	signedIn := map[string]string{"X-Emby-Token": "app-token"}

	if rec := serveQuickConnect(router, http.MethodPost, "/QuickConnect/Authorize?code=482177", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous Authorize = %d", rec.Code)
	}
	other := PseudoUserID(7, "alice").String()
	if rec := serveQuickConnect(router, http.MethodPost, "/QuickConnect/Authorize?code=482177&userId="+other, "", signedIn); rec.Code != http.StatusForbidden {
		t.Fatalf("Authorize for another user = %d", rec.Code)
	}
	rec := serveQuickConnect(router, http.MethodPost, "/QuickConnect/Authorize?Code=482177&userId="+session.PseudoUserID.String(), "", signedIn)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "true" {
		t.Fatalf("Authorize = %d %q", rec.Code, rec.Body.String())
	}
	if service.approved.code != "482177" || service.approved.accessToken != "native-access" || service.approved.profileID != "kids" {
		t.Fatalf("approved with %+v", service.approved)
	}

	service.approveErr = &auth.QuickConnectAttemptsError{RetryAfter: 90 * time.Second}
	rec = serveQuickConnect(router, http.MethodPost, "/QuickConnect/Authorize?code=482177", "", signedIn)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "90" {
		t.Fatalf("Authorize past the budget = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	service.approveErr = auth.ErrDeviceLoginNotFound
	if rec := serveQuickConnect(router, http.MethodPost, "/QuickConnect/Authorize?code=000000", "", signedIn); rec.Code != http.StatusNotFound {
		t.Fatalf("Authorize unknown code = %d", rec.Code)
	}
}

// TestAuthenticateWithQuickConnectMintsProfileSession: an approved secret
// buys a compat session bound to the approved profile, answered like
// AuthenticateByName, and the same session again while the request lives; a
// secret that is not approved is 404.
func TestAuthenticateWithQuickConnectMintsProfileSession(t *testing.T) {
	service := &fakeQuickConnect{collect: &auth.DeviceLoginPollResult{
		Status:      auth.DeviceLoginStatusApproved,
		TokenPair:   &auth.TokenPair{AccessToken: "native-access", RefreshToken: "native-refresh", ExpiresIn: 3600, SessionID: "login-1"},
		User:        &models.User{ID: 7, Username: "alice"},
		ProfileID:   "kids",
		ProfileName: "Kids",
	}}
	router, store := quickConnectRouter(t, service)
	rec := serveQuickConnect(router, http.MethodPost, "/Users/AuthenticateWithQuickConnect", `{"Secret":"SECRET"}`, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("AuthenticateWithQuickConnect = %d %s", rec.Code, rec.Body.String())
	}
	var out authenticateByNameResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	token := quickConnectSessionToken("SECRET")
	if out.AccessToken != token || out.User.Name != "alice#Kids" || out.User.ID != PseudoUserID(7, "kids").String() {
		t.Fatalf("response = %+v", out)
	}
	session, ok := store.Get(token)
	if !ok || session.ProfileID != "kids" || session.StreamAppUserID != 7 || session.StreamAppRefreshToken != "native-refresh" || session.LoginSessionID != "login-1" {
		t.Fatalf("stored session = %+v, %v", session, ok)
	}

	// The app lost the answer and asks again: the request is collected, so
	// the same session comes back, as Jellyfin answers a collected secret.
	service.collect = &auth.DeviceLoginPollResult{Status: auth.DeviceLoginStatusConsumed}
	rec = serveQuickConnect(router, http.MethodPost, "/Users/AuthenticateWithQuickConnect", `{"Secret":"SECRET"}`, nil)
	var again authenticateByNameResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &again); rec.Code != http.StatusOK || err != nil || again.AccessToken != token {
		t.Fatalf("second exchange = %d %s", rec.Code, rec.Body.String())
	}
	// Once the app signs out, the collected secret buys nothing.
	store.Delete(token)
	if rec := serveQuickConnect(router, http.MethodPost, "/Users/AuthenticateWithQuickConnect", `{"Secret":"SECRET"}`, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("exchange after sign-out = %d", rec.Code)
	}

	service.collect = &auth.DeviceLoginPollResult{Status: auth.DeviceLoginStatusPending}
	if rec := serveQuickConnect(router, http.MethodPost, "/users/authenticatewithquickconnect", `{"secret":"SECRET"}`, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("pending secret = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveQuickConnect(router, http.MethodPost, "/Users/AuthenticateWithQuickConnect", `{}`, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing secret = %d", rec.Code)
	}
}

func TestQuickConnectUsernameMatchesPasswordSignIn(t *testing.T) {
	if got := quickConnectUsername("alice", "Alice"); got != "alice" {
		t.Fatalf("account-named profile = %q", got)
	}
	if got := quickConnectUsername("alice", "Kids"); got != "alice#Kids" {
		t.Fatalf("other profile = %q", got)
	}
}

// TestQuickConnectSpendsDeviceBudgets: opening a request spends the native
// device_start budget per client address, answered with the compat 429.
func TestQuickConnectSpendsDeviceBudgets(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	mw := newTestRateLimit(t, map[string]string{
		"ratelimit.auth.device_start.requests_per_minute": "1",
		"ratelimit.auth.device_start.burst":               "1",
	})
	service := &fakeQuickConnect{}
	store := NewSessionStore(time.Hour, time.Now)
	router := NewRouter(Dependencies{
		Config:           cfg,
		ClientIPResolver: clientip.NewResolver(nil),
		SessionStore:     store,
		RateLimit:        mw,
		DeviceLogin:      service,
		LoginResolver:    NewLoginResolver(nil, nil, store, nil, nil).WithQuickConnect(service),
	})
	const tv, other = "203.0.113.10", "198.51.100.20"
	if rec := serveFrom(router, tv, http.MethodPost, "/QuickConnect/Initiate", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("first Initiate = %d %s", rec.Code, rec.Body.String())
	}
	rec := serveFrom(router, tv, http.MethodPost, "/QuickConnect/Initiate", "", "")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "TooManyRequests") {
		t.Fatalf("Initiate over the budget = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveFrom(router, other, http.MethodPost, "/QuickConnect/Initiate", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("Initiate from another address = %d", rec.Code)
	}
}
