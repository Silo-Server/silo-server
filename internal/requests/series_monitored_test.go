package requests

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// sonarrSeriesJSON is how Sonarr answers /api/v3/series?tvdbId=.
const sonarrSeriesJSON = `[
	{"id": 7, "tvdbId": 423022, "monitored": true, "seasons": [
		{"seasonNumber": 0, "monitored": true},
		{"seasonNumber": 1, "monitored": false},
		{"seasonNumber": 2, "monitored": true}
	]},
	{"id": 8, "tvdbId": 423022, "monitored": false, "seasons": [
		{"seasonNumber": 3, "monitored": true}
	]}
]`

func TestFindSonarrSeriesAndMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/series" || r.URL.Query().Get("tvdbId") != "423022" || r.Header.Get("X-Api-Key") != "key" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(sonarrSeriesJSON))
	}))
	defer srv.Close()

	series, err := findSonarrSeries(t.Context(), srv.URL+"/", "key", 423022)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]bool{}
	mergeMonitoredSeasons(got, series)
	// Specials and the unmonitored series' seasons stay out.
	if len(got) != 1 || !got[2] {
		t.Fatalf("monitored = %v, want only season 2", got)
	}
}

func TestFindSonarrSeriesReportsServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, err := findSonarrSeries(t.Context(), srv.URL, "bad", 1); err == nil {
		t.Fatal("a 401 must be an error")
	}
}

func TestMarkMonitoredSeasons(t *testing.T) {
	seasons := []RequestSeason{{Number: 1}, {Number: 2}, {Number: 3, Requested: true}}
	markMonitoredSeasons(seasons, map[int]bool{2: true})
	if seasons[0].Requested || !seasons[1].Requested || !seasons[2].Requested {
		t.Fatalf("seasons = %+v, want 2 marked and 3 kept", seasons)
	}
}

func TestHasOpenRequestSeason(t *testing.T) {
	held := RequestSeason{Number: 1, Availability: AvailabilityAvailable}
	requested := RequestSeason{Number: 2, Availability: AvailabilityMissing, Requested: true}
	open := RequestSeason{Number: 3, Availability: AvailabilityPartial}
	if hasOpenRequestSeason([]RequestSeason{held, requested}) {
		t.Fatal("held and requested seasons reported open")
	}
	if !hasOpenRequestSeason([]RequestSeason{held, open}) {
		t.Fatal("partial season not reported open")
	}
}

func TestIsSonarrConnection(t *testing.T) {
	sonarr := Integration{Enabled: true, CapabilityID: "arr", BaseURL: "http://sonarr:8989", APIKeyRef: "key",
		PluginConfig: map[string]any{"service_kind": "sonarr"}}
	if !isSonarrConnection(sonarr) {
		t.Fatal("enabled Sonarr connection not recognized")
	}
	radarr := sonarr
	radarr.PluginConfig = map[string]any{"service_kind": "radarr"}
	disabled := sonarr
	disabled.Enabled = false
	keyless := sonarr
	keyless.APIKeyRef = ""
	for name, in := range map[string]Integration{"radarr": radarr, "disabled": disabled, "keyless": keyless} {
		if isSonarrConnection(in) {
			t.Fatalf("%s counted as a Sonarr connection", name)
		}
	}
}
