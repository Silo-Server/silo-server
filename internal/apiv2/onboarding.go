package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/onboarding"
)

// The onboarding domain: the first-run tour a profile walks through once.
// The flow is server-composed per surface and per profile (child profiles
// skip steps they may not act on); progress is stored per profile for the
// current tour only.

// OnboardingStepLink is one outbound link on a step.
type OnboardingStepLink struct {
	Label string `json:"label" doc:"Button label" example:"App Store"`
	URL   string `json:"url" doc:"External URL" example:"https://apps.apple.com/app/silo"`
}

// OnboardingSettingOption is one choice of a segmented or select control.
type OnboardingSettingOption struct {
	Value string `json:"value" doc:"The value written when chosen" example:"auto"`
	Label string `json:"label" doc:"Display label" example:"Automatic"`
}

// OnboardingSetting describes the control a setting_choice step renders and
// where the chosen value is written.
type OnboardingSetting struct {
	Target  string                    `json:"target" doc:"Which API the choice writes through: profile_field (updateProfile), setting, or device_setting" example:"profile_field"`
	Key     string                    `json:"key" doc:"The profile member or setting key written" example:"quality_preference"`
	Control string                    `json:"control" doc:"Control kind: segmented, toggle, or select" example:"segmented"`
	Options []OnboardingSettingOption `json:"options" doc:"Choices; empty for a toggle"`
	Default string                    `json:"default,omitempty" doc:"Preselected value" example:"auto"`
	Label   string                    `json:"label,omitempty" doc:"Annotation of a toggle control" example:"Show subtitles"`
}

// OnboardingStep is one stop in the tour.
type OnboardingStep struct {
	ID           string               `json:"id" doc:"Stable step id; the value recordOnboardingProgress takes as last_step" example:"welcome"`
	Kind         string               `json:"kind" doc:"Step kind: welcome, feature_card, setting_choice, or handoff; clients skip unknown kinds" example:"welcome"`
	Title        string               `json:"title,omitempty" example:"Welcome to Silo"`
	Body         string               `json:"body,omitempty" example:"A quick tour of what you can do."`
	Illustration string               `json:"illustration,omitempty" doc:"Client-side asset key; never a URL" example:"welcome"`
	Setting      *OnboardingSetting   `json:"setting,omitempty" doc:"Present on setting_choice steps"`
	Route        string               `json:"route,omitempty" doc:"Client route for a feature_card action or a handoff" example:"/requests"`
	ActionLabel  string               `json:"action_label,omitempty" doc:"Label of the optional feature_card action" example:"Browse requests"`
	Links        []OnboardingStepLink `json:"links" doc:"Outbound links; empty when none"`
}

// OnboardingFlow is the tour for one surface.
type OnboardingFlow struct {
	Version int              `json:"version" doc:"Flow format version" example:"1"`
	TourID  string           `json:"tour_id" doc:"The current tour; progress is recorded against it" example:"core-2026-07"`
	Steps   []OnboardingStep `json:"steps" doc:"Ordered steps for the surface and profile"`
}

// OnboardingFlowInput selects the surface.
type OnboardingFlowInput struct {
	Surface string `query:"surface" enum:"web,phone,tv" doc:"The client surface; web when absent" example:"tv"`
}

// OnboardingFlowOutput is the getOnboardingFlow response.
type OnboardingFlowOutput struct {
	Body OnboardingFlow
}

// OnboardingState is a profile's progress through the current tour.
type OnboardingState struct {
	TourID      string `json:"tour_id" doc:"The current tour" example:"core-2026-07"`
	LastStep    string `json:"last_step,omitempty" doc:"The last step the profile reached; absent before the tour starts" example:"playback-quality"`
	CompletedAt string `json:"completed_at,omitempty" doc:"RFC 3339 instant the tour was completed; absent otherwise" example:"2026-01-02T03:04:05Z"`
	SkippedAt   string `json:"skipped_at,omitempty" doc:"RFC 3339 instant the tour was skipped; absent otherwise" example:"2026-01-02T03:04:05Z"`
	Done        bool   `json:"done" doc:"True once the tour was completed or skipped: do not show it again" example:"false"`
}

// OnboardingStateOutput is the getOnboardingState response.
type OnboardingStateOutput struct {
	Body OnboardingState
}

// OnboardingProgressInput records progress through the current tour.
type OnboardingProgressInput struct {
	Body struct {
		TourID    string `json:"tour_id,omitempty" maxLength:"64" doc:"The tour the progress belongs to; a tour other than the current one is refused" example:"core-2026-07"`
		LastStep  string `json:"last_step,omitempty" maxLength:"64" doc:"The step the profile reached" example:"playback-quality"`
		Completed bool   `json:"completed,omitempty" doc:"Mark the tour completed" example:"false"`
		Skipped   bool   `json:"skipped,omitempty" doc:"Mark the tour skipped" example:"false"`
	}
}

