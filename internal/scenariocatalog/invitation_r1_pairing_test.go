package scenariocatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInvitationR1Selection(t *testing.T) {
	catalogs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := InvitationR1Acceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range RequiredInvitationR1Scenarios {
		for _, failure := range []string{"missing", "duplicate", "pair", "authority", "repeat", "requirement", "operation", "body", "sequence", "registration"} {
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
							case "repeat":
								s.Request.Repeat++
							case "requirement":
								s.Requires = []string{"database_unavailable"}
							case "operation":
								s.V2Expectation.OperationID = "getCurrentUser"
							case "body":
								s.V2Expectation.Request.Body = json.RawMessage(`{"different":true}`)
							case "registration":
								r.RegistrationIndex = 0
							case "sequence":
								s.Then = []Step{{}}
							}
							break
						}
					}
				}
				if _, err := InvitationR1Acceptance(changed); err == nil || (failure != "registration" && !strings.Contains(err.Error(), id)) {
					t.Fatalf("accepted %s: %v", failure, err)
				}
			})
		}
	}
}
