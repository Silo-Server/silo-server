package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

// A client that narrows the ceiling asked for less than the profile may see,
// so the profile's allowed titles stop passing it; a client that narrows
// nothing keeps them.
func TestClampMaturityLimitsDropsAllowancesWhenNarrowed(t *testing.T) {
	limits := access.MaturityLimits{MaxContentRating: "PG-13", AllowedTitleIDs: []string{"movie-r"}}
	for _, tc := range []struct {
		requested string
		keep      bool
	}{{"", true}, {"R", true}, {"banana", true}, {"G", false}} {
		got := clampMaturityLimits(limits, tc.requested)
		if kept := len(got.AllowedTitleIDs) > 0; kept != tc.keep {
			t.Errorf("MaxOfficialRating %q: allowances kept = %v, want %v", tc.requested, kept, tc.keep)
		}
	}
}
