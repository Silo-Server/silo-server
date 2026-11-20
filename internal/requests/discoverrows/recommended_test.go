package discoverrows

import "testing"

// A recommended row takes exactly its media type, always hides titles the
// library has, and the two default ones keep their media type.
func TestRecommendedRowNormalize(t *testing.T) {
	row, errs := NormalizeRow(Row{Type: TypeRecommended, Title: "For you", Recommended: &RecommendedSpec{MediaType: " series "}})
	if len(errs) > 0 || row.Recommended.MediaType != MediaSeries || !row.HideAvailable || row.Limit != DefaultLimit {
		t.Fatalf("normalized = %+v %v", row, errs)
	}
	for name, bad := range map[string]Row{
		"no spec":        {Type: TypeRecommended, Title: "x"},
		"mixed media":    {Type: TypeRecommended, Title: "x", Recommended: &RecommendedSpec{MediaType: "all"}},
		"another member": {Type: TypeRecommended, Title: "x", Recommended: &RecommendedSpec{MediaType: MediaMovie}, Chart: &ChartSpec{}},
	} {
		if _, errs := NormalizeRow(bad); len(errs) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	movies, _ := recommendedRow("recommended_movies")
	asSeries := movies
	asSeries.Recommended = &RecommendedSpec{MediaType: MediaSeries}
	asChart := movies
	asChart.Type, asChart.Recommended, asChart.Chart = TypeTMDBChart, nil, &ChartSpec{Preset: presetPopular, MediaType: MediaMovie}
	if _, errs := NormalizeLayout([]Row{asSeries}); len(errs) == 0 {
		t.Error("a default recommended row changed its media type")
	}
	// A custom chart could already use this ID before recommended rows existed.
	if _, errs := NormalizeLayout([]Row{asChart}); len(errs) > 0 {
		t.Errorf("existing chart layout rejected: %v", errs)
	}
	if _, errs := NormalizeLayout(DefaultRows()); len(errs) > 0 {
		t.Fatalf("default layout: %v", errs)
	}
}
