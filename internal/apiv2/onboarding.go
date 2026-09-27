package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/onboarding"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type OnboardingService interface {
	ReadOnboardingFlow(context.Context, int, string, string) (onboarding.Flow, error)
	ReadOnboardingProgress(context.Context, int, string) (*userstore.OnboardingProgress, error)
	SaveOnboardingProgress(context.Context, int, string, string, bool, bool, int64) (*userstore.OnboardingProgress, error)
}
type OnboardingFlowInput struct {
	Surface string `query:"surface" default:"web" enum:"web,phone,tv"`
}
type OnboardingFlowOutput struct{ Body onboarding.Flow }
type OnboardingState struct {
	TourID      string   `json:"tour_id"`
	LastStep    string   `json:"last_step,omitempty"`
	CompletedAt *Instant `json:"completed_at,omitempty"`
	SkippedAt   *Instant `json:"skipped_at,omitempty"`
	Done        bool     `json:"done"`
}
type OnboardingStateOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   OnboardingState
}
type OnboardingStateInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type OnboardingProgressInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		TourID    string `json:"tour_id" minLength:"1" maxLength:"128"`
		LastStep  string `json:"last_step,omitempty" maxLength:"128"`
		Completed bool   `json:"completed,omitzero"`
		Skipped   bool   `json:"skipped,omitzero"`
	}
}
type OnboardingCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         OnboardingCapabilitiesOutputBody
}

type OnboardingCapabilitiesOutputBody struct {
	Capability
	Available       bool `json:"available"`
	RevisionGuarded bool `json:"revision_guarded"`
}

func onboardingTag(ctx context.Context, revision int64) EntityTag {
	return RenderETag("onboarding:"+strconv.Itoa(claimsFrom(ctx).UserID)+":"+profileFrom(ctx), onboarding.TourID, revision)
}
func onboardingStateOf(ctx context.Context, p *userstore.OnboardingProgress) *OnboardingStateOutput {
	return &OnboardingStateOutput{ETag: onboardingTag(ctx, p.Revision).String(), Body: OnboardingState{TourID: p.TourID, LastStep: p.LastStep, CompletedAt: onboardingInstant(p.CompletedAt), SkippedAt: onboardingInstant(p.SkippedAt), Done: p.CompletedAt != "" || p.SkippedAt != ""}}
}
func registerOnboarding(reg *Registry) {
	op := func(method, path, id string) Operation {
		return Operation{Operation: humaOp(method, Prefix+"/onboarding"+path, id, "onboarding", "Read or advance the active profile's onboarding tour."), Class: ClassProfileScoped, ServiceBacked: true}
	}
	Register(reg, op(http.MethodGet, "/capabilities", "getOnboardingCapabilities"), func(_ context.Context, _ *CapabilityInput) (*OnboardingCapabilitiesOutput, error) {
		out := new(OnboardingCapabilitiesOutput)
		out.Body.Available = reg.deps.Onboarding != nil
		out.Body.RevisionGuarded = true
		return out, nil
	})
	Register(reg, op(http.MethodGet, "/flow", "getOnboardingFlow"), func(ctx context.Context, in *OnboardingFlowInput) (*OnboardingFlowOutput, error) {
		if reg.deps.Onboarding == nil {
			return nil, unavailable("onboarding")
		}
		flow, err := reg.deps.Onboarding.ReadOnboardingFlow(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), in.Surface)
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &OnboardingFlowOutput{Body: flow}, nil
	})
	read := op(http.MethodGet, "/state", "getOnboardingState")
	read.Conditional = true
	Register(reg, read, func(ctx context.Context, in *OnboardingStateInput) (*OnboardingStateOutput, error) {
		if reg.deps.Onboarding == nil {
			return nil, unavailable("onboarding")
		}
		state, err := reg.deps.Onboarding.ReadOnboardingProgress(ctx, claimsFrom(ctx).UserID, profileFrom(ctx))
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := onboardingStateOf(ctx, state)
		tag := onboardingTag(ctx, state.Revision)
		if same, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if same {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	write := op(http.MethodPut, "/progress", "updateOnboardingProgress")
	write.Guarded = true
	write.RetrySafety = RetrySafetyNonRetryable
	write.DemoRestricted = true
	write.Errors = []int{http.StatusConflict}
	Register(reg, write, func(ctx context.Context, in *OnboardingProgressInput) (*OnboardingStateOutput, error) {
		svc := reg.deps.Onboarding
		if svc == nil {
			return nil, unavailable("onboarding")
		}
		if in.Body.TourID != onboarding.TourID {
			return nil, NewProblem(TypeConflict, "This tour is no longer current.")
		}
		if in.Body.Completed && in.Body.Skipped {
			return nil, NewProblem(TypeValidationFailed, "Choose completion or skipping, not both.")
		}
		if strings.TrimSpace(in.IfMatch) == "*" {
			return nil, NewProblem(TypeMalformedRequest, "An exact onboarding ETag is required.")
		}
		before, err := svc.ReadOnboardingProgress(ctx, claimsFrom(ctx).UserID, profileFrom(ctx))
		if err != nil {
			return nil, serviceProblem(err)
		}
		if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, onboardingTag(ctx, before.Revision)); p != nil {
			return nil, p
		}
		state, err := svc.SaveOnboardingProgress(ctx, claimsFrom(ctx).UserID, profileFrom(ctx), in.Body.LastStep, in.Body.Completed, in.Body.Skipped, before.Revision)
		if errors.Is(err, userstore.ErrOnboardingRevision) {
			current, e := svc.ReadOnboardingProgress(ctx, claimsFrom(ctx).UserID, profileFrom(ctx))
			if e != nil {
				return nil, serviceProblem(e)
			}
			return nil, StaleVersionProblem(onboardingTag(ctx, current.Revision))
		}
		if err != nil {
			return nil, serviceProblem(err)
		}
		return onboardingStateOf(ctx, state), nil
	})
}

func onboardingInstant(raw string) *Instant {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil
	}
	return new(NewInstant(t))
}

func (c OnboardingCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
