package executor

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed testdata/setup_family_originals.json
var setupFamilyOriginals []byte

var setupFamilyIDs = []string{
	"setup.status_headers", "setup.field_shape", "setup.data_meaning",
	"setup.already_complete.r1", "setup.missing_fields.r1", "setup.malformed_json.r1",
	"setup.status_headers.r1", "setup.field_shape.r1", "setup.data_meaning.r1", "setup.rate_limited.r1",
}

type setupFamilyCase struct {
	file     string
	row      scenariocatalog.Row
	scenario scenariocatalog.Scenario
}

func selectSetupFamily(catalogs []*scenariocatalog.Catalog) ([]setupFamilyCase, error) {
	var originals []scenariocatalog.Scenario
	if err := json.Unmarshal(setupFamilyOriginals, &originals); err != nil {
		return nil, err
	}
	if len(originals) != len(setupFamilyIDs) {
		return nil, fmt.Errorf("original count changed")
	}
	var selected []setupFamilyCase
	for _, id := range setupFamilyIDs {
		var found *setupFamilyCase
		for _, c := range catalogs {
			for _, row := range c.Rows {
				for _, s := range row.Scenarios {
					if s.ID != id {
						continue
					}
					if found != nil {
						return nil, fmt.Errorf("duplicate %s", id)
					}
					if row.Method != "POST" || row.Path != "/api/v1/auth/setup" || row.RegistrationIndex != 0 && row.RegistrationIndex != 1 {
						return nil, fmt.Errorf("wrong route %s", id)
					}
					if (row.RegistrationIndex == 1) != strings.HasSuffix(id, ".r1") {
						return nil, fmt.Errorf("wrong registration %s", id)
					}
					if s.V2Expectation == nil {
						return nil, fmt.Errorf("missing pair %s", id)
					}
					if err := scenariocatalog.ValidatePairing(s.V2Expectation); err != nil {
						return nil, err
					}
					pair := s.V2Expectation
					if pair.OperationID != "setupServer" || pair.Method != "POST" || pair.Request.Path != "/api/v2/auth/setup" || pair.Principal != nil || len(pair.Then) != 0 {
						return nil, fmt.Errorf("invalid setup pair %s", id)
					}
					original := s
					original.V2Expectation = nil
					got, _ := json.Marshal(original)
					var want []byte
					for _, o := range originals {
						if o.ID == id {
							want, _ = json.Marshal(o)
						}
					}
					if !bytes.Equal(got, want) {
						return nil, fmt.Errorf("original oracle/intent changed %s", id)
					}
					request := pair.Request
					request.Path = s.Request.Path
					requestJSON, _ := json.Marshal(request)
					originalRequestJSON, _ := json.Marshal(s.Request)
					if !bytes.Equal(requestJSON, originalRequestJSON) {
						return nil, fmt.Errorf("request intent changed %s", id)
					}
					status := 409
					if strings.Contains(id, "missing_fields") {
						status = 422
					}
					if strings.Contains(id, "malformed_json") {
						status = 400
					}
					if strings.Contains(id, "rate_limited") {
						status = 429
					}
					if pair.Expect.Status != status {
						return nil, fmt.Errorf("wrong expected status %s", id)
					}
					found = &setupFamilyCase{c.File, row, s}
				}
			}
		}
		if found == nil {
			return nil, fmt.Errorf("missing %s", id)
		}
		selected = append(selected, *found)
	}
	return selected, nil
}

