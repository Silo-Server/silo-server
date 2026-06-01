package requests

import "testing"

func inst(kind, id string, def, def4k, anime bool) Integration {
	qp := 1
	return Integration{
		ID: id, Kind: kind, Name: id, Enabled: true, BaseURL: "http://x",
		APIKeyRef: "k", RootFolder: "/std", QualityProfileID: &qp,
		Is4K: def4k, IsDefault: def, IsDefault4K: def4k, AnimeEnabled: anime,
	}
}

func TestRouteTargets(t *testing.T) {
	hd := inst("radarr", "hd", true, false, false)
	uhd := inst("radarr", "uhd", false, true, false)
	hdAnime := inst("radarr", "hda", true, false, true)

	cases := []struct {
		name      string
		req       Request
		ceiling   string
		force     bool
		instances []Integration
		want      []Quality
		wantAnime bool
	}{
		{"hd only, sd user", Request{MediaType: MediaTypeMovie}, "1080p", false, []Integration{hd, uhd}, []Quality{Quality1080p}, false},
		{"4k user dual", Request{MediaType: MediaTypeMovie}, "2160p", false, []Integration{hd, uhd}, []Quality{Quality1080p, Quality2160p}, false},
		{"force dual overrides role", Request{MediaType: MediaTypeMovie}, "1080p", true, []Integration{hd, uhd}, []Quality{Quality1080p, Quality2160p}, false},
		{"4k user but no 4k default", Request{MediaType: MediaTypeMovie}, "2160p", false, []Integration{hd}, []Quality{Quality1080p}, false},
		{"no hd default", Request{MediaType: MediaTypeMovie}, "2160p", false, []Integration{uhd}, []Quality{Quality2160p}, false},
		{"anime on anime-enabled hd", Request{MediaType: MediaTypeMovie, IsAnime: true}, "1080p", false, []Integration{hdAnime}, []Quality{Quality1080p}, true},
		{"empty ceiling hd only", Request{MediaType: MediaTypeMovie}, "", false, []Integration{hd, uhd}, []Quality{Quality1080p}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routeTargets(tc.req, tc.ceiling, Settings{ForceDualQuality: tc.force}, tc.instances)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d targets, want %d (%v)", len(got), len(tc.want), got)
			}
			for i, q := range tc.want {
				if got[i].Quality != q {
					t.Fatalf("target %d quality = %s, want %s", i, got[i].Quality, q)
				}
				if got[i].IsAnime != tc.wantAnime {
					t.Fatalf("target %d isAnime = %v, want %v", i, got[i].IsAnime, tc.wantAnime)
				}
			}
		})
	}
}
