package apiv2

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
)

func adminAccountInsightDeps() (Dependencies, *fakeAdminAccounts) {
	deps := withAdminAccountInsights(requestDeps(fixtureRequests()))
	accounts := fixtureAdminAccounts()
	deps.AdminAccounts = accounts
	return deps, accounts
}

func TestAdminUserDevicesContract(t *testing.T) {
	deps, accounts := adminAccountInsightDeps()
	devices := deps.AdminAccountDevices.(*fakeAdminAccountDevices)
	h := NewHandler(deps)
	path := Prefix + "/admin/users/7/devices"

	reply := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if reply.Code != http.StatusOK || devices.user != 7 {
		t.Fatalf("%d %s user=%d", reply.Code, reply.Body.String(), devices.user)
	}
	var body Collection[AdminUserDevice]
	if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.Page == nil || body.Page.HasMore {
		t.Fatalf("collection: %s", reply.Body.String())
	}
	tv, settingsOnly := body.Items[0], body.Items[1]
	if tv.DeviceID != "device-tv" || !tv.LastSeenAt.Valid || tv.LastSeenAt.Time.String() != "2026-01-02T03:04:05.678Z" || tv.OverrideCount != 2 || tv.DevicePlatform != "tvOS" {
		t.Fatalf("registered device: %+v", tv)
	}
	if len(tv.Profiles) != 2 || tv.Profiles[0].ProfileID != "p-owner" || !tv.Profiles[0].LastSeenAt.Valid || tv.Profiles[1].ProfileName != "" || tv.Profiles[1].LastSeenAt.Valid {
		t.Fatalf("device profiles: %+v", tv.Profiles)
	}
	if settingsOnly.DeviceID != "device-settings-only" || settingsOnly.LastSeenAt.Valid || !settingsOnly.LastUpdated.Valid {
		t.Fatalf("settings-only device: %+v", settingsOnly)
	}
	if !strings.Contains(reply.Body.String(), `"last_seen_at":null`) {
		t.Fatalf("null last_seen_at not rendered: %s", reply.Body.String())
	}

	// Seen devices sort newest first, then by name and id; unseen devices last.
	devices.rows = []handlers.AdminUserDeviceView{
		adminUserDeviceView(`{"device_id":"z-unseen","device_name":"A"}`, "", nil),
		adminUserDeviceView(`{"device_id":"b","device_name":"Same"}`, "2026-01-01T00:00:00Z", nil),
		adminUserDeviceView(`{"device_id":"a","device_name":"Same"}`, "2026-01-01T00:00:00Z", nil),
		adminUserDeviceView(`{"device_id":"newest","device_name":"Zed"}`, "2026-01-03T00:00:00Z", nil),
	}
	reply = do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, item := range body.Items {
		order = append(order, item.DeviceID)
	}
	if got := strings.Join(order, ","); got != "newest,a,b,z-unseen" {
		t.Fatalf("order %s", got)
	}

	calls := devices.calls
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/users/abc/devices", "", actingRequestAdmin), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(memberToken)), TypePermissionDenied)
	accounts.err = auth.ErrNotFound
	requireProblem(t, do(t, h, http.MethodGet, path, "", actingRequestAdmin), TypeNotFound)
	if devices.calls != calls {
		t.Fatal("a refused read reached the device service")
	}
	accounts.err = nil
	devices.err = errors.New("PRIVATE store failure")
	failed := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	requireProblem(t, failed, TypeInternalError)
	if strings.Contains(failed.Body.String(), "PRIVATE") {
		t.Fatal("private error leaked")
	}
	deps.AdminAccountDevices = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
}

