package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Refuse existing destinations before taking ownership of synthetic server channel rows.
func guardNewServerChannelFixture(t *testing.T) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect server channel fixture database")
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('notification_server_channels') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		return
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_server_channels`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("NEW server channel fixture requires empty destinations before reseeding")
	}
}

func TestRequiredNewServerChannels(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-server-channels for required NEW acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; NEW acceptance cannot skip")
	}
	guardNewServerChannelFixture(t)
	e := New(t)
	cipher, err := secret.New([]byte(masterKey))
	if err != nil {
		t.Fatal(err)
	}
	// Construct services without starting any notification dispatcher or worker.
	system := notifications.NewSystem(e.pool, nil, e.stores, nil, nil, nil, nil, cipher, nil)
	server := httptest.NewServer(api.NewRouter(api.Dependencies{Config: e.config(), AppContext: t.Context(), DB: e.pool, SecretCipher: cipher, ClientIPResolver: clientip.NewResolver(nil), NodeID: "fixture-node", PublicURL: publicURL, UserStoreProvider: e.stores, PolicySystem: e.policy, Notifications: system}))
	defer server.Close()
	ids := []string{"00000000-0000-4000-8000-000000000101", "00000000-0000-4000-8000-000000000102", "00000000-0000-4000-8000-000000000103"}
	cleanup := func() { e.mustExec(`DELETE FROM notification_server_channels WHERE id=ANY($1)`, ids) }
	defer func() { cleanup(); e.Reseed(); e.guardScratchDatabase(); guardNewServerChannelFixture(t) }()
	var results []Result
	requests, effects := 0, 0
	for _, id := range []string{"tied_traversal", "cursor_binding", "authority", "empty"} {
		cleanup()
		e.Reseed()
		if id != "empty" {
			for i, key := range ids {
				e.mustExec(`INSERT INTO notification_server_channels(id,name,type,url_ciphertext,url_host,signing_secret_ciphertext,enabled,created_by_user_id,created_at,watermark_created_at,watermark_id,last_attempt_at,consecutive_failures,disabled_reason,last_failure_at,last_failure_status,last_failure_message,notify_new_movies,notify_new_episodes,notify_new_audiobooks,notify_new_ebooks,notify_request_submitted) VALUES($1,$2,'generic','synthetic-private-url','example.invalid','synthetic-private-signing',$3,$4,'2026-01-02T03:04:05.123456Z','2026-01-01T01:02:03.456789Z','synthetic-watermark','2026-01-02T01:02:03.456789Z',5,'synthetic-disabled','2026-01-02T01:02:03.456789Z',503,'synthetic-failure',true,false,true,false,true)`, key, fmt.Sprintf("Synthetic channel%d", i), i != 2, e.users[fixtureAdmin].ID)
			}
		}
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_server_channels." + id + "/v2", Scenario: "new_server_channels." + id, Transport: "v2"}
			defer func() {
				if t.Failed() && len(result.Failures) == 0 {
					result.Failures = append(result.Failures, "scenario assertion failed; see test log")
				}
				results = append(results, result)
			}()
			snapshot := func() []byte {
				t.Helper()
				effects++
				var raw []byte
				if err := e.pool.QueryRow(e.ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(w) ORDER BY id),'[]') FROM notification_server_channels w`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var rows []json.RawMessage
				if err := json.Unmarshal(raw, &rows); err != nil {
					t.Fatal(err)
				}
				want := 3
				if id == "empty" {
					want = 0
				}
				if len(rows) != want {
					t.Fatalf("server channel rows%d,want%d", len(rows), want)
				}
				return raw
			}
			before := snapshot()
			get := func(query map[string]string, p scenariocatalog.Principal, expect scenariocatalog.Expect) response {
				t.Helper()
				requests++
				resp, failures, err := e.exchange(server.URL, http.MethodGet, scenariocatalog.Request{Path: "/api/v2/admin/notifications/server-channels", Query: query}, p, expect, nil, nil, nil)
				if err != nil {
					failures = append(failures, err.Error())
				}
				result.Failures = append(result.Failures, failures...)
				if len(failures) > 0 {
					t.Fatalf("server channel GET: %v", failures)
				}
				for _, private := range []string{"synthetic-private-url", "synthetic-private-signing", "url_ciphertext", "signing_secret_ciphertext", "watermark_id", "watermark_created_at", "last_attempt_at", "created_by_user_id"} {
					if bytes.Contains(resp.Raw, []byte(private)) {
						t.Fatalf("private server channel field exposed%s", private)
					}
				}
				return resp
			}
			next := func(resp response) string {
				t.Helper()
				var body struct {
					Page struct {
						Next string `json:"next_cursor"`
					} `json:"page"`
				}
				if err := json.Unmarshal(resp.Raw, &body); err != nil {
					t.Fatal(err)
				}
				if body.Page.Next == "" {
					t.Fatal("missing required continuation")
				}
				return body.Page.Next
			}
			viewer := scenariocatalog.Principal{Class: "acting_admin"}
			one := func(i int, more bool) scenariocatalog.Expect {
				return catalogOK(catalogAssertion("/items", "length", 1), catalogAssertion("/items/0/id", "equals", ids[i]), catalogAssertion("/items/0/enabled", "equals", i != 2), catalogAssertion("/items/0/url_host", "equals", "example.invalid"), catalogAssertion("/items/0/last_success_at", "equals", nil), catalogAssertion("/page/has_more", "equals", more),
					catalogAssertion("/items/0/created_at", "equals", "2026-01-02T03:04:05.123Z"),
					catalogAssertion("/items/0/last_failure_at", "equals", "2026-01-02T01:02:03.456Z"),
					catalogAssertion("/items/0/last_failure_status", "equals", 503),
					catalogAssertion("/items/0/last_failure_message", "equals", "synthetic-failure"),
					catalogAssertion("/items/0/consecutive_failures", "equals", 5),
					catalogAssertion("/items/0/disabled_reason", "equals", "synthetic-disabled"),
					catalogAssertion("/items/0/notify_new_movies", "equals", true),
					catalogAssertion("/items/0/notify_new_episodes", "equals", false),
					catalogAssertion("/items/0/notify_new_audiobooks", "equals", true),
					catalogAssertion("/items/0/notify_new_ebooks", "equals", false),
					catalogAssertion("/items/0/notify_request_submitted", "equals", true))
			}
			switch id {
			case "tied_traversal":
				cursor := ""
				for i := range 3 {
					query := map[string]string{"limit": "1"}
					if cursor != "" {
						query["cursor"] = cursor
					}
					resp := get(query, viewer, one(i, i < 2))
					if i < 2 {
						cursor = next(resp)
					}
				}
			case "cursor_binding":
				first := get(map[string]string{"limit": "1"}, viewer, one(0, true))
				cursor := next(first)
				get(map[string]string{"limit": "2", "cursor": cursor}, viewer, catalogProblem(400, "invalid_cursor"))
			case "authority":
				for _, p := range []scenariocatalog.Principal{{Class: "primary_profile"}, {Class: "acting_admin", Profile: "admin_secondary"}} {
					get(nil, p, catalogProblem(403, "permission_denied"))
				}
				get(nil, scenariocatalog.Principal{Class: "public"}, catalogProblem(401, "authentication_required"))
			case "empty":
				get(nil, viewer, catalogOK(catalogAssertion("/items", "equals", []any{}), catalogAssertion("/page/has_more", "equals", false), catalogAssertion("/page/next_cursor", "absent", nil)))
			default:
				t.Fatal("unimplemented case")
			}
			if !bytes.Equal(before, snapshot()) {
				t.Fatal("server channel read changed stored destinations")
			}
		})
	}
	if len(results) != 4 || requests != 9 || effects != 8 {
		t.Errorf("NEW server channel evidence%d/%d/%d,want4/9/8", len(results), requests, effects)
	}
	seen := map[string]bool{}
	for _, r := range results {
		if seen[r.ID] || !r.Passed() {
			t.Errorf("duplicate or failed result%s: %v", r.ID, r.Failures)
		}
		seen[r.ID] = true
	}
	if path := os.Getenv("SILO_SCENARIO_REPORT"); path != "" {
		report := struct {
			Scope                                       string
			NewScenarios, PhysicalRequests, EffectReads int
			Results                                     []Result
		}{"NEW server channels (outside frozen 598)", len(results), requests, effects, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
