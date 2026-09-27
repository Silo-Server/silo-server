package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/discord"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeAdminNotificationDiscord struct {
	calls int
	err   error
}

func (f *fakeAdminNotificationDiscord) TestDiscordBot(context.Context) (discord.User, error) {
	f.calls++
	return discord.User{Username: "fixture-bot"}, f.err
}

func TestAdminNotificationDiscord(t *testing.T) {
	fake := new(fakeAdminNotificationDiscord)
	deps := pilotDeps(nil, nil)
	deps.AdminNotificationDiscord = fake
	h := NewHandler(deps)
	path := Prefix + "/admin/notifications/discord/test"
	for _, headers := range []map[string]string{nil, profileOwner(), with(bearer(adminToken), "X-Profile-Id", "p-owner")} {
		rec := do(t, h, http.MethodPost, path, "", headers)
		if rec.Code != 401 && rec.Code != 403 {
			t.Fatalf("unauthorized: %d %s", rec.Code, rec.Body.String())
		}
	}
	if fake.calls != 0 {
		t.Fatal("unauthorized provider call")
	}
	for _, tc := range []struct {
		err     error
		ok      bool
		message string
	}{
		{nil, true, "Connected as fixture-bot"},
		{fmt.Errorf("wrapped: %w", notifications.ErrDiscordNotConfigured), false, "Bot token is not configured"},
		{errors.New("private provider URL and credential"), false, "Could not verify the Discord bot credential"},
	} {
		fake.err = tc.err
		before := fake.calls
		rec := do(t, h, http.MethodPost, path, "", bearer(adminToken))
		var result AdminNotificationDiscordTestResult
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || fake.calls != before+1 || result.OK != tc.ok || result.Message != tc.message || result.DurationMS < 0 {
			t.Fatalf("%d %+v calls=%d", rec.Code, result, fake.calls)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
	}
	deps.AdminNotificationDiscord = nil
	rec := do(t, NewHandler(deps), http.MethodPost, path, "", bearer(adminToken))
	if rec.Code != 503 {
		t.Fatalf("missing service: %d", rec.Code)
	}
}

func adminNotificationDiscordFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "admin_discord_test", operationID: testAdminDiscordNotificationOperation, method: "POST", path: Prefix + "/admin/notifications/discord/test", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/AdminNotificationDiscordTestResult", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "A synthetic bot identity lookup verifies credentials without sending a notification."}}
}
