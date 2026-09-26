package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredDeviceDecisionsAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-device-decisions")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.DeviceDecisionsAcceptance(catalogs)
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
						e.Reseed()
						defer e.Reseed()
						result := Result{ID: s.ID + "/" + transport, Scenario: s.ID, Transport: transport, Catalog: c.File, Row: row.Key().String()}
						defer func() {
							if t.Failed() && len(result.Failures) == 0 {
								result.Failures = append(result.Failures, "scenario assertion failed; see test log")
							}
							results = append(results, result)
						}()
						principal := s.Principal
						steps := []scenariocatalog.Step{{Method: row.Method, Request: s.Request, Expect: s.Expect}}
						if transport == "v1" {
							steps = append(steps, s.Then...)
						} else {
							pair := s.V2Expectation
							result.OperationID = pair.OperationID
							steps = []scenariocatalog.Step{{Method: pair.Method, Request: pair.Request, Expect: pair.Expect}}
							for _, step := range pair.Then {
								steps = append(steps, step.Step)
							}
						}
						snapshot := func() map[string]json.RawMessage {
							t.Helper()
							effects++
							var raw []byte
							if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM user_profiles p),'keys',(SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM api_keys k),'settings',(SELECT jsonb_agg(to_jsonb(s) ORDER BY key) FROM server_settings s),'sessions',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM auth_sessions a),'device_requests',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM device_login_requests d),'invitations',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invitations i),'invite_codes',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM invite_codes c))`).Scan(&raw); err != nil {
								t.Fatal(err)
							}
							var out map[string]json.RawMessage
							if err := json.Unmarshal(raw, &out); err != nil {
								t.Fatal(err)
							}
							if len(out) != 8 {
								t.Fatal("expected eight full tables")
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
						for i, step := range steps {
							who := principal
							if step.Principal != nil {
								who = *step.Principal
							}
							for repetition := range max(step.Request.Repeat, 1) {
								before := snapshot()
								dbLower, wallLower := dbNow(), time.Now()
								req, err := e.buildRequest(e.live.URL, step.Method, step.Request, who)
								if err != nil {
									t.Fatal("cannot construct original device request")
								}
								requests++
								reply, err := send(req)
								wallUpper, dbUpper := time.Now(), dbNow()
								after := snapshot()
								if err != nil {
									t.Fatal("device request transport failed")
								}
								expect := step.Expect
								// The original repeat oracle applies to the final exchange. Capture
								// the first real credential issuance separately, without changing it.
								if s.ID == "device_poll.consumed" && repetition == 0 {
									expect = scenariocatalog.Expect{Status: 200}
								}
								resolved, err := e.substituteExpect(expect)
								if err != nil {
									t.Fatal(err)
								}
								failures := check(resolved, reply)
								if len(failures) > 0 {
									// Credential responses must never enter logs or reports.
									result.Failures = append(result.Failures, "original/paired response assertion failed")
									t.Errorf("step %d repetition %d response assertions failed (credential body withheld)", i, repetition)
								}
								e.checkDeviceDecisionEffects(t, step.Request, reply, transport, before, after, wallLower, wallUpper, dbLower, dbUpper)
								for table, want := range before {
									if !bytes.Equal(want, after[table]) {
										t.Errorf("step %d repetition %d unexpected stored change in %s", i, repetition, table)
									}
								}
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredDeviceDecisionsScenarios); err != nil {
		t.Error(err)
	}
	if requests != 34 || effects != 68 {
		t.Errorf("evidence %dHTTP/%d snapshots, want34/68", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// Only proven columns are normalized; the caller compares all eight complete tables.
func (e *Env) checkDeviceDecisionEffects(t *testing.T, request scenariocatalog.Request, reply response, transport string, before, after map[string]json.RawMessage, wallLower, wallUpper, dbLower, dbUpper time.Time) {
	t.Helper()
	decode := func(raw json.RawMessage) map[string]map[string]any {
		t.Helper()
		var rows []map[string]any
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]any{}
		for _, r := range rows {
			id, ok := r["id"].(string)
			if !ok || out[id] != nil {
				t.Fatal("invalid row identity")
			}
			out[id] = r
		}
		return out
	}
	encode := func(v any) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	bound := func(row map[string]any, key string, lower, upper time.Time) time.Time {
		t.Helper()
		raw, ok := row[key].(string)
		if !ok {
			t.Fatalf("missing %s", key)
		}
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil || at.Before(lower) || at.After(upper) {
			t.Fatalf("%s outside exact request clock bounds", key)
		}
		return at
	}
	prior, current := decode(before["device_requests"]), decode(after["device_requests"])
	target := "00000000-0000-4000-8000-0000000000e1"
	body := string(request.Body)
	if strings.Contains(body, "remote_approved") {
		target = "00000000-0000-4000-8000-0000000000e6"
	} else if strings.Contains(body, "_approved") {
		target = "00000000-0000-4000-8000-0000000000e5"
	} else if strings.Contains(body, "_denied") {
		target = "00000000-0000-4000-8000-0000000000e3"
	}
	old, now := prior[target], current[target]
	if old == nil || now == nil || len(prior) != len(current) {
		t.Fatal("device row inventory changed")
	}
	var doc map[string]any
	if err := json.Unmarshal(reply.Raw, &doc); err != nil {
		t.Fatal("device JSON invalid")
	}
	set := func(key string, value any) {
		t.Helper()
		if !reflect.DeepEqual(now[key], value) {
			t.Fatalf("unexpected device %s", key)
		}
		old[key] = value
	}
	timestamp := func(key string) { t.Helper(); bound(now, key, wallLower, wallUpper); old[key] = now[key] }
	switch {
	case strings.HasSuffix(request.Path, "/approve"):
		if old["status"] == "pending" {
			set("status", "approved")
			set("approved_by_user_id", float64(e.users[fixtureMember].ID))
			set("denied_at", nil)
			timestamp("approved_at")
			timestamp("updated_at")
			if now["approved_at"] != now["updated_at"] {
				t.Fatal("approval timestamps differ")
			}
		} else if old["status"] != "approved" {
			t.Fatal("invalid approval fixture")
		}
		if doc["status"] != "approved" {
			t.Fatal("approval not observed")
		}
	case strings.HasSuffix(request.Path, "/deny"):
		if old["status"] != "denied" {
			if old["status"] != "pending" && old["status"] != "approved" {
				t.Fatal("invalid denial fixture")
			}
			set("status", "denied")
			set("approved_by_user_id", nil)
			set("approved_profile_id", nil)
			set("approved_at", nil)
			timestamp("denied_at")
			timestamp("updated_at")
			if now["denied_at"] != now["updated_at"] {
				t.Fatal("denial timestamps differ")
			}
		}
		if doc["status"] != "denied" {
			t.Fatal("denial not observed")
		}
	case strings.HasSuffix(request.Path, "/poll"):
		if old["status"] == "approved" {
			if doc["status"] != "approved" {
				t.Fatal("first poll did not issue approved credentials")
			}
			tokenDoc := doc
			if transport == "v2" {
				var ok bool
				tokenDoc, ok = doc["tokens"].(map[string]any)
				if !ok {
					t.Fatal("token envelope absent")
				}
			}
			accessText, _ := tokenDoc["access_token"].(string)
			refreshText, _ := tokenDoc["refresh_token"].(string)
			access, err := e.jwt.ValidateToken(accessText)
			if err != nil {
				t.Fatal("access signature invalid")
			}
			refresh, err := e.jwt.ValidateToken(refreshText)
			if err != nil {
				t.Fatal("refresh signature invalid")
			}
			user := e.users[fixtureMember]
			for _, c := range []*auth.Claims{access, refresh} {
				if c.UserID != user.ID || c.Role != user.Role || c.SessionID == "" || c.ProfileID != "" || c.ImpersonatorUserID != nil {
					t.Fatal("issued account authority mismatch")
				}
				if c.IssuedAt == nil || c.ExpiresAt == nil || c.IssuedAt.Before(wallLower.Truncate(time.Second)) || c.IssuedAt.After(wallUpper) {
					t.Fatal("credential issuance time invalid")
				}
			}
			if access.TokenType != auth.TokenTypeAccess || refresh.TokenType != auth.TokenTypeRefresh || access.SessionID != refresh.SessionID || tokenDoc["expires_in"] != float64(28800) || access.ExpiresAt.Sub(access.IssuedAt.Time) != 8*time.Hour || refresh.ExpiresAt.Sub(refresh.IssuedAt.Time) != e.jwt.RefreshExpiry() {
				t.Fatal("credential kind/session/lifetime mismatch")
			}
			if _, err := uuid.Parse(access.SessionID); err != nil {
				t.Fatal("session UUID invalid")
			}
			responseUser, ok := tokenDoc["user"].(map[string]any)
			if !ok || responseUser["username"] != user.Username {
				t.Fatal("response account absent")
			}
			expectedID := any(float64(user.ID))
			if transport == "v2" {
				expectedID = fmt.Sprint(user.ID)
			}
			if responseUser["id"] != expectedID {
				t.Fatal("response account ID mismatch")
			}
			sessionsOld, sessionsNew := decode(before["sessions"]), decode(after["sessions"])
			session := sessionsNew[access.SessionID]
			if sessionsOld[access.SessionID] != nil || session == nil || len(sessionsNew) != len(sessionsOld)+1 || len(session) != 9 {
				t.Fatal("poll must create exactly one nine-field session")
			}
			for key, value := range map[string]any{"id": access.SessionID, "user_id": float64(user.ID), "device_name": old["device_name"], "ip_address": old["ip_address"], "revoked_at": nil, "impersonator_user_id": nil, "impersonation_started_at": nil} {
				if !reflect.DeepEqual(session[key], value) {
					t.Fatalf("new session %s mismatch", key)
				}
			}
			bound(session, "created_at", dbLower, dbUpper)
			ttl := e.jwt.RefreshExpiry()
			remote := old["temporary"] == true
			if remote {
				ttl = min(ttl, 24*time.Hour)
			}
			expiry := bound(session, "expires_at", wallLower.Add(ttl), wallUpper.Add(ttl))
			if remote {
				var currentPolicyRevision int64
				if err := e.pool.QueryRow(e.ctx, "SELECT access_policy_revision FROM users WHERE id=$1", user.ID).Scan(&currentPolicyRevision); err != nil {
					t.Fatal(err)
				}
				token, _ := doc["profile_token"].(string)
				claims, err := e.profileTok.Validate(token)
				if err != nil || claims.UserID != user.ID || claims.SessionID != access.SessionID || claims.ProfileID != profilePrimary || claims.PolicyRevision != currentPolicyRevision || doc["profile_id"] != profilePrimary || doc["temporary"] != true || old["approved_profile_id"] != profilePrimary {
					t.Fatal("temporary profile authority invalid")
				}
				// The fixture is the current account's unlocked primary profile; this does
				// not assert approval of a locked profile without PIN verification.
				var owner int
				var pin string
				if err := e.pool.QueryRow(e.ctx, "SELECT user_id,pin_hash FROM user_profiles WHERE id=$1", profilePrimary).Scan(&owner, &pin); err != nil || owner != user.ID || pin != "" {
					t.Fatal("remote profile fixture authority changed")
				}
				precision := time.Second
				if transport == "v2" {
					precision = time.Millisecond
				}
				at := bound(doc, "session_expires_at", wallLower.Add(ttl).Truncate(precision), wallUpper.Add(ttl).Truncate(precision))
				if !at.Equal(expiry.Truncate(precision)) {
					t.Fatal("wire/database temporary expiry mismatch")
				}

			} else {
				if transport == "v1" {
					for _, key := range []string{"temporary", "profile_id", "profile_token", "session_expires_at"} {
						if _, ok := doc[key]; ok {
							t.Fatal("ordinary poll leaked profile capability")
						}
					}
				} else if doc["temporary"] != false || doc["profile_id"] != "" || doc["profile_token"] != "" {
					t.Fatal("ordinary poll profile defaults invalid")
				}
			}
			delete(sessionsNew, access.SessionID)
			before["sessions"] = encode(sessionsOld)
			after["sessions"] = encode(sessionsNew)
			set("status", "consumed")
			set("auth_session_id", access.SessionID)
			timestamp("consumed_at")
			timestamp("updated_at")
			if now["consumed_at"] != now["updated_at"] {
				t.Fatal("consumption timestamps differ")
			}
		} else {
			if doc["status"] != old["status"] {
				t.Fatal("terminal poll changed status")
			}
			for _, key := range []string{"tokens", "access_token", "refresh_token", "user"} {
				if _, ok := doc[key]; ok {
					t.Fatal("terminal poll emitted credentials")
				}
			}
			if v, ok := doc["profile_token"]; ok && v != "" {
				t.Fatal("terminal poll emitted profile proof")
			}
		}
	default:
		t.Fatal("unexpected device request")
	}
	before["device_requests"] = encode(prior)
	after["device_requests"] = encode(current)
}
