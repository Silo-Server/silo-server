package userstore

import (
	"context"
	"errors"
)

var ErrOnboardingRevision = errors.New("onboarding progress revision changed")

// OnboardingProgressStore atomically fences progress by the previously read
// revision. Revision zero represents untouched state. Bridge writes invalidate
// revisions too; completion and skipped timestamps remain monotonic.
type OnboardingProgressStore interface {
	ReadOnboardingProgress(context.Context, string, string) (*OnboardingProgress, error)
	SaveOnboardingProgress(context.Context, OnboardingState, int64) (*OnboardingProgress, error)
}
type OnboardingProgress struct {
	OnboardingState
	Revision int64
}