func TestAdminUserWatchSummaryContract(t *testing.T) {
	deps, accounts := adminAccountInsightDeps()
	summary := deps.AdminWatchSummary.(*fakeAdminWatchSummary)
	h := NewHandler(deps)
	path := Prefix + "/admin/users/7/watch-summary"

	before := time.Now().UTC()
	reply := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
	after := time.Now().UTC()
	if reply.Code != http.StatusOK {
		t.Fatalf("%d %s", reply.Code, reply.Body.String())
	}
	q := summary.query
	if q.UserID != 7 || q.Days != 30 || q.ProfileID != "" || q.Now.Before(before) || q.Now.After(after) {
		t.Fatalf("default query %+v", q)
	}
	var body AdminUserWatchSummary
	if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// The window start is the service's now minus days, rendered as given.
	if body.Days != 30 || !body.Since.Equal(fixedTime().AddDate(0, 0, -30).Truncate(time.Millisecond)) || body.Plays != 41 || body.CompletedPlays != 29 || body.WatchedSeconds != 66000 || !body.LastPlayedAt.Valid {
		t.Fatalf("projection %+v", body)
	}
	if strings.Contains(reply.Body.String(), "profile_id") {
		t.Fatalf("absent profile filter echoed: %s", reply.Body.String())
	}

	reply = do(t, h, http.MethodGet, path+"?days=7&profile_id=p-owner", "", actingRequestAdmin)
	if reply.Code != http.StatusOK || summary.query.Days != 7 || summary.query.ProfileID != "p-owner" || !strings.Contains(reply.Body.String(), `"profile_id":"p-owner"`) {
		t.Fatalf("profile filter: %d %s %+v", reply.Code, reply.Body.String(), summary.query)
	}

	calls := summary.calls
	for _, q := range []string{"?days=0", "?days=366", "?days=abc"} {
		requireProblem(t, do(t, h, http.MethodGet, path+q, "", actingRequestAdmin), TypeValidationFailed)
	}
	accounts.err = auth.ErrNotFound
	requireProblem(t, do(t, h, http.MethodGet, path, "", actingRequestAdmin), TypeNotFound)
	if summary.calls != calls {
		t.Fatal("a refused read reached the summary service")
	}
	accounts.err = nil

	summary.err = errors.New("PRIVATE database failure")
	requireProblem(t, do(t, h, http.MethodGet, path, "", actingRequestAdmin), TypeInternalError)
	deps.AdminWatchSummary = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
}

func TestAdminRequestUserUsageContract(t *testing.T) {
	deps, accounts := adminAccountInsightDeps()
	usage := deps.AdminRequestUsage.(*fakeAdminRequestUsage)
	h := NewHandler(deps)
	path := Prefix + "/admin/request-users/7/usage"

	read := func() AdminRequestUserUsage {
		t.Helper()
		reply := do(t, h, http.MethodGet, path, "", actingRequestAdmin)
		if reply.Code != http.StatusOK {
			t.Fatalf("%d %s", reply.Code, reply.Body.String())
		}
		var body AdminRequestUserUsage
		if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	body := read()
	if usage.user != 7 || !body.RequestsEnabled || !body.Allowed || body.Unlimited || body.Used != 12 || body.MaxRequests != 20 || body.Remaining != 8 || body.WindowDays != 30 || body.WindowStart.IsZero() {
		t.Fatalf("projection %+v", body)
	}

	usage.policy.Blocked = true
	if body = read(); body.Allowed {
		t.Fatalf("blocked account allowed: %+v", body)
	}
	usage.policy = fixtureRequestUsagePolicy()
	usage.policy.Unlimited, usage.policy.Used, usage.policy.Remaining = true, 0, 0
	if body = read(); !body.Unlimited || body.Used != 0 {
		t.Fatalf("unlimited account: %+v", body)
	}

	calls := usage.calls
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/request-users/0/usage", "", actingRequestAdmin), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, path, "", bearer(memberToken)), TypePermissionDenied)
	accounts.err = auth.ErrNotFound
	requireProblem(t, do(t, h, http.MethodGet, path, "", actingRequestAdmin), TypeNotFound)
	if usage.calls != calls {
		t.Fatal("a refused read reached the request service")
	}
	accounts.err = nil

	// Without account administration the read still answers.
	deps.AdminAccounts = nil
	if reply := do(t, NewHandler(deps), http.MethodGet, path, "", actingRequestAdmin); reply.Code != http.StatusOK {
		t.Fatalf("without account service: %d %s", reply.Code, reply.Body.String())
	}
	deps.AdminRequestUsage = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodGet, path, "", actingRequestAdmin), TypeDependencyUnavailable)
}

func TestAdminAccountInsightCapabilities(t *testing.T) {
	read := func(deps Dependencies) map[string]any {
		t.Helper()
		reply := do(t, NewHandler(deps), http.MethodGet, Prefix+"/admin/users/capabilities", "", actingRequestAdmin)
		if reply.Code != http.StatusOK {
			t.Fatalf("%d %s", reply.Code, reply.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	flags := []string{"account_devices", "watch_summary", "account_downloads", "request_usage"}
	deps, _ := adminAccountInsightDeps()
	on := read(deps)
	off := read(requestDeps(fixtureRequests()))
	for _, flag := range flags {
		if on[flag] != true || off[flag] != false {
			t.Errorf("%s: configured %v, unconfigured %v", flag, on[flag], off[flag])
		}
	}
}
