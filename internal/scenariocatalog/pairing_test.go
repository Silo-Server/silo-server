package scenariocatalog

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Silo-Server/silo-server/contracts/api/v2/scenarios"
)

func TestPairingSchemaAndOperation(t *testing.T) {
	for _, field := range []string{"method", "request", "expect", "operation_id", "headers", "body", "wrong_operation", "wrong_method", "wrong_path"} {
		t.Run(field, func(t *testing.T) {
			schema, _ := scenarios.FS.ReadFile(scenarios.SchemaPath)
			raw, _ := scenarios.FS.ReadFile("api/api-v1-profiles.json")
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			row := doc["rows"].([]any)[0].(map[string]any)
			pair := row["scenarios"].([]any)[0].(map[string]any)["v2_expectation"].(map[string]any)
			switch field {
			case "headers", "body":
				delete(pair["expect"].(map[string]any), field)
			case "wrong_operation":
				pair["operation_id"] = "deleteProfile"
			case "wrong_method":
				pair["method"] = "DELETE"
			case "wrong_path":
				pair["request"].(map[string]any)["path"] = "/api/v2/profiles/other"
			default:
				delete(pair, field)
			}
			raw, _ = json.Marshal(doc)
			_, err := load(fstest.MapFS{scenarios.SchemaPath: {Data: schema}, "api/api-v1-profiles.json": {Data: raw}})
			if err == nil {
				t.Fatalf("accepted invalid %s", field)
			}
		})
	}
}

func TestRequiredDevicePairingCannotShrink(t *testing.T) {
	for _, id := range RequiredDeviceListScenarios {
		for _, remove := range []bool{false, true} {
			catalogs := loadPairingCatalogs(t)
			pilot, err := DeviceListAcceptance(catalogs)
			if err != nil {
				t.Fatal(err)
			}
			rows := pilot[0].Rows[0].Scenarios
			for i := range rows {
				if rows[i].ID != id {
					continue
				}
				if remove {
					pilot[0].Rows[0].Scenarios = append(rows[:i], rows[i+1:]...)
				} else {
					rows[i].V2Expectation = nil
				}
				break
			}
			if _, err := DeviceListAcceptance(pilot); err == nil || !strings.Contains(err.Error(), id) {
				t.Fatalf("missing %s accepted: %v", id, err)
			}
		}
	}
}

func TestRequiredDevicePairingRejectsDuplicate(t *testing.T) {
	catalogs := loadPairingCatalogs(t)
	pilot, err := DeviceListAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	pilot[0].Rows[0].Scenarios = append(pilot[0].Rows[0].Scenarios, pilot[0].Rows[0].Scenarios[0])
	if _, err := DeviceListAcceptance(pilot); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestV2FollowupSchemaAndOperation(t *testing.T) {
	for _, kind := range []string{"valid", "unknown field", "wrong operation", "wrong method", "bad pointer", "authority header", "undeclared query", "too many requests"} {
		t.Run(kind, func(t *testing.T) {
			schema, _ := scenarios.FS.ReadFile(scenarios.SchemaPath)
			raw, _ := scenarios.FS.ReadFile("api/api-v1-profiles.json")
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			pair := doc["rows"].([]any)[0].(map[string]any)["scenarios"].([]any)[0].(map[string]any)["v2_expectation"].(map[string]any)
			binding := map[string]any{"pointer": "/page/next_cursor", "query": "cursor"}
			step := map[string]any{"operation_id": "listDevices", "method": "GET", "request": map[string]any{"path": "/api/v2/devices"}, "expect": map[string]any{"status": 200}, "from_previous": []any{binding}}
			switch kind {
			case "unknown field":
				binding["body_pointer"] = "/x"
			case "wrong operation":
				step["operation_id"] = "listProfiles"
			case "wrong method":
				step["method"] = "DELETE"
			case "bad pointer":
				binding["pointer"] = "/bad~2"
			case "authority header":
				delete(binding, "query")
				binding["request_header"] = "Authorization"
			case "undeclared query":
				binding["query"] = "unknown"
			case "too many requests":
				step["request"].(map[string]any)["repeat"] = 16
			}
			pair["then"] = []any{step}
			raw, _ = json.Marshal(doc)
			_, err := load(fstest.MapFS{scenarios.SchemaPath: {Data: schema}, "api/api-v1-profiles.json": {Data: raw}})
			if (err == nil) != (kind == "valid") {
				t.Fatalf("%s: %v", kind, err)
			}
		})
	}
}

func TestRequiredProfileMutationPairingCannotShrink(t *testing.T) {
	testMutationPairingCannotShrink(t, RequiredProfileMutationScenarios, ProfileMutationAcceptance)
}

func testMutationPairingCannotShrink(t *testing.T, required []string, selectCases func([]*Catalog) ([]*Catalog, error)) {
	t.Helper()
	for _, id := range required {
		for _, failure := range []string{"missing case", "missing pair", "missing read-after"} {
			t.Run(id+"/"+failure, func(t *testing.T) {
				catalogs := loadPairingCatalogs(t)
				pilot, err := selectCases(catalogs)
				if err != nil {
					t.Fatal(err)
				}
				for _, c := range pilot {
					for ri := range c.Rows {
						row := &c.Rows[ri]
						for i := range row.Scenarios {
							s := &row.Scenarios[i]
							if s.ID != id {
								continue
							}
							switch failure {
							case "missing case":
								row.Scenarios = append(row.Scenarios[:i], row.Scenarios[i+1:]...)
							case "missing pair":
								s.V2Expectation = nil
							case "missing read-after":
								s.V2Expectation.Then = nil
							}
							break
						}
					}
				}
				if _, err := selectCases(pilot); err == nil || !strings.Contains(err.Error(), id) {
					t.Fatalf("accepted %s: %v", failure, err)
				}
			})
		}
	}
}

func TestRequiredAccountCapabilityPairingCannotShrink(t *testing.T) {
	for _, id := range RequiredAccountCapabilityScenarios {
		for _, failure := range []string{"missing case", "missing pair", "unsupported requirements"} {
			t.Run(id+"/"+failure, func(t *testing.T) {
				catalogs := loadPairingCatalogs(t)
				selected, err := AccountCapabilityAcceptance(catalogs)
				if err != nil {
					t.Fatal(err)
				}
				for _, c := range selected {
					for ri := range c.Rows {
						row := &c.Rows[ri]
						for i := range row.Scenarios {
							s := &row.Scenarios[i]
							if s.ID != id {
								continue
							}
							switch failure {
							case "missing case":
								row.Scenarios = append(row.Scenarios[:i], row.Scenarios[i+1:]...)
							case "missing pair":
								s.V2Expectation = nil
							case "unsupported requirements":
								s.Requires = []string{"unhandled"}
							}
							break
						}
					}
				}
				if _, err := AccountCapabilityAcceptance(selected); err == nil || !strings.Contains(err.Error(), id) {
					t.Fatalf("accepted %s: %v", failure, err)
				}
			})
		}
	}
}
