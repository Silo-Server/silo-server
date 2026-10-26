package autoscan

import "testing"

// differsFrom must compare what the plugin is handed (ConnectionResolver):
// the linked Requests integration when there is one, the row's base URL
// otherwise.
func TestConnectionUpstreamDiffersFrom(t *testing.T) {
	str := func(s string) *string { return &s }
	for name, tc := range map[string]struct {
		old  connectionUpstream
		new  Connection
		want bool
	}{
		"same base url": {
			old: connectionUpstream{baseURL: str("http://sonarr.invalid")},
			new: Connection{Kind: "radarr", BaseURL: "http://sonarr.invalid"},
		},
		"base url change": {
			old:  connectionUpstream{baseURL: str("http://sonarr.invalid")},
			new:  Connection{BaseURL: "http://sonarr-4k.invalid"},
			want: true,
		},
		"linked row ignores its stored base url": {
			old: connectionUpstream{baseURL: str("http://stale.invalid"), integrationID: str("sonarr")},
			new: Connection{RequestIntegrationID: str("sonarr")},
		},
		"switching integration": {
			old:  connectionUpstream{integrationID: str("sonarr")},
			new:  Connection{RequestIntegrationID: str("radarr")},
			want: true,
		},
		"linking a direct connection": {
			old:  connectionUpstream{baseURL: str("http://sonarr.invalid")},
			new:  Connection{BaseURL: "http://sonarr.invalid", RequestIntegrationID: str("sonarr")},
			want: true,
		},
		"unlinking": {
			old:  connectionUpstream{baseURL: str("http://sonarr.invalid"), integrationID: str("sonarr")},
			new:  Connection{BaseURL: "http://sonarr.invalid"},
			want: true,
		},
		"blank link is no link": {
			old: connectionUpstream{baseURL: str("http://sonarr.invalid"), integrationID: str(" ")},
			new: Connection{BaseURL: "http://sonarr.invalid"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.old.differsFrom(tc.new); got != tc.want {
				t.Fatalf("differsFrom = %v, want %v", got, tc.want)
			}
		})
	}
}
