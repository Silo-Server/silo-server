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
			c.Accounts[i].PlanIneligible = true
		}
		return nil
	})
}
