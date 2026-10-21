package apiv2

import (
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func discordManagement(profiles []userstore.Profile) *handlers.DiscordNotificationsHandler {
	h := handlers.NewDiscordNotificationsHandler(nil, "")
	h.Stores = householdStores{profiles: profiles}
	return h
}

func TestDiscordV2HouseholdAdmission(t *testing.T) {
	unrestricted := []userstore.Profile{{ID: "p-primary", IsPrimary: true}, {ID: "p-owner"}}
	restricted := []userstore.Profile{{ID: "p-primary", IsPrimary: true}, {ID: "p-owner", MaxContentRating: "PG"}}
	locked := []userstore.Profile{{ID: "p-primary-locked", IsPrimary: true, PINHash: "synthetic-hash"}, {ID: "p-owner"}}
	for _, tc := range []struct {
		name     string
		profiles []userstore.Profile
		headers  map[string]string
		want     ProblemType
	}{
		{"member primary", unrestricted, with(bearer(memberToken), "X-Profile-Id", "p-primary"), ProblemType{}},
		{"admin primary", unrestricted, with(bearer(adminToken), "X-Profile-Id", "p-primary"), ProblemType{}},
		{"secondary", unrestricted, profileOwner(), TypePermissionDenied},
		{"admin secondary", unrestricted, with(bearer(adminToken), "X-Profile-Id", "p-owner"), TypePermissionDenied},
		{"restricted secondary", restricted, profileOwner(), TypePermissionDenied},
		{"locked primary missing proof", locked, with(bearer(memberToken), "X-Profile-Id", "p-primary-locked"), TypeProfileVerificationRequired},
		{"locked primary verified", locked, with(with(bearer(memberToken), "X-Profile-Id", "p-primary-locked"), "X-Profile-Token", "synthetic-proof"), ProblemType{}},
		{"key skipped PIN", locked, with(bearer(apiKeyToken), "X-Profile-Id", "p-primary-locked"), TypeProfileVerificationRequired},
		{"profileless member", unrestricted, bearer(memberToken), TypePermissionDenied},
		{"profileless unrestricted admin", unrestricted, bearer(adminToken), ProblemType{}},
		{"profileless restricted admin", restricted, bearer(adminToken), TypePermissionDenied},
		{"profileless locked admin", locked, bearer(adminToken), TypePermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, op := range []struct {
				method, path, body string
				status             int
			}{
				{http.MethodPost, "/discord/link/init", "", 200},
				{http.MethodPut, "/discord-preferences", `{"mode":"off"}`, 200},
				{http.MethodDelete, "/discord-link", "", 204},
			} {
				channels := new(fakeNotificationChannels)
				links := new(fakeNotificationDiscordLinks)
				deps := pilotDeps(nil, nil)
				deps.NotificationChannels = channels
				deps.NotificationDiscordLinks = links
				deps.NotificationDiscordManagement = discordManagement(tc.profiles)
				rec := do(t, NewHandler(deps), op.method, Prefix+"/notifications"+op.path, op.body, tc.headers)
				if tc.want != (ProblemType{}) {
					requireProblem(t, rec, tc.want)
					if channels.writes != 0 || links.calls != 0 {
						t.Fatal("refused mutation reached backend")
					}
				} else if rec.Code != op.status || channels.writes+links.calls != 1 {
					t.Fatalf("status=%d writes=%d links=%d body=%s", rec.Code, channels.writes, links.calls, rec.Body.String())
				}
			}
		})
	}
}
