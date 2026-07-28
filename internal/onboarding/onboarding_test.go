package onboarding

import (
	"context"
	"testing"
)

func gatesAllOn() Gates {
	on := func(context.Context) bool { return true }
	return Gates{
		Requests: on, WatchTogether: on, Recommendations: on,
		Notifications: on, Calendar: on, JellyfinCompat: on,
	}
}

func stepIDs(flow Flow) []string {
	ids := make([]string, 0, len(flow.Steps))
	for _, s := range flow.Steps {
		ids = append(ids, s.ID)
	}
	return ids
}

func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func TestFlowForAllGatesOn(t *testing.T) {
	flow := FlowFor(context.Background(), gatesAllOn(), SurfaceWeb, false)
	if flow.Version != Version || flow.TourID != TourID {
		t.Fatalf("flow envelope = %d/%q", flow.Version, flow.TourID)
	}
	ids := stepIDs(flow)
	if ids[0] != StepIDWelcome {
		t.Errorf("first step = %q, want welcome", ids[0])
	}
	if ids[len(ids)-1] != StepIDHandoffTaste {
		t.Errorf("last step = %q, want handoff-taste", ids[len(ids)-1])
	}
	for _, want := range []string{StepIDRequests, StepIDWatchTogether, StepIDRecommendations, StepIDPlaybackQuality} {
		if !contains(ids, want) {
			t.Errorf("missing step %q with all gates on", want)
		}
	}
}

func TestFlowForDropsGatedSteps(t *testing.T) {
	flow := FlowFor(context.Background(), Gates{}, SurfaceWeb, false)
	ids := stepIDs(flow)
	for _, gated := range []string{StepIDRequests, StepIDWatchTogether, StepIDRecommendations, "calendar-notifications"} {
		if contains(ids, gated) {
			t.Errorf("step %q shown with its feature off", gated)
		}
	}
	// Core steps survive.
	for _, want := range []string{StepIDWelcome, StepIDPlaybackQuality, "subtitles", "favorites-watchlist", StepIDHandoffTaste} {
		if !contains(ids, want) {
			t.Errorf("core step %q missing", want)
		}
	}
}

func TestFlowForTVDropsInputSteps(t *testing.T) {
	flow := FlowFor(context.Background(), gatesAllOn(), SurfaceTV, false)
	for _, step := range flow.Steps {
		if step.Kind == KindSettingChoice {
			t.Errorf("TV surface got setting_choice step %q", step.ID)
		}
	}
}

func TestWebOnlyStepsSkippedOffWeb(t *testing.T) {
	webIDs := stepIDs(FlowFor(context.Background(), gatesAllOn(), SurfaceWeb, false))
	for _, want := range []string{StepIDApps, StepIDJellyfinCompat} {
		if !contains(webIDs, want) {
			t.Errorf("web surface missing %q", want)
		}
	}
	// The apps card is pointless inside the apps it advertises, and TV can't
	// open store links either.
	for _, surface := range []string{SurfacePhone, SurfaceTV} {
		ids := stepIDs(FlowFor(context.Background(), gatesAllOn(), surface, false))
		for _, gated := range []string{StepIDApps, StepIDJellyfinCompat} {
			if contains(ids, gated) {
				t.Errorf("surface %q got web-only step %q", surface, gated)
			}
		}
	}
}

func TestJellyfinStepGated(t *testing.T) {
	ids := stepIDs(FlowFor(context.Background(), Gates{}, SurfaceWeb, false))
	if contains(ids, StepIDJellyfinCompat) {
		t.Error("jellyfin step shown with compat disabled")
	}
	if !contains(ids, StepIDApps) {
		t.Error("apps step must not depend on the jellyfin gate")
	}
}

func TestFlowForChildDropsRequests(t *testing.T) {
	flow := FlowFor(context.Background(), gatesAllOn(), SurfaceWeb, true)
	if contains(stepIDs(flow), StepIDRequests) {
		t.Error("child profile got the requests step")
	}
}

func TestSettingStepsUseKnownTargets(t *testing.T) {
	for _, step := range tourSteps {
		if step.Kind != KindSettingChoice {
			continue
		}
		if step.Setting == nil {
			t.Errorf("setting_choice %q has no setting spec", step.ID)
			continue
		}
		switch step.Setting.Target {
		case TargetProfileField, TargetSetting, TargetDeviceSetting:
		default:
			t.Errorf("step %q has unknown setting target %q", step.ID, step.Setting.Target)
		}
		if !step.needsInput {
			t.Errorf("setting_choice %q must be marked needsInput for TV filtering", step.ID)
		}
	}
}
