package scenariocatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKeyAuthorityUsageSelector(t *testing.T) {
	catalogs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := KeyAuthorityUsageAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range RequiredKeyAuthorityUsageScenarios {
		for _, change := range []string{"missing", "duplicate", "pair", "principal", "oracle", "repeat", "method", "operation", "pair principal", "pair path", "requirements"} {
			t.Run(id+"/"+change, func(t *testing.T) {
				var cs []*Catalog
				if err := json.Unmarshal(data, &cs); err != nil {
					t.Fatal(err)
				}
				for _, c := range cs {
					for ri := range c.Rows {
						r := &c.Rows[ri]
						for i := range r.Scenarios {
							s := &r.Scenarios[i]
							if s.ID != id {
								continue
							}
							switch change {
							case "missing":
								r.Scenarios = append(r.Scenarios[:i], r.Scenarios[i+1:]...)
							case "duplicate":
								r.Scenarios = append(r.Scenarios, *s)
							case "pair":
								s.V2Expectation = nil
							case "principal":
								s.Principal.Class = "authenticated"
							case "oracle":
								s.Expect.Status = 418
							case "repeat":
								s.Request.Repeat = 2
							case "method":
								r.Method = "PATCH"
							case "operation":
								s.V2Expectation.OperationID = "login"
							case "pair principal":
								s.V2Expectation.Principal = &Principal{Class: "authenticated"}
							case "pair path":
								s.V2Expectation.Request.Path = "/api/v2/account/other"
							case "requirements":
								s.Requires = []string{"database"}
							}
							break
						}
					}
				}
				if _, err := KeyAuthorityUsageAcceptance(cs); err == nil || (change != "method" && !strings.Contains(err.Error(), id)) {
					t.Fatalf("accepted %s: %v", change, err)
				}
			})
		}
	}
}
