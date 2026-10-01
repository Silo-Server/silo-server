package scenariocatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequiredInviteCodeTopUpRefusalsPairingCannotChange(t *testing.T) {
	catalogs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := InviteCodeTopUpRefusalsAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range RequiredInviteCodeTopUpRefusalsScenarios {
		for _, failure := range []string{"missing case", "duplicate case", "missing pair", "wrong operation", "wrong method", "wrong route", "changed bearer", "original request", "principal", "pair principal", "requirements", "settings", "sequence", "status", "original body", "translated body"} {
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
							case "duplicate case":
								row.Scenarios = append(row.Scenarios, *s)
							case "missing pair":
								s.V2Expectation = nil
							case "wrong operation":
								s.V2Expectation.OperationID = "listProfiles"
							case "wrong method":
								s.V2Expectation.Method = "PUT"
							case "wrong route":
								s.V2Expectation.Request.Path = "/api/v2/profiles"
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
							case "original body":
								s.Request.Body = json.RawMessage(`{"additional_uses":2}`)
							case "translated body":
								s.V2Expectation.Request.Body = json.RawMessage(`{"additional_uses":2}`)
							case "status":
								s.V2Expectation.Expect.Status = 200
							}
							break
						}
					}
				}
				if _, err := InviteCodeTopUpRefusalsAcceptance(changed); err == nil || !strings.Contains(err.Error(), id) {
					t.Fatalf("accepted %s: %v", failure, err)
				}
			})
		}
	}
}
