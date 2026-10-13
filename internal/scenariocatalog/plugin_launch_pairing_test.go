package scenariocatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPluginLaunchSelection(t *testing.T) {
	catalogs := loadPairingCatalogs(t)
	selected, err := PluginLaunchAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range RequiredPluginLaunchScenarios {
		for _, failure := range []string{"missing", "authority", "status", "repeat", "requirement", "sequence"} {
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
							case "authority":
								s.Principal.Class = "invalid"
							case "status":
								s.V2Expectation.Expect.Status = 599
							case "repeat":
								s.Request.Repeat++
							case "requirement":
								s.Requires = []string{"database_unavailable"}
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
