package scenariocatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequiredOAuthRefusalPairingCannotChange(t *testing.T) {
	catalogs := loadPairingCatalogs(t)
	selected, err := OAuthRefusalAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 {
		t.Fatalf("selected %d catalogs, want the oauth and auth catalogs", len(selected))
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range RequiredOAuthRefusalScenarios {
		for _, failure := range []string{"missing case", "original request", "original status", "pair status", "principal", "pair principal", "requirements", "sequence", "pair sequence"} {
			t.Run(id+"/"+failure, func(t *testing.T) {
				var changed []*Catalog
				if err := json.Unmarshal(data, &changed); err != nil {
					t.Fatal(err)
				}
				for _, c := range changed {
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
							case "original request":
								s.Request.Repeat = 2
							case "original status":
								s.Expect.Status = 418
							case "pair status":
								s.V2Expectation.Expect.Status = 418
							case "principal":
								s.Principal = Principal{Class: "public", User: "member"}
								if id != "me.disabled_account" {
									// A public handshake principal is the original; refuse a repeat instead.
									s.V2Expectation.Request.Repeat = 2
								}
							case "pair principal":
								s.V2Expectation.Principal = &Principal{Class: "authenticated"}
							case "requirements":
								s.Requires = []string{"plugin_runtime"}
							case "sequence":
								s.Then = []Step{{}}
							case "pair sequence":
								s.V2Expectation.Then = []V2Step{{}}
							}
							break
						}
					}
				}
				if _, err := OAuthRefusalAcceptance(changed); err == nil || !strings.Contains(err.Error(), id) {
					t.Fatalf("accepted %s: %v", failure, err)
				}
			})
		}
	}
}
