package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/webhooksync"
)

const webhookFixtureID = "00000000-0000-4000-8000-000000000001"

type fakeWebhookManagement struct {
	calls     int
	user      int
	before    *webhooksync.PageKey
	deleteErr error
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
func (f *fakeWebhookManagement) CreateWebhookConnection(context.Context, int, webhooksync.CreateConnectionInput) (*webhooksync.CreateConnectionResult, error) {
	f.calls++
	return &webhooksync.CreateConnectionResult{Connection: webhooksync.Connection{ID: webhookFixtureID, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}}, nil
}
func (f *fakeWebhookManagement) UpdateWebhookConnection(context.Context, int, string, webhooksync.UpdateConnectionInput) (*webhooksync.Connection, error) {
	f.calls++
	return &webhooksync.Connection{ID: webhookFixtureID, CreatedAt: fixedTime(), UpdatedAt: fixedTime()}, nil
}
func (f *fakeWebhookManagement) DeleteWebhookConnection(_ context.Context, user int, id string) error {
	f.calls++
	f.user = user
	return f.deleteErr
}
func (f *fakeWebhookManagement) GetWebhookMappings(context.Context, int, string) (*webhooksync.ProfileMappingsResponse, error) {
	f.calls++
	return &webhooksync.ProfileMappingsResponse{}, nil
}
func (f *fakeWebhookManagement) UpdateWebhookMappings(context.Context, int, string, webhooksync.UpdateProfileMappingsInput) ([]webhooksync.ProfileMapping, error) {
	f.calls++
	return nil, nil
}
func (f *fakeWebhookManagement) ListWebhookEvents(context.Context, int, string, *webhooksync.PageKey, int) ([]webhooksync.WebhookEventLog, bool, error) {
	f.calls++
	return nil, false, nil
}

// webhookManagement is the real household-manager rule over fixed profiles.
func webhookManagement(profiles []userstore.Profile) *handlers.WebhookSyncHandler {
	return handlers.NewWebhookSyncHandler(nil, householdStores{profiles: profiles}, nil)
}

// webhookManager is the member account acting as its primary profile.
func webhookManager() map[string]string {
	return with(bearer(memberToken), "X-Profile-Id", "p-primary")
}

func webhookDeps(f *fakeWebhookManagement) Dependencies {
	deps := pilotDeps(nil, nil)
	deps.WebhookSync = f
	deps.WebhookSyncManagement = webhookManagement([]userstore.Profile{{ID: "p-primary", IsPrimary: true}})
	return deps
}

func TestWebhookManagementAccountCursorAndSecrets(t *testing.T) {
	f := &fakeWebhookManagement{deleteErr: &handlers.APIError{Status: 404, Code: "not_found", Message: "Not found"}}
	h := newTestHandler(t, webhookDeps(f))
	rec := do(t, h, http.MethodGet, Prefix+"/webhook-sync/connections?limit=1", "", webhookManager())
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
	rec = do(t, h, http.MethodGet, Prefix+"/webhook-sync/connections?limit=1&cursor="+page.Page.Next, "", webhookManager())
	if rec.Code != 200 || f.before == nil || f.before.ID != webhookFixtureID || !f.before.At.Equal(fixedTime()) {
		t.Fatalf("continuation: %d %s", rec.Code, rec.Body)
	}
	// A connection list cursor is never accepted for another collection.
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/webhook-sync/connections/"+webhookFixtureID+"/events?cursor="+page.Page.Next, "", webhookManager()), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/webhook-sync/connections?limit=201", "", webhookManager()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/webhook-sync/connections", "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodDelete, Prefix+"/webhook-sync/connections/"+webhookFixtureID, "", webhookManager()), TypeNotFound)
}

func TestWebhookMappingsRejectDuplicateUsersBeforeWrite(t *testing.T) {
	h := newTestHandler(t, webhookDeps(new(fakeWebhookManagement)))
	for _, body := range []string{`{"mappings":[{"external_user_id":" ","external_user_name":"x","silo_profile_id":null}]}`, `{"mappings":[{"external_user_id":"same","external_user_name":"x","silo_profile_id":null},{"external_user_id":"same","external_user_name":"y","silo_profile_id":null}]}`} {
		requireProblem(t, do(t, h, http.MethodPut, Prefix+"/webhook-sync/connections/"+webhookFixtureID+"/profile-mappings", body, webhookManager()), TypeValidationFailed)
	}
}

// TestWebhookSyncV2HouseholdAdmission: connections are account-wide, the list
// carries each receiver secret, and mappings write into any profile, so every
// operation, reads included, answers only the household manager.
func TestWebhookSyncV2HouseholdAdmission(t *testing.T) {
	unrestricted := []userstore.Profile{{ID: "p-primary", IsPrimary: true}, {ID: "p-owner"}}
	restricted := []userstore.Profile{{ID: "p-primary", IsPrimary: true}, {ID: "p-owner", MaxContentRating: "PG"}}
	locked := []userstore.Profile{{ID: "p-primary-locked", IsPrimary: true, PINHash: "synthetic-hash"}, {ID: "p-owner"}}
	connection := "/connections/" + webhookFixtureID
	for _, tc := range []struct {
		name     string
		profiles []userstore.Profile
		headers  map[string]string
		want     ProblemType
	}{
		{"primary", unrestricted, webhookManager(), ProblemType{}},
		{"secondary", unrestricted, profileOwner(), TypePermissionDenied},
		{"restricted secondary", restricted, profileOwner(), TypePermissionDenied},
		{"profileless member", unrestricted, bearer(memberToken), TypePermissionDenied},
		{"locked primary missing proof", locked, with(bearer(memberToken), "X-Profile-Id", "p-primary-locked"), TypeProfileVerificationRequired},
		{"key skipped PIN", locked, with(bearer(apiKeyToken), "X-Profile-Id", "p-primary-locked"), TypeProfileVerificationRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := new(fakeWebhookManagement)
			deps := pilotDeps(nil, nil)
			deps.WebhookSync = f
			deps.WebhookSyncManagement = webhookManagement(tc.profiles)
			h := newTestHandler(t, deps)
			for _, op := range []struct {
				method, path, body string
				status             int
			}{
				{http.MethodGet, "/connections", "", 200},
				{http.MethodPost, "/connections", `{"provider":"plex","server_name":"Example","default_profile_id":"p-owner"}`, 201},
				{http.MethodPut, connection, `{"server_name":"Renamed"}`, 200},
				{http.MethodDelete, connection, "", 204},
				{http.MethodPost, connection + "/webhook/rotate", "", 200},
				{http.MethodGet, connection + "/profile-mappings", "", 200},
				{http.MethodPut, connection + "/profile-mappings", `{"mappings":[{"external_user_id":"7","external_user_name":"x","silo_profile_id":"p-primary"}]}`, 200},
				{http.MethodGet, connection + "/events", "", 200},
			} {
				*f = fakeWebhookManagement{}
				rec := do(t, h, op.method, Prefix+"/webhook-sync"+op.path, op.body, tc.headers)
				if tc.want != (ProblemType{}) {
					requireProblem(t, rec, tc.want)
					if f.calls != 0 {
						t.Fatalf("%s %s: refused request reached the service", op.method, op.path)
					}
				} else if rec.Code != op.status || f.calls != 1 {
					t.Fatalf("%s %s: status=%d calls=%d body=%s", op.method, op.path, rec.Code, f.calls, rec.Body.String())
				}
			}
		})
	}
}
