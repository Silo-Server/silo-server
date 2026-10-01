package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/discord"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeDiscordLinkBackend struct {
	available                                       bool
	state                                           string
	userID, beginCalls, consumeCalls, exchangeCalls int
	redirectURI, code                               string
	consumed                                        bool
	exchangeErr                                     error
}

func (f *fakeDiscordLinkBackend) DiscordAvailable(context.Context) bool { return f.available }
func (f *fakeDiscordLinkBackend) BeginDiscordLink(_ context.Context, state string, userID int) error {
	f.beginCalls++
	f.state = state
	f.userID = userID
	return nil
}
func (f *fakeDiscordLinkBackend) ConsumeDiscordLinkState(_ context.Context, state string) (int, bool, error) {
	f.consumeCalls++
	if state != f.state || f.consumed {
		return 0, false, nil
	}
	f.consumed = true
	return f.userID, true, nil
}
func (f *fakeDiscordLinkBackend) CompleteDiscordLink(_ context.Context, userID int, code, redirectURI string) (discord.User, error) {
	f.exchangeCalls++
	f.userID = userID
	f.code = code
	f.redirectURI = redirectURI
	return discord.User{}, f.exchangeErr
}

type discordLinkSettings struct{}

func (discordLinkSettings) Get(context.Context, string) (string, error) { return "fixture-client", nil }

func TestNotificationDiscordLinkConsentAndCallback(t *testing.T) {
	fake := &fakeDiscordLinkBackend{available: true}
	links := handlers.NewDiscordLinkHandler(fake, notifications.NewSettings(discordLinkSettings{}), "https://server.example.test/")
	target, err := links.BeginNotificationDiscordLink(t.Context(), 17)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	redirect := "https://server.example.test/api/v2/notifications/discord/link/callback"
	if parsed.Scheme != "https" || parsed.Host != "discord.com" || query.Get("redirect_uri") != redirect || query.Get("state") != fake.state || len(fake.state) != 64 || fake.userID != 17 || fake.beginCalls != 1 || query.Get("scope") != "identify" {
		t.Fatalf("consent URL: %s %+v", target, fake)
	}
	h := apiv2.NewHandler(apiv2.Dependencies{NotificationDiscordLinks: links})
	callback := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/notifications/discord/link/callback?state="+fake.state+"&code=synthetic-code", nil)
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Authorization", "Bearer unrelated-session")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := callback()
	if rec.Code != 302 || rec.Header().Get("Location") != "/settings/notifications?discord_linked=1" || fake.exchangeCalls != 1 || fake.userID != 17 || fake.code != "synthetic-code" || fake.redirectURI != redirect {
		t.Fatalf("callback: %d %v %+v", rec.Code, rec.Header(), fake)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("callback headers")
	}
	rec = callback()
	if rec.Code != 302 || !strings.Contains(rec.Header().Get("Location"), "state_invalid") || fake.exchangeCalls != 1 {
		t.Fatal("consumed state exchanged again")
	}
}

func TestNotificationDiscordCallbackFailures(t *testing.T) {
	for _, tc := range []struct {
		name, query, result string
		available           bool
		exchangeErr         error
		consumes, exchanges int
	}{
		{"disabled", "state=state&code=code", "disabled", false, nil, 0, 0},
		{"denied", "error=access_denied", "denied", true, nil, 0, 0},
		{"missing-code", "state=state", "invalid_callback", true, nil, 0, 0},
		{"unknown-state", "state=other&code=code", "state_invalid", true, nil, 1, 0},
		{"exchange-failure", "state=state&code=code", "exchange_failed", true, errors.New("private provider detail"), 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDiscordLinkBackend{available: tc.available, state: "state", userID: 17, exchangeErr: tc.exchangeErr}
			links := handlers.NewDiscordLinkHandler(fake, notifications.NewSettings(discordLinkSettings{}), "https://server.example.test")
			h := apiv2.NewHandler(apiv2.Dependencies{NotificationDiscordLinks: links})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/notifications/discord/link/callback?"+tc.query, nil))
			if rec.Code != 302 || rec.Header().Get("Location") != "/settings/notifications?discord_error="+tc.result || fake.consumeCalls != tc.consumes || fake.exchangeCalls != tc.exchanges {
				t.Fatalf("callback: %d %v %+v", rec.Code, rec.Header(), fake)
			}
		})
	}
}
