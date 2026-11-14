package recipes

import (
	"encoding/json"
	"testing"
)

func TestTrendingDiscoverValidateFilterType(t *testing.T) {
	cases := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{"both, as rows saved before the setting", `{"source":"tmdb","window":"week"}`, false},
		{"movies", `{"source":"tmdb","window":"week","filter_type":"movie"}`, false},
		{"series", `{"source":"tmdb","window":"day","filter_type":"series"}`, false},
		{"an episode type is not a trending title", `{"source":"tmdb","window":"week","filter_type":"episode"}`, true},
		{"TMDB's tv spelling", `{"source":"tmdb","window":"week","filter_type":"tv"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := trendingDiscoverRecipe{}.Validate(json.RawMessage(tc.config))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate(%s) error = %v, want error %v", tc.config, err, tc.wantErr)
			}
		})
	}
}
