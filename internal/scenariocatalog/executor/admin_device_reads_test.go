package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Inspect before New can migrate or reseed any account-owned settings.
func guardNewAdminDeviceFixture(t *testing.T) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect administrator device fixture")
	}
	defer pool.Close()
	for _, table := range []string{"user_device_settings", "user_setting_values", "user_devices"} {
		var exists bool
		if err := pool.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			continue
		}
		query := "SELECT count(*) FROM " + table
		var args []any
		if table == "user_devices" {
			query += ` d LEFT JOIN users u ON u.id=d.user_id WHERE u.email IS DISTINCT FROM $1 OR NOT ((d.profile_id=$2 AND d.device_id IN ($3,$4)) OR (d.profile_id=$5 AND d.device_id='fixture-device-c'))`
			args = []any{memberEmail, profilePrimary, deviceIDA, deviceIDB, profileSecondary}
		}
		var count int
		if err := pool.QueryRow(t.Context(), query, args...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("NEW administrator device fixture refuses existing %s rows", table)
		}
	}
}

func TestRequiredNewAdminDeviceReads(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-admin-device-reads for required NEW acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; NEW acceptance cannot skip")
	}
	guardNewAdminDeviceFixture(t)
	e := New(t)
	defer func() { e.Reseed(); e.guardScratchDatabase(); guardNewAdminDeviceFixture(t) }()
	var results []Result
	requests, effects := 0, 0
	for _, id := range []string{"merged_overrides", "shared_account_identity", "pagination", "missing_identity", "authority"} {
		e.Reseed()
		member, adminID := e.users[fixtureMember].ID, e.users[fixtureAdmin].ID
		e.mustExec(`INSERT INTO user_devices(user_id,profile_id,device_id,device_name,device_platform,last_seen_at) VALUES($1,$2,$3,'Other account TV','tvos','2026-01-02T03:04:05Z')`, adminID, profileAdminPrimary, deviceIDA)
		e.mustExec(`INSERT INTO user_device_settings(user_id,profile_id,device_id,key,value,device_name,device_platform,updated_at) VALUES($1,$2,$3,'subtitle_appearance','{"fontSize":"large"}','Fixture TV','tvos','2026-01-03T03:04:05.123456Z')`, member, profilePrimary, deviceIDA)
		for _, v := range []struct{ profile, key, value string }{{profilePrimary, "playback.subtitle_appearance", `{"fontSize":"large"}`}, {profilePrimary, "player.playback_speed", "1.25"}, {profileSecondary, "player.playback_speed", "1.5"}} {
			e.mustExec(`INSERT INTO user_setting_values(user_id,profile_id,device_id,key,scope,value,updated_at) VALUES($1,$2,$3,$4,'profile_device',$5::jsonb,'2026-01-03T03:04:05.123456Z')`, member, v.profile, deviceIDA, v.key, v.value)
		}
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_admin_device_reads." + id + "/v2", Scenario: "new_admin_device_reads." + id, Transport: "v2"}
			defer func() {
				if t.Failed() && len(result.Failures) == 0 {
					result.Failures = append(result.Failures, "scenario assertion failed; see test log")
				}
				results = append(results, result)
			}()
			snapshot := func() []byte {
				t.Helper()
				effects++
				var raw []byte
				err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('devices',(SELECT jsonb_agg(to_jsonb(d) ORDER BY user_id,profile_id,device_id) FROM user_devices d),'legacy',(SELECT jsonb_agg(to_jsonb(d) ORDER BY user_id,profile_id,device_id,key) FROM user_device_settings d),'canonical',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM user_setting_values d))`).Scan(&raw)
				if err != nil {
					t.Fatal(err)
				}
				var tables map[string][]json.RawMessage
				if err := json.Unmarshal(raw, &tables); err != nil {
					t.Fatal(err)
				}
				if len(tables["devices"]) != 4 || len(tables["legacy"]) != 1 || len(tables["canonical"]) != 3 {
					t.Fatal("unexpected fixture row counts")
				}
				return raw
			}
			before := snapshot()
			admin := scenariocatalog.Principal{Class: "acting_admin"}
			get := func(suffix string, query map[string]string, p scenariocatalog.Principal, expect scenariocatalog.Expect) response {
				t.Helper()
				requests++
				resp, failures, err := e.exchange(e.live.URL, http.MethodGet, scenariocatalog.Request{Path: "/api/v2/admin/devices" + suffix, Query: query}, p, expect, nil, nil, nil)
				if err != nil {
					failures = append(failures, err.Error())
				}
				result.Failures = append(result.Failures, failures...)
				if len(failures) > 0 {
					t.Fatalf("device GET: %v", failures)
				}
				return resp
			}
			memberPath := fmt.Sprintf("/%d/%s", member, deviceIDA)
			switch id {
			case "merged_overrides":
				get("", nil, admin, catalogOK(catalogAssertion("/items", "length", 4), catalogAssertion("/items/0/user_id", "equals", strconv.Itoa(member)), catalogAssertion("/items/0/device_id", "equals", deviceIDA), catalogAssertion("/items/0/override_count", "equals", 3), catalogAssertion("/items/0/profile_count", "equals", 2), catalogAssertion("/items/0/last_updated", "equals", "2026-01-03T03:04:05.000Z")))
				get(memberPath, nil, admin, catalogOK(catalogAssertion("/user_id", "equals", strconv.Itoa(member)), catalogAssertion("/override_count", "equals", 3), catalogAssertion("/profiles/0/profile_id", "equals", profilePrimary), catalogAssertion("/profiles/0/override_count", "equals", 2), catalogAssertion("/profiles/1/profile_id", "equals", profileSecondary), catalogAssertion("/profiles/1/override_count", "equals", 1), catalogAssertion("/settings", "length", 1), catalogAssertion("/settings/0/key", "equals", "subtitle_appearance"), catalogAssertion("/settings/0/value", "equals", `{"fontSize":"large"}`)))
			case "shared_account_identity":
				get(fmt.Sprintf("/%d/%s", adminID, deviceIDA), nil, admin, catalogOK(catalogAssertion("/user_id", "equals", strconv.Itoa(adminID)), catalogAssertion("/device_name", "equals", "Other account TV"), catalogAssertion("/profile_count", "equals", 1), catalogAssertion("/profiles/0/profile_id", "equals", profileAdminPrimary), catalogAssertion("/override_count", "equals", 0), catalogAssertion("/settings", "equals", []any{})))
			case "pagination":
				first := get("", map[string]string{"limit": "2"}, admin, catalogOK(catalogAssertion("/items", "length", 2), catalogAssertion("/items/0/user_id", "equals", strconv.Itoa(member)), catalogAssertion("/items/1/user_id", "equals", strconv.Itoa(adminID)), catalogAssertion("/items/1/device_id", "equals", deviceIDA), catalogAssertion("/page/has_more", "equals", true)))
				var body struct {
					Page struct {
						Next string `json:"next_cursor"`
					}
				}
				if err := json.Unmarshal(first.Raw, &body); err != nil {
					t.Fatal(err)
				}
				if body.Page.Next == "" {
					t.Fatal("missing continuation")
				}
				get("", map[string]string{"limit": "2", "cursor": body.Page.Next}, admin, catalogOK(catalogAssertion("/items", "length", 2), catalogAssertion("/items/0/device_id", "equals", "fixture-device-c"), catalogAssertion("/items/1/device_id", "equals", deviceIDB), catalogAssertion("/page/has_more", "equals", false), catalogAssertion("/page/next_cursor", "absent", nil)))
				get("", map[string]string{"limit": "3", "cursor": body.Page.Next}, admin, catalogProblem(400, "invalid_cursor"))
			case "missing_identity":
				get(fmt.Sprintf("/%d/%s", adminID, deviceIDB), nil, admin, catalogProblem(404, "not_found"))
				get("/0/"+deviceIDA, nil, admin, catalogProblem(422, "validation_failed"))
			case "authority":
				for _, p := range []scenariocatalog.Principal{{Class: "primary_profile"}, {Class: "acting_admin", Profile: "admin_secondary"}} {
					get(memberPath, nil, p, catalogProblem(403, "permission_denied"))
				}
				get("", nil, scenariocatalog.Principal{Class: "public"}, catalogProblem(401, "authentication_required"))
			default:
				t.Fatal("unimplemented case")
			}
			if !bytes.Equal(before, snapshot()) {
				t.Fatal("administrator read changed device or override rows")
			}
		})
	}
	if len(results) != 5 || requests != 11 || effects != 10 {
		t.Errorf("NEW administrator device evidence%d/%d/%d,want5/11/10", len(results), requests, effects)
	}
	seen := map[string]bool{}
	for _, r := range results {
		if seen[r.ID] || !r.Passed() {
			t.Errorf("duplicate or failed result%s: %v", r.ID, r.Failures)
		}
		seen[r.ID] = true
	}
	if path := os.Getenv("SILO_SCENARIO_REPORT"); path != "" {
		report := struct {
			Scope                                       string
			NewScenarios, PhysicalRequests, EffectReads int
			Results                                     []Result
		}{"NEW administrator device reads (outside frozen 598)", len(results), requests, effects, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
