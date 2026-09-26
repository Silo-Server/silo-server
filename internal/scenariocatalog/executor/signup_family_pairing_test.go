package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredSignupFamilyAcceptance(t *testing.T) { runSignupFamilyAcceptance(t, false) }

// Focused recovery runs the six original success cases when only their effect
// checker changed. The default family target still requires all fourteen cases.
func TestRequiredSignupSuccessAcceptance(t *testing.T) { runSignupFamilyAcceptance(t, true) }

func runSignupFamilyAcceptance(t *testing.T, successesOnly bool) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-signup-family for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.SignupFamilyAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect the reserved database before even constructing the offline router.
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect signup family scratch database")
	}
	defer pool.Close()
	probe := &Env{t: t, ctx: t.Context(), pool: pool}
	probe.guardScratchDatabase()
	pool.Close()
	guardFrozenAPIKeyFixture(t)
	t.Log("signup family pre-constructor guards passed")
	e := New(t)
	defer func() { e.Reseed(); e.guardScratchDatabase(); guardFrozenAPIKeyFixture(t) }()
	required := scenariocatalog.RequiredSignupFamilyScenarios
	wantRequests, wantEffects := 40, 80
	if successesOnly {
		required = []string{"signup.ok", "signup.user_meaning", "signup.field_shape", "signup.ok.r1", "signup.user_meaning.r1", "signup.field_shape.r1"}
		wantRequests, wantEffects = 12, 24
	}
	var results []Result
	requests, effects := 0, 0
	for _, c := range selected {
		for _, row := range c.Rows {
			for _, s := range row.Scenarios {
				if successesOnly && s.Expect.Status != 201 {
					continue
				}
				for _, transport := range []string{"v1", "v2"} {
					t.Run(s.ID+"/"+transport, func(t *testing.T) {
						e.Reseed()
						defer e.Reseed()
						result := Result{ID: s.ID + "/" + transport, Scenario: s.ID, Transport: transport, Catalog: c.File, Row: row.Key().String()}
						defer func() {
							if t.Failed() && len(result.Failures) == 0 {
								result.Failures = append(result.Failures, "scenario assertion failed; see log")
							}
							results = append(results, result)
						}()
						if len(s.Settings) > 0 {
							defer e.applySettings(s.Settings)()
						}
						server := e.live
						if row.RegistrationIndex == 1 {
							if !e.rowRateLimited(row) {
								t.Fatal("original registration must be rate limited")
							}
							server = e.liveLimited
							e.resetRateLimits()
							defer e.resetRateLimits()
							cfg, err := ratelimit.LoadConfig(e.ctx, e.settings)
							if err != nil {
								t.Fatal(err)
							}
							ep := cfg.AuthEndpoints["signup"]
							if !cfg.Enabled || ep.Burst != 6 || ep.RequestsPerMinute != 10 {
								t.Fatal("original signup rate configuration changed")
							}
						} else if e.rowRateLimited(row) {
							t.Fatal("registration0 unexpectedly rate limited")
						}
						snapshot := func() map[string]json.RawMessage {
							t.Helper()
							effects++
							out := map[string]json.RawMessage{}
							for _, table := range signupEffectTables {
								var raw []byte
								if err := e.pool.QueryRow(e.ctx, fmt.Sprintf("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM %s t", table)).Scan(&raw); err != nil {
									t.Fatal(err)
								}
								out[table] = raw
							}
							return out
						}
						dbNow := func() time.Time {
							t.Helper()
							var now time.Time
							if err := e.pool.QueryRow(e.ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
								t.Fatal(err)
							}
							return now
						}
						request, expect, method := s.Request, s.Expect, row.Method
						if transport == "v2" {
							p := s.V2Expectation
							request, expect, method = p.Request, p.Expect, p.Method
							result.OperationID = p.OperationID
						}
						burstStart := time.Now()
						for repetition := range max(request.Repeat, 1) {
							before := snapshot()
							dbStart, appStart := dbNow(), time.Now()
							req, err := e.buildRequest(server.URL, method, request, s.Principal)
							if err != nil {
								t.Fatal("construct signup request")
							}
							requests++
							resp, err := send(req)
							if err != nil {
								t.Fatal("signup transport failed")
							}
							appEnd, dbEnd := time.Now(), dbNow()
							after := snapshot()
							expected := expect
							rateCase := s.ID == "signup.rate_limited.r1"
							if rateCase && repetition < 6 {
								status := 400
								if transport == "v2" {
									status = 422
								}
								expected = scenariocatalog.Expect{Status: status}
							}
							resolved, err := e.substituteExpect(expected)
							if err != nil {
								t.Fatal(err)
							}
							if failures := check(resolved, resp); len(failures) > 0 {
								result.Failures = append(result.Failures, "signup response assertion failed; credential body withheld")
								t.Errorf("signup repetition%d status%d oracle failed (credential body withheld)", repetition, resp.Status)
							}
							if transport == "v2" && (strings.TrimSuffix(s.ID, ".r1") == "signup.duplicate" || (rateCase && repetition == 6)) {
								// Catalogs use the established problem suffix convention. Keep the
								// full origin check here through the accepted shared problem catalog.
								var problem struct {
									Type string `json:"type"`
								}
								if err := json.Unmarshal(resp.Raw, &problem); err != nil {
									t.Fatal("problem JSON invalid")
								}
								want := apiv2.TypeConflict.URI()
								if rateCase {
									want = apiv2.TypeRateLimited.URI()
								}
								if problem.Type != want {
									t.Error("signup problem type differs from accepted contract")
								}
							}
							if rateCase {
								var body map[string]any
								if err := json.Unmarshal(resp.Raw, &body); err != nil {
									t.Fatal("rate sequence JSON invalid")
								}
								if repetition < 6 {
									if transport == "v1" {
										if body["error"] != "bad_request" {
											t.Error("admitted burst must reach original missing-fields refusal")
										}
									} else if kind, _ := body["type"].(string); !strings.HasSuffix(kind, "/validation_failed") {
										t.Error("admitted burst must reach v2 validation refusal")
									}
								} else {
									if appEnd.Sub(burstStart) >= 6*time.Second {
										t.Error("burst crossed actual token refill interval")
									}
									retry, err := strconv.Atoi(resp.Headers.Get("Retry-After"))
									if err != nil || retry < 1 || retry > 6 {
										t.Error("retry header outside real refill bound")
									}
									if transport == "v1" {
										if body["retry_after"] != float64(retry) {
											t.Error("retry body/header mismatch")
										}
										reset, err := strconv.ParseInt(resp.Headers.Get("X-RateLimit-Reset"), 10, 64)
										if err != nil || reset < appStart.Unix() || reset > appEnd.Add(time.Second).Unix() {
											t.Error("reset outside limiter clock bounds")
										}
									}
								}
							}
							success := s.Expect.Status == 201
							if success {
								assertSignupEffects(t, e, s.ID, transport, resp, before, after, dbStart, dbEnd, appStart, appEnd)
							}
							for table, want := range before {
								if success && (table == "users" || table == "user_profiles" || table == "auth_sessions" || table == "invite_codes") {
									continue
								}
								if !bytes.Equal(want, after[table]) {
									t.Errorf("unexpected signup table effect: %s", table)
								}
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, required); err != nil {
		t.Error(err)
	}
	if requests != wantRequests || effects != wantEffects {
		t.Errorf("signup evidence %dHTTP/%dPG want%d/%d", requests, effects, wantRequests, wantEffects)
	}
	t.Logf("signup family: %d results, %d HTTP, %d full snapshots, %d table observations", len(results), requests, effects, effects*len(signupEffectTables))
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

var signupEffectTables = append(append([]string{}, householdEffectTables...), "user_setting_mutations", "user_setting_migration_rejects", "user_profile_allowed_libraries", "user_settings", "user_profile_onboarding", "request_settings", "access_groups")
