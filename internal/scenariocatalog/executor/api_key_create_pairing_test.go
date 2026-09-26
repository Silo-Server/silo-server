package executor

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredAPIKeyCreateAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-api-key-creations for required paired acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.APIKeyCreateAcceptance(catalogs)
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
							if err := e.pool.QueryRow(e.ctx, `SELECT COALESCE(jsonb_object_agg(id::text,to_jsonb(k)),'{}') FROM api_keys k`).Scan(&raw); err != nil {
								t.Fatal(err)
							}
							var rows map[string]json.RawMessage
							if err := json.Unmarshal(raw, &rows); err != nil {
								t.Fatal(err)
							}
							return rows
						}
						before := snapshot()
						if len(before) != 3 {
							t.Fatal("API-key fixture must contain exactly three keys")
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
						response, failures, err := e.exchange(e.live.URL, method, request, principal, expect, nil, nil, nil)
						if err != nil {
							failures = append(failures, err.Error())
						}
						result.Failures = append(result.Failures, failures...)
						if len(failures) > 0 {
							t.Errorf("frozen exchange: %v", failures)
						}
						after := snapshot()
						checkCreatedAPIKey(t, response.Raw, transport, s.ID, e.fixtures["member_user_id"], before, after)
						if len(after) != len(before)+1 {
							t.Error("unexpected API-key row count after creation")
						}
						for id, want := range before {
							if !bytes.Equal(want, after[id]) {
								t.Error("API-key rows changed during creation")
							}
						}
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredAPIKeyCreateScenarios); err != nil {
		t.Error(err)
	}
	if requests != 8 || effects != 16 {
		t.Errorf("paired creation evidence %dHTTP/%dPG, want8/16", requests, effects)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// checkCreatedAPIKey links the receipt to the persisted row without logging credentials.
func checkCreatedAPIKey(t *testing.T, raw []byte, transport, scenario, member string, before, after map[string]json.RawMessage) {
	t.Helper()
	var receipt struct {
		ID        json.RawMessage
		Key       string
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal("invalid creation receipt")
	}
	id := string(receipt.ID)
	if transport == "v2" {
		if err := json.Unmarshal(receipt.ID, &id); err != nil {
			t.Fatal("invalid string receipt ID")
		}
	}
	if _, exists := before[id]; exists {
		t.Fatal("creation reused an existing row ID")
	}
	var row struct {
		ID         int64      `json:"id"`
		UserID     int64      `json:"user_id"`
		Key        string     `json:"api_key"`
		Label      string     `json:"label"`
		Scopes     []string   `json:"scopes"`
		RateTier   string     `json:"rate_tier"`
		CreatedAt  time.Time  `json:"created_at"`
		LastUsedAt *time.Time `json:"last_used_at"`
	}
	if err := json.Unmarshal(after[id], &row); err != nil {
		t.Fatal("receipt row missing or invalid")
	}
	scopes, label := []string{}, "fixture-created"
	if scenario == "keys_create.scoped" {
		scopes, label = []string{"admin:users"}, "scoped"
	}
	if strconv.FormatInt(row.ID, 10) != id || strconv.FormatInt(row.UserID, 10) != member || row.Key != receipt.Key || row.Key == "" || row.Label != label || row.RateTier != "standard" || !slices.Equal(row.Scopes, scopes) || row.LastUsedAt != nil {
		t.Error("created row does not match exact receipt/account/normalized configuration")
	}
	created := row.CreatedAt
	if transport == "v2" {
		created = created.Truncate(time.Millisecond)
	}
	if !receipt.CreatedAt.Equal(created) {
		t.Error("creation receipt timestamp does not match stored row")
	}
}
