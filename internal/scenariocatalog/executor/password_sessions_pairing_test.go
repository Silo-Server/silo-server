package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredPasswordSessionsAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-password-sessions")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.PasswordSessionsAcceptance(catalogs)
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
							before := snapshot()
							dbLower, wallLower := dbNow(), time.Now()
							requests += max(step.Request.Repeat, 1)
							reply, failures, err := e.exchange(e.live.URL, step.Method, step.Request, who, step.Expect, nil, nil, nil)
							wallUpper, dbUpper := time.Now(), dbNow()
							after := snapshot()
							if err != nil {
								failures = append(failures, err.Error())
							}
							result.Failures = append(result.Failures, failures...)
							if len(failures) > 0 {
								t.Errorf("step %d failed original/paired response assertions", i)
							}
							if i == 0 {
								e.checkPasswordSessionMutation(t, s.ID, before, after, dbLower, dbUpper)
							}
							if i == 2 {
								e.checkAuthLifecycleEffects(t, "login.user_meaning", transport, reply, before, after, wallLower, wallUpper, dbLower, dbUpper)
							}
							for table, want := range before {
								if !bytes.Equal(want, after[table]) {
									t.Errorf("step %d unexpected stored change in %s", i, table)
								}
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredPasswordSessionsScenarios); err != nil {
		t.Error(err)
	}
	if requests != 18 || effects != 32 {
		t.Errorf("evidence %dHTTP/%d snapshots, want18/32", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

func (e *Env) checkPasswordSessionMutation(t *testing.T, id string, before, after map[string]json.RawMessage, lower, upper time.Time) {
	t.Helper()
	table, target := "sessions", e.sessions[fixtureMember]
	password := strings.HasPrefix(id, "password.")
	if password {
		table = "users"
		target = strconv.Itoa(e.users[fixtureMember].ID)
	} else if id != "session_delete.meaning" {
		target = "00000000-0000-4000-8000-00000000dead"
	}
	var prior, current []map[string]json.RawMessage
	if err := json.Unmarshal(before[table], &prior); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after[table], &current); err != nil {
		t.Fatal(err)
	}
	if len(prior) != len(current) {
		t.Fatal("mutation changed row inventory")
	}
	matched := 0
	for i, row := range prior {
		rawID := strings.Trim(string(row["id"]), "\"")
		if rawID != target {
			continue
		}
		matched++
		if !bytes.Equal(row["id"], current[i]["id"]) {
			t.Fatal("target identity changed")
		}
		field := "revoked_at"
		if password {
			field = "updated_at"
		}
		var now time.Time
		if err := json.Unmarshal(current[i][field], &now); err != nil {
			t.Fatal("invalid mutation timestamp")
		}
		if now.Before(lower) || now.After(upper) {
			t.Fatal("mutation timestamp outside database bounds")
		}
		if string(row[field]) != "null" {
			var old time.Time
			if err := json.Unmarshal(row[field], &old); err != nil {
				t.Fatal(err)
			}
			if now.Before(old) {
				t.Fatal("mutation timestamp went backwards")
			}
		}
		if password {
			var oldHash, newHash string
			if err := json.Unmarshal(row["password_hash"], &oldHash); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(current[i]["password_hash"], &newHash); err != nil {
				t.Fatal(err)
			}
			user := *e.users[fixtureMember]
			user.PasswordHash = oldHash
			if !auth.CheckPassword(&user, memberPass) || auth.CheckPassword(&user, "fixture-rotated-pw1") {
				t.Fatal("wrong initial credential state")
			}
			user.PasswordHash = newHash
			if oldHash == newHash || auth.CheckPassword(&user, memberPass) || !auth.CheckPassword(&user, "fixture-rotated-pw1") {
				t.Fatal("password transition invalid")
			}
			var oldRevision, newRevision int64
			if err := json.Unmarshal(row["admin_revision"], &oldRevision); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(current[i]["admin_revision"], &newRevision); err != nil {
				t.Fatal(err)
			}
			if newRevision != oldRevision+1 {
				t.Fatal("password update must increment administrator revision exactly once")
			}
			row["admin_revision"] = current[i]["admin_revision"]
			row["password_hash"] = current[i]["password_hash"]
		} else if id == "session_delete.meaning" && string(row[field]) != "null" {
			t.Fatal("current session already revoked")
		}
		row[field] = current[i][field]
	}
	if matched != 1 {
		t.Fatal("expected exactly one target mutation")
	}
	var err error
	before[table], err = json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	after[table], err = json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
}
