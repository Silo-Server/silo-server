package playback

import (
	"slices"
	"testing"
)

func TestPinAttemptStickyFeaturesV3(t *testing.T) {
	tests := []struct {
		name       string
		requested  []string
		negotiated []string
		want       []string
	}{
		{
			name:       "a replan cannot drop a negotiated sticky feature",
			requested:  []string{FeaturePlaybackPlanV3},
			negotiated: []string{FeaturePlaybackPlanV3, FeatureHeaderAuthenticatedMediaV3, FeatureSoftwareVideoDecodeV3},
			want:       []string{FeaturePlaybackPlanV3, FeatureHeaderAuthenticatedMediaV3, FeatureSoftwareVideoDecodeV3},
		},
		{
			name:       "a replan cannot add a sticky feature mid-attempt",
			requested:  []string{FeaturePlaybackPlanV3, FeatureHeaderAuthenticatedMediaV3, FeatureSoftwareVideoDecodeV3},
			negotiated: []string{FeaturePlaybackPlanV3},
			want:       []string{FeaturePlaybackPlanV3},
		},
		{
			name:       "an empty list still restores every sticky feature",
			requested:  []string{},
			negotiated: []string{FeatureHeaderAuthenticatedMediaV3, FeatureAuthorizedMediaOriginsV3, FeatureSoftwareVideoDecodeV3},
			want:       []string{FeatureHeaderAuthenticatedMediaV3, FeatureAuthorizedMediaOriginsV3, FeatureSoftwareVideoDecodeV3},
		},
		{
			// The origin trust set is what the client enforces against the URLs
			// it was handed; a replan that revoked it mid-attempt would leave a
			// live plan pointing at an origin the client no longer accepts.
			name:       "a replan cannot drop the negotiated media origins",
			requested:  []string{FeaturePlaybackPlanV3, FeatureHeaderAuthenticatedMediaV3},
			negotiated: []string{FeatureHeaderAuthenticatedMediaV3, FeatureAuthorizedMediaOriginsV3},
			want:       []string{FeaturePlaybackPlanV3, FeatureHeaderAuthenticatedMediaV3, FeatureAuthorizedMediaOriginsV3},
		},
		{
			name:       "a replan cannot add media origins mid-attempt",
			requested:  []string{FeatureHeaderAuthenticatedMediaV3, FeatureAuthorizedMediaOriginsV3},
			negotiated: []string{FeatureHeaderAuthenticatedMediaV3},
			want:       []string{FeatureHeaderAuthenticatedMediaV3},
		},
		{
			name:       "case and padding do not smuggle a duplicate through",
			requested:  []string{"  Header_Authenticated_Media_V1 ", FeatureDeviceQuirksV3},
			negotiated: []string{FeatureHeaderAuthenticatedMediaV3},
			want:       []string{FeatureDeviceQuirksV3, FeatureHeaderAuthenticatedMediaV3},
		},
		{
			name:       "non-sticky features pass through in order",
			requested:  []string{FeatureDeviceQuirksV3, FeatureClientVideoTransforms},
			negotiated: []string{FeatureClientVideoTransforms},
			want:       []string{FeatureDeviceQuirksV3, FeatureClientVideoTransforms},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PinAttemptStickyFeaturesV3(test.requested, test.negotiated); !slices.Equal(got, test.want) {
				t.Fatalf("pinned = %v, want %v", got, test.want)
			}
		})
	}
}

// Every sticky feature must be one the server actually advertises, or a client
// could never negotiate it in the first place.
func TestAttemptStickyFeaturesV3AreAdvertised(t *testing.T) {
	advertised := ServerFeaturesV3()
	for _, feature := range AttemptStickyFeaturesV3() {
		if !HasFeatureV3(advertised, feature) {
			t.Fatalf("attempt-sticky feature %q is not advertised by the server", feature)
		}
	}
}
