package scenariocatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPluginLaunchSelection(t *testing.T) {
	catalogs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := PluginLaunchAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range RequiredPluginLaunchScenarios {
		for _, failure := range []string{"missing", "duplicate", "pair", "authority", "status", "repeat", "requirement", "operation", "path", "sequence"} {
			t.Run(id+"/"+failure, func(t *testing.T) {
				var changed []*Catalog
				if err := json.Unmarshal(data, &changed); err != nil {
					t.Fatal(err)
				}
				for _, c := range changed {
					for ri := range c.Rows {
						r := &c.Rows[ri]
						for i := range r.Scenarios {
							s := &r.Scenarios[i]
							if s.ID != id {
								continue
							}
							switch failure {
							case "missing":
								r.Scenarios = append(r.Scenarios[:i], r.Scenarios[i+1:]...)
							case "duplicate":
								r.Scenarios = append(r.Scenarios, *s)
							case "pair":
								s.V2Expectation = nil
							case "authority":
								s.Principal.Class = "invalid"
							case "status":
								s.V2Expectation.Expect.Status = 599
							case "repeat":
								s.Request.Repeat++
							case "requirement":
								s.Requires = []string{"database_unavailable"}
							case "operation":
								s.V2Expectation.OperationID = "getCurrentUser"
							case "path":
								s.V2Expectation.Request.Path = "/api/v2/auth/plugin-launch/other"
							case "sequence":
								s.V2Expectation.Then = []V2Step{{}}
							}
							break
						}
					}
				}
				if _, err := PluginLaunchAcceptance(changed); err == nil || !strings.Contains(err.Error(), id) {
					t.Fatalf("accepted %s: %v", failure, err)
				}
			})
		}
	}
}
