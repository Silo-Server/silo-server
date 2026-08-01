package displayprefs

import "testing"

func TestPlanLegacyRowParsesHandlerWrittenKeys(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		prefsID string
		client  string
	}{
		{"typical", "jellycompat:displayprefs:usersettings:emby", "usersettings", "emby"},
		{"empty client", "jellycompat:displayprefs:usersettings:", "usersettings", ""},
		{"guid id", "jellycompat:displayprefs:f137a2dd21bbc1b99aa5c0f6bf02a805:jellyfin-web", "f137a2dd21bbc1b99aa5c0f6bf02a805", "jellyfin-web"},
		{"id containing colons", "jellycompat:displayprefs:a:b:emby", "a:b", "emby"},
		{"empty id", "jellycompat:displayprefs::emby", "", "emby"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob, reject := PlanLegacyRow(tc.key, `{"SortBy":"SortName"}`)
			if reject != nil {
				t.Fatalf("rejected: %s", reject.Reason)
			}
			if blob.PrefsID != tc.prefsID || blob.Client != tc.client {
				t.Errorf("parsed (%q, %q), want (%q, %q)", blob.PrefsID, blob.Client, tc.prefsID, tc.client)
			}
			if blob.Value != `{"SortBy":"SortName"}` {
				t.Errorf("value not copied verbatim: %q", blob.Value)
			}
			// The move must be reversible: the legacy key reconstructs exactly.
			if got := LegacyKey(blob.PrefsID, blob.Client); got != tc.key {
				t.Errorf("LegacyKey round-trip = %q, want %q", got, tc.key)
			}
		})
	}
}

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
