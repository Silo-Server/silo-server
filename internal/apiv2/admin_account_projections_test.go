package apiv2

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAdminAccountProjectionCursorsAndIdentity(t *testing.T) {
	deps := requestDeps(fixtureRequests())
	deps.AdminAccounts = fixtureAdminAccounts()
	settings := &fakeAdminAccountSettings{}
	deps.AdminAccountSettings = settings
	deps.AdminAPIKeys = fixtureAdminAPIKeys()
	deps.AdminAccountActivity = &fakeAdminAccountActivity{}
	h := NewHandler(deps)
	for _, path := range []string{Prefix + "/admin/users/7/settings/values", Prefix + "/admin/users/7/api-keys"} {
		first := do(t, h, http.MethodGet, path+"?limit=1", "", actingRequestAdmin)
		if first.Code != 200 {
			t.Fatal(first.Code, first.Body.String())
		}
		var page struct {
			Page struct {
				HasMore    bool   `json:"has_more"`
				NextCursor string `json:"next_cursor"`
			} `json:"page"`
		}
		if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil || !page.Page.HasMore || page.Page.NextCursor == "" {
			t.Fatalf("%s %v", first.Body.String(), err)
		}
		cursor := url.QueryEscape(page.Page.NextCursor)
		second := do(t, h, http.MethodGet, path+"?limit=1&cursor="+cursor, "", actingRequestAdmin)
		if second.Code != 200 {
			t.Fatal(second.Code, second.Body.String())
		}
		if strings.Contains(second.Body.String(), `"has_more":true`) {
			t.Fatal("last page stayed open")
		}
		requireProblem(t, do(t, h, http.MethodGet, path+"?limit=2&cursor="+cursor, "", actingRequestAdmin), TypeInvalidCursor)
		requireProblem(t, do(t, h, http.MethodGet, strings.Replace(path, "/7/", "/8/", 1)+"?limit=1&cursor="+cursor, "", actingRequestAdmin), TypeInvalidCursor)
		requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(memberToken)), TypePermissionDenied)
	}
	path := Prefix + "/admin/users/7/settings/values/playback.subtitle_language"
	requireProblem(t, do(t, h, http.MethodPut, path, `{"value":"de"}`, actingRequestAdmin), TypeValidationFailed)
	saved := do(t, h, http.MethodPut, path+"?scope=profile_device&profile_id=target-profile&device_id=device", `{"value":"de"}`, actingRequestAdmin)
	if saved.Code != 200 || settings.last.ProfileID != "target-profile" || settings.last.DeviceID != "device" {
		t.Fatal(saved.Code, saved.Body.String(), settings.last)
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/ips?ip=invalid", "", actingRequestAdmin), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/users/7/ips?days=366", "", actingRequestAdmin), TypeValidationFailed)
}
