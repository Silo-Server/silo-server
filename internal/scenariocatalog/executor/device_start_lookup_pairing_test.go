package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRequiredDeviceStartLookupAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run device start/lookup acceptance explicitly")
	}
	dsn := os.Getenv(DatabaseEnv)
	if dsn == "" {
		t.Fatal(DatabaseEnv + " required before constructor")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.DeviceStartLookupAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	keyUsageGuard(t, pool)
	pool.Close()
	e := New(t)
	defer e.Reseed()
	var results []Result
	requests, snapshots, created := 0, 0, 0
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
					t.Run(s.ID+"/"+transport, func(t *testing.T) {
						e.Reseed()
						defer e.Reseed()
						limited := e.rowRateLimited(row)
						if limited != (row.RegistrationIndex == 1) {
							t.Fatal("registration/ledger rate-limit mismatch")
						}
						server, err := e.server(true, false, limited)
						if err != nil {
							t.Fatal(err)
						}
						if limited {
							e.resetRateLimits()
						}
						result := Result{ID: s.ID + "/" + transport, Scenario: s.ID, Transport: transport, Catalog: c.File, Row: row.Key().String()}
						defer func() {
							if t.Failed() && len(result.Failures) == 0 {
								result.Failures = []string{"packet effects assertion failed; see log"}
							}
							results = append(results, result)
						}()
						request, expect, method := s.Request, s.Expect, row.Method
						if transport == "v2" {
							pair := s.V2Expectation
							request, expect, method = pair.Request, pair.Expect, pair.Method
							result.OperationID = pair.OperationID
						}
						before := snapshot()
						start := time.Now().UTC()
						var openings []response
						count := max(request.Repeat, 1)
						request.Repeat = 1
						for i := range count {
							assertion := expect
							if i < count-1 {
								status := 404
								if strings.HasPrefix(s.ID, "device_start.") {
									status = 201
								} else if transport == "v2" {
									status = 422
								}
								assertion = scenariocatalog.Expect{Status: status}
							}
							resp, failures, err := e.exchange(server.URL, method, request, s.Principal, assertion, nil, nil, nil)
							requests++
							if err != nil {
								failures = append(failures, err.Error())
							}
							result.Failures = append(result.Failures, failures...)
							if len(failures) > 0 {
								t.Errorf("exchange %d/%d: %v", i+1, count, failures)
							}
							if resp.Status == 201 {
								openings = append(openings, resp)
							}
						}
						end := time.Now().UTC()
						after := snapshot()
						// The transport's own oracle decides how many pairing requests a
						// start opens: v2 refuses a body-less start (415) that v1 defaults.
						expectedCreated := 0
						if strings.HasPrefix(s.ID, "device_start.") {
							if expect.Status == 201 {
								expectedCreated = 1
							}
							if expect.Status == 429 {
								expectedCreated = 10
							}
						}
						if len(openings) != expectedCreated {
							t.Errorf("opened %d want%d", len(openings), expectedCreated)
						}
						assertDeviceOpenings(t, s, transport, server.URL, openings, before["device_login_requests"], after["device_login_requests"], start, end)
						created += len(openings)
						for table, want := range before {
							if table == "device_login_requests" {
								continue
							}
							if !bytes.Equal(want, after[table]) {
								t.Errorf("unexpected full-table effect: %s", table)
							}
						}
						t.Logf("%s registration%d limited=%t: %dHTTP %dcreated,26 full tables", result.ID, row.RegistrationIndex, limited, count, len(openings))
					})
				}
			}
		}
	}
	if err := requiredPairedResults(results, scenariocatalog.RequiredDeviceStartLookupScenarios); err != nil {
		t.Error(err)
	}
	// 98 HTTP exchanges, 76 snapshots; 34 device requests: 17 v1/v2 single
	// starts less the two v2 body-less refusals, plus 10 per rate-limited burst.
	if requests != 98 || snapshots != 76 || created != 34 {
		t.Errorf("counts HTTP/snapshot/created=%d/%d/%d want98/76/34", requests, snapshots, created)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// Match every inserted row to one actual response. No raw device/browser/user
// secret is stored, no account/profile/login token is minted by start or lookup.
func assertDeviceOpenings(t *testing.T, s scenariocatalog.Scenario, transport, base string, responses []response, before, after json.RawMessage, start, end time.Time) {
	t.Helper()
	var old, rows []map[string]any
	if err := json.Unmarshal(before, &old); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &rows); err != nil {
		t.Fatal(err)
	}
	known := map[any]map[string]any{}
	for _, r := range old {
		known[r["id"]] = r
	}
	added := map[string]map[string]any{}
	for _, r := range rows {
		if prev, ok := known[r["id"]]; ok {
			if !reflect.DeepEqual(prev, r) {
				t.Error("existing device request changed")
			}
			delete(known, r["id"])
		} else {
			hash, ok := r["device_code_hash"].(string)
			if !ok {
				t.Fatal("device hash type")
			}
			added[hash] = r
		}
	}
	if len(known) != 0 || len(rows) != len(old)+len(responses) || len(added) != len(responses) {
		t.Fatal("unexpected device additions/deletions")
	}
	var request struct {
		DeviceName     string `json:"device_name"`
		DevicePlatform string `json:"device_platform"`
		ClientPurpose  string `json:"client_purpose"`
		Temporary      bool   `json:"temporary"`
	}
	if len(s.Request.Body) > 0 {
		if err := json.Unmarshal(s.Request.Body, &request); err != nil {
			t.Fatal(err)
		}
	}
	if request.DeviceName == "" {
		request.DeviceName = "silo-scenario-executor/1"
	}
	if request.ClientPurpose == "" {
		request.ClientPurpose = "device_login"
	}
	for _, resp := range responses {
		var body map[string]any
		if err := json.Unmarshal(resp.Raw, &body); err != nil {
			t.Fatal(err)
		}
		stringField := func(key string) string {
			t.Helper()
			v, ok := body[key].(string)
			if !ok {
				t.Fatalf("response %s type", key)
			}
			return v
		}
		device, user, match := stringField("device_code"), stringField("user_code"), stringField("match_code")
		complete, err := url.Parse(stringField("verification_uri_complete"))
		if err != nil {
			t.Fatal(err)
		}
		browser := complete.Query().Get("token")
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(device) || !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(browser) || device == browser {
			t.Error("invalid/distinct secret shape")
		}
		if !regexp.MustCompile(`^[A-Z0-9]{4}-[A-Z0-9]{4}$`).MatchString(user) || !regexp.MustCompile(`^(blue|busy|calm|cozy|fast|gold|kind|soft|tall|tame|tiny|warm) (barn|bell|cart|coop|corn|cow|duck|goat|hay|hen|lamb|milk|oats|pail|pond|pony|rake|shed|silo|wool)$`).MatchString(match) {
			t.Error("user/match code shape")
		}
		if stringField("verification_uri") != base+"/activate" || complete.Scheme+"://"+complete.Host+complete.Path != base+"/activate" || len(complete.Query()) != 1 {
			t.Error("verification authority mismatch")
		}
		if body["expires_in"] != float64(600) || body["interval"] != float64(3) || body["device_name"] != request.DeviceName || body["device_platform"] != request.DevicePlatform || body["client_purpose"] != request.ClientPurpose || body["temporary"] != request.Temporary {
			t.Error("start response defaults/identity")
		}
		r, ok := added[hashDevice(device)]
		if !ok {
			t.Fatal("response lacks newly committed device")
		}
		delete(added, hashDevice(device))
		id, err := uuid.Parse(fmt.Sprint(r["id"]))
		if err != nil || id.Version() != 4 {
			t.Error("new request ID is not UUID4")
		}
		created, err := time.Parse(time.RFC3339Nano, fmt.Sprint(r["created_at"]))
		if err != nil || created.Before(start.Truncate(time.Microsecond)) || created.After(end) {
			t.Error("creation outside application bounds")
		}
		expiry, err := time.Parse(time.RFC3339Nano, fmt.Sprint(r["expires_at"]))
		if err != nil || expiry.Sub(created) != 10*time.Minute {
			t.Error("expiry not600seconds from creation")
		}
		wire, err := time.Parse(time.RFC3339Nano, stringField("expires_at"))
		precision := time.Second
		if transport == "v2" {
			precision = time.Millisecond
		}
		if err != nil || !wire.Equal(expiry.Truncate(precision)) {
			t.Error("expiry projection differs")
		}
		want := map[string]any{"id": r["id"], "device_code_hash": hashDevice(device), "browser_code_hash": hashDevice(browser), "user_code_hash": hashDevice(strings.ReplaceAll(user, "-", "")), "match_code": match, "device_name": request.DeviceName, "device_platform": request.DevicePlatform, "ip_address": "127.0.0.1", "requested_user_agent": "silo-scenario-executor/1", "status": "pending", "client_purpose": request.ClientPurpose, "temporary": request.Temporary, "expires_at": r["expires_at"], "created_at": r["created_at"], "updated_at": r["created_at"], "approved_by_user_id": nil, "approved_profile_id": nil, "auth_session_id": nil, "approved_at": nil, "denied_at": nil, "consumed_at": nil}
		if !reflect.DeepEqual(want, r) {
			t.Error("new device full row/hash/defaults differ")
		}
	}
}
