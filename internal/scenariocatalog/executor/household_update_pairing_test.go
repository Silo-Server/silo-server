package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredHouseholdUpdateAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-household-update for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.HouseholdUpdateAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect the reserved database before even constructing the offline router.
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect household update scratch database")
	}
	defer pool.Close()
	probe := &Env{t: t, ctx: t.Context(), pool: pool}
	probe.guardScratchDatabase()
	pool.Close()
	guardFrozenAPIKeyFixture(t)
	t.Log("household update pre-constructor guards passed")
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
							rows := map[string]json.RawMessage{}
							for _, table := range append(append([]string{}, householdEffectTables...), "user_setting_mutations", "user_setting_migration_rejects", "user_profile_allowed_libraries") {
								var raw []byte
								if err := e.pool.QueryRow(e.ctx, fmt.Sprintf("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM %s t", table)).Scan(&raw); err != nil {
									t.Fatal(err)
								}
								rows[table] = raw
							}
							return rows
						}
						before := snapshot()
						sequence := func() int64 {
							var value int64
							var called bool
							if err := e.pool.QueryRow(e.ctx, "SELECT last_value, is_called FROM user_setting_values_id_seq").Scan(&value, &called); err != nil {
								t.Fatal(err)
							}
							if !called {
								return value - 1
							}
							return value
						}
						seqBefore := sequence()
						var dbStart time.Time
						if err := e.pool.QueryRow(e.ctx, "SELECT clock_timestamp()").Scan(&dbStart); err != nil {
							t.Fatal(err)
						}
						appStart := time.Now().UTC().Truncate(time.Second)
						if len(before) != 22 {
							t.Fatal("snapshot must contain twenty-two full tables")
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
						appEnd := time.Now().UTC()
						var dbEnd time.Time
						if err := e.pool.QueryRow(e.ctx, "SELECT clock_timestamp()").Scan(&dbEnd); err != nil {
							t.Fatal(err)
						}
						after := snapshot()
						field := ""
						if s.ID == "profiles_update.self_service" {
							field = "auto_skip_credits"
						}
						if s.ID == "profiles_update.shape" {
							field = "auto_skip_recap"
						}
						advance := int64(0)
						if field != "" {
							advance = 1
						}
						if sequence() != seqBefore+advance {
							t.Error("unexpected canonical setting sequence allocation")
						}
						if field != "" {
							assertHouseholdPreferenceUpdate(t, field, e.users[fixtureMember].ID, seqBefore+1, appStart, appEnd, dbStart, dbEnd, before, after)
						}
						if len(before) != len(after) {
							t.Error("unexpected snapshot table count")
						}
						for id, want := range before {
							if field != "" && (id == "user_profiles" || id == "user_setting_values") {
								continue
							}
							if !bytes.Equal(want, after[id]) {
								t.Error("account/profile/API-key/settings/session/device-request/invitation/profile rows changed during household update")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredHouseholdUpdateScenarios); err != nil {
		t.Error(err)
	}
	if requests != 20 || effects != 40 {
		t.Errorf("paired household update evidence %dHTTP/%dPG, want20/40", requests, effects)
	}
	t.Logf("household updates: %d results, %d HTTP, %d snapshots, %d table observations", len(results), requests, effects, effects*22)
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// A successful update changes only one flag and its canonical profile setting.
// Every column is checked; timestamps are bounded by the clock that authors them.
func assertHouseholdPreferenceUpdate(t *testing.T, field string, userID int, settingID int64, appStart, appEnd, dbStart, dbEnd time.Time, before, after map[string]json.RawMessage) {
	t.Helper()
	decode := func(raw json.RawMessage) []map[string]any {
		t.Helper()
		var rows []map[string]any
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	bounded := func(value any, start, end time.Time) {
		t.Helper()
		text, ok := value.(string)
		if !ok {
			t.Fatalf("timestamp type %T", value)
		}
		instant, err := time.Parse(time.RFC3339Nano, text)
		if err != nil || instant.Before(start) || instant.After(end) {
			t.Errorf("timestamp outside request clock bounds: %v (%v)", value, err)
		}
	}
	previous, current := decode(before["user_profiles"]), decode(after["user_profiles"])
	if len(previous) != len(current) {
		t.Fatal("profile row count changed")
	}
	byID := map[any]map[string]any{}
	for _, row := range current {
		byID[row["id"]] = row
	}
	matched := 0
	for _, want := range previous {
		got, ok := byID[want["id"]]
		if !ok {
			t.Fatal("profile disappeared")
		}
		if want["id"] == profileSecondary {
			matched++
			if want[field] != false || got[field] != true {
				t.Errorf("expected false-to-true %s", field)
			}
			bounded(got["updated_at"], appStart, appEnd)
			want[field] = true
			want["updated_at"] = got["updated_at"]
		}
		if !reflect.DeepEqual(want, got) {
			t.Error("unexpected profile column change")
		}
	}
	if matched != 1 {
		t.Fatal("expected exactly one target profile")
	}
	oldSettings, newSettings := decode(before["user_setting_values"]), decode(after["user_setting_values"])
	if len(oldSettings) != 0 || len(newSettings) != 1 {
		t.Fatal("expected precisely one new canonical setting from empty fixture")
	}
	got := newSettings[0]
	bounded(got["created_at"], dbStart, dbEnd)
	bounded(got["updated_at"], dbStart, dbEnd)
	if got["created_at"] != got["updated_at"] {
		t.Error("new canonical timestamps differ")
	}
	want := map[string]any{"id": float64(settingID), "user_id": float64(userID), "key": "playback." + field, "scope": "profile", "profile_id": profileSecondary, "client_family": nil, "device_id": nil, "library_id": nil, "series_id": nil, "value": true, "revision": float64(1), "created_at": got["created_at"], "updated_at": got["updated_at"]}
	if !reflect.DeepEqual(want, got) {
		t.Error("unexpected canonical setting identity/value/revision/default/column")
	}
}
