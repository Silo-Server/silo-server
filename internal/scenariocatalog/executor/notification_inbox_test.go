package executor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestRequiredNewNotificationInbox(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-notification-inbox for required NEW acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; NEW acceptance cannot skip")
	}
	e := New(t)
	// The normal scratch guard predates notifications, whose tables have no user FK.
	// Refuse existing data before taking ownership of this additional fixture scope.
	var count int
	if err := e.pool.QueryRow(e.ctx, `SELECT (SELECT count(*) FROM notification_deliveries)+(SELECT count(*) FROM notification_inbox_clocks)`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("NEW notification fixture requires empty deliveries and inbox clocks")
	}
	cipher, err := secret.New([]byte(masterKey))
	if err != nil {
		t.Fatal(err)
	}
	system := notifications.NewSystem(e.pool, nil, e.stores, nil, nil, nil, nil, nil, nil)
	server := httptest.NewServer(api.NewRouter(api.Dependencies{
		Config: e.config(), AppContext: t.Context(), DB: e.pool, SecretCipher: cipher, ClientIPResolver: clientip.NewResolver(nil),
		NodeID: "fixture-node", PublicURL: publicURL, UserStoreProvider: e.stores, PolicySystem: e.policy, Notifications: system,
	}))
	defer server.Close()
	deliveryIDs := []string{"00000000-0000-4000-8000-000000000041", "00000000-0000-4000-8000-000000000042", "00000000-0000-4000-8000-000000000043", "00000000-0000-4000-8000-000000000044"}
	cleanup := func() {
		e.mustExec(`DELETE FROM notification_deliveries WHERE id=ANY($1)`, deliveryIDs)
		e.mustExec(`DELETE FROM notification_inbox_clocks WHERE profile_id=ANY($1)`, []string{profileSecondary, profilePrimary})
	}
	defer func() { cleanup(); e.Reseed(); e.guardScratchDatabase() }()
	seed := func(index int, profile string) {
		e.mustExec(`INSERT INTO notification_deliveries(id,user_id,profile_id,type,reason_flags,status,delivered_at) VALUES($1,$2,$3,'system.test','{"title":"Synthetic acceptance"}','delivered',now())`, deliveryIDs[index], e.users[fixtureMember].ID, profile)
	}
	var results []Result
	requests, effects := 0, 0
	ids := []string{"list_window", "read_cutoff", "read_idempotent", "foreign_delivery", "cursor_profile", "cutoff_profile", "sync_tail", "auth"}
	for _, id := range ids {
		cleanup()
		e.Reseed()
		if id != "sync_tail" {
			seed(0, profileSecondary)
			seed(1, profileSecondary)
		}
		seed(2, profilePrimary)
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_notification_inbox." + id + "/v2", Scenario: "new_notification_inbox." + id, Transport: "v2"}
			defer func() { results = append(results, result) }()
			viewer := scenariocatalog.Principal{Class: "profile"}
			const base = "/api/v2/notifications"
			exchange := func(method, path string, query map[string]string, body any, principal scenariocatalog.Principal, expect scenariocatalog.Expect) response {
				t.Helper()
				requests++
				var raw json.RawMessage
				if body != nil {
					var err error
					raw, err = json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
				}
				resp, failures, err := e.exchange(server.URL, method, scenariocatalog.Request{Path: path, Query: query, Body: raw}, principal, expect, nil, nil, nil)
				if err != nil {
					failures = append(failures, err.Error())
				}
				result.Failures = append(result.Failures, failures...)
				if len(failures) > 0 {
					t.Fatalf("%s %s: %v", method, path, failures)
				}
				return resp
			}
			get := func(path string, query map[string]string, principal scenariocatalog.Principal, checks ...scenariocatalog.BodyAssertion) response {
				t.Helper()
				return exchange(http.MethodGet, path, query, nil, principal, catalogOK(checks...))
			}
			token := func(resp response, key string) string {
				t.Helper()
				var body map[string]json.RawMessage
				if err := json.Unmarshal(resp.Raw, &body); err != nil {
					t.Fatal(err)
				}
				if key == "next_cursor" {
					if err := json.Unmarshal(body["page"], &body); err != nil {
						t.Fatal(err)
					}
				}
				var value string
				if err := json.Unmarshal(body[key], &value); err != nil || value == "" {
					result.Failures = append(result.Failures, "missing "+key)
					t.Fatalf("missing %s: %v", key, err)
				}
				return value
			}
			readStates := func(want map[string]bool) {
				t.Helper()
				effects++
				rows, err := e.pool.Query(e.ctx, `SELECT id,read_at IS NOT NULL FROM notification_deliveries WHERE id=ANY($1)`, deliveryIDs)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				seen := 0
				for rows.Next() {
					var key string
					var read bool
					if err := rows.Scan(&key, &read); err != nil {
						t.Fatal(err)
					}
					expected, ok := want[key]
					if !ok || read != expected {
						result.Failures = append(result.Failures, "unexpected persisted read state")
						t.Fatalf("read state %s=%v, expected %v (present %v)", key, read, expected, ok)
					}
					seen++
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				if seen != len(want) {
					t.Fatalf("effect rows %d, want %d", seen, len(want))
				}
			}
			list := func() response {
				return get(base, map[string]string{"limit": "1"}, viewer, catalogAssertion("/items", "length", 1), catalogAssertion("/items/0/id", "equals", deliveryIDs[1]), catalogAssertion("/items/0/profile_id", "equals", profileSecondary), catalogAssertion("/items/0/read_at", "equals", nil), catalogAssertion("/page/has_more", "equals", true))
			}
			switch id {
			case "list_window":
				first := list()
				seed(3, profileSecondary)
				get(base, map[string]string{"limit": "1", "cursor": token(first, "next_cursor")}, viewer, catalogAssertion("/items", "length", 1), catalogAssertion("/items/0/id", "equals", deliveryIDs[0]), catalogAssertion("/page/has_more", "equals", false), catalogAssertion("/page/next_cursor", "absent", nil))
			case "read_cutoff":
				first := list()
				seed(3, profileSecondary)
				exchange(http.MethodPost, base+"/read-all", nil, map[string]string{"through": token(first, "read_cutoff")}, viewer, scenariocatalog.Expect{Status: 204, BodyKind: "empty"})
				readStates(map[string]bool{deliveryIDs[0]: true, deliveryIDs[1]: true, deliveryIDs[2]: false, deliveryIDs[3]: false})
				get(base+"/unread-count", nil, viewer, catalogAssertion("/count", "equals", 1))
			case "read_idempotent":
				path := base + "/" + deliveryIDs[0] + "/read"
				var first time.Time
				for attempt := range 2 {
					exchange(http.MethodPost, path, nil, nil, viewer, scenariocatalog.Expect{Status: 204, BodyKind: "empty"})
					effects++
					var read time.Time
					if err := e.pool.QueryRow(e.ctx, `SELECT read_at FROM notification_deliveries WHERE id=$1`, deliveryIDs[0]).Scan(&read); err != nil {
						t.Fatal(err)
					}
					if attempt == 0 {
						first = read
					} else if !read.Equal(first) {
						result.Failures = append(result.Failures, "retry changed read_at")
						t.Fatal("retry changed read_at")
					}
				}
				get(base+"/unread-count", nil, viewer, catalogAssertion("/count", "equals", 1))
			case "foreign_delivery":
				exchange(http.MethodGet, base+"/"+deliveryIDs[2], nil, nil, viewer, catalogProblem(404, "not_found"))
				exchange(http.MethodPost, base+"/"+deliveryIDs[2]+"/read", nil, nil, viewer, catalogProblem(404, "not_found"))
				readStates(map[string]bool{deliveryIDs[0]: false, deliveryIDs[1]: false, deliveryIDs[2]: false})
			case "cursor_profile":
				first := list()
				exchange(http.MethodGet, base, map[string]string{"limit": "1", "cursor": token(first, "next_cursor")}, nil, scenariocatalog.Principal{Class: "primary_profile"}, catalogProblem(400, "invalid_cursor"))
			case "cutoff_profile":
				first := list()
				exchange(http.MethodPost, base+"/read-all", nil, map[string]string{"through": token(first, "read_cutoff")}, scenariocatalog.Principal{Class: "primary_profile"}, catalogProblem(400, "invalid_cursor"))
				readStates(map[string]bool{deliveryIDs[0]: false, deliveryIDs[1]: false, deliveryIDs[2]: false})
			case "sync_tail":
				first := get(base+"/sync", map[string]string{"limit": "1"}, viewer, catalogAssertion("/items", "length", 0), catalogAssertion("/initial_snapshot", "equals", true), catalogAssertion("/unread_count", "equals", 0))
				seed(0, profileSecondary)
				seed(1, profileSecondary)
				page := get(base+"/sync", map[string]string{"limit": "1", "cursor": token(first, "sync_cursor")}, viewer, catalogAssertion("/items", "length", 1), catalogAssertion("/items/0/id", "equals", deliveryIDs[0]), catalogAssertion("/initial_snapshot", "equals", false), catalogAssertion("/page/has_more", "equals", true), catalogAssertion("/unread_count", "equals", 2))
				last := get(base+"/sync", map[string]string{"limit": "1", "cursor": token(page, "next_cursor")}, viewer, catalogAssertion("/items", "length", 1), catalogAssertion("/items/0/id", "equals", deliveryIDs[1]), catalogAssertion("/page/has_more", "equals", false))
				get(base+"/sync", map[string]string{"limit": "1", "cursor": token(last, "sync_cursor")}, viewer, catalogAssertion("/items", "length", 0), catalogAssertion("/initial_snapshot", "equals", false), catalogAssertion("/page/has_more", "equals", false), catalogAssertion("/sync_cursor", "type", "string"))
			case "auth":
				exchange(http.MethodGet, base, nil, nil, scenariocatalog.Principal{Class: "public"}, catalogProblem(401, "authentication_required"))
				exchange(http.MethodGet, base, nil, nil, scenariocatalog.Principal{Class: "authenticated"}, catalogProblem(422, "validation_failed"))
			default:
				result.Failures = append(result.Failures, "unimplemented case")
				t.Fatal("unimplemented case")
			}
		})
	}
	if len(results) != 8 || requests != 20 || effects != 5 {
		t.Errorf("NEW inbox evidence: %d results/%d requests/%d effects, want8/20/5", len(results), requests, effects)
	}
	seen := map[string]bool{}
	for _, r := range results {
		if seen[r.ID] {
			t.Errorf("duplicate %s", r.ID)
		}
		seen[r.ID] = true
		if !r.Passed() {
			t.Errorf("required NEW case %s failed: %v", r.ID, r.Failures)
		}
	}
	if path := os.Getenv("SILO_SCENARIO_REPORT"); path != "" {
		report := struct {
			Scope                                       string
			NewScenarios, PhysicalRequests, EffectReads int
			Results                                     []Result
		}{"NEW notification inbox (outside frozen 598)", len(results), requests, effects, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
