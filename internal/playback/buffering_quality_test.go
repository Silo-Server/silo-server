package playback

import "testing"

func TestBufferingQualityRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name, source, limit      string
		feature, estimate, valid bool
	}{
		{name: "legacy", valid: true},
		{name: "probe", source: "probe", feature: true, estimate: true, valid: true},
		{name: "observed", source: "observed_rebuffer", feature: true, estimate: true, valid: true},
		{name: "clear", limit: "clear", feature: true, valid: true},
		{name: "ignore", limit: "ignore", feature: true, valid: true},
		{name: "missing estimate", source: "probe", feature: true},
		{name: "missing feature", source: "observed_rebuffer", estimate: true},
		{name: "bad source", source: "meter", feature: true, estimate: true},
		{name: "bad control", limit: "reset", feature: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var features []string
			var estimate *int
			if tc.feature {
				features = []string{FeatureBufferingQualityV1}
			}
			if tc.estimate {
				estimate = new(44100)
			}
			err := validateBufferingQualityV3(features, tc.source, tc.limit, estimate)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
