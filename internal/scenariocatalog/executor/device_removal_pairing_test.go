package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestRequiredDeviceRemovalAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-device-removal for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.DeviceRemovalAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	guardFrozenAPIKeyFixture(t)
	e := New(t)
	defer func() { e.Reseed(); e.guardScratchDatabase(); guardFrozenAPIKeyFixture(t) }()
	var results []Result
	requests, effects := 0, 0
	for _, c := range selected {
		for _, row := range c.Rows {
			for _, s := range row.Scenarios {
				for _, transport := range []string{"v1", "v2"} {
					t.Run(s.ID+"/"+transport, func(t *testing.T) {
						// This focused runner supplies fresh state before AND after each transport,
						// including FreshState originals, and observes effects before teardown.
						e.Reseed()
						defer e.Reseed()
						deviceRemovalOverlay(t, e)
						result := Result{ID: s.ID + "/" + transport, Scenario: s.ID, Transport: transport, Catalog: c.File, Row: row.Key().String()}
						defer func() {
							if t.Failed() && len(result.Failures) == 0 {
								result.Failures = append(result.Failures, "scenario assertion failed; see test log")
							}
							results = append(results, result)
						}()
						if len(s.Settings) > 0 {
							settings := map[string]string{}
							for k, v := range s.Settings {
								resolved, err := e.substitute(v)
								if err != nil {
									t.Fatal(err)
								}
								settings[k] = resolved
							}
							defer e.applySettings(settings)()
						}
						snapshot := func() map[string]json.RawMessage {
							t.Helper()
							effects++
							var raw []byte
							if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM users x),'profiles',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_profiles x),'keys',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM api_keys x),'settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM server_settings x),'sessions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM auth_sessions x),'device_requests',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM device_login_requests x),'invitations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM invitations x),'invite_codes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM invite_codes x),'devices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_devices x),'legacy_device_settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_device_settings x),'canonical',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_setting_values x),'mutations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_setting_mutations x),'migration_rejects',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_setting_migration_rejects x),'legacy_settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_settings x))`).Scan(&raw); err != nil {
								t.Fatal(err)
							}
							var rows map[string]json.RawMessage
							if err := json.Unmarshal(raw, &rows); err != nil {
								t.Fatal(err)
							}
							return rows
						}
						before := snapshot()
						if len(before) != 14 {
							t.Fatal("snapshot must contain fourteen full tables")
						}
						request, expect, principal, method := s.Request, s.Expect, s.Principal, row.Method
						if transport == "v2" {
							pair := s.V2Expectation
							request, expect, method = pair.Request, pair.Expect, pair.Method
							result.OperationID = pair.OperationID
							if pair.Principal != nil {
								principal = *pair.Principal
							}
						}
						requests += max(request.Repeat, 1)
						_, failures, err := e.exchange(e.live.URL, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("frozen exchange: %v", failures)
						}
						after := snapshot()
						e.checkDeviceRemovalEffects(t, s.ID, before, after)
						if len(before) != len(after) {
							t.Error("unexpected snapshot table count")
						}
						for id, want := range before {
							if !bytes.Equal(want, after[id]) {
								t.Errorf("unexpected stored change in %s", id)
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredDeviceRemovalScenarios); err != nil {
		t.Error(err)
	}
	if requests != 26 || effects != 52 {
		t.Errorf("paired device removal evidence %dHTTP/%dPG, want26/52", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// Populate both settings generations and overlapping device IDs without changing
// the original requests or making device_a belong to the secondary profile.
func deviceRemovalOverlay(t *testing.T, e *Env) {
	t.Helper()
	targets := []struct{ account, profile, device string }{
		{fixtureMember, profilePrimary, deviceIDA}, {fixtureMember, profilePrimary, deviceIDB}, {fixtureMember, profileSecondary, "fixture-device-c"},
		{fixtureMember, profileSecondary, deviceIDB}, {fixtureMember, profilePrimary, "fixture-device-c"},
		{fixtureAdmin, profileAdminPrimary, deviceIDA}, {fixtureAdmin, profileAdminPrimary, deviceIDB}, {fixtureAdmin, profileAdminPrimary, "fixture-device-c"},
	}
	for _, v := range targets {
		store, err := e.stores.ForUser(e.ctx, e.users[v.account].ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetDeviceSetting(e.ctx, userstore.DeviceSettingEntry{ProfileID: v.profile, DeviceID: v.device, Key: "theme", Value: "dark", DeviceName: "Fixture settings device", DevicePlatform: "test"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpsertSettingValue(e.ctx, userstore.SettingIdentity{Key: "theme", Scope: settingscontract.ScopeProfileDevice, ProfileID: v.profile, DeviceID: v.device}, json.RawMessage(`"dark"`)); err != nil {
			t.Fatal(err)
		}
	}
	// Profile values must survive clearing device overrides (inheritance remains).
	for _, v := range []struct{ account, profile string }{{fixtureMember, profilePrimary}, {fixtureMember, profileSecondary}, {fixtureAdmin, profileAdminPrimary}} {
		store, err := e.stores.ForUser(e.ctx, e.users[v.account].ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpsertSettingValue(e.ctx, userstore.SettingIdentity{Key: "theme", Scope: settingscontract.ScopeProfile, ProfileID: v.profile}, json.RawMessage(`"light"`)); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *Env) checkDeviceRemovalEffects(t *testing.T, id string, before, after map[string]json.RawMessage) {
	t.Helper()
	var devices, canonical, legacy []map[string]any
	for raw, target := range map[string]*[]map[string]any{"devices": &devices, "canonical": &canonical, "legacy_device_settings": &legacy} {
		if err := json.Unmarshal(before[raw], target); err != nil {
			t.Fatal(err)
		}
	}
	if len(devices) != 8 || len(canonical) != 11 || len(legacy) != 8 {
		t.Fatal("populated isolated device fixture inventory invalid")
	}
	suffix := strings.Split(id, ".")[1]
	if suffix != "named_profile" && suffix != "shape" {
		return
	}
	profile, device := profilePrimary, deviceIDA
	forget := strings.HasPrefix(id, "device_forget.")
	if suffix == "named_profile" {
		profile, device = profileSecondary, "fixture-device-c"
	} else if forget {
		device = deviceIDB
	}
	// Filter exactly the requested account/profile/device identity from complete
	// before-state rows. Every other column and row must remain byte-identical.
	for _, table := range []string{"devices", "canonical", "legacy_device_settings"} {
		if table == "devices" && !forget {
			continue
		}
		var rows []map[string]any
		if err := json.Unmarshal(before[table], &rows); err != nil {
			t.Fatal(err)
		}
		kept := make([]map[string]any, 0, len(rows))
		removed := 0
		for _, row := range rows {
			target := row["user_id"] == float64(e.users[fixtureMember].ID) && row["profile_id"] == profile && row["device_id"] == device
			if table == "canonical" {
				target = target && row["scope"] == string(settingscontract.ScopeProfileDevice)
			}
			if target {
				removed++
			} else {
				kept = append(kept, row)
			}
		}
		if removed != 1 {
			t.Fatalf("expected exactly one target row in %s", table)
		}
		var current []map[string]any
		if err := json.Unmarshal(after[table], &current); err != nil {
			t.Fatal(err)
		}
		var err error
		before[table], err = json.Marshal(kept)
		if err != nil {
			t.Fatal(err)
		}
		after[table], err = json.Marshal(current)
		if err != nil {
			t.Fatal(err)
		}
	}
}
