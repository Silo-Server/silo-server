package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

// TestRequiredResourcesHandoffImpersonationAcceptance runs the frozen unsampled
// resource reads, successful remote-playback handoff approvals and impersonation
// end on both transports with full before/after snapshots around every request.
func TestRequiredResourcesHandoffImpersonationAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-resources-handoff-impersonation")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.ResourcesHandoffImpersonationAcceptance(catalogs)
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
						// The impersonation bearer is minted lazily and adds a session row;
						// mint it before the first snapshot so the snapshot pair isolates
						// the request's own effect.
						impersonationSession := ""
						if strings.HasPrefix(s.ID, "imp_end.") {
							token, _ := e.placeholder("impersonation_token")
							claims, err := e.jwt.ValidateToken(token)
							if err != nil || claims.SessionID == "" || claims.ImpersonatorUserID == nil || *claims.ImpersonatorUserID != e.users[fixtureAdmin].ID || claims.UserID != e.users[fixtureMember].ID {
								t.Fatal("impersonation fixture authority invalid")
							}
							impersonationSession = claims.SessionID
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
									t.Fatal("cannot construct original request")
								}
								requests++
								reply, err := send(req)
								wallUpper, dbUpper := time.Now(), dbNow()
								after := snapshot()
								if err != nil {
									t.Fatal("request transport failed")
								}
								expect := step.Expect
								// The original repeat oracle applies to the final exchange; the
								// first impersonation end must still succeed and revoke.
								if s.ID == "imp_end.meaning" && repetition == 0 {
									expect = scenariocatalog.Expect{Status: 204, BodyKind: "empty"}
								}
								resolved, err := e.substituteExpect(expect)
								if err != nil {
									t.Fatal(err)
								}
								if failures := check(resolved, reply); len(failures) > 0 {
									// Credential responses must never enter logs or reports.
									result.Failures = append(result.Failures, "original/paired response assertion failed")
									t.Errorf("step %d repetition %d response assertions failed (credential body withheld)", i, repetition)
								}
								window := clockWindow{wallLower, wallUpper, dbLower, dbUpper}
								switch {
								case strings.HasPrefix(s.ID, "resources."):
									// Reads must not touch stored state; nothing to normalize.
								case strings.HasSuffix(step.Request.Path, "/approve-handoff"):
									e.checkHandoffApproval(t, who, reply, before, after, window)
								case strings.HasSuffix(step.Request.Path, "/poll"):
									e.checkHandoffPoll(t, transport, reply, before, after, window)
								case strings.HasSuffix(step.Request.Path, "/impersonation/end"):
									e.checkImpersonationEnd(t, impersonationSession, reply, before, after, window, repetition == 0)
								default:
									t.Fatal("unexpected request")
								}
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
	if err := requiredPairedResults(results, scenariocatalog.RequiredResourcesHandoffImpersonationScenarios); err != nil {
		t.Error(err)
	}
	if requests != 26 || effects != 52 {
		t.Errorf("evidence %dHTTP/%d snapshots, want26/52", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

type clockWindow struct{ wallLower, wallUpper, dbLower, dbUpper time.Time }

func decodeRowsByID(t *testing.T, raw json.RawMessage) map[string]map[string]any {
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

func encodeRows(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func boundTimestamp(t *testing.T, row map[string]any, key string, lower, upper time.Time) time.Time {
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

const remoteHandoffRequest = "00000000-0000-4000-8000-0000000000e2"

// checkHandoffApproval proves the pending remote-playback request became approved
// for exactly the calling member profile and normalizes only those proven columns.
func (e *Env) checkHandoffApproval(t *testing.T, who scenariocatalog.Principal, reply response, before, after map[string]json.RawMessage, w clockWindow) {
	t.Helper()
	prior, current := decodeRowsByID(t, before["device_requests"]), decodeRowsByID(t, after["device_requests"])
	old, now := prior[remoteHandoffRequest], current[remoteHandoffRequest]
	if old == nil || now == nil || len(prior) != len(current) {
		t.Fatal("device row inventory changed")
	}
	if old["status"] != "pending" || old["client_purpose"] != "remote_playback" || old["temporary"] != true || old["approved_profile_id"] != nil {
		t.Fatal("invalid remote handoff fixture")
	}
	var doc map[string]any
	if err := json.Unmarshal(reply.Raw, &doc); err != nil || doc["status"] != "approved" {
		t.Fatal("approval not observed")
	}
	profile := profilePrimary
	if who.Profile == "locked" {
		profile = profileLocked
	}
	set := func(key string, value any) {
		t.Helper()
		if !reflect.DeepEqual(now[key], value) {
			t.Fatalf("unexpected device %s", key)
		}
		old[key] = value
	}
	set("status", "approved")
	set("approved_by_user_id", float64(e.users[fixtureMember].ID))
	set("approved_profile_id", profile)
	set("denied_at", nil)
	set("auth_session_id", nil)
	set("consumed_at", nil)
	approved := boundTimestamp(t, now, "approved_at", w.wallLower, w.wallUpper)
	updated := boundTimestamp(t, now, "updated_at", w.wallLower, w.wallUpper)
	if !approved.Equal(updated) {
		t.Fatal("approval timestamps differ")
	}
	old["approved_at"], old["updated_at"] = now["approved_at"], now["updated_at"]
	var owner int
	if err := e.pool.QueryRow(e.ctx, "SELECT user_id FROM user_profiles WHERE id=$1", profile).Scan(&owner); err != nil || owner != e.users[fixtureMember].ID {
		t.Fatal("approving profile authority changed")
	}
	before["device_requests"] = encodeRows(t, prior)
	after["device_requests"] = encodeRows(t, current)
}

// checkHandoffPoll proves the device receives exactly one temporary login bound
// to the approving primary profile and the request row is consumed once.
func (e *Env) checkHandoffPoll(t *testing.T, transport string, reply response, before, after map[string]json.RawMessage, w clockWindow) {
	t.Helper()
	prior, current := decodeRowsByID(t, before["device_requests"]), decodeRowsByID(t, after["device_requests"])
	old, now := prior[remoteHandoffRequest], current[remoteHandoffRequest]
	if old == nil || now == nil || len(prior) != len(current) || old["status"] != "approved" || old["approved_profile_id"] != profilePrimary {
		t.Fatal("poll fixture is not the approved remote handoff")
	}
	var doc map[string]any
	if err := json.Unmarshal(reply.Raw, &doc); err != nil || doc["status"] != "approved" {
		t.Fatal("poll did not issue approved credentials")
	}
	tokenDoc := doc
	if transport == "v2" {
		var ok bool
		if tokenDoc, ok = doc["tokens"].(map[string]any); !ok {
			t.Fatal("token envelope absent")
		}
	}
	accessText, _ := tokenDoc["access_token"].(string)
	access, err := e.jwt.ValidateToken(accessText)
	if err != nil {
		t.Fatal("access signature invalid")
	}
	user := e.users[fixtureMember]
	if access.UserID != user.ID || access.SessionID == "" || access.ImpersonatorUserID != nil || access.TokenType != auth.TokenTypeAccess {
		t.Fatal("issued account authority mismatch")
	}
	sessionsOld, sessionsNew := decodeRowsByID(t, before["sessions"]), decodeRowsByID(t, after["sessions"])
	session := sessionsNew[access.SessionID]
	if sessionsOld[access.SessionID] != nil || session == nil || len(sessionsNew) != len(sessionsOld)+1 {
		t.Fatal("poll must create exactly one session")
	}
	for key, value := range map[string]any{"user_id": float64(user.ID), "revoked_at": nil, "impersonator_user_id": nil} {
		if !reflect.DeepEqual(session[key], value) {
			t.Fatalf("new session %s mismatch", key)
		}
	}
	boundTimestamp(t, session, "created_at", w.dbLower, w.dbUpper)
	ttl := min(e.jwt.RefreshExpiry(), 24*time.Hour)
	boundTimestamp(t, session, "expires_at", w.wallLower.Add(ttl), w.wallUpper.Add(ttl))
	var currentPolicyRevision int64
	if err := e.pool.QueryRow(e.ctx, "SELECT access_policy_revision FROM users WHERE id=$1", user.ID).Scan(&currentPolicyRevision); err != nil {
		t.Fatal(err)
	}
	token, _ := doc["profile_token"].(string)
	claims, err := e.profileTok.Validate(token)
	if err != nil || claims.UserID != user.ID || claims.SessionID != access.SessionID || claims.ProfileID != profilePrimary || claims.PolicyRevision != currentPolicyRevision || doc["profile_id"] != profilePrimary || doc["temporary"] != true {
		t.Fatal("temporary profile authority invalid")
	}
	delete(sessionsNew, access.SessionID)
	before["sessions"] = encodeRows(t, sessionsOld)
	after["sessions"] = encodeRows(t, sessionsNew)
	set := func(key string, value any) {
		t.Helper()
		if !reflect.DeepEqual(now[key], value) {
			t.Fatalf("unexpected device %s", key)
		}
		old[key] = value
	}
	set("status", "consumed")
	set("auth_session_id", access.SessionID)
	consumed := boundTimestamp(t, now, "consumed_at", w.wallLower, w.wallUpper)
	if !consumed.Equal(boundTimestamp(t, now, "updated_at", w.wallLower, w.wallUpper)) {
		t.Fatal("consumption timestamps differ")
	}
	old["consumed_at"], old["updated_at"] = now["consumed_at"], now["updated_at"]
	before["device_requests"] = encodeRows(t, prior)
	after["device_requests"] = encodeRows(t, current)
}

// checkImpersonationEnd proves the first end revokes exactly the impersonated
// session inside the database request window, and a repeat changes nothing.
func (e *Env) checkImpersonationEnd(t *testing.T, sessionID string, reply response, before, after map[string]json.RawMessage, w clockWindow, first bool) {
	t.Helper()
	prior, current := decodeRowsByID(t, before["sessions"]), decodeRowsByID(t, after["sessions"])
	old, now := prior[sessionID], current[sessionID]
	if old == nil || now == nil || len(prior) != len(current) {
		t.Fatal("session inventory changed")
	}
	if old["impersonator_user_id"] != float64(e.users[fixtureAdmin].ID) || old["user_id"] != float64(e.users[fixtureMember].ID) {
		t.Fatal("impersonation session fixture invalid")
	}
	if !first {
		if old["revoked_at"] == nil || len(reply.Raw) == 0 {
			t.Fatal("repeat must find the already revoked session")
		}
		return
	}
	if old["revoked_at"] != nil || len(reply.Raw) != 0 {
		t.Fatal("first end must revoke a live session with an empty body")
	}
	boundTimestamp(t, now, "revoked_at", w.dbLower, w.dbUpper)
	old["revoked_at"] = now["revoked_at"]
	before["sessions"] = encodeRows(t, prior)
	after["sessions"] = encodeRows(t, current)
}
