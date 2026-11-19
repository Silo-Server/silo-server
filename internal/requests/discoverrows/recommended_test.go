package discoverrows

import "testing"

// A recommended row takes exactly its media type, always hides titles the
// library has, and the two default ones keep their type and media type.
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
	for name, rows := range map[string][]Row{"media type": {asSeries}, "type": {asChart}} {
		if _, errs := NormalizeLayout(rows); len(errs) == 0 {
			t.Errorf("a default recommended row changed its %s", name)
		}
	}
	if _, errs := NormalizeLayout(DefaultRows()); len(errs) > 0 {
		t.Fatalf("default layout: %v", errs)
	}
}
