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
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

func TestHandleAuthenticateByNameRejectsOversizedBody(t *testing.T) {
	// No login resolver is wired, so reaching it would panic: a 413 proves the
	// handler stopped before checking credentials.
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, nil, nil)

	body := `{"Username":"admin","Pw":"` + strings.Repeat("a", maxAuthenticateByNameBodyBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleAuthenticateByName(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
}

// A name that has used its sign-in attempt budget answers 429 in the
// listener's error envelope with the time left in Retry-After.
func TestHandleAuthenticateByNameThrottledName(t *testing.T) {
	svc := auth.NewService(nil, nil, nil, nil, nil, nil, nil)
	svc.SetSignInAttempts(ratelimit.NewMemoryAttemptLimiter(ratelimit.AttemptPolicy{MaxAttempts: 1, Window: time.Minute}))
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, NewLoginResolver(svc, nil, nil, nil, nil), nil)
	signIn := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"alice","Pw":"secret"}`))
		rec := httptest.NewRecorder()
		h.HandleAuthenticateByName(rec, req)
		return rec
	}

	if rec := signIn(); rec.Code != http.StatusUnauthorized {
		t.Fatalf("first attempt: status = %d, want 401; body %s", rec.Code, rec.Body.String())
	}
	rec := signIn()
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" || !strings.Contains(rec.Body.String(), "TooManyRequests") {
		t.Fatalf("locked name: status = %d, Retry-After = %q, body %s; want 429, 60, TooManyRequests",
			rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}

type fixedLoginResolver struct{ session *Session }

func (f fixedLoginResolver) Resolve(context.Context, string, string, string, string) (*Session, error) {
	return f.session, nil
}

// Strict decoders such as Lenna's reject the whole sign-in response when a key
// Jellyfin always sends is missing, so each must be present and the session
// must describe the device from the MediaBrowser header.
func TestHandleAuthenticateByNameSendsJellyfinRequiredFields(t *testing.T) {
	session := &Session{Token: "tok", Username: "alice", PseudoUserID: PseudoUserID(7, "kids")}
	h := NewAuthHandler(func() *config.Config { return &config.Config{} }, fixedLoginResolver{session}, nil)
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"alice","Pw":"secret"}`))
	req.Header.Set("Authorization", `MediaBrowser Client="Lenna", Device="My%20iPhone", DeviceId="dev-1", Version="1.0.17"`)
	rec := httptest.NewRecorder()

	h.HandleAuthenticateByName(rec, req)

	var out struct {
		User        map[string]json.RawMessage
		SessionInfo map[string]json.RawMessage
	}
	var policy map[string]json.RawMessage
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || json.Unmarshal(out.User["Policy"], &policy) != nil {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	for name, keys := range map[string]struct {
		got  map[string]json.RawMessage
		want []string
	}{
		"User":        {out.User, []string{"Id", "Name", "ServerId", "EnableAutoLogin", "LastLoginDate", "LastActivityDate", "Policy", "Configuration"}},
		"User.Policy": {policy, []string{"BlockedTags", "AllowedTags", "AccessSchedules", "BlockUnratedItems", "EnableContentDeletionFromFolders", "EnabledDevices", "EnabledChannels", "EnabledFolders", "BlockedMediaFolders", "BlockedChannels"}},
		"SessionInfo": {out.SessionInfo, []string{"Id", "UserId", "Client", "DeviceId", "DeviceName", "ApplicationVersion", "RemoteEndPoint", "PlayState", "Capabilities", "AdditionalUsers", "NowPlayingQueue", "SupportedCommands", "PlayableMediaTypes"}},
	} {
		for _, key := range keys.want {
			if v, ok := keys.got[key]; !ok || string(v) == "null" {
				t.Errorf("%s.%s missing or null; body %s", name, key, rec.Body.String())
			}
		}
	}
	for key, want := range map[string]string{"Client": `"Lenna"`, "DeviceName": `"My iPhone"`, "DeviceId": `"dev-1"`, "ApplicationVersion": `"1.0.17"`} {
		if got := string(out.SessionInfo[key]); got != want {
			t.Errorf("SessionInfo.%s = %s, want %s", key, got, want)
		}
	}

	// The Id is stable for one device across sign-ins. Without a DeviceId it
	// follows the login session: a retry that returns the same session keeps
	// it, and two sign-ins never share one.
	sessionID := func(loginSessionID, authorization string) string {
		session.LoginSessionID = loginSessionID
		req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"alice","Pw":"secret"}`))
		req.Header.Set("Authorization", authorization)
		rec := httptest.NewRecorder()
		h.HandleAuthenticateByName(rec, req)
		var out struct {
			SessionInfo struct {
				ID string `json:"Id"`
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.SessionInfo.ID == "" {
			t.Fatalf("sign-in = %d %s", rec.Code, rec.Body.String())
		}
		return out.SessionInfo.ID
	}
	device, noDevice := `MediaBrowser Client="Lenna", DeviceId="dev-1"`, `MediaBrowser Client="Lenna"`
	if a, b := sessionID("login-1", device), sessionID("login-2", device); a != b {
		t.Errorf("same device got Ids %s and %s", a, b)
	}
	if a, b := sessionID("login-1", noDevice), sessionID("login-1", noDevice); a != b {
		t.Errorf("one login session without a DeviceId got Ids %s and %s", a, b)
	}
	if a, b := sessionID("login-1", noDevice), sessionID("login-2", noDevice); a == b {
		t.Errorf("two sign-ins without a DeviceId share Id %s", a)
	}
}
