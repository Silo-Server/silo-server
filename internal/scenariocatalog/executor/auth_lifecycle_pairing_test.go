package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredAuthLifecycleAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-auth-lifecycle for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.AuthLifecycleAcceptance(catalogs)
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
							if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM user_profiles p),'keys',(SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM api_keys k),'settings',(SELECT jsonb_agg(to_jsonb(s) ORDER BY key) FROM server_settings s),'sessions',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM auth_sessions a),'device_requests',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM device_login_requests d),'invitations',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invitations i),'invite_codes',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM invite_codes c))`).Scan(&raw); err != nil {
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
						before := snapshot()
						dbLower, wallLower := dbNow(), time.Now()
						if len(before) != 8 {
							t.Fatal("snapshot must contain eight full tables")
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
						reply, failures, err := e.exchange(e.live.URL, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("frozen exchange: %v", failures)
						}
						wallUpper, dbUpper := time.Now(), dbNow()
						after := snapshot()
						e.checkAuthLifecycleEffects(t, s.ID, transport, reply, before, after, wallLower, wallUpper, dbLower, dbUpper)
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
	if err := requiredPairedResults(results, scenariocatalog.RequiredAuthLifecycleScenarios); err != nil {
		t.Error(err)
	}
	if requests != 22 || effects != 40 {
		t.Errorf("paired auth lifecycle evidence %dHTTP/%dPG, want22/40", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// Normalize only explicitly proved effects before the caller compares every table.
func (e *Env) checkAuthLifecycleEffects(t *testing.T, id, transport string, reply response, before, after map[string]json.RawMessage, wallLower, wallUpper, dbLower, dbUpper time.Time) {
	t.Helper()
	decode := func(raw json.RawMessage) map[string]map[string]json.RawMessage {
		t.Helper()
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]json.RawMessage{}
		for _, row := range rows {
			var key string
			if err := json.Unmarshal(row["id"], &key); err != nil {
				t.Fatal(err)
			}
			if out[key] != nil {
				t.Fatal("duplicate session identity")
			}
			out[key] = row
		}
		return out
	}
	prior, current := decode(before["sessions"]), decode(after["sessions"])
	timestamp := func(row map[string]json.RawMessage, key string, lower, upper time.Time) {
		t.Helper()
		var got time.Time
		if err := json.Unmarshal(row[key], &got); err != nil {
			t.Fatal("invalid session timestamp")
		}
		if got.Before(lower) || got.After(upper) {
			t.Fatalf("%s outside request clock bounds", key)
		}
	}
	login := strings.HasPrefix(id, "login.")
	rejected := id == "login.unknown_fields_ignored" && transport == "v2"
	if login && !rejected || strings.HasPrefix(id, "refresh.") {
		var tokens struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
			Expires int    `json:"expires_in"`
		}
		if err := json.Unmarshal(reply.Raw, &tokens); err != nil {
			t.Fatal("invalid credential response")
		}
		access, err := e.jwt.ValidateToken(tokens.Access)
		if err != nil {
			t.Fatal("access signature invalid")
		}
		refresh, err := e.jwt.ValidateToken(tokens.Refresh)
		if err != nil {
			t.Fatal("refresh signature invalid")
		}
		user := e.users[fixtureMember]
		if id == "login.grouped_download_policy" {
			user = e.users[fixtureGrouped]
		}
		if id == "login.admin_permissions" {
			user = e.users[fixtureAdmin]
		}
		for _, claims := range []*auth.Claims{access, refresh} {
			if claims.UserID != user.ID || claims.Role != user.Role || claims.SessionID == "" || claims.ProfileID != "" || claims.ImpersonatorUserID != nil {
				t.Fatal("credential authority mismatch")
			}
		}
		if access.TokenType != auth.TokenTypeAccess || refresh.TokenType != auth.TokenTypeRefresh || access.SessionID != refresh.SessionID || tokens.Expires != 28800 {
			t.Fatal("credential kind/session/TTL mismatch")
		}
		if access.ExpiresAt == nil || refresh.ExpiresAt == nil || access.IssuedAt == nil || refresh.IssuedAt == nil {
			t.Fatal("credential times absent")
		}
		if access.ExpiresAt.Sub(access.IssuedAt.Time) != 8*time.Hour || refresh.ExpiresAt.Sub(refresh.IssuedAt.Time) != e.jwt.RefreshExpiry() {
			t.Fatal("credential lifetime mismatch")
		}
		target := access.SessionID
		row := current[target]
		if row == nil {
			t.Fatal("credential session absent")
		}
		if login {
			if prior[target] != nil || len(current) != len(prior)+1 {
				t.Fatal("login must add exactly one fresh session")
			}
			if len(row) != 9 {
				t.Fatal("unexpected new session field inventory")
			}
			expected := map[string]any{"id": target, "user_id": user.ID, "device_name": "silo-scenario-executor/1", "ip_address": "127.0.0.1", "revoked_at": nil, "impersonator_user_id": nil, "impersonation_started_at": nil}
			for key, value := range expected {
				want, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(row[key], want) {
					t.Errorf("new session %s mismatch", key)
				}
			}
			timestamp(row, "created_at", dbLower, dbUpper)
			timestamp(row, "expires_at", wallLower.Add(e.jwt.RefreshExpiry()), wallUpper.Add(e.jwt.RefreshExpiry()))
			delete(current, target)
		} else {
			if target != e.sessions[fixtureMember] || len(prior) != len(current) || prior[target] == nil {
				t.Fatal("refresh changed session identity")
			}
			timestamp(row, "expires_at", wallLower.Add(e.jwt.RefreshExpiry()), wallUpper.Add(e.jwt.RefreshExpiry()))
			var old, timeNow time.Time
			if err := json.Unmarshal(prior[target]["expires_at"], &old); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(row["expires_at"], &timeNow); err != nil {
				t.Fatal(err)
			}
			if !timeNow.After(old) {
				t.Fatal("refresh did not extend fixture expiry")
			}
			prior[target]["expires_at"] = row["expires_at"]
		}
	} else if id == "logout.session_gone" {
		target := e.sessions[fixtureMember]
		if prior[target] == nil || current[target] == nil || len(prior) != len(current) {
			t.Fatal("logout changed session inventory")
		}
		if string(prior[target]["revoked_at"]) != "null" {
			t.Fatal("logout fixture already revoked")
		}
		timestamp(current[target], "revoked_at", dbLower, dbUpper)
		prior[target]["revoked_at"] = current[target]["revoked_at"]
	}
	var err error
	before["sessions"], err = json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	after["sessions"], err = json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
}
