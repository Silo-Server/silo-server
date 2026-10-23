package scenariocatalog

import (
	"testing"
)

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
