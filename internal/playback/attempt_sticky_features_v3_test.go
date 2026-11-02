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

// Every sticky feature must be one the server actually advertises on some
// surface, or a client could never negotiate it in the first place.
func TestAttemptStickyFeaturesV3AreAdvertised(t *testing.T) {
	advertised := NativeServerFeaturesV3()
	for _, feature := range AttemptStickyFeaturesV3() {
		if !HasFeatureV3(advertised, feature) {
			t.Fatalf("attempt-sticky feature %q is not advertised by the server", feature)
		}
	}
}

// The hls playlist form is fixed at start: a replan's own hls delivery
// features can neither add hls_variable_substitution_v1 nor drop it, because
// a reused HLS transport keeps serving the form its session was built with.
func TestPinHLSVariableSubstitutionV3(t *testing.T) {
	withFeature := func(request StartRequestV3) StartRequestV3 {
		delivery := request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
		delivery.Features = append(slices.Clone(delivery.Features), FeatureHLSVariableSubstitutionV3)
		request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = delivery
		return request
	}
	for _, negotiated := range []bool{true, false} {
		start, replan := validStartRequestV3(), withFeature(validStartRequestV3())
		if negotiated {
			start, replan = withFeature(validStartRequestV3()), validStartRequestV3()
		}
		PinHLSVariableSubstitutionV3(&replan.ClientPlaybackContext, start)
		if got := HLSVariableSubstitutionV3(replan); got != negotiated {
			t.Fatalf("negotiated at start = %v, replan pinned to %v", negotiated, got)
		}
	}
}

// A replan that omits or disables the hls delivery still persists its
// context as the attempt's durable request. The start-time opt-in must
// survive that, so a later replan that brings HLS back gets the playlist form
// the attempt started with.
func TestPinHLSVariableSubstitutionV3SurvivesReplanWithoutHLS(t *testing.T) {
	for name, withoutHLS := range map[string]func(*ClientPlaybackContextV3){
		"omitted": func(ctx *ClientPlaybackContextV3) { delete(ctx.Deliveries, DeliveryClassHLSV3) },
		"disabled": func(ctx *ClientPlaybackContextV3) {
			delivery := ctx.Deliveries[DeliveryClassHLSV3]
			delivery.Enabled = false
			ctx.Deliveries[DeliveryClassHLSV3] = delivery
		},
	} {
		t.Run(name, func(t *testing.T) {
			record := validStartRequestV3()
			delivery := record.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
			delivery.Features = append(slices.Clone(delivery.Features), FeatureHLSVariableSubstitutionV3)
			record.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = delivery

			first := validStartRequestV3()
			withoutHLS(&first.ClientPlaybackContext)
			PinHLSVariableSubstitutionV3(&first.ClientPlaybackContext, record)
			if deliveryAvailableV3(first, DeliveryClassHLSV3) {
				t.Fatal("pinning made the omitted or disabled hls delivery available")
			}
			// executeReplanV3 persists the replan's context.
			record.ClientPlaybackContext = first.ClientPlaybackContext

			second := validStartRequestV3()
			PinHLSVariableSubstitutionV3(&second.ClientPlaybackContext, record)
			if !HLSVariableSubstitutionV3(second) {
				t.Fatal("a replan that re-enabled hls lost the start-time opt-in")
			}
		})
	}
}
