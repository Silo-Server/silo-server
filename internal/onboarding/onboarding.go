// Package onboarding owns the server-driven first-run tour: an ordered step
// manifest filtered per server (features that are off never produce a step)
// and per surface (a TV can't type), plus per-profile completion state in
// the user store. Clients render step kinds they know and skip the rest,
// which is what lets the server add a stop without three app-store releases.
//
// Spec: docs/superpowers/specs/2026-07-27-invitations-and-onboarding-design.md
package onboarding

import "context"

// Version is the manifest contract version. Bump only for breaking changes
// to the step envelope itself; new step kinds are NOT breaking (clients skip
// unknown kinds by contract).
const Version = 1

// TourID identifies the current tour's content generation. A profile that
// completed this tour is never re-prompted for it; shipping a materially
// different tour later means minting a new ID.
const TourID = "core-2026-07"

// Surfaces a client can request the flow for.
const (
	SurfaceWeb   = "web"
	SurfacePhone = "phone"
	SurfaceTV    = "tv"
)

// Step kinds. Clients switch on these and silently skip unknown values.
const (
	KindWelcome       = "welcome"
	KindFeatureCard   = "feature_card"
	KindSettingChoice = "setting_choice"
	KindHandoff       = "handoff"
)

// Gate names steps may reference (see Gates).
const (
	gateRequests        = "requests"
	gateWatchTogether   = "watch_together"
	gateRecommendations = "recommendations"
	gateNotifications   = "notifications"
	gateCalendar        = "calendar"
	gateJellyfinCompat  = "jellyfin_compat"
)

// Setting targets: which existing API a setting_choice writes through.
const (
	TargetProfileField  = "profile_field"
	TargetSetting       = "setting"
	TargetDeviceSetting = "device_setting"
)

// Step is one stop in the tour.
type Step struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	// Illustration is a client-side asset key; the server never sends URLs.
	Illustration string `json:"illustration,omitempty"`
	// Setting is present on setting_choice steps.
	Setting *SettingSpec `json:"setting,omitempty"`
	// Route is a client route for feature_card actions and handoff steps.
	Route string `json:"route,omitempty"`
	// ActionLabel labels the optional feature_card route action.
	ActionLabel string `json:"action_label,omitempty"`
	// Links are external URLs (store listings, docs) rendered as outbound
	// buttons. Additive: clients that predate the field ignore it.
	Links []StepLink `json:"links,omitempty"`

	// needsInput marks steps unsuitable for 10-foot surfaces.
	needsInput bool
	// webOnly marks steps that only make sense in a browser (e.g. "install
	// the apps" — pointless inside the app it advertises).
	webOnly bool
	// gate names the feature flag that must be on; empty = always shown.
	gate string
}

// StepLink is one outbound link on a step.
type StepLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// SettingSpec describes the control a setting_choice renders and where the
// chosen value is written. Target selects the API: profile_field goes
// through PUT /profiles/{id}, setting through PUT /settings/{key},
// device_setting through PUT /settings/device/{key}.
type SettingSpec struct {
	Target  string          `json:"target"`
	Key     string          `json:"key"`
	Control string          `json:"control"` // "segmented" | "toggle" | "select"
	Options []SettingOption `json:"options,omitempty"`
	Default string          `json:"default,omitempty"`
	// Label annotates toggle controls.
	Label string `json:"label,omitempty"`
}

// SettingOption is one choice of a segmented/select control.
type SettingOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Flow is the response of GET /onboarding/flow.
type Flow struct {
	Version int    `json:"version"`
	TourID  string `json:"tour_id"`
	Steps   []Step `json:"steps"`
}

// Gates reports which optional server features are on. Each check is
// consulted at request time so admin toggles apply without a restart; a nil
// check means "off".
type Gates struct {
	Requests        func(ctx context.Context) bool
	WatchTogether   func(ctx context.Context) bool
	Recommendations func(ctx context.Context) bool
	Notifications   func(ctx context.Context) bool
	Calendar        func(ctx context.Context) bool
	JellyfinCompat  func(ctx context.Context) bool
}

func (g Gates) enabled(ctx context.Context, gate string) bool {
	check := map[string]func(context.Context) bool{
		gateRequests:        g.Requests,
		gateWatchTogether:   g.WatchTogether,
		gateRecommendations: g.Recommendations,
		gateNotifications:   g.Notifications,
		gateCalendar:        g.Calendar,
		gateJellyfinCompat:  g.JellyfinCompat,
	}[gate]
	if check == nil {
		return false
	}
	return check(ctx)
}

// FlowFor returns the ordered steps for one surface with gated and
// unsuitable steps removed. isChild drops steps a child profile may not act
// on (requests) or that write fields a child session can't.
func FlowFor(ctx context.Context, gates Gates, surface string, isChild bool) Flow {
	steps := make([]Step, 0, len(tourSteps))
	for _, step := range tourSteps {
		if step.gate != "" && !gates.enabled(ctx, step.gate) {
			continue
		}
		if surface == SurfaceTV && step.needsInput {
			continue
		}
		if surface != SurfaceWeb && step.webOnly {
			continue
		}
		if isChild && step.gate == gateRequests {
			continue
		}
		steps = append(steps, step)
	}
	return Flow{Version: Version, TourID: TourID, Steps: steps}
}
