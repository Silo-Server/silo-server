package executor

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredInviteCodeCreationAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-invite-code-creation for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.InviteCodeCreationAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect the reserved database before even constructing the offline router.
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect invite-code creation scratch database")
	}
	defer pool.Close()
	probe := &Env{t: t, ctx: t.Context(), pool: pool}
	probe.guardScratchDatabase()
	pool.Close()
	guardFrozenAPIKeyFixture(t)
	t.Log("invite-code creation pre-constructor guards passed")
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
							if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM user_profiles p),'keys',(SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM api_keys k),'settings',(SELECT jsonb_agg(to_jsonb(t) ORDER BY key) FROM server_settings t),'sessions',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM auth_sessions t),'device_requests',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM device_login_requests t),'invitations',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM invitations t),'invite_codes',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM invite_codes t))`).Scan(&raw); err != nil {
								t.Fatal(err)
							}
							var rows map[string]json.RawMessage
							if err := json.Unmarshal(raw, &rows); err != nil {
								t.Fatal(err)
							}
							return rows
						}
						var started time.Time
						if err := e.pool.QueryRow(e.ctx, `SELECT clock_timestamp()`).Scan(&started); err != nil {
							t.Fatal(err)
						}
						before := snapshot()
						sequence := func() int64 {
							var n int64
							var called bool
							if err := e.pool.QueryRow(e.ctx, "SELECT last_value,is_called FROM invite_codes_id_seq").Scan(&n, &called); err != nil {
								t.Fatal(err)
							}
							if !called {
								t.Fatal("seeded code sequence must be called")
							}
							return n
						}
						sequenceBefore := sequence()
						if transport == "v2" && s.ID != "codes_create.explicit_code" {
							e.fixtures["caller_invite_code"] = strings.ToUpper(rand.Text()[:8])
						}
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
						resp, failures, err := e.exchange(e.live.URL, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("frozen exchange: %v", failures)
						}
						after := snapshot()
						var finished time.Time
						if err := e.pool.QueryRow(e.ctx, `SELECT clock_timestamp()`).Scan(&finished); err != nil {
							t.Fatal(err)
						}
						if sequence() != sequenceBefore+1 {
							t.Error("creation must allocate exactly one code identity")
						}
						assertInviteCodeCreation(t, e, s.ID, transport, resp, sequenceBefore+1, before["invite_codes"], after["invite_codes"], started, finished)
						if len(before) != len(after) {
							t.Error("unexpected snapshot table count")
						}
						for id, want := range before {
							if id == "invite_codes" {
								continue
							}
							if !bytes.Equal(want, after[id]) {
								t.Error("account/profile/API-key/settings/session/device-request/invitation/code rows changed during invite-code creation")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredInviteCodeCreationScenarios); err != nil {
		t.Error(err)
	}
	if requests != 8 || effects != 16 {
		t.Errorf("paired invite-code creation evidence %dHTTP/%dPG, want8/16", requests, effects)
	}
	t.Logf("invite-code creations: %d results, %d HTTP, %d snapshots, %d table observations", len(results), requests, effects, effects*8)
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// Require one fresh row, preserving every existing code and every default column.
func assertInviteCodeCreation(t *testing.T, e *Env, id, transport string, resp response, newID int64, before, after json.RawMessage, start, end time.Time) {
	t.Helper()
	var old, rows []map[string]any
	if err := json.Unmarshal(before, &old); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(old)+1 {
		t.Fatal("expected one newly issued code")
	}
	known := map[any]map[string]any{}
	for _, row := range old {
		known[row["id"]] = row
	}
	var added map[string]any
	count := 0
	for _, row := range rows {
		if prev, ok := known[row["id"]]; ok {
			if !reflect.DeepEqual(prev, row) {
				t.Error("existing code changed")
			}
			delete(known, row["id"])
		} else {
			added = row
			count++
		}
	}
	if count != 1 || len(known) != 0 {
		t.Fatal("unexpected code addition/deletion")
	}
	code, ok := added["code"].(string)
	if !ok {
		t.Fatal("issued code type")
	}
	if id == "codes_create.explicit_code" {
		if code != "FIXTURE7" {
			t.Error("explicit code changed")
		}
	} else if transport == "v2" {
		if code != e.fixtures["caller_invite_code"] {
			t.Error("caller chosen code changed")
		}
	} else if !regexp.MustCompile(`^[A-Z0-9]{8}$`).MatchString(code) {
		t.Error("legacy generated code shape")
	}
	for _, row := range old {
		if row["code"] == code {
			t.Error("issued code already existed")
		}
	}
	label, maxUses := "fixture", float64(3)
	if id == "codes_create.explicit_code" {
		label, maxUses = "explicit", 1
	}
	if id == "codes_create.shape" {
		label, maxUses = "x", 1
	}
	for _, key := range []string{"created_at", "updated_at"} {
		value, ok := added[key].(string)
		if !ok {
			t.Fatal("code timestamp type")
		}
		stamp, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || stamp.Before(start) || stamp.After(end) {
			t.Error("code timestamp outside database bounds")
		}
	}
	if added["created_at"] != added["updated_at"] {
		t.Error("new code timestamps differ")
	}
	want := map[string]any{"id": float64(newID), "code": code, "label": label, "max_uses": maxUses, "use_count": float64(0), "created_by": float64(e.users[fixtureAdmin].ID), "enabled": true, "created_at": added["created_at"], "updated_at": added["updated_at"]}
	if !reflect.DeepEqual(want, added) {
		t.Error("unexpected issued code column/default/identity effect")
	}
	var body map[string]any
	if err := json.Unmarshal(resp.Raw, &body); err != nil {
		t.Fatal(err)
	}
	wireID, wireOwner := any(float64(newID)), any(float64(e.users[fixtureAdmin].ID))
	if transport == "v2" {
		wireID = strconv.FormatInt(newID, 10)
		wireOwner = strconv.Itoa(e.users[fixtureAdmin].ID)
	}
	if body["id"] != wireID || body["created_by"] != wireOwner || body["code"] != code || body["label"] != label || body["max_uses"] != maxUses || body["use_count"] != float64(0) || body["enabled"] != true {
		t.Error("issued response differs from committed row")
	}
}
