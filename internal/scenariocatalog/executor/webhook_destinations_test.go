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

// Refuse existing destinations before taking ownership of synthetic webhook rows.
func guardNewWebhookFixture(t *testing.T) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), os.Getenv(DatabaseEnv))
	if err != nil {
		t.Fatal("connect webhook fixture database")
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('notification_webhooks') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		return
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_webhooks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("NEW webhook fixture requires empty destinations before reseeding")
	}
}

func TestRequiredNewWebhookDestinations(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-webhook-destinations for required NEW acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; NEW acceptance cannot skip")
	}
	guardNewWebhookFixture(t)
	e := New(t)
	cipher, err := secret.New([]byte(masterKey))
	if err != nil {
		t.Fatal(err)
	}
	// Construct services without starting any notification dispatcher or worker.
	system := notifications.NewSystem(e.pool, nil, e.stores, nil, nil, nil, nil, cipher, nil)
	server := httptest.NewServer(api.NewRouter(api.Dependencies{Config: e.config(), AppContext: t.Context(), DB: e.pool, SecretCipher: cipher, ClientIPResolver: clientip.NewResolver(nil), NodeID: "fixture-node", PublicURL: publicURL, UserStoreProvider: e.stores, PolicySystem: e.policy, Notifications: system}))
	defer server.Close()
	ids := []string{"00000000-0000-4000-8000-000000000101", "00000000-0000-4000-8000-000000000102", "00000000-0000-4000-8000-000000000103", "00000000-0000-4000-8000-000000000104"}
	cleanup := func() { e.mustExec(`DELETE FROM notification_webhooks WHERE id=ANY($1)`, ids) }
	defer func() { cleanup(); e.Reseed(); e.guardScratchDatabase(); guardNewWebhookFixture(t) }()
	var results []Result
	requests, effects := 0, 0
	for _, id := range []string{"tied_traversal", "cursor_binding", "profile_isolation", "authentication"} {
		cleanup()
		e.Reseed()
		for i, key := range ids {
			profile := profileSecondary
			if i == 3 {
				profile = profilePrimary
			}
			e.mustExec(`INSERT INTO notification_webhooks(id,user_id,profile_id,name,type,url_ciphertext,url_host,signing_secret_ciphertext,enabled,created_at) VALUES($1,$2,$3,$4,'generic','synthetic-private-url','example.invalid','synthetic-private-signing',$5,'2026-01-02T03:04:05.123456Z')`, key, e.users[fixtureMember].ID, profile, fmt.Sprintf("Synthetic webhook%d", i), i != 2)
		}
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_webhook_destinations." + id + "/v2", Scenario: "new_webhook_destinations." + id, Transport: "v2"}
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
				if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_agg(to_jsonb(w) ORDER BY id) FROM notification_webhooks w`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var rows []json.RawMessage
				if err := json.Unmarshal(raw, &rows); err != nil {
					t.Fatal(err)
				}
				if len(rows) != 4 {
					t.Fatalf("webhook rows%d,want4", len(rows))
				}
				return raw
			}
			before := snapshot()
			get := func(query map[string]string, p scenariocatalog.Principal, expect scenariocatalog.Expect) response {
				t.Helper()
				requests++
				resp, failures, err := e.exchange(server.URL, http.MethodGet, scenariocatalog.Request{Path: "/api/v2/notifications/webhooks", Query: query}, p, expect, nil, nil, nil)
				if err != nil {
					failures = append(failures, err.Error())
				}
				result.Failures = append(result.Failures, failures...)
				if len(failures) > 0 {
					t.Fatalf("webhook GET: %v", failures)
				}
				for _, private := range []string{"synthetic-private-url", "synthetic-private-signing", "url_ciphertext", "signing_secret_ciphertext"} {
					if bytes.Contains(resp.Raw, []byte(private)) {
						t.Fatalf("private webhook field exposed%s", private)
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
			viewer := scenariocatalog.Principal{Class: "profile"}
			one := func(i int, more bool) scenariocatalog.Expect {
				return catalogOK(catalogAssertion("/items", "length", 1), catalogAssertion("/items/0/id", "equals", ids[i]), catalogAssertion("/items/0/enabled", "equals", i != 2), catalogAssertion("/items/0/url_host", "equals", "example.invalid"), catalogAssertion("/items/0/last_success_at", "equals", nil), catalogAssertion("/page/has_more", "equals", more))
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
				get(map[string]string{"limit": "1", "cursor": cursor}, scenariocatalog.Principal{Class: "primary_profile"}, catalogProblem(400, "invalid_cursor"))
				get(map[string]string{"limit": "2", "cursor": cursor}, viewer, catalogProblem(400, "invalid_cursor"))
			case "profile_isolation":
				get(nil, scenariocatalog.Principal{Class: "primary_profile"}, one(3, false))
				get(nil, scenariocatalog.Principal{Class: "acting_admin"}, catalogOK(catalogAssertion("/items", "equals", []any{})))
			case "authentication":
				get(nil, scenariocatalog.Principal{Class: "public"}, catalogProblem(401, "authentication_required"))
				get(nil, scenariocatalog.Principal{Class: "authenticated"}, catalogProblem(422, "validation_failed"))
			default:
				t.Fatal("unimplemented case")
			}
			if !bytes.Equal(before, snapshot()) {
				t.Fatal("webhook read changed stored destinations")
			}
		})
	}
	if len(results) != 4 || requests != 10 || effects != 8 {
		t.Errorf("NEW webhook evidence%d/%d/%d,want4/10/8", len(results), requests, effects)
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
		}{"NEW webhook destinations (outside frozen 598)", len(results), requests, effects, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