// Reject wrong resources and unknown keys before New can migrate or reseed.
func guardSetupFamily(t *testing.T) {
	t.Helper()
	u, err := url.Parse(os.Getenv(DatabaseEnv))
	if err != nil || u == nil || u.Scheme != "postgres" || u.Hostname() != "127.0.0.1" || u.Port() == "" || slices.Contains([]string{"55443", "55445", "55446"}, u.Port()) || !strings.HasPrefix(u.Path, "/silo_worker_setup_") || os.Getenv("SILO_WORKER_SETUP_OWNED") != "1" {
		t.Fatal("setup family requires its explicitly owned ephemeral loopback resource")
	}
	pool, err := pgxpool.New(t.Context(), u.String())
	if err != nil {
		t.Fatal("connect owned setup resource")
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('api_keys') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		return
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM api_keys k LEFT JOIN users u ON u.id=k.user_id WHERE NOT (COALESCE(u.email,'')=$1 AND k.label IN ('fixture-unscoped','fixture-scoped') OR COALESCE(u.email,'')=$2 AND k.label='fixture-member-key')`, adminEmail, memberEmail).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("non-fixture API keys: refuse constructor/reseed")
	}
}

func TestRequiredSetupFamilyAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("requires owned setup family resource")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectSetupFamily(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	guardSetupFamily(t)
	e := New(t)
	defer func() { guardSetupFamily(t); e.Reseed(); e.guardScratchDatabase() }()
	var results []Result
	requests, snapshots := 0, 0
	snapshot := func(t *testing.T) map[string]json.RawMessage {
		t.Helper()
		snapshots++
		out := map[string]json.RawMessage{}
		for _, table := range []string{"users", "user_profiles", "api_keys", "server_settings", "auth_sessions", "device_login_requests", "invitations", "invite_codes"} {
			var raw []byte
			if err := e.pool.QueryRow(e.ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM `+table+` t`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			out[table] = raw
		}
		return out
	}
	for _, item := range selected {
		s := item.scenario
		for _, transport := range []string{"v1", "v2"} {
			t.Run(s.ID+"/"+transport, func(t *testing.T) {
				guardSetupFamily(t)
				e.Reseed()
				e.resetRateLimits()
				result := Result{ID: s.ID + "/" + transport, Scenario: s.ID, Transport: transport, Catalog: item.file, Row: item.row.Key().String()}
				defer func() {
					if t.Failed() && len(result.Failures) == 0 {
						result.Failures = append(result.Failures, "assertion failed; see log")
					}
					results = append(results, result)
				}()
				request, expect := s.Request, s.Expect
				if transport == "v2" {
					request, expect = s.V2Expectation.Request, s.V2Expectation.Expect
					result.OperationID = s.V2Expectation.OperationID
				}
				base := e.live.URL
				if item.row.RegistrationIndex == 1 {
					base = e.liveLimited.URL
				}
				count := max(request.Repeat, 1)
				request.Repeat = 1
				for i := range count {
					before := snapshot(t)
					stepExpect := expect
					// The frozen repeated case asserts request seven. Independently assert
					// the six preceding completed-setup refusals without changing its oracle.
					if i < count-1 {
						index := slices.IndexFunc(selected, func(c setupFamilyCase) bool { return c.scenario.ID == "setup.already_complete.r1" })
						stepExpect = selected[index].scenario.Expect
						if transport == "v2" {
							stepExpect = selected[index].scenario.V2Expectation.Expect
						}
					}
					requests++
					_, failures, err := e.exchange(base, "POST", request, s.Principal, stepExpect, nil, nil, nil)
					if err != nil {
						failures = append(failures, err.Error())
					}
					result.Failures = append(result.Failures, failures...)
					if len(failures) > 0 {
						t.Errorf("request %d: %v", i+1, failures)
					}
					after := snapshot(t)
					for table, want := range before {
						if !bytes.Equal(want, after[table]) {
							t.Errorf("%s changed on request %d", table, i+1)
						}
					}
				}
			})
		}
	}
	if err := requiredPairedResults(results, setupFamilyIDs); err != nil {
		t.Error(err)
	}
	if requests != 32 || snapshots != 64 {
		t.Errorf("got %d requests/%d snapshots, want32/64", requests, snapshots)
	}
	t.Logf("setup family: %d results/%d HTTP/%d snapshots/%d full-table observations", len(results), requests, snapshots, snapshots*8)
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

func TestSetupFamilySelection(t *testing.T) {
	load := func() []*scenariocatalog.Catalog {
		t.Helper()
		c, err := scenariocatalog.Load()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if _, err := selectSetupFamily(load()); err != nil {
		t.Fatal(err)
	}
	for _, id := range setupFamilyIDs {
		for _, mutation := range []string{"missing", "duplicate", "unpaired", "oracle", "request", "status", "registration"} {
			t.Run(id+"/"+mutation, func(t *testing.T) {
				catalogs := load()
				for _, c := range catalogs {
					for ri := range c.Rows {
						row := &c.Rows[ri]
						for si := range row.Scenarios {
							s := &row.Scenarios[si]
							if s.ID != id {
								continue
							}
							switch mutation {
							case "missing":
								s.ID = "removed"
							case "duplicate":
								row.Scenarios = append(row.Scenarios, *s)
							case "unpaired":
								s.V2Expectation = nil
							case "oracle":
								s.Expect.Status = 299
							case "request":
								s.V2Expectation.Request.RawBody = new("changed")
							case "status":
								s.V2Expectation.Expect.Status = 299
							case "registration":
								row.RegistrationIndex = 9
							}
							break
						}
					}
				}
				if _, err := selectSetupFamily(catalogs); err == nil {
					t.Fatal("invalid packet accepted")
				}
			})
		}
	}
}
