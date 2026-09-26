package scenariocatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequiredSignupFamilyPairingCannotChange(t *testing.T) {
	catalogs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SignupFamilyAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range RequiredSignupFamilyScenarios {
		for _, failure := range []string{"registration", "missing case", "duplicate case", "missing pair", "wrong operation", "wrong method", "wrong route", "changed bearer", "original request", "principal", "pair principal", "requirements", "settings", "sequence", "status", "original body", "translated body"} {
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
							case "registration":
								row.RegistrationIndex = 9
							case "missing case":
								row.Scenarios = append(row.Scenarios[:i], row.Scenarios[i+1:]...)
							case "duplicate case":
								row.Scenarios = append(row.Scenarios, *s)
							case "missing pair":
								s.V2Expectation = nil
							case "wrong operation":
								s.V2Expectation.OperationID = "listProfiles"
							case "wrong method":
								s.V2Expectation.Method = "PATCH"
							case "wrong route":
								s.V2Expectation.Request.Path = "/api/v2/profiles/other"
							case "changed bearer":
								s.V2Expectation.Request.Headers = map[string]*string{inviteCodeAuthorizationHeader: new("Bearer replacement")}
							case "original request":
								s.Request.Repeat = 99
							case "principal":
								s.Principal.Class = "member"
							case "pair principal":
								s.V2Expectation.Principal = &Principal{Class: inviteCodePublicPrincipal}
							case "requirements":
								s.Requires = []string{"invented_requirement"}
							case "settings":
								s.Settings = map[string]string{"demo.enabled": "true"}
							case "sequence":
								s.Then = []Step{{}}
							case "original body":
								s.Request.Body = json.RawMessage(`{"additional_uses":2}`)
							case "translated body":
								s.V2Expectation.Request.Body = json.RawMessage(`{"additional_uses":2}`)
							case "status":
								s.V2Expectation.Expect.Status = 418
							}
							break
						}
					}
				}
				if _, err := SignupFamilyAcceptance(changed); err == nil || (failure != "registration" && !strings.Contains(err.Error(), id)) {
					t.Fatalf("accepted %s: %v", failure, err)
				}
			})
		}
	}
}
