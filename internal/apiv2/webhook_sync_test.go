package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/webhooksync"
)

const webhookFixtureID = "00000000-0000-4000-8000-000000000001"

type fakeWebhookManagement struct {
	WebhookSyncService
	calls  int
	user   int
	before *webhooksync.PageKey
}

func (f *fakeWebhookManagement) ListWebhookConnections(_ context.Context, user int, before *webhooksync.PageKey, limit int) ([]webhooksync.Connection, bool, error) {
	f.calls++
	f.user = user
	f.before = before
	if before != nil {
		return []webhooksync.Connection{}, false, nil
	}
	return []webhooksync.Connection{{ID: webhookFixtureID, Provider: "plex", ServerID: "external", ServerName: "Example", DefaultProfileID: "p-owner", WebhookSecret: "receiver-secret", AccessToken: "must-not-be-returned", CreatedAt: fixedTime(), UpdatedAt: fixedTime()}}, true, nil
}
func (f *fakeWebhookManagement) DeleteWebhookConnection(_ context.Context, user int, id string) error {
	f.calls++
	f.user = user
	return &handlers.APIError{Status: 404, Code: "not_found", Message: "Not found"}
}
func TestWebhookManagementAccountCursorAndSecrets(t *testing.T) {
	f := new(fakeWebhookManagement)
	deps := pilotDeps(nil, nil)
	deps.WebhookSync = f
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, Prefix+"/webhook-sync/connections?limit=1", "", bearer(memberToken))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if f.user != 1 || strings.Contains(rec.Body.String(), "must-not-be-returned") || strings.Contains(rec.Body.String(), "access_token") {
		t.Fatalf("unsafe response: %s", rec.Body)
	}
	var body WebhookConnectionCollection
	decodeBody(t, rec.Body, &body)
	if len(body.Items) != 1 || body.Items[0].WebhookURL != "/api/v2/webhook-sync/webhooks/receiver-secret" {
		t.Fatalf("response: %s", rec.Body)
	}
	// Inspect cursor through public JSON to preserve envelope type encapsulation.
	var page struct {
		Page struct {
			Next string `json:"next_cursor"`
		} `json:"page"`
	}
	decodeBody(t, rec.Body, &page)
	if page.Page.Next == "" {
		t.Fatal("missing continuation")
	}
	rec = do(t, h, http.MethodGet, Prefix+"/webhook-sync/connections?limit=1&cursor="+page.Page.Next, "", bearer(memberToken))
	if rec.Code != 200 || f.before == nil || f.before.ID != webhookFixtureID || !f.before.At.Equal(fixedTime()) {
		t.Fatalf("continuation: %d %s", rec.Code, rec.Body)
	}
	// A connection list cursor is never accepted for another collection.
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/webhook-sync/connections/"+webhookFixtureID+"/events?cursor="+page.Page.Next, "", bearer(memberToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/webhook-sync/connections?limit=201", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/webhook-sync/connections", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/webhook-sync/connections/"+webhookFixtureID, "", bearer(memberToken)), TypeNotFound)
}
func TestWebhookManagementMissingAndValidation(t *testing.T) {
	h := newTestHandler(t, pilotDeps(nil, nil))
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/webhook-sync/connections", "", bearer(memberToken)), TypeDependencyUnavailable)
	requireProblem(t, do(t, h, http.MethodPost, Prefix+"/webhook-sync/connections", `{"provider":"unknown","server_id":"x","server_name":"x","default_profile_id":"p-owner"}`, bearer(memberToken)), TypeValidationFailed)
}

func TestWebhookMappingsRejectDuplicateUsersBeforeWrite(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.WebhookSync = new(fakeWebhookManagement)
	h := newTestHandler(t, deps)
	for _, body := range []string{`{"mappings":[{"external_user_id":" ","external_user_name":"x","silo_profile_id":null}]}`, `{"mappings":[{"external_user_id":"same","external_user_name":"x","silo_profile_id":null},{"external_user_id":"same","external_user_name":"y","silo_profile_id":null}]}`} {
		requireProblem(t, do(t, h, http.MethodPut, Prefix+"/webhook-sync/connections/"+webhookFixtureID+"/profile-mappings", body, bearer(memberToken)), TypeValidationFailed)
	}
}
