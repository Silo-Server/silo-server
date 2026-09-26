package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredInvitationR1Acceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-invitation-r1 for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.InvitationR1Acceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect guarded invitation r1 database")
	}
	probe := &Env{t: t, ctx: t.Context(), pool: pool}
	probe.guardScratchDatabase()
	pool.Close()
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
						if row.RegistrationIndex != 1 || !e.rowRateLimited(row) {
							t.Fatal("r1 must use the actual rate-limited registration")
						}
						e.resetRateLimits()
						config, err := ratelimit.LoadConfig(e.ctx, e.settings)
						if err != nil {
							t.Fatal(err)
						}
						ep := config.AuthEndpoints["invitation"]
						if !config.Enabled || ep.Burst != 10 || ep.RequestsPerMinute != 20 {
							t.Fatal("original invitation rate config changed")
						}
						defer e.resetRateLimits()
						var smtpRows int
						if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM server_settings WHERE key LIKE 'email.%'`).Scan(&smtpRows); err != nil || smtpRows != 0 {
							t.Fatal("requires absent SMTP configuration")
						}
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
							if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM users x),'profiles',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_profiles x),'keys',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM api_keys x),'settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM server_settings x),'sessions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM auth_sessions x),'device_requests',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM device_login_requests x),'invitations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM invitations x),'invite_codes',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM invite_codes x),'devices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_devices x),'legacy_device_settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_device_settings x),'canonical',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_setting_values x),'mutations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_setting_mutations x),'migration_rejects',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_setting_migration_rejects x),'legacy_settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_settings x),'profile_libraries',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM user_profile_allowed_libraries x),'groups',(SELECT jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text) FROM access_groups x))`).Scan(&raw); err != nil {
								t.Fatal(err)
							}
							var rows map[string]json.RawMessage
							if err := json.Unmarshal(raw, &rows); err != nil {
								t.Fatal(err)
							}
							return rows
						}
						dbNow := func() time.Time {
							t.Helper()
							var now time.Time
							if err := e.pool.QueryRow(e.ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
								t.Fatal(err)
							}
							return now
						}
						request, expect, principal, method := s.Request, s.Expect, s.Principal, row.Method
						if transport == "v2" {
							p := s.V2Expectation
							request, expect, method = p.Request, p.Expect, p.Method
							result.OperationID = p.OperationID
						}
						start := time.Now()
						for repetition := range max(request.Repeat, 1) {
							before := snapshot()
							if len(before) != 16 {
								t.Fatal("expected sixteen tables")
							}
							var beforeSequence int64
							if err := e.pool.QueryRow(e.ctx, "SELECT last_value FROM users_id_seq").Scan(&beforeSequence); err != nil {
								t.Fatal(err)
							}
							dbLower, wallLower := dbNow(), time.Now()
							req, err := e.buildRequest(e.liveLimited.URL, method, request, principal)
							if err != nil {
								t.Fatal("cannot construct original invitation request")
							}
							requests++
							reply, err := send(req)
							if err != nil {
								t.Fatal("login request transport failed")
							}
							wallUpper, dbUpper := time.Now(), dbNow()
							after := snapshot()
							expected := expect
							rateCase := strings.Contains(s.ID, ".rate_limited.")
							if s.ID == "inv_accept.single_use.r1" && repetition == 0 {
								expected = scenariocatalog.Expect{Status: 201}
							}
							if rateCase && repetition < 10 {
								expected = scenariocatalog.Expect{Status: 404}
							}
							resolved, err := e.substituteExpect(expected)
							if err != nil {
								t.Fatal(err)
							}
							failures := check(resolved, reply)
							if len(failures) > 0 {
								result.Failures = append(result.Failures, "r1 response assertion failed; credential body withheld")
								t.Errorf("r1 response repetition%d failed (credential body withheld)", repetition)
							}
							if rateCase {
								var doc map[string]any
								if err := json.Unmarshal(reply.Raw, &doc); err != nil {
									t.Fatal("rate sequence JSON invalid")
								}
								if repetition < 10 {
									if transport == "v1" {
										if doc["error"] != "not_found" {
											t.Fatal("burst admission did not reach token lookup refusal")
										}
									} else if kind, _ := doc["type"].(string); !strings.HasSuffix(kind, "/not_found") {
										t.Fatal("burst admission did not reach v2 token lookup refusal")
									}
								} else {
									if wallUpper.Sub(start) >= 3*time.Second {
										t.Fatal("burst crossed first token replenishment interval; no exhaustion proof")
									}
									retry, err := strconv.Atoi(reply.Headers.Get("Retry-After"))
									if err != nil || retry < 1 || retry > 3 {
										t.Fatal("retry interval outside original invitation refill bounds")
									}
									if transport == "v1" && doc["retry_after"] != float64(retry) {
										t.Fatal("retry header/body mismatch")
									}
									if transport == "v1" {
										reset, err := strconv.ParseInt(reply.Headers.Get("X-RateLimit-Reset"), 10, 64)
										if err != nil || reset < wallLower.Unix() || reset > wallUpper.Add(time.Second).Unix() {
											t.Fatal("reset outside limiter clock bounds")
										}
									}

								}
							}
							baseID := strings.TrimSuffix(s.ID, ".r1")
							creates := invitationR1TokenCreates(baseID) && repetition == 0
							var afterSequence int64
							if err := e.pool.QueryRow(e.ctx, "SELECT last_value FROM users_id_seq").Scan(&afterSequence); err != nil {
								t.Fatal(err)
							}
							delta := int64(0)
							if creates {
								delta = 1
							}
							if afterSequence != beforeSequence+delta {
								t.Fatal("unexpected invitation account sequence allocation")
							}
							if creates {
								assertInvitationR1Effects(t, e, baseID, reply, before, after, dbLower, dbUpper, wallLower, wallUpper)
							}

							for table, want := range before {
								if (!creates || !invitationR1TokenChangedTable(baseID, table)) && !bytes.Equal(want, after[table]) {
									t.Errorf("repetition%d unexpected stored change in %s", repetition, table)
								}
							}
						}

					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredInvitationR1Scenarios); err != nil {
		t.Error(err)
	}
	if requests != 74 || effects != 148 {
		t.Errorf("paired invitation r1 evidence %dHTTP/%dPG, want74/148", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}
