package scenariocatalog

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Silo-Server/silo-server/contracts/api/v2/scenarios"
)

func sequenceFixture(t *testing.T, id string) (Catalog, int, int) {
	t.Helper()
	raw, err := scenarios.FS.ReadFile("api/api-v1-auth.json")
	if err != nil {
		t.Fatal(err)
	}
	var c Catalog
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	for i, row := range c.Rows {
		for j, s := range row.Scenarios {
			if s.ID != id {
				continue
			}
			op := "getDeviceLogin"
			if row.Method == "POST" {
				op = "pollDeviceLogin"
			}
			request := s.Request
			request.Path = strings.Replace(request.Path, "/api/v1/", "/api/v2/", 1)
			c.Rows[i].Scenarios[j].V2Expectation = &V2Expectation{Kind: "intentional_difference", Summary: "Boundary fixture only", RecordedIn: "internal/scenariocatalog/sequence_originals_test.go", OperationID: op, Method: row.Method, Request: request, Expect: Expect{Status: 429, Headers: []HeaderAssertion{}, Body: []BodyAssertion{}}}
			return c, i, j
		}
	}
	t.Fatal("missing frozen original")
	return c, 0, 0
}

func TestFrozenSequenceAccommodation(t *testing.T) {
	mutations := []struct {
		name   string
		change func(*Row, *Scenario)
	}{
		{"exact", func(*Row, *Scenario) {}},
		{"forged ID", func(_ *Row, s *Scenario) { s.ID = "invented.rate_limited.r1" }},
		{"other frozen ID", func(_ *Row, s *Scenario) {
			if s.ID == "device_lookup.rate_limited.r1" {
				s.ID = "device_poll.rate_limited.r1"
			} else {
				s.ID = "device_lookup.rate_limited.r1"
			}
		}},
		{"registration", func(r *Row, _ *Scenario) { r.RegistrationIndex = 0 }},
		{"listener", func(r *Row, _ *Scenario) { r.Listener = "worker" }},
		{"row path", func(r *Row, _ *Scenario) { r.Path += "/extra" }},
		{"row method", func(r *Row, _ *Scenario) { r.Method = "DELETE" }},
		{"original repeat", func(_ *Row, s *Scenario) { s.Request.Repeat++ }},
		{"original oracle", func(_ *Row, s *Scenario) { s.Expect.Status = 200 }},
		{"original principal", func(_ *Row, s *Scenario) { s.Principal.Class = "admin" }},
		{"original requirements", func(_ *Row, s *Scenario) { s.Requires = nil }},
		{"original fresh state", func(_ *Row, s *Scenario) { s.FreshState = false }},
		{"original settings", func(_ *Row, s *Scenario) { s.Settings = map[string]string{"ratelimit.enabled": "false"} }},
		{"original followup", func(_ *Row, s *Scenario) { s.Then = []Step{{Method: "GET"}} }},
		{"original request", func(_ *Row, s *Scenario) { s.Request.Query = map[string]string{"token": "x"} }},
		{"over limit", func(_ *Row, s *Scenario) { s.V2Expectation.Request.Repeat++ }},
		{"shortened", func(_ *Row, s *Scenario) { s.V2Expectation.Request.Repeat = 16 }},
		{"overflow", func(_ *Row, s *Scenario) { s.V2Expectation.Request.Repeat = int(^uint(0) >> 1) }},
		{"negative", func(_ *Row, s *Scenario) { s.V2Expectation.Request.Repeat = -1 }},
		{"extra step", func(_ *Row, s *Scenario) { s.V2Expectation.Then = []V2Step{deviceStep()} }},
		{"split steps", func(_ *Row, s *Scenario) {
			s.V2Expectation.Request.Repeat = 16
			s.V2Expectation.Then = []V2Step{deviceStep()}
		}},
		{"operation", func(_ *Row, s *Scenario) { s.V2Expectation.OperationID = "listDevices" }},
		{"method", func(_ *Row, s *Scenario) { s.V2Expectation.Method = "DELETE" }},
		{"path", func(_ *Row, s *Scenario) { s.V2Expectation.Request.Path += "/extra" }},
		{"query", func(_ *Row, s *Scenario) { s.V2Expectation.Request.Query = map[string]string{"token": "x"} }},
		{"headers", func(_ *Row, s *Scenario) {
			s.V2Expectation.Request.Headers = map[string]*string{"Authorization": new("x")}
		}},
		{"body", func(_ *Row, s *Scenario) { s.V2Expectation.Request.Body = json.RawMessage(`{"device_code":"changed"}`) }},
		{"bodyref", func(_ *Row, s *Scenario) { s.V2Expectation.Request.BodyRef = "another" }},
		{"rawbody", func(_ *Row, s *Scenario) { s.V2Expectation.Request.RawBody = new("{}") }},
		{"multipart", func(_ *Row, s *Scenario) { s.V2Expectation.Request.Multipart = &Multipart{} }},
		{"principal", func(_ *Row, s *Scenario) { s.V2Expectation.Principal = &Principal{Class: "public"} }},
		{"status", func(_ *Row, s *Scenario) { s.V2Expectation.Expect.Status = 200 }},
	}
	schema, err := scenarios.FS.ReadFile(scenarios.SchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"device_lookup.rate_limited.r1", "device_poll.rate_limited.r1"} {
		for _, tc := range mutations {
			t.Run(id+"/"+tc.name, func(t *testing.T) {
				c, i, j := sequenceFixture(t, id)
				r := &c.Rows[i]
				s := &r.Scenarios[j]
				tc.change(r, s)
				want := tc.name == "exact"
				if err := ValidateScenarioPairing(*r, *s); (err == nil) != want {
					t.Fatalf("contextual boundary: %v", err)
				}
				if ValidateV2Sequence(s.V2Expectation) == nil && s.V2Expectation.Request.Repeat > 16 {
					t.Fatal("context-free cap bypassed")
				}
				// Preserve the source document's explicit empty assertion arrays;
				// typed Expect intentionally omits empty fields when marshaled.
				source, _ := scenarios.FS.ReadFile("api/api-v1-auth.json")
				var doc map[string]any
				if err := json.Unmarshal(source, &doc); err != nil {
					t.Fatal(err)
				}
				rowDoc := doc["rows"].([]any)[i].(map[string]any)
				rowDoc["listener"], rowDoc["method"], rowDoc["path"], rowDoc["registration_index"] = r.Listener, r.Method, r.Path, r.RegistrationIndex
				encoded, err := json.Marshal(s)
				if err != nil {
					t.Fatal(err)
				}
				var scenarioDoc map[string]any
				if err := json.Unmarshal(encoded, &scenarioDoc); err != nil {
					t.Fatal(err)
				}
				expectDoc := scenarioDoc["v2_expectation"].(map[string]any)["expect"].(map[string]any)
				expectDoc["headers"], expectDoc["body"] = []any{}, []any{}
				rowDoc["scenarios"].([]any)[j] = scenarioDoc
				raw, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				_, err = load(fstest.MapFS{scenarios.SchemaPath: {Data: schema}, "api/api-v1-auth.json": {Data: raw}})
				if (err == nil) != want {
					t.Fatalf("loader boundary: %v", err)
				}
			})
		}
	}
}

