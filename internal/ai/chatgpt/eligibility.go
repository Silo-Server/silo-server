package chatgpt

import (
	"context"
	"slices"
	"time"
)

// RejectPlanToken records a terminal eligibility restriction for the credential
// used by a draft connection check. Live requests also bind the registration.
func (s *Service) RejectPlanToken(ctx context.Context, token string) error {
	return s.rejectPlan(ctx, "", token)
}

func (s *Service) rejectPlan(ctx context.Context, clientID, token string) error {
	return s.updateTokenAccount(ctx, clientID, token, func(a *account) { a.PlanIneligible = true })
}

// RejectUsageToken pauses new text requests without assuming a reset time.
func (s *Service) RejectUsageToken(ctx context.Context, token string) error {
	return s.pauseUsage(ctx, "", token)
}

func (s *Service) pauseUsage(ctx context.Context, clientID, token string) error {
	return s.updateTokenAccount(ctx, clientID, token, func(a *account) {
		a.UsageLimited = true
		a.UsagePauseID = randomValue()
	})
}

func (s *Service) verifyUsageToken(ctx context.Context, clientID, token, pauseID string) error {
	return s.updateTokenAccount(ctx, clientID, token, func(a *account) {
		if a.UsagePauseID == pauseID {
			a.UsageLimited = false
			a.UsagePauseID = ""
		}
	})
}

func (s *Service) updateTokenAccount(ctx context.Context, clientID, token string, update func(*account)) error {
	if token == "" {
		return nil
	}
	rejectionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return s.mutate(rejectionCtx, func(c *credentials) error {
		i := slices.IndexFunc(c.Accounts, func(a account) bool {
			return (clientID == "" || a.ClientID == clientID) && a.AccessToken == token
		})
		if i >= 0 {
			update(&c.Accounts[i])
		}
		return nil
	})
}
