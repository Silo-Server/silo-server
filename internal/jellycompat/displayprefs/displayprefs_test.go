package displayprefs

import "testing"

func TestPlanLegacyRowRejectsNonDisplayprefsRows(t *testing.T) {
	for _, key := range []string{
		"jellycompat:something-else",     // extension-bag invention outside displayprefs
		"jellycompat:displayprefs:plain", // no id:client separator
		"jellycompat:displayprefs:",      // empty remainder
	} {
		blob, reject := PlanLegacyRow(key, "whatever")
		if blob != nil {
			t.Errorf("%q parsed as a blob (%q, %q); want reject", key, blob.PrefsID, blob.Client)
			continue
		}
		if reject == nil {
			t.Errorf("%q produced neither blob nor reject", key)
			continue
		}
		if reject.Key != key || reject.Value != "whatever" || reject.Reason == "" {
			t.Errorf("%q reject is incomplete: %+v", key, reject)
		}
	}
}
