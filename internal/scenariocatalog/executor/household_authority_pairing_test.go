package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

var householdAuthorityTables = []string{"users", "user_profiles", "api_keys", "server_settings", "auth_sessions", "device_login_requests", "invitations", "invite_codes", "user_devices", "user_device_settings", "user_favorites", "user_watchlist", "user_watch_progress", "user_collection_sort_preferences", "user_personal_collections", "user_personal_collection_items", "user_series_playback_preferences", "user_library_playback_preferences", "user_setting_values", "user_setting_mutations", "user_setting_migration_rejects", "user_profile_allowed_libraries", "user_settings", "user_profile_onboarding", "request_settings", "playback_sessions_sync"}

func TestRequiredHouseholdAuthorityAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run household authority acceptance explicitly")
	}
	dsn := os.Getenv(DatabaseEnv)
	if dsn == "" {
		t.Fatal(DatabaseEnv + " required before constructor")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.HouseholdAuthorityAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	keyUsageGuard(t, pool)
	var exists bool
	if err = pool.QueryRow(t.Context(), `SELECT to_regclass('playback_sessions_sync') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		var count int
		if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM playback_sessions_sync`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("occupied playback fixture; refuse constructor")
		}
	}
	pool.Close()
	t.Log("household authority pre-constructor guards passed")
	e := New(t)
	defer e.Reseed()
	var results []Result
	snapshots := 0
	followup := os.Getenv("SILO_HOUSEHOLD_READBACK_FOLLOWUP") == "1"
	expectedIDs := scenariocatalog.RequiredHouseholdAuthorityScenarios
	snapshot := func() map[string]json.RawMessage {
		t.Helper()
		snapshots++
		out := map[string]json.RawMessage{}
		for _, table := range householdAuthorityTables {
			var raw []byte
			if err := e.pool.QueryRow(e.ctx, fmt.Sprintf("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM %s t", table)).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			out[table] = raw
		}
		return out
	}
	for _, c := range selected {
		for _, row := range c.Rows {
			for _, s := range row.Scenarios {
				for _, transport := range []string{"v1", "v2"} {
					if followup && (transport != "v2" || s.ID == "profiles_update.quality_normalized" || s.Expect.Status != 200) {
						continue
					}
					t.Run(s.ID+"/"+transport, func(t *testing.T) {
						e.Reseed()
						defer e.Reseed()
						result := Result{ID: s.ID + "/" + transport, Scenario: s.ID, Transport: transport, Catalog: c.File, Row: row.Key().String()}
						defer func() {
							if t.Failed() && len(result.Failures) == 0 {
								result.Failures = []string{"effect assertion failed; see log"}
							}
							results = append(results, result)
						}()
						before := snapshot()
						if !bytes.Equal(before["playback_sessions_sync"], []byte("[]")) {
							t.Fatal("original empty playback fixture required")
						}
						request, expect, method := s.Request, s.Expect, row.Method
						if transport == "v2" {
							pair := s.V2Expectation
							request, expect, method = pair.Request, pair.Expect, pair.Method
							result.OperationID = pair.OperationID
						}
						start := time.Now().UTC().Truncate(time.Second)
						_, failures, err := e.exchange(e.live.URL, method, request, s.Principal, expect, nil, nil, nil)
						end := time.Now().UTC()
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = failures
						if len(failures) > 0 {
							t.Errorf("exchange: %v", failures)
						}
						after := snapshot()
						quality := s.ID == "profiles_update.quality_normalized"
						if quality {
							assertHouseholdQuality(t, e.users[fixtureMember].ID, start, end, before, after)
						}
						for table, want := range before {
							if quality && (table == "users" || table == "user_profiles") {
								continue
							}
							if !bytes.Equal(want, after[table]) {
								t.Errorf("unexpected full table change: %s", table)
							}
						}
						t.Logf("%s: 26 full tables compared, quality=%t", result.ID, quality)
					})
				}
			}
		}
	}
	if followup {
		expectedIDs = []string{"household.ok", "household.empty", "household.shape", "household.admin"}
		if len(results) != 4 {
			t.Fatal("followup requires exactly four results")
		}
		for _, id := range expectedIDs {
			count := 0
			for _, r := range results {
				if r.Scenario == id && r.Transport == "v2" && len(r.Failures) == 0 && r.Skipped == "" {
					count++
				}
			}
			if count != 1 {
				t.Errorf("missing successful followup %s", id)
			}
		}
	} else if err := requiredPairedResults(results, expectedIDs); err != nil {
		t.Error(err)
	}
	expectedSnapshots := 36
	if followup {
		expectedSnapshots = 8
	}
	if snapshots != expectedSnapshots {
		t.Errorf("snapshots=%d want36", snapshots)
	}
	t.Logf("%d exchanges, %d full snapshots, %d table observations", len(results), snapshots, snapshots*26)
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

func assertHouseholdQuality(t *testing.T, owner int, start, end time.Time, before, after map[string]json.RawMessage) {
	t.Helper()
	for _, table := range []string{"users", "user_profiles"} {
		var old, newRows []map[string]any
		if err := json.Unmarshal(before[table], &old); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(after[table], &newRows); err != nil {
			t.Fatal(err)
		}
		if len(old) != len(newRows) {
			t.Fatal("row count changed")
		}
		indexed := map[any]map[string]any{}
		for _, r := range newRows {
			indexed[r["id"]] = r
		}
		matched := 0
		for _, want := range old {
			got := indexed[want["id"]]
			if table == "users" && want["id"] == float64(owner) {
				matched++
				for _, key := range []string{"access_policy_revision", "admin_revision"} {
					n, ok := want[key].(float64)
					if !ok {
						t.Fatal("revision type")
					}
					want[key] = n + 1
				}
			}
			if table == "user_profiles" && want["id"] == profileSecondary {
				matched++
				if want["max_playback_quality"] != "" {
					t.Fatal("fixture quality must be empty")
				}
				want["max_playback_quality"] = "1080p"
				stamp, ok := got["updated_at"].(string)
				if !ok {
					t.Fatal("timestamp type")
				}
				at, err := time.Parse(time.RFC3339Nano, stamp)
				if err != nil || at.Before(start) || at.After(end) {
					t.Fatal("profile timestamp outside application request bounds")
				}
				want["updated_at"] = got["updated_at"]
			}
			if !reflect.DeepEqual(want, got) {
				t.Errorf("unexpected %s row effects", table)
			}
		}
		if matched != 1 {
			t.Fatal("expected one quality target/owner")
		}
	}
}
