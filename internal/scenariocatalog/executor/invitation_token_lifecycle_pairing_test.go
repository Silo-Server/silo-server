package executor

import (
	"bytes"
	"encoding/json"
	"os"

	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredInvitationTokenLifecycleAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-invitation-token-lifecycle for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.InvitationTokenLifecycleAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect the reserved database before even constructing the offline router.
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect invitation token lifecycle scratch database")
	}
	defer pool.Close()
	probe := &Env{t: t, ctx: t.Context(), pool: pool}
	probe.guardScratchDatabase()
	pool.Close()
	guardFrozenAPIKeyFixture(t)
	t.Log("invitation token lifecycle pre-constructor guards passed")
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
							if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM user_profiles p),'keys',(SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM api_keys k),'settings',(SELECT jsonb_agg(to_jsonb(t) ORDER BY key) FROM server_settings t),'sessions',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM auth_sessions t),'device_requests',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM device_login_requests t),'invitations',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM invitations t),'invite_codes',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM invite_codes t),'devices',(SELECT jsonb_agg(to_jsonb(t) ORDER BY user_id,device_id) FROM user_devices t),'profile_libraries',(SELECT jsonb_agg(to_jsonb(t) ORDER BY user_id,profile_id,library_id) FROM user_profile_allowed_libraries t),'groups',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM access_groups t))`).Scan(&raw); err != nil {
								t.Fatal(err)
							}
							var rows map[string]json.RawMessage
							if err := json.Unmarshal(raw, &rows); err != nil {
								t.Fatal(err)
							}
							return rows
						}
						var smtpRows int
						if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM server_settings WHERE key LIKE 'email.%'`).Scan(&smtpRows); err != nil || smtpRows != 0 {
							t.Fatal("synthetic lifecycle requires absent SMTP configuration")
						}
						applicationStarted := time.Now()
						var started time.Time
						if err := e.pool.QueryRow(e.ctx, `SELECT clock_timestamp()`).Scan(&started); err != nil {
							t.Fatal(err)
						}
						var beforeSequence int64
						if err := e.pool.QueryRow(e.ctx, `SELECT last_value FROM users_id_seq`).Scan(&beforeSequence); err != nil {
							t.Fatal(err)
						}
						before := snapshot()
						if len(before) != 11 {
							t.Fatal("snapshot must contain eleven full tables")
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
						var middle map[string]json.RawMessage
						var accepted response
						if request.Repeat == 2 {
							single := request
							single.Repeat = 1
							first, firstFailures, firstErr := e.exchange(e.live.URL, method, single, principal, scenariocatalog.Expect{Status: 201}, nil, nil, nil)
							accepted = first
							if firstErr != nil || len(firstFailures) > 0 {
								t.Fatal("first acceptance exchange failed", firstErr, firstFailures)
							}
							middle = snapshot()
							request.Repeat = 1
						}
						resp, failures, err := e.exchange(e.live.URL, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("frozen exchange: %v", failures)
						}
						after := snapshot()
						var afterSequence int64
						if err := e.pool.QueryRow(e.ctx, `SELECT last_value FROM users_id_seq`).Scan(&afterSequence); err != nil {
							t.Fatal(err)
						}
						delta := int64(0)
						if invitationTokenCreates(s.ID) {
							delta = 1
						}
						if afterSequence != beforeSequence+delta {
							t.Error("unexpected account sequence effect")
						}

						for key, value := range middle {
							if !bytes.Equal(value, after[key]) {
								t.Errorf("second acceptance changed %s", key)
							}
						}
						var finished time.Time
						if err := e.pool.QueryRow(e.ctx, `SELECT clock_timestamp()`).Scan(&finished); err != nil {
							t.Fatal(err)
						}
						if accepted.Status == 0 {
							accepted = resp
						}
						assertInvitationTokenEffects(t, e, s.ID, accepted, before, after, started, finished, applicationStarted, time.Now())
						if len(before) != len(after) {
							t.Error("unexpected snapshot table count")
						}
						for id, want := range before {
							if !invitationTokenChangedTable(s.ID, id) && !bytes.Equal(want, after[id]) {
								t.Error("account/profile/API-key/settings/session/device-request/invitation/code rows changed during invitation lifecycle")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredInvitationTokenLifecycleScenarios); err != nil {
		t.Error(err)
	}
	if requests != 30 || effects != 58 {
		t.Errorf("paired invitation token lifecycle evidence %dHTTP/%dPG, want30/58", requests, effects)
	}
	t.Logf("invitation token lifecycles: %d results, %d HTTP, %d snapshots, %d table observations", len(results), requests, effects, effects*11)
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}
