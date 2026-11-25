package handlers

import "testing"

// Listing episode cards take the series' episode labels and the episode's air
// date from the card context every catalog listing loads (browse, search,
// Explore All of a home row). A numbered series sends neither.
func TestApplyEpisodeBrowseMetadataEpisodeLabels(t *testing.T) {
	for _, tt := range []struct {
		name               string
		meta               episodeBrowseMetadata
		wantLabels, wantOn string
	}{
		{"date-labeled", episodeBrowseMetadata{EpisodeLabels: "air_date", AirDate: "2026-11-20"}, "air_date", "2026-11-20"},
		{"numbered", episodeBrowseMetadata{AirDate: "2026-11-20"}, "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var resp itemListResponse
			applyEpisodeBrowseMetadata(&resp, tt.meta)
			if resp.EpisodeLabels != tt.wantLabels || resp.AirDate != tt.wantOn {
				t.Fatalf("labels, air date = %q, %q; want %q, %q", resp.EpisodeLabels, resp.AirDate, tt.wantLabels, tt.wantOn)
			}
		})
	}
}