func registerOnboarding(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/onboarding/flow", "getOnboardingFlow", "onboarding",
			"Get the first-run tour for the acting profile on one surface."),
		Class: ClassProfileScoped, ServiceBacked: true,
	}, reg.getOnboardingFlow)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/onboarding/state", "getOnboardingState", "onboarding",
			"Get the acting profile's progress through the current tour."),
		Class: ClassProfileScoped, ServiceBacked: true,
	}, reg.getOnboardingState)
	progress := humaOp(http.MethodPost, Prefix+"/onboarding/progress", "recordOnboardingProgress", "onboarding",
		"Record the acting profile's progress through the current tour.")
	// A stale tour_id is 409 conflict.
	progress.Errors = []int{http.StatusConflict}
	Register(reg, Operation{Operation: progress, Class: ClassProfileScoped, ServiceBacked: true}, reg.recordOnboardingProgress)
}

func (reg *Registry) onboardingPrincipal(ctx context.Context) (int, string, *Problem) {
	claims := claimsFrom(ctx)
	profileID := profileFrom(ctx)
	if claims == nil || profileID == "" {
		return 0, "", NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	return claims.UserID, profileID, nil
}

func (reg *Registry) getOnboardingFlow(ctx context.Context, in *OnboardingFlowInput) (*OnboardingFlowOutput, error) {
	if reg.deps.Onboarding == nil {
		return nil, unavailable("onboarding")
	}
	userID, profileID, p := reg.onboardingPrincipal(ctx)
	if p != nil {
		return nil, p
	}
	flow, err := reg.deps.Onboarding.Flow(ctx, userID, profileID, in.Surface)
	if err != nil {
		var apiErr *handlers.APIError
		if errors.As(err, &apiErr) && apiErr.Field != "" {
			return nil, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: "query." + apiErr.Field, Code: codeInvalid, Detail: apiErr.Message})
		}
		return nil, serviceProblem(err)
	}
	return &OnboardingFlowOutput{Body: onboardingFlowOf(flow)}, nil
}

func onboardingFlowOf(f onboarding.Flow) OnboardingFlow {
	steps := make([]OnboardingStep, 0, len(f.Steps))
	for _, s := range f.Steps {
		step := OnboardingStep{ID: s.ID, Kind: s.Kind, Title: s.Title, Body: s.Body, Illustration: s.Illustration, Route: s.Route, ActionLabel: s.ActionLabel, Links: []OnboardingStepLink{}}
		for _, l := range s.Links {
			step.Links = append(step.Links, OnboardingStepLink{Label: l.Label, URL: l.URL})
		}
		if s.Setting != nil {
			setting := &OnboardingSetting{Target: s.Setting.Target, Key: s.Setting.Key, Control: s.Setting.Control, Default: s.Setting.Default, Label: s.Setting.Label, Options: []OnboardingSettingOption{}}
			for _, o := range s.Setting.Options {
				setting.Options = append(setting.Options, OnboardingSettingOption{Value: o.Value, Label: o.Label})
			}
			step.Setting = setting
		}
		steps = append(steps, step)
	}
	return OnboardingFlow{Version: f.Version, TourID: f.TourID, Steps: steps}
}

func (reg *Registry) getOnboardingState(ctx context.Context, _ *struct{}) (*OnboardingStateOutput, error) {
	if reg.deps.Onboarding == nil {
		return nil, unavailable("onboarding")
	}
	userID, profileID, p := reg.onboardingPrincipal(ctx)
	if p != nil {
		return nil, p
	}
	state, err := reg.deps.Onboarding.State(ctx, userID, profileID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &OnboardingStateOutput{Body: OnboardingState{TourID: state.TourID, LastStep: state.LastStep, CompletedAt: state.CompletedAt, SkippedAt: state.SkippedAt, Done: state.Done}}, nil
}

func (reg *Registry) recordOnboardingProgress(ctx context.Context, in *OnboardingProgressInput) (*struct{}, error) {
	if reg.deps.Onboarding == nil {
		return nil, unavailable("onboarding")
	}
	userID, profileID, p := reg.onboardingPrincipal(ctx)
	if p != nil {
		return nil, p
	}
	err := reg.deps.Onboarding.RecordProgress(ctx, userID, profileID, handlers.OnboardingProgressInput{
		TourID: in.Body.TourID, LastStep: in.Body.LastStep, Completed: in.Body.Completed, Skipped: in.Body.Skipped,
	})
	if err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}
