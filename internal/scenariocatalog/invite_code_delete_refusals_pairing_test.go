package scenariocatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequiredInviteCodeDeleteRefusalsPairingCannotChange(t *testing.T) {
	catalogs := loadPairingCatalogs(t)
	selected, err := InviteCodeDeleteRefusalsAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range RequiredInviteCodeDeleteRefusalsScenarios {
		for _, failure := range []string{"missing case", "changed bearer", "original request", "principal", "pair principal", "requirements", "settings", "sequence", "status"} {
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
							case "changed bearer":
								s.V2Expectation.Request.Headers = map[string]*string{inviteCodeAuthorizationHeader: new("Bearer replacement")}
							case "original request":
								s.Request.Repeat = 2
							case "principal":
								s.Principal.Class = "member"
							case "pair principal":
								s.V2Expectation.Principal = &Principal{Class: inviteCodePublicPrincipal}
							case "requirements":
								s.Requires = []string{"database"}
							case "settings":
								s.Settings = map[string]string{"demo.enabled": "true"}
							case "sequence":
								s.Then = []Step{{}}
							case "status":
								s.V2Expectation.Expect.Status = 200
							}
							break
						}
					}
				}
				if _, err := InviteCodeDeleteRefusalsAcceptance(changed); err == nil || !strings.Contains(err.Error(), id) {
					t.Fatalf("accepted %s: %v", failure, err)
				}
			})
		}
	}
}