func TestSequenceAccommodationKeepsDefaultGuards(t *testing.T) {
	for _, tc := range []struct {
		name           string
		repeat, follow int
		malformed      bool
		ok             bool
	}{
		{name: "default sixteen", repeat: 16, ok: true},
		{name: "default seventeen", repeat: 17},
		{name: "combined sixteen", repeat: 15, follow: 1, ok: true},
		{name: "combined seventeen", repeat: 16, follow: 1},
		{name: "followup overflow", repeat: 1, follow: int(^uint(0) >> 1)},
		{name: "binding guard", repeat: 1, follow: 1, malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair := &V2Expectation{OperationID: "listDevices", Method: "GET", Request: Request{Path: "/api/v2/devices", Repeat: tc.repeat}}
			if tc.follow != 0 {
				step := deviceStep()
				step.Request.Repeat = tc.follow
				if tc.malformed {
					step.FromPrevious = []ResponseBinding{{Header: "ETag", RequestHeader: "Authorization"}}
				}
				pair.Then = []V2Step{step}
			}
			s := Scenario{ID: "unlisted", V2Expectation: pair}
			if err := ValidateScenarioPairing(Row{}, s); (err == nil) != tc.ok {
				t.Fatalf("default guards: %v", err)
			}
		})
	}
}
